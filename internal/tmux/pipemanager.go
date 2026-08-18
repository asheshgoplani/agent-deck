package tmux

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

// PipeManager manages ControlPipes for all active tmux sessions.
// It provides zero-subprocess CapturePane and event-driven output detection.
// Falls back to subprocess execution when pipes are unavailable.
type PipeManager struct {
	pipes map[string]*ControlPipe // sessionName -> pipe
	mu    sync.RWMutex            // protects pipes and wantPipe

	// Callback for output events (invoked when %output detected from a session)
	onOutput func(sessionName string)

	// Callback for window change events (invoked when %window-add or %window-close detected)
	onWindowChange func()

	// wantPipe, when non-nil, gates which sessions may hold a live pipe.
	// Connect and watchPipe consult it so background sessions are never
	// connected or auto-reconnected. nil = legacy behaviour (want everything).
	wantPipe func(sessionName string) bool

	// Reconnection tracking
	reconnectMu  sync.Mutex
	reconnecting map[string]bool

	// Lifecycle
	ctx    context.Context
	cancel context.CancelFunc
}

// NewPipeManager creates a new PipeManager. The onOutput callback is invoked
// whenever a connected session produces terminal output (via %output events).
func NewPipeManager(ctx context.Context, onOutput func(sessionName string)) *PipeManager {
	childCtx, cancel := context.WithCancel(ctx)
	return &PipeManager{
		pipes:        make(map[string]*ControlPipe),
		onOutput:     onOutput,
		reconnecting: make(map[string]bool),
		ctx:          childCtx,
		cancel:       cancel,
	}
}

// Connect creates a control mode pipe for the given tmux session.
// If a pipe already exists and is alive, this is a no-op.
// Uses reconnecting map to prevent concurrent pipe creation for the same session.
// Connect opens a control-mode pipe to sessionName on the tmux server selected
// by socketName (Session.SocketName). Pass "" to target the user's default
// server. Safe to call repeatedly; a live pipe short-circuits and returns nil.
func (pm *PipeManager) Connect(sessionName, socketName string) error {
	// Background sessions are not wanted — connecting them is what scaled pipe
	// count to instances×sessions. Silent no-op so existing callers (startup
	// loop, new-session hook, reviver, sweep) need no per-call gating.
	if !pm.wants(sessionName) {
		return nil
	}

	pm.mu.Lock()

	// Already connected and alive?
	if existing, ok := pm.pipes[sessionName]; ok && existing.IsAlive() {
		pm.mu.Unlock()
		return nil
	}

	// Clean up dead pipe if present
	if existing, ok := pm.pipes[sessionName]; ok {
		existing.Close()
		delete(pm.pipes, sessionName)
	}
	pm.mu.Unlock()

	// Prevent concurrent pipe creation for the same session (TOCTOU guard)
	pm.reconnectMu.Lock()
	if pm.reconnecting[sessionName] {
		pm.reconnectMu.Unlock()
		return nil // Another goroutine is already connecting
	}
	pm.reconnecting[sessionName] = true
	pm.reconnectMu.Unlock()

	defer func() {
		pm.reconnectMu.Lock()
		delete(pm.reconnecting, sessionName)
		pm.reconnectMu.Unlock()
	}()

	// Kill stale control-mode clients left over from previous TUI instances.
	// Without this, each TUI reconnect accumulates orphan `tmux -C attach-session`
	// processes that are never cleaned up (#595).
	killStaleControlClients(sessionName, socketName)

	// Create new pipe (outside lock since it spawns a process)
	pipe, err := NewControlPipe(sessionName, socketName)
	if err != nil {
		return fmt.Errorf("connect pipe for %s: %w", sessionName, err)
	}

	pm.mu.Lock()
	// Double-check: another goroutine may have connected while we were creating
	if existing, ok := pm.pipes[sessionName]; ok && existing.IsAlive() {
		pm.mu.Unlock()
		pipe.Close() // Discard the one we just created
		return nil
	}
	pm.pipes[sessionName] = pipe
	pm.mu.Unlock()

	// Start output event forwarder
	go pm.forwardOutputEvents(sessionName, pipe)

	// Start reconnection watcher
	go pm.watchPipe(sessionName, pipe)

	return nil
}

// Disconnect closes and removes the pipe for the given session.
func (pm *PipeManager) Disconnect(sessionName string) {
	pm.mu.Lock()
	pipe, ok := pm.pipes[sessionName]
	if ok {
		delete(pm.pipes, sessionName)
	}
	pm.mu.Unlock()

	if pipe != nil {
		pipe.Close()
	}
	pipeLog.Debug("pipe_disconnected", slog.String("session", logging.SanitizeValue(sessionName)))
}

// GetPipe returns the ControlPipe for a session, or nil if not connected.
func (pm *PipeManager) GetPipe(sessionName string) *ControlPipe {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.pipes[sessionName]
}

// CapturePane routes capture-pane through the control mode pipe if available.
// Falls back to subprocess execution if the pipe is nil, dead, or errors.
func (pm *PipeManager) CapturePane(sessionName string) (string, error) {
	pm.mu.RLock()
	pipe := pm.pipes[sessionName]
	pm.mu.RUnlock()

	if pipe == nil || !pipe.IsAlive() {
		return "", fmt.Errorf("no pipe for session %s", sessionName)
	}

	return pipe.CapturePaneVia()
}

// GetWindowActivity sends a display-message command through the pipe to get
// the window_activity timestamp. Falls back to error if pipe unavailable.
func (pm *PipeManager) GetWindowActivity(sessionName string) (int64, error) {
	pm.mu.RLock()
	pipe := pm.pipes[sessionName]
	pm.mu.RUnlock()

	if pipe == nil || !pipe.IsAlive() {
		return 0, fmt.Errorf("no pipe for session %s", sessionName)
	}

	output, err := pipe.SendCommand(fmt.Sprintf(`display-message -t %s -p "#{window_activity}"`, sessionName))
	if err != nil {
		return 0, err
	}

	var ts int64
	_, err = fmt.Sscanf(strings.TrimSpace(output), "%d", &ts)
	if err != nil {
		return 0, fmt.Errorf("parse window_activity: %w", err)
	}
	return ts, nil
}

// selectPipesPerSocket returns one alive pipe for each distinct socket among
// the given pipes. `list-windows -a` only reports sessions on the server its
// pipe is attached to, so a single arbitrary pipe misses every session living
// on another socket. When agent-deck sessions are split across more than one
// tmux server (e.g. some on the default socket, some under [tmux] socket_name),
// querying just one pipe makes the others' sessions look gone — they flip to
// StatusError/tmux_missing and can then be killed by restart machinery. Probing
// one pipe per socket and merging keeps the cache complete. Dead pipes are
// skipped. See the multi-socket cache aliasing note.
func selectPipesPerSocket(pipes map[string]*ControlPipe) []*ControlPipe {
	seen := make(map[string]bool)
	var selected []*ControlPipe
	for _, p := range pipes {
		if p == nil || !p.IsAlive() {
			continue
		}
		if seen[p.socketName] {
			continue
		}
		seen[p.socketName] = true
		selected = append(selected, p)
	}
	return selected
}

// RefreshAllActivities sends a list-windows command through one pipe per distinct
// socket to get activity timestamps for ALL sessions across every tmux server we
// have a live pipe to. This replaces the subprocess call in RefreshSessionCache.
// Session names carry random suffixes, so cross-socket name collisions are
// effectively impossible and merging by name is safe.
func (pm *PipeManager) RefreshAllActivities() (map[string]int64, map[string][]WindowInfo, error) {
	pm.mu.RLock()
	pipes := selectPipesPerSocket(pm.pipes)
	pm.mu.RUnlock()

	if len(pipes) == 0 {
		return nil, nil, fmt.Errorf("no alive pipes available")
	}

	sessionCache := make(map[string]int64)
	windowCache := make(map[string][]WindowInfo)
	var firstErr error
	gotAny := false
	for _, pipe := range pipes {
		// Must use the same tmuxFieldSep as parseListWindowsOutput (shared with the
		// subprocess path). A control client negotiates UTF-8, so TAB would usually
		// survive here, but the delimiter MUST still match what the parser splits on.
		// tmux control mode requires the format string double-quoted.
		output, err := pipe.SendCommand(`list-windows -a -F "` + tmuxFmt("#{session_name}", "#{window_activity}", "#{window_index}", "#{window_name}") + `"`)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		gotAny = true
		sc, wc := parseListWindowsOutput(output)
		maps.Copy(sessionCache, sc)
		maps.Copy(windowCache, wc)
	}

	if !gotAny {
		return nil, nil, fmt.Errorf("list-windows via pipe: %w", firstErr)
	}

	return sessionCache, windowCache, nil
}

// RefreshAllPaneInfo sends a single list-panes command through any available
// pipe to get pane titles and current commands for ALL sessions. This provides
// the data needed for title-based state detection without subprocess spawns.
// Also returns per-window tool detection data for enriching the window cache.
func (pm *PipeManager) RefreshAllPaneInfo() (map[string]PaneInfo, map[string]map[int]string, error) {
	pm.mu.RLock()
	var pipe *ControlPipe
	for _, p := range pm.pipes {
		if p.IsAlive() {
			pipe = p
			break
		}
	}
	pm.mu.RUnlock()

	if pipe == nil {
		return nil, nil, fmt.Errorf("no alive pipes available")
	}

	// Share the producer format AND parser with the subprocess path
	// (parseListPanesOutput): pane_title last, tmuxFieldSep-delimited. Keeps the
	// pipe and subprocess paths from drifting in field order or delimiter.
	output, err := pipe.SendCommand(`list-panes -a -F "` + tmuxFmt("#{session_name}", "#{pane_current_command}", "#{pane_dead}", "#{window_index}", "#{pane_index}", "#{pane_title}") + `"`)
	if err != nil {
		return nil, nil, fmt.Errorf("list-panes via pipe: %w", err)
	}

	result, windowTools := parseListPanesOutput(output)
	return result, windowTools, nil
}

// LastOutputTime returns the last output time for a session from its pipe.
// Returns zero time if no pipe or no output recorded.
func (pm *PipeManager) LastOutputTime(sessionName string) time.Time {
	pm.mu.RLock()
	pipe := pm.pipes[sessionName]
	pm.mu.RUnlock()

	if pipe == nil {
		return time.Time{}
	}
	return pipe.LastOutputTime()
}

// IsConnected returns true if a session has an alive pipe.
func (pm *PipeManager) IsConnected(sessionName string) bool {
	pm.mu.RLock()
	pipe := pm.pipes[sessionName]
	pm.mu.RUnlock()
	return pipe != nil && pipe.IsAlive()
}

// ConnectedCount returns the number of alive pipes.
func (pm *PipeManager) ConnectedCount() int {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	count := 0
	for _, p := range pm.pipes {
		if p.IsAlive() {
			count++
		}
	}
	return count
}

// Close shuts down all pipes and cancels the context.
func (pm *PipeManager) Close() {
	pm.cancel()

	pm.mu.Lock()
	pipes := make(map[string]*ControlPipe, len(pm.pipes))
	maps.Copy(pipes, pm.pipes)
	pm.pipes = make(map[string]*ControlPipe)
	pm.mu.Unlock()

	for name, pipe := range pipes {
		pipe.Close()
		pipeLog.Debug("pipe_shutdown", slog.String("session", name))
	}
}

// SetWindowChangeCallback sets the callback for window add/close events.
// Must be called before Connect to ensure all pipes forward events.
func (pm *PipeManager) SetWindowChangeCallback(cb func()) {
	pm.onWindowChange = cb
}

// SetWantPipe installs the predicate that decides which sessions hold a live
// pipe. Call once at startup before Connect. nil-safe: an unset predicate means
// every session is wanted (legacy behaviour).
func (pm *PipeManager) SetWantPipe(fn func(sessionName string) bool) {
	pm.mu.Lock()
	pm.wantPipe = fn
	pm.mu.Unlock()
}

// wants reports whether sessionName is currently wanted. nil predicate => true.
func (pm *PipeManager) wants(sessionName string) bool {
	pm.mu.RLock()
	fn := pm.wantPipe
	pm.mu.RUnlock()
	return fn == nil || fn(sessionName)
}

// ConnectedSessions returns the names of sessions with an alive pipe.
func (pm *PipeManager) ConnectedSessions() []string {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	out := make([]string, 0, len(pm.pipes))
	for name, p := range pm.pipes {
		if p.IsAlive() {
			out = append(out, name)
		}
	}
	return out
}

// forwardOutputEvents reads from a pipe's output and window events channels
// and calls the appropriate callbacks. Runs until the pipe dies or context is cancelled.
func (pm *PipeManager) forwardOutputEvents(sessionName string, pipe *ControlPipe) {
	for {
		select {
		case <-pm.ctx.Done():
			return
		case _, ok := <-pipe.OutputEvents():
			if !ok {
				return
			}
			if pm.onOutput != nil {
				pm.onOutput(sessionName)
			}
		case _, ok := <-pipe.WindowEvents():
			if !ok {
				return
			}
			if pm.onWindowChange != nil {
				pm.onWindowChange()
			}
		case <-pipe.Done():
			return
		}
	}
}

// shouldConcludeSessionGone decides whether a failed has-session probe during
// reconnect means the session is permanently gone, or just a transient miss to
// retry. A tmux server that is briefly busy (e.g. tearing down another session)
// can make the probe report absent for a session that is actually alive, so a
// single early miss must not delete the pipe. Only a probe still absent on the
// final attempt — after the retry/backoff window lets contention clear —
// concludes the session is gone.
func shouldConcludeSessionGone(probeFoundSession bool, attempt, maxRetries int) bool {
	if probeFoundSession {
		return false
	}
	return attempt >= maxRetries-1
}

// wantsReconnect reports whether a dead pipe for sessionName should be
// reconnected. nil predicate => yes (legacy). A false result means the session
// fell out of the live set (intentional Disconnect, or a background pipe died)
// and must stay gone.
func wantsReconnect(wantPipe func(string) bool, sessionName string) bool {
	return wantPipe == nil || wantPipe(sessionName)
}

// watchPipe monitors a pipe and attempts reconnection when it dies.
// Uses exponential backoff: 2s, 4s, 8s, 16s, 30s max.
// Stops retrying if the tmux session no longer exists.
func (pm *PipeManager) watchPipe(sessionName string, pipe *ControlPipe) {
	select {
	case <-pipe.Done():
		// Pipe died
	case <-pm.ctx.Done():
		return
	}

	pipeLog.Debug("pipe_died_scheduling_reconnect", slog.String("session", sessionName))

	// If the session is no longer wanted (intentional Disconnect, or a
	// background pipe that died), do not resurrect it.
	pm.mu.RLock()
	wantFn := pm.wantPipe
	pm.mu.RUnlock()
	if !wantsReconnect(wantFn, sessionName) {
		pipeLog.Debug("pipe_not_wanted_skipping_reconnect", slog.String("session", sessionName))
		pm.mu.Lock()
		delete(pm.pipes, sessionName)
		pm.mu.Unlock()
		return
	}

	// Check if already reconnecting
	pm.reconnectMu.Lock()
	if pm.reconnecting[sessionName] {
		pm.reconnectMu.Unlock()
		return
	}
	pm.reconnecting[sessionName] = true
	pm.reconnectMu.Unlock()

	defer func() {
		pm.reconnectMu.Lock()
		delete(pm.reconnecting, sessionName)
		pm.reconnectMu.Unlock()
	}()

	backoff := 2 * time.Second
	maxBackoff := 30 * time.Second
	maxRetries := 5

	for attempt := 0; attempt < maxRetries; attempt++ {
		select {
		case <-pm.ctx.Done():
			return
		case <-time.After(backoff):
		}

		// Wantedness can flip during backoff (the session fell out of the live
		// set, or was intentionally disconnected). Re-check before reconnecting:
		// otherwise pm.Connect silently no-ops on an unwanted session, returns
		// nil, and we'd log a phantom "reconnected" while leaving the dead pipe
		// entry in the map. Prune it and stop instead.
		pm.mu.RLock()
		loopWantFn := pm.wantPipe
		pm.mu.RUnlock()
		if !wantsReconnect(loopWantFn, sessionName) {
			pipeLog.Debug("pipe_not_wanted_skipping_reconnect", slog.String("session", sessionName))
			pm.mu.Lock()
			delete(pm.pipes, sessionName)
			pm.mu.Unlock()
			return
		}

		// Check if session still exists before trying to reconnect.
		// Avoids infinite reconnect loops for deleted/non-existent sessions.
		// Target the same socket the original pipe lived on — checking the
		// default server for a session that lives on an isolated agent-deck
		// socket would answer "no" and silently delete a healthy pipe.
		reconnectSocket := pipe.socketName
		exists := tmuxSessionExistsOnSocket(reconnectSocket, sessionName)
		if shouldConcludeSessionGone(exists, attempt, maxRetries) {
			pipeLog.Debug("pipe_reconnect_session_gone",
				slog.String("session", sessionName),
				slog.String("socket", reconnectSocket))
			pm.mu.Lock()
			delete(pm.pipes, sessionName)
			pm.mu.Unlock()
			return
		}
		if !exists {
			// Probe reported absent but this may be transient tmux-server
			// contention, not a real death. Back off and retry rather than
			// deleting a pipe whose session is still alive (the cascade where
			// one torn-down session flips its neighbors to error).
			pipeLog.Debug("pipe_reconnect_probe_miss_retry",
				slog.String("session", sessionName),
				slog.Int("attempt", attempt+1))
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}

		err := pm.Connect(sessionName, reconnectSocket)
		if err == nil {
			pipeLog.Info("pipe_reconnected", slog.String("session", sessionName))
			return
		}

		pipeLog.Debug("pipe_reconnect_failed",
			slog.String("session", sessionName),
			slog.String("error", err.Error()),
			slog.Int("attempt", attempt+1),
			slog.Duration("next_retry", backoff))

		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}

	pipeLog.Debug("pipe_reconnect_gave_up", slog.String("session", sessionName), slog.Int("max_retries", maxRetries))
	pm.mu.Lock()
	delete(pm.pipes, sessionName)
	pm.mu.Unlock()
}

// killStaleControlClients kills control-mode clients attached to a session
// that are *orphans* — i.e., spawned by a previous agent-deck TUI whose
// process has died, leaving the `tmux -C attach-session` child reparented to
// init / systemd-user / launchd. These accumulate after agent-deck
// crash/SIGKILL, OOM kill, or any exit that bypasses PipeManager.Close()
// (#595).
//
// Critically: control clients owned by a *live sibling* agent-deck TUI
// (instances.allow_multiple=true scenario — e.g. PC + phone-over-SSH) MUST
// be preserved. #927 was the regression where every client whose pid !=
// os.Getpid() was treated as stale, so two simultaneous TUIs would
// SIGTERM each other's pipes in a loop and brick every session inside ~20s.
//
// See isControlClientOrphan for how orphans are distinguished from live
// siblings.
func killStaleControlClients(sessionName, socketName string) {
	// Once per run, also sweep orphaned one-shot *command* clients (poll/query/
	// status set-option) that this function's control-mode-only filter can never
	// reach. Off this goroutine — see startOrphanReapOnce.
	startOrphanReapOnce()

	// Bounded — see tmuxPollTimeout. This is the sweep that reaps stale
	// clients; if its own enumeration hangs on an fd-exhausted client, the
	// cleanup path becomes another leak source instead of a fix.
	out, err := runBoundedOutput(socketName,
		"list-clients", "-t", sessionName,
		"-F", "#{client_control_mode} #{client_pid}",
	)
	if err != nil {
		return // session may not exist or no clients attached
	}
	budget, cancel := context.WithTimeout(context.Background(), staleControlSweepBudget)
	defer cancel()
	reapStaleControlClients(budget, string(out), sessionName)
}

// SweepStaleControlClients reaps orphaned control-mode clients across EVERY
// session on the tmux server selected by socketName (pass "" for the default
// server), not just one named session. killStaleControlClients only sweeps the
// single session passed to PipeManager.Connect(), so orphans belonging to
// sessions the TUI never reconnects to accumulate indefinitely: each prior
// crashed / SIGKILL'd / OOM-killed TUI leaves one orphaned `tmux -C` client
// per session, and only the sessions actively reopened ever get cleaned.
// Observed in the wild as 176 orphaned control clients exhausting the macOS
// pty cap (kern.tty.ptmx_max=511), blocking all new tmux/terminal sessions.
//
// Run once at TUI startup, this server-wide sweep clears the entire backlog
// left by previous dead TUIs. Live sibling TUIs' clients
// (instances.allow_multiple=true) are preserved via the same
// isControlClientOrphan check used by killStaleControlClients (#927).
//
// Bounded by staleControlSweepTimeout: this runs on the boot path, so a hung or
// unresponsive tmux server must not stall startup. A timed-out (or otherwise
// failed) list-clients is treated as best-effort and skipped — the next launch
// sweeps again.
func SweepStaleControlClients(socketName string) {
	budget, cancel := context.WithTimeout(context.Background(), staleControlSweepBudget)
	defer cancel()

	// The query gets its own, tighter cap nested inside the budget: an
	// unresponsive server must not spend the whole allowance before a single
	// client has been looked at.
	queryCtx, cancelQuery := context.WithTimeout(budget, staleControlSweepTimeout)
	defer cancelQuery()
	out, err := tmuxExecContext(queryCtx, socketName,
		"list-clients",
		"-F", "#{client_control_mode} #{client_pid}",
	).Output()
	if err != nil {
		return // no server running, no clients attached, or the probe timed out
	}
	reapStaleControlClients(budget, string(out), "(all-sessions)")
}

// staleControlSweepBudget is the aggregate deadline for one stale-control-client
// sweep: the identity reads AND the SIGTERM/grace of every victim, not just the
// enumeration. It is deliberately far larger than staleControlSweepTimeout,
// because the two bound different risks — the query cap stops one wedged server
// from stalling the sweep, while this one stops the sweep as a whole from
// stalling the boot path.
//
// Sized to clear a real backlog rather than to be tight. The dominant cost is
// controlClientKillGrace per victim, and only for a client that ignores
// SIGTERM: softKillProcessChecked polls and returns as soon as the pid is gone,
// so the 176-client case this sweep exists for normally costs milliseconds of
// /proc reads plus prompt exits. Ten seconds leaves that untouched and caps the
// pathological host, where the remainder is reported and swept again next
// launch.
//
// Why this sweep stays synchronous, unlike its sibling: main.go runs it
// immediately BEFORE the TUI starts connecting its own pipes, precisely because
// the failure it cleans up is an exhausted pty table. Backgrounding it would
// let this process start claiming ptys while the backlog is still there, which
// is the ordering the boot-path call exists to guarantee.
var staleControlSweepBudget = 10 * time.Second

// staleControlSweepTimeout bounds the boot-path server-wide list-clients query
// in SweepStaleControlClients so an unresponsive tmux server can't hang startup.
var staleControlSweepTimeout = 2 * time.Second

// reapStaleControlClients parses `list-clients -F "#{client_control_mode}
// #{client_pid}"` output and soft-kills each orphaned control-mode client.
// sessionLabel identifies the sweep scope in the observability logs ("(all-
// sessions)" for the server-wide startup sweep, otherwise the session name).
// Returns the number of clients killed, and the number never examined because
// the budget ran out.
//
// budget bounds the WHOLE sweep, not one probe of it. Before this, the only
// deadline in this area was staleControlSweepTimeout on the `list-clients`
// query that feeds this function; everything after it — one identity read per
// listed pid, then a SIGTERM and up to controlClientKillGrace per victim — ran
// unbounded and synchronously on the boot path. Over the backlog this function
// exists for (176 orphaned clients, see SweepStaleControlClients) that is a
// startup stall measured in minutes, on a host already in trouble. Commit
// 3a5af543 makes this argument to get the orphan sweep off the Connect path;
// the same reasoning applies here and was not applied.
//
// Candidates left unexamined are counted and logged rather than dropped
// quietly: the next launch sweeps again, and a sweep that quietly narrows
// itself toward inert is the original failure of this whole area.
func reapStaleControlClients(budget context.Context, listOutput, sessionLabel string) (killed, unexamined int) {
	myPID := os.Getpid()

	// Track burst stats so production logs surface how often this fires N>0
	// SIGTERMs across parallel Connect() calls. The cascade pattern (multiple
	// SIGTERMs within tens of milliseconds, across concurrent Connect()
	// goroutines) is the trigger shape for tmux/tmux#4980's server-side
	// use-after-free in control_notify_client_detached. The Debug-level
	// killed_stale_control_client log emits per-PID; the Info line below
	// surfaces the cascade as a single observable event.
	burstStart := time.Now()

	// Pass 1: identify every pid in the snapshot BEFORE anything is killed.
	//
	// The two passes are the point. Killing is sequential and each victim can
	// hold the loop for a full grace period, so with the capture inline the Nth
	// pid's identity would be read N × grace after tmux listed it — and a pid
	// recycled during an earlier victim's grace would be identified as its NEW
	// occupant, which then matches at signal time and is killed. This sweep has
	// no comm check and no live-server query to catch that (unlike the orphan
	// sweep); isControlClientOrphan alone would not, since it says "orphan" for
	// any process not parented to an agent-deck binary. Capturing everything up
	// front shrinks the snapshot-to-identity gap to the microseconds it takes to
	// read /proc, and the re-check inside softKillProcessChecked covers the rest
	// of the wait.
	type staleControlClient struct {
		pid      int
		identity string
	}
	candidates := make([]staleControlClient, 0, 8)
	for _, line := range strings.Split(strings.TrimSpace(listOutput), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 || parts[0] != "1" {
			continue // not a control-mode client
		}
		var pid int
		if _, err := fmt.Sscanf(parts[1], "%d", &pid); err != nil || pid == 0 {
			continue
		}
		if pid == myPID {
			continue // don't kill our own process
		}
		if budget.Err() != nil {
			unexamined++
			continue
		}
		identity, err := processIdentityOf(budget, pid)
		if err != nil {
			// Fail closed: a pid we cannot identify is not signalled. See
			// stillSameIncarnation.
			pipeLog.Warn("skipped_unidentifiable_control_client_pid",
				slog.String("session", logging.SanitizeValue(sessionLabel)),
				slog.Int("pid", pid),
				slog.String("reason", "could not read a start-time identity for this pid; "+
					"refusing to signal a pid that cannot be tied to the client tmux reported"))
			continue
		}
		candidates = append(candidates, staleControlClient{pid: pid, identity: identity})
	}

	// Pass 2: judge and kill. A verdict here can rest on /proc reads taken after
	// an earlier victim's grace, so it can in principle describe a different
	// process than the one listed — but nothing is signalled on the strength of
	// it: softKillProcessChecked re-checks the identity captured in pass 1
	// immediately before each signal, and a pid that changed hands is refused.
	for _, c := range candidates {
		pid, identity := c.pid, c.identity
		if budget.Err() != nil {
			unexamined++
			continue
		}
		switch verdict := controlClientOrphanOf(pid); verdict {
		case parentageUnknown:
			// This sweep has no comm filter and no live-server query, so the
			// parentage check is the only thing between a list-clients line and
			// a SIGTERM. An indeterminate answer is not a licence to send one.
			pipeLog.Warn("skipped_unknown_parentage",
				slog.String("session", logging.SanitizeValue(sessionLabel)),
				slog.Int("pid", pid),
				slog.String("reason", "could not establish whether a live agent-deck TUI owns "+
					"this control client (its parent pid, that parent's liveness, or that "+
					"parent's executable could not be read); refusing to signal rather than "+
					"assume the owner is gone"))
			continue
		case parentageCandidateGone:
			pipeLog.Debug("skipped_control_client_already_gone",
				slog.String("session", logging.SanitizeValue(sessionLabel)),
				slog.Int("pid", pid))
			continue
		case parentageOwned:
			// Live sibling TUI — leave its pipe alone. Without this guard
			// two concurrent agent-deck TUIs (allow_multiple=true) would
			// SIGTERM each other's control clients on every reconnect (#927).
			pipeLog.Debug("preserved_live_sibling_control_client",
				slog.String("session", logging.SanitizeValue(sessionLabel)),
				slog.Int("pid", pid))
			continue
		case parentageOrphaned:
			// The only verdict that reaches a signal. Fall through.
		default:
			// A verdict added later and not handled here must never reach the
			// kill by default.
			pipeLog.Warn("skipped_unhandled_parentage_verdict",
				slog.String("session", logging.SanitizeValue(sessionLabel)),
				slog.Int("pid", pid),
				slog.Int("verdict", int(verdict)))
			continue
		}
		// Soft-kill the stale control-mode client process.
		// On macOS Homebrew tmux 3.6a there is an unfixed NULL-deref in the
		// control-mode notify path that races with client teardown (#737).
		// SIGKILL'ing a TUI while it holds an active control client can crash
		// the entire tmux server, wiping every agent-deck session. A SIGTERM
		// lets the client drain and exit cleanly; SIGKILL is retained as a
		// 500ms fallback for clients that ignore TERM.
		usedSIGKILL, signalled := softKillProcessChecked(budget, pid, identity, controlClientKillGrace)
		if !signalled {
			// Nothing was sent, so nothing is counted — the burst metric below
			// exists to observe real kill cascades. Two causes share this
			// branch and the event name stays neutral between them: the pid's
			// identity no longer matches (it changed hands since the snapshot),
			// or it was already gone by the time we signalled (ESRCH). Only
			// the first is a recycled pid, and from here they are not
			// distinguishable.
			pipeLog.Debug("skipped_control_client_not_signalled",
				slog.String("session", logging.SanitizeValue(sessionLabel)),
				slog.Int("pid", pid))
			continue
		}
		killed++
		pipeLog.Debug("killed_stale_control_client",
			slog.String("session", logging.SanitizeValue(sessionLabel)),
			slog.Int("pid", pid),
			slog.Bool("used_sigkill", usedSIGKILL))
	}

	if killed > 0 || unexamined > 0 {
		pipeLog.Info("stale_control_clients_swept",
			slog.String("session", logging.SanitizeValue(sessionLabel)),
			slog.Int("kill_count", killed),
			slog.Int("unexamined_out_of_budget", unexamined),
			slog.Duration("duration", time.Since(burstStart)))
	}
	return killed, unexamined
}

// orphanReapStarted ensures the process-wide orphaned-poll-client sweep is
// launched at most once per agent-deck run (on the first session Connect after
// startup), instead of re-scanning all of /proc on every Connect.
var orphanReapStarted atomic.Bool

// orphanReapFn is the seam the launcher tests swap. The sweep itself walks all
// of /proc and signals processes; a test of "is it launched once, and off the
// caller's goroutine" has no business doing either.
var orphanReapFn = reapOrphanedPollClients

// startOrphanReapOnce launches the orphan sweep in the background, at most once
// per run.
//
// In the background because it sits on Connect, and Connect is the fleet-restore
// path: every session opened at startup goes through it. The sweep used to be
// pure /proc reads — microseconds — but it now asks a tmux server about each
// surviving candidate, and those queries are slowest exactly when the host is
// unhealthy, which is the same condition that produces the orphans. Run inline
// behind a once-guard, every other session's Connect blocks on the slowest
// candidate set: two orphans cost seconds, twenty cost a startup nobody would
// describe as working. Nothing on the Connect path consumes the sweep's result,
// so there is nothing to wait for.
//
// A run that exits before the sweep finishes simply leaves the remaining
// orphans for the next launch, which is what a missed sweep has always meant.
func startOrphanReapOnce() {
	if orphanReapStarted.CompareAndSwap(false, true) {
		go orphanReapFn()
	}
}

// orphanSweepBudget is the ONE deadline the whole orphan sweep runs under:
// every external probe it makes — each candidate's identity reads, each tmux
// server query — derives from it, so the sweep's total cost is bounded no
// matter how many candidates /proc offers or how wedged the host is.
//
// The per-probe caps (processProbeTimeout, tmuxLiveQueryTimeout) are nested
// inside it rather than being separate budgets of their own: each probe expires
// at whichever comes first, so one wedged probe cannot eat the whole budget and
// starve every later candidate. The ceiling is this budget plus one candidate:
// the loop checks the budget between candidates, so a candidate already in
// flight when it expires runs to its own caps before the sweep stops.
//
// 20s buys roughly five fully-wedged candidates at the per-probe caps, or many
// hundreds of healthy ones — a live server answers `#{pid}` in milliseconds.
// Overrunning it is not a failure: the remaining candidates are logged as
// unexamined and swept on the next run.
var orphanSweepBudget = 20 * time.Second

// isReapableTmuxClientComm reports whether a /proc/<pid>/comm value identifies
// a tmux CLIENT — the only process class reapOrphanedPollClients may kill.
//
// tmux renames both of its process roles after startup, so comm is "tmux:
// client" / "tmux: server" rather than the bare "tmux" of the argv[0] the
// process was exec'd with. (Verified on tmux 3.0a / Linux 5.4: `pgrep -x tmux`
// matches nothing on a host running twenty tmux processes.) An earlier
// equality test against "tmux" therefore matched no process at all and left
// the sweep inert — orphaned query clients spun at 100% CPU for as long as the
// host stayed up.
//
// The bare "tmux" case is kept for the window between exec and the client's own
// proc_start: a process that has not renamed itself yet is a client that has not
// connected yet, never a server (the server is forked from an already-renamed
// client, so it never presents a bare name).
//
// It is the ROLE token that authorises the kill, not the program name — see
// tmuxCommRole for why a longer argv[0] is still safe to reap when its role
// survives, and isTruncatedTmuxComm for what happens when it does not.
//
// A server MUST NOT match. The sweep SIGKILLs whatever it matches, and a server
// holds every session it hosts, so a false positive there destroys the user's
// running work rather than a leaked one-shot query.
func isReapableTmuxClientComm(comm string) bool {
	trimmed := strings.TrimSpace(comm)
	if trimmed == "tmux" {
		return true
	}
	role, ok := tmuxCommRole(trimmed)
	return ok && role == "client"
}

// tmuxCommRole splits tmux's setproctitle-style comm into its role token.
// tmux formats "<progname>: <role> (<socket path>)", so the role is whatever
// follows the first ": ".
//
// Keying on the role rather than on the literal "tmux: client" keeps the sweep
// working for an installation whose binary is invoked under a longer name: a
// 5-char progname still yields "tmuxx: client", which names its role
// unambiguously and is as safe to reap as the canonical form.
//
// The role either survives whole or vanishes whole — it is never truncated to a
// prefix. The kernel caps comm at 15 bytes and tmux then cuts the result back to
// its last space, which is the one separating the role from the socket path. So
// a partial "clie"/"serve" cannot reach this function, and matching role ==
// "client" can never be satisfied by a server.
func tmuxCommRole(comm string) (string, bool) {
	idx := strings.Index(comm, ": ")
	if idx <= 0 {
		return "", false
	}
	role := comm[idx+2:]
	if role == "" {
		return "", false
	}
	return role, true
}

// isTruncatedTmuxComm reports whether comm looks like tmux's setproctitle output
// whose role token was lost entirely to the 15-byte comm limit.
//
// Measured, not inferred: invoking /usr/bin/tmux through a symlink named
// "tmux-3.5a" produces the comm "tmux-3.5a:" — cut back to the space right after
// the colon, taking "client"/"server" with it. A server under that binary
// produces the identical string, so such a process cannot be classified at all
// and must not be killed.
//
// agent-deck cannot produce one of these itself: every spawn is
// exec.Command("tmux", …), so argv[0] is the literal "tmux" whatever the binary
// is called on disk. The case therefore belongs to a user's own tmux, which the
// sweep has no business killing regardless.
//
// It is still worth reporting. The bug this whole sweep exists to prevent was
// invisible — an inert filter that killed nothing and logged nothing while two
// orphans burned a core each for 14 hours. Anything that silently narrows the
// sweep back toward inert should say so.
func isTruncatedTmuxComm(comm string) bool {
	trimmed := strings.TrimSpace(comm)
	if trimmed == "tmux" {
		return false
	}
	if _, ok := tmuxCommRole(trimmed); ok {
		return false
	}
	return strings.Contains(trimmed, "tmux") && strings.HasSuffix(trimmed, ":")
}

// reapableOneShotVerbs are the tmux subcommands reapOrphanedPollClients may
// kill: the short-lived cadence queries and option writes agent-deck fires on a
// timer, every one of which is safe to lose because its caller re-issues it on
// the next tick. The set mirrors the poll/mutation lists that
// TestPollCommandsAreBounded enforces deadlines for — same commands, same
// reason: they run on a cadence, so they are the ones that leak.
var reapableOneShotVerbs = map[string]struct{}{
	"bind-key":         {},
	"capture-pane":     {},
	"detach-client":    {},
	"display-message":  {},
	"has-session":      {},
	"kill-session":     {},
	"list-clients":     {},
	"list-panes":       {},
	"list-sessions":    {},
	"refresh-client":   {},
	"set-option":       {},
	"show-environment": {},
	"show-option":      {},
	"switch-client":    {},
	"unbind-key":       {},
}

// isKnownCadenceArgv reports whether a /proc/<pid>/cmdline names at least one
// of the one-shot cadence verbs this sweep exists to reap (reapableOneShotVerbs).
// cmdline fields are NUL-separated and NUL-terminated, so each argv element is
// compared whole — a session name or path that merely contains a verb as a
// substring cannot match.
//
// This is a SCOPE ALLOWLIST, not a safety boundary. A miss here only ever
// narrows the sweep (skip, don't reap) — it must never be read as "argv proves
// this is safe to kill". See isLiveTmuxClientOrServer for why: this function
// used to be paired with a denylist (neverReapVerbs, matching only the literal
// strings "attach-session" / "new-session") that WAS treated as a safety
// boundary, and it was wrong to. tmux resolves command names by unambiguous
// prefix — "attach-session" itself is the only tmux command starting with "a",
// so "attach", "attac", "att", "at", even bare "a" all invoke it unmodified
// (verified live: `tmux -C a -t sess` attaches on tmux 3.7b/darwin). A denylist
// of literal verb strings cannot keep up with that; a chained
// `tmux attach -t agentdeck_x \; set-option status on` cleared the old denylist
// (wrong spelling) while "set-option" satisfied this allowlist, so the whole
// line was ruled reapable with a live interactive client sitting behind it.
// isLiveTmuxClientOrServer is the replacement: it asks the tmux server itself
// whether the pid is attached, which cannot be abbreviated around.
func isKnownCadenceArgv(cmdline string) bool {
	for _, field := range strings.Split(strings.TrimRight(cmdline, "\x00"), "\x00") {
		if _, ok := reapableOneShotVerbs[field]; ok {
			return true
		}
	}
	return false
}

// tmuxLiveQueryTimeout caps each server-identity query isLiveTmuxClientOrServer
// issues while classifying an orphan-sweep candidate. It is a cap nested inside
// the sweep's single budget (orphanSweepBudget), not an allowance of its own:
// each query expires at whichever comes first, so a wedged server on one socket
// cannot delay the verdict for candidates on other sockets, and the sweep's
// total cost still cannot exceed the budget however many candidates there are.
// A timeout is already treated as "cannot classify, refuse to kill", so there
// is no correctness reason to wait longer.
var tmuxLiveQueryTimeout = 2 * time.Second

// candidateSocketPath resolves the socket the CANDIDATE is talking to, out of
// the candidate's own environment and its own argv — the two inputs tmux itself
// used when it picked a socket.
//
// This exists because a socket NAME is not a socket. `-L work` resolves to
// $TMUX_TMPDIR/tmux-<uid>/work, so the same name under two bases is two
// different servers, and the name alone cannot tell them apart. Reading the
// candidate's TMUX/TMUX_TMPDIR is the only way to say which one it meant.
//
// ok=false means the candidate's socket could not be established: a different
// mount namespace, or an environment that could not be read — a pid that has
// exited, a zombie (measured on this kernel: the read FAILS with EPERM, or
// ENOENT once reaped), another user's process, or a live process exec'd with a
// genuinely empty environment, which is the one case that reads back
// successfully with nothing in it. All are refusals: a socket that cannot be
// established is not a socket that can be compared.
//
// The uid is this process's, deliberately. A candidate belonging to another uid
// resolves its own -L name under tmux-<theirs>, so the comparison this feeds
// cannot match and the candidate is refused — which is the right answer for a
// process this sweep has no business killing anyway.
func candidateSocketPath(pid int, cmdlineFields []string) (path string, ok bool) {
	// A path is only a name for a file within one mount namespace. A candidate
	// in another one — a container on the same host, same uid — resolves its
	// -L name to a path that is character-for-character ours and names a
	// different socket on a different filesystem. String equality would then
	// call it our server and judge it there, which is the false-match direction:
	// a wrong refusal costs a leaked client, a wrong match costs a live one.
	if !sameMountNamespace(pid) {
		return "", false
	}
	lookupEnv, err := readProcessEnviron(pid)
	if err != nil {
		return "", false
	}
	var args []string
	if len(cmdlineFields) > 1 {
		args = cmdlineFields[1:]
	}
	return normalizeTmpPath(resolveTmuxSocketPath(lookupEnv, os.Getuid(), "", args)), true
}

// sameMountNamespace reports whether pid resolves filesystem paths the same way
// this process does. Anything else — including a namespace that cannot be read —
// is a no.
//
// This costs no reach that candidateSocketPath does not already need:
// /proc/<pid>/ns/mnt and /proc/<pid>/environ are gated by the same
// PTRACE_MODE_READ check, so a candidate whose environment is readable has a
// readable namespace link too (verified on this host under yama
// ptrace_scope=1, for a same-uid process that is not a descendant).
func sameMountNamespace(pid int) bool {
	theirs, err := os.Readlink(fmt.Sprintf("/proc/%d/ns/mnt", pid))
	if err != nil {
		return false
	}
	ours, err := os.Readlink("/proc/self/ns/mnt")
	if err != nil {
		return false
	}
	return theirs == ours
}

// readProcessEnviron reads pid's environment out of procfs as a lookup function
// shaped for resolveTmuxSocketPath.
//
// An empty read is an error, not an empty environment. Treating zero bytes as
// "this process has no TMUX_TMPDIR" would leave every lookup returning "" and
// resolve the candidate to the DEFAULT socket — a confident answer built from
// nothing, which is then used to pick which server to judge it on. Measured
// rather than assumed: a zombie does NOT reach here (its environ read fails),
// and what does read back empty-and-successful is a live process exec'd with no
// environment at all — never one of agent-deck's own clients, which inherit
// os.Environ().
func readProcessEnviron(pid int) (func(string) string, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("empty environment for pid %d", pid)
	}
	env := make(map[string]string, 32)
	for _, entry := range strings.Split(string(raw), "\x00") {
		if key, value, found := strings.Cut(entry, "="); found {
			env[key] = value
		}
	}
	return func(key string) string { return env[key] }, nil
}

// candidateSocketName extracts the -L <name> (or default) socket selector
// from a poll-sweep candidate's OWN argv — cmdlineFields[0] is the program
// name (e.g. "tmux"); a leading `-L <name>` may follow it, mirroring exactly
// what tmuxArgs would have inserted to produce this argv in the first place.
//
// A leading `-S <path>` returns ok=false. agent-deck itself never spawns tmux
// that way (tmuxArgs only ever emits -L; grep the package), so a candidate
// carrying -S was not spawned by agent-deck's own factory — meaning the
// isLiveTmuxClientOrServer caller cannot safely resolve it through the
// package's -L-only tmuxExecContext funnel, and per the "refuse anything
// ambiguous" rule, unresolvable is not reapable rather than a case worth
// widening the tmux-exec factory for.
func candidateSocketName(cmdlineFields []string) (name string, ok bool) {
	if len(cmdlineFields) == 0 {
		return "", true
	}
	rest := cmdlineFields[1:]
	if socketPathFlag(rest) != "" {
		return "", false
	}
	return socketNameFlag(rest), true
}

// unreachableSockets memoizes, for the duration of ONE sweep, the sockets whose
// tmux server could not be reached. It is reset at the start of every sweep.
//
// Caching the ANSWERS to the two identity queries is rejected on purpose (see
// reapOrphanedPollClients' notes): a cached client list goes stale inside a
// sweep, and a client that connects mid-sweep would be absent from it and
// eligible to be killed. Freshness is the safety property there.
//
// Caching the REFUSAL is a different thing. "This socket could not be reached"
// only ever produces more refusals, never a kill, so it is fail-closed by
// construction — and it is the expensive case: an unreachable or wedged server
// is exactly what makes a query ride its full tmuxLiveQueryTimeout, twice per
// candidate. On the incident host that is the difference between judging every
// candidate and spending the whole budget on the first handful, and it saves a
// spawn per skipped query — each of which is another client against a server
// that leaks an fd per client.
//
// It also closes a window rather than opening one: without it, a server that
// appears mid-sweep can answer for a candidate whose absence was judged against
// the socket being empty, and that answer is a kill verdict.
var (
	unreachableSocketsMu sync.Mutex
	unreachableSockets   = map[string]struct{}{}
)

// resetUnreachableSockets clears the memo. Called at the start of each sweep:
// carrying it across sweeps would make a socket that was briefly unreachable
// permanently unreapable, which is the inert-sweep failure this area began with.
func resetUnreachableSockets() {
	unreachableSocketsMu.Lock()
	defer unreachableSocketsMu.Unlock()
	unreachableSockets = map[string]struct{}{}
}

func markSocketUnreachable(socket string) {
	unreachableSocketsMu.Lock()
	defer unreachableSocketsMu.Unlock()
	unreachableSockets[socket] = struct{}{}
}

func socketKnownUnreachable(socket string) bool {
	unreachableSocketsMu.Lock()
	defer unreachableSocketsMu.Unlock()
	_, ok := unreachableSockets[socket]
	return ok
}

// isLiveTmuxClientOrServer authoritatively asks the tmux server that the
// candidate's OWN argv targets whether pid IS that server, or is one of its
// currently-connected clients — the two process classes reapOrphanedPollClients
// must never kill. It replaces argv-verb pattern matching (isKnownCadenceArgv's
// former denylist partner, neverReapVerbs) with the server's own live
// bookkeeping, which cannot be abbreviated or aliased around: see
// isKnownCadenceArgv's doc comment for the concrete bypass that motivated this.
//
// Two independent facts are checked against the server's own state:
//
//  1. pid == the server's own pid (queried as `#{pid}`). A server mid-startup
//     still carries its creating client's comm and argv — tmux renames the
//     client to "tmux: client" before forking, and the child keeps that comm
//     and argv until its own post-daemon() rename — so comm+argv alone cannot
//     rule out "this pid IS the server, not a client of it". Asking the server
//     for its own pid can.
//  2. pid appears in the server's `list-clients` output. A registered client is
//     attached right now — interactively or in control mode — no matter what
//     verb, alias, or abbreviation its argv spells the attach with, because
//     registration happens on connect, not by re-parsing argv. A one-shot
//     cadence command (list-clients, set-option, …) never registers as a
//     persistent client even while its process is alive and spinning —
//     verified live by SIGSTOPping one mid-flight and observing list-clients
//     on the same server come back empty — so absence here rules out an
//     attach specifically, not just "any client-shaped process".
//
// Before either query is issued, the candidate's OWN socket is resolved from
// its own environment and argv (candidateSocketPath) and compared against the
// socket the query will reach. A tmux socket NAME is not a server: `-L work`
// means $TMUX_TMPDIR/tmux-<uid>/work, and this package resolves that name
// through ITS environment, not the candidate's. Same name, different base,
// different server — and asking the wrong server is not a failure that
// announces itself. Both queries succeed, neither the server pid nor any client
// pid matches, and the answer comes back "not live" with full confidence about
// a client that is attached and alive on its own server. A socket that differs,
// or that cannot be established at all, is ok=false.
//
// ok=false means the candidate's server could not be identified as ours, or
// neither fact could be established against it (no server at the resolved
// socket, a -S-path candidate the package's -L-only exec funnel cannot resolve,
// connection refused, or the query ran past tmuxLiveQueryTimeout). The caller
// MUST treat ok=false as unclassifiable and refuse to kill — the same
// fail-closed rule isTruncatedTmuxComm already established for an unreadable
// comm value.
//
// ⚠️ Accepted gap, measured on tmux 3.0a (the version the CPU-spin incident
// happened on): a server that is RUNNING but holds ZERO sessions answers both
// queries with "no current target", because each needs a target session to
// resolve against. That is ok=false, so orphans left behind on a server whose
// sessions have all closed are never reaped — precisely the incident-shaped
// host. Widening it means finding a query that identifies a server without a
// session, and that is a change to make deliberately, not by relaxing a kill
// guard.
//
// A second, narrower window is inherent to asking the server at all: a user's
// own interactive client that has started but not yet registered reads as
// not-live for the width of one query. Parentage does not protect it (its
// parent is the user's shell, so isControlClientOrphan says orphan), so such a
// client can be SIGTERMed if the sweep lands in that gap. It is one query wide,
// happens at most once per agent-deck run, and costs an aborted attach rather
// than a session — the alternative, caching or trusting argv text, is what this
// function exists to replace.
func isLiveTmuxClientOrServer(budget context.Context, pid int, cmdlineFields []string) (live bool, ok bool) {
	socketName, resolvable := candidateSocketName(cmdlineFields)
	if !resolvable {
		return false, false
	}

	// Establish that the server about to be asked is the candidate's server.
	// Without this the two are only assumed to be the same, and when they are
	// not, both queries succeed against a stranger's server, neither pid
	// matches, and the confident answer is "not live" — the kill verdict —
	// about a client that is attached and alive on its own.
	candidateSocket, resolved := candidateSocketPath(pid, cmdlineFields)
	if !resolved {
		return false, false
	}
	// What tmuxExecContext will actually reach: the same -L name, resolved
	// through THIS process's environment, which is the whole asymmetry.
	querySocket := normalizeTmpPath(resolveTmuxSocketPath(os.Getenv, os.Getuid(), socketName, nil))
	if candidateSocket != querySocket {
		return false, false
	}

	if socketKnownUnreachable(querySocket) {
		return false, false
	}

	ctx, cancel := context.WithTimeout(budget, tmuxLiveQueryTimeout)
	defer cancel()
	serverPIDOut, err := tmuxExecContext(ctx, socketName, "display-message", "-p", "#{pid}").Output()
	if err != nil {
		markSocketUnreachable(querySocket)
		return false, false
	}
	serverPID, err := strconv.Atoi(strings.TrimSpace(string(serverPIDOut)))
	if err != nil {
		return false, false
	}
	if serverPID == pid {
		return true, true // pid IS the server
	}

	ctx2, cancel2 := context.WithTimeout(budget, tmuxLiveQueryTimeout)
	defer cancel2()
	clientsOut, err := tmuxExecContext(ctx2, socketName, "list-clients", "-F", "#{client_pid}").Output()
	if err != nil {
		markSocketUnreachable(querySocket)
		return false, false
	}
	target := strconv.Itoa(pid)
	for _, line := range strings.Split(strings.TrimSpace(string(clientsOut)), "\n") {
		if strings.TrimSpace(line) == target {
			return true, true // pid is a currently-registered client
		}
	}
	return false, true
}

// reapOrphanedPollClients kills leaked one-shot tmux *command* clients — the
// `list-clients` / `display-message` / `list-panes` / status `set-option`
// invocations agent-deck fires on a cadence — that a previous run spawned and
// never reaped. killStaleControlClients only sweeps control-mode clients
// (client_control_mode == 1); these short-lived query/option clients are
// invisible to it. When one hangs on a wedged server (tmux 3.0a spins at 100%
// CPU rather than exiting) and its owning TUI then dies, the kernel reparents
// it to init / systemd --user and it burns a whole core indefinitely.
//
// tmuxPollTimeout (Part A) stops NEW leaks by bounding every such command; this
// sweep mops up orphans that predate the current run, or that escaped the
// timeout because the TUI was SIGKILL'd / OOM-killed mid-command.
//
// Safety — a process is killed only when ALL hold:
//   - it is a tmux CLIENT process (isReapableTmuxClientComm; the server
//     renames itself to "tmux: server" and is never matched),
//   - its argv targets an agent-deck session (contains SessionPrefix), so a
//     user's unrelated tmux is never touched,
//   - its argv names a one-shot cadence verb (isKnownCadenceArgv) — a scope
//     narrowing, not the safety check; see its doc comment for why an argv
//     denylist cannot be the safety check,
//   - the tmux server itself confirms, right now, that this pid is neither
//     itself nor one of its connected clients (isLiveTmuxClientOrServer) —
//     this is the actual guarantee that a live interactive/control attach or
//     a server still inside its startup rename window is never hit, and it
//     cannot be defeated by an abbreviated or aliased attach verb because it
//     never inspects argv verbs at all, and
//   - it is a reparented orphan no longer owned by any live agent-deck TUI
//     (isControlClientOrphan — its parentage check is client-type-agnostic
//     despite the name). A live TUI's own in-flight poll has PPID == our PID,
//     so isControlClientOrphan returns false and it is preserved, and
//   - it is still the same process all of the above was decided about. Every
//     check reads /proc or a tmux server for a PID, and a PID is not a stable
//     name for a process; the identity captured before the first of those reads
//     is re-checked immediately before each signal (readOrphanCandidate,
//     softKillProcessChecked). A PID that changes hands anywhere in the
//     gauntlet is refused, not killed on the strength of a dead process's
//     facts.
//
// Any of the two checks that query external state (comm-role classification,
// tmux-server identity) failing to produce a definite answer is treated as
// "cannot classify, refuse to kill" — never "assume safe". That fail-closed
// rule is why isTruncatedTmuxComm and isLiveTmuxClientOrServer both return an
// explicit ok/unclassifiable signal instead of collapsing into a bool.
//
// Linux-only: relies on procfs. On darwin/BSD it is a no-op (a `ps`-based
// enumeration would be the port); the tmuxPollTimeout guard still applies
// there, so new leaks are prevented regardless.
func reapOrphanedPollClients() {
	if runtime.GOOS != "linux" {
		return
	}
	start := time.Now()
	resetUnreachableSockets()
	budget, cancel := context.WithTimeout(context.Background(), orphanSweepBudget)
	defer cancel()
	candidates, unidentifiable := collectOrphanCandidates(budget)
	killed, unclassifiable, notSignalled, unknownParent, unexamined := sweepOrphanCandidates(budget, candidates)
	if killed > 0 || unclassifiable > 0 || notSignalled > 0 || unidentifiable > 0 || unknownParent > 0 || unexamined > 0 {
		// One line per sweep that did anything at all, kills included or not:
		// the counters are the only place a run that refused everything is
		// visible at Info level, and "the sweep went inert and nobody noticed"
		// is the failure this whole area exists to prevent.
		pipeLog.Info("orphan_sweep_finished",
			slog.Int("kill_count", killed),
			slog.Int("skipped_unclassifiable", unclassifiable),
			slog.Int("skipped_not_signalled", notSignalled),
			slog.Int("skipped_unidentifiable", unidentifiable),
			slog.Int("skipped_unknown_parentage", unknownParent),
			slog.Int("unexamined_out_of_budget", unexamined),
			slog.Duration("duration", time.Since(start)))
	}
}

// orphanCandidate is one process the sweep has read out of /proc and not yet
// judged: the facts its verdict rests on, plus the identity that ties those
// facts to a single incarnation of the pid.
type orphanCandidate struct {
	pid      int
	comm     string
	cmdline  string
	identity string
}

// readOrphanCandidate reads the facts for one /proc entry, and returns them only
// if they all describe the same incarnation of pid.
//
// The identity is captured BEFORE the reads that classify the process and read
// again after them. That bracketing is the point. Capturing it later — at kill
// time, as an earlier revision did — defeats the guard in exactly the case it
// exists for: if the pid changes hands while the sweep is working, the classify
// reads describe the process that died and the identity describes the one that
// took its number, so the check at signal time compares the new process against
// itself and agrees. Bracketing means either every fact belongs to one process
// or the candidate is dropped.
//
// ok=false means "not a candidate, or not readable as one consistent process".
// identifiable=false narrows that to the cases the caller must report rather
// than pass over: no identity could be read, or the two reads disagreed. Both
// mean a process that might be a leak was left alone, and a sweep quietly
// narrowing itself back toward inert is the original failure of this code.
func readOrphanCandidate(budget context.Context, pid int) (c orphanCandidate, identifiable, ok bool) {
	// Cheap pre-filter first. /proc holds every process on the host and only a
	// handful can ever be tmux clients, so the two identity reads below are
	// paid for only the few entries that get past this. This read carries no
	// safety weight — the comm the verdict rests on is the bracketed one.
	preComm, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return c, true, false
	}
	if !isReapableTmuxClientComm(string(preComm)) && !isTruncatedTmuxComm(string(preComm)) {
		return c, true, false
	}

	before, err := processIdentityOf(budget, pid)
	if err != nil {
		return c, false, false
	}
	comm, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return c, true, false // exited mid-read; nothing to judge and nothing to kill
	}
	cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return c, true, false
	}
	after, err := processIdentityOf(budget, pid)
	if err != nil || after != before {
		return c, false, false
	}

	return orphanCandidate{
		pid:      pid,
		comm:     string(comm),
		cmdline:  string(cmdline),
		identity: before,
	}, true, true
}

// collectOrphanCandidates walks /proc and returns every entry that could be a
// leaked one-shot client, together with a count of the entries that looked like
// one but could not be pinned to a single incarnation (see readOrphanCandidate).
//
// Enumerating first, judging second, keeps the /proc walk short: the judging
// half queries a tmux server per candidate and can block for as long as the
// sweep's budget allows, and holding a directory listing of every process on
// the host open across that is pointless.
func collectOrphanCandidates(budget context.Context) (candidates []orphanCandidate, unidentifiable int) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, 0
	}
	myPID := os.Getpid()
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == myPID {
			continue
		}
		c, identifiable, ok := readOrphanCandidate(budget, pid)
		if !identifiable {
			unidentifiable++
			pipeLog.Warn("orphan_sweep_skipped_unidentifiable_pid",
				slog.Int("pid", pid),
				slog.String("reason", "a tmux-shaped process whose start-time identity could not be "+
					"read, or changed between reads; its facts cannot be tied to one process, so it "+
					"is left alone rather than judged as a chimera"))
			continue
		}
		if !ok {
			continue
		}
		candidates = append(candidates, c)
	}
	return candidates, unidentifiable
}

// liveTmuxIdentityOf is the seam the sweep tests swap for
// isLiveTmuxClientOrServer. The real one needs a live tmux server to answer
// (see orphan_reap_live_identity_test.go, which exercises it against one); the
// sweep tests are about what the gauntlet does with the answer, and about the
// pid changing hands while that answer is being fetched.
var liveTmuxIdentityOf = isLiveTmuxClientOrServer

// sweepOrphanCandidates runs the kill gauntlet over already-read candidates.
//
// The three refusal counts are kept apart because they mean different things to
// whoever is reading the log: unclassifiable is "the tmux server or the comm
// could not tell us what this is", notSignalled is "we decided to kill it and
// then it stopped being the process we decided about", and unexamined is "we
// ran out of budget". Each refusal also logs a line of its own — Debug for the
// routine ones, Warn for the ones that mean the sweep is losing ground — and
// every one of them reaches Info level through the caller's summary, so a run
// that refused everything can never look like a run that found nothing.
func sweepOrphanCandidates(budget context.Context, candidates []orphanCandidate) (killed, unclassifiable, notSignalled, unknownParent, unexamined int) {
	for i, c := range candidates {
		if err := budget.Err(); err != nil {
			// Out of budget. Report what was left unexamined rather than
			// returning counts that read like a clean sweep — this whole area
			// exists because a sweep that quietly did nothing looked exactly
			// like one that found nothing.
			unexamined = len(candidates) - i
			pipeLog.Warn("orphan_sweep_budget_exhausted",
				slog.Int("unexamined", unexamined),
				slog.Int("examined", i),
				slog.String("reason", "the sweep's total deadline (orphanSweepBudget) passed before every "+
					"candidate was judged; the remaining ones are left for the next run"))
			break
		}
		reapable := isReapableTmuxClientComm(c.comm)
		// A comm whose role token was truncated away cannot be classified as
		// client or server. It is not killed; it is carried to the end of the
		// gauntlet so that a process which is otherwise indistinguishable from
		// a leak gets reported instead of vanishing from the sweep in silence.
		commUnclassifiable := !reapable && isTruncatedTmuxComm(c.comm)
		if !reapable && !commUnclassifiable {
			// Neither, despite passing the collector's pre-filter: tmux renames
			// itself from the bare "tmux" it was exec'd as to "tmux: server"
			// shortly after startup, so a candidate admitted on the bare name
			// can read as a server by the time its facts are taken. Same
			// process, later name — and a server is never a kill candidate.
			continue
		}
		// cmdline fields are NUL-separated; substring search still matches the
		// "agentdeck_" target token regardless of separators.
		if !strings.Contains(c.cmdline, SessionPrefix) {
			continue
		}
		// Scope narrowing only — see isKnownCadenceArgv's doc comment for why
		// this must never be read as the safety check.
		if !isKnownCadenceArgv(c.cmdline) {
			continue
		}
		if commUnclassifiable {
			// Decided already, so decide it here rather than after the tmux
			// query: this candidate can never be killed, and the query is the
			// most expensive step in the gauntlet — up to 2 × tmuxLiveQueryTimeout
			// against exactly the wedged server that produces these candidates.
			// Spending a fifth of the sweep's whole budget on an answer that is
			// then discarded starves the candidates that could still be judged,
			// and on a host whose tmux is invoked under a longer name (the
			// measured "tmux-3.5a:" case) there can be a handful of these.
			//
			// The reporting is unchanged, which is the point of refusing here
			// rather than skipping silently.
			unclassifiable++
			pipeLog.Warn("orphan_sweep_skipped_unclassifiable_tmux",
				slog.Int("pid", c.pid),
				slog.String("comm", logging.SanitizeValue(strings.TrimSpace(c.comm))),
				slog.String("reason", "comm lost its role token to truncation; "+
					"cannot prove this is a client rather than a server, so it is left alone"))
			continue
		}
		// THE safety check: ask the tmux server itself, not argv text, whether
		// this pid is live. live==true or ok==false both mean "do not kill".
		cmdlineFields := strings.Split(strings.TrimRight(c.cmdline, "\x00"), "\x00")
		live, ok := liveTmuxIdentityOf(budget, c.pid, cmdlineFields)
		if !ok {
			unclassifiable++
			pipeLog.Warn("orphan_sweep_skipped_unclassifiable_tmux",
				slog.Int("pid", c.pid),
				slog.String("comm", logging.SanitizeValue(strings.TrimSpace(c.comm))),
				slog.String("reason", "could not confirm via the tmux server itself whether this "+
					"pid is live (the candidate's own socket is not the one this process "+
					"resolves that name to — a changed TMUX_TMPDIR does this, and every orphan "+
					"from the old base stays unreapable until it is restored; the candidate's "+
					"environment or mount namespace could not be read; no server at the resolved "+
					"socket; a running server holding no sessions, which cannot answer either "+
					"query on tmux 3.0a; an unresolvable -S candidate; or the query timed out); "+
					"refusing to kill rather than guess"))
			continue
		}
		if live {
			pipeLog.Debug("preserved_live_tmux_client_or_server",
				slog.Int("pid", c.pid),
				slog.String("comm", logging.SanitizeValue(strings.TrimSpace(c.comm))))
			continue
		}
		switch verdict := controlClientOrphanOf(c.pid); verdict {
		case parentageUnknown:
			unknownParent++
			pipeLog.Warn("orphan_sweep_skipped_unknown_parentage",
				slog.Int("pid", c.pid),
				slog.String("comm", logging.SanitizeValue(strings.TrimSpace(c.comm))),
				slog.String("reason", "could not establish whether a live agent-deck TUI owns "+
					"this client (its parent pid, that parent's liveness, or that parent's "+
					"executable could not be read); refusing to kill rather than guess"))
			continue
		case parentageCandidateGone:
			// Not a refusal: it exited while the gauntlet ran, which is routine.
			pipeLog.Debug("orphan_sweep_candidate_already_gone", slog.Int("pid", c.pid))
			continue
		case parentageOwned:
			continue // owned by a live agent-deck TUI (incl. a sibling) — keep
		case parentageOrphaned:
			// The only verdict that reaches a signal. Fall through.
		default:
			unknownParent++
			pipeLog.Warn("orphan_sweep_skipped_unhandled_parentage_verdict",
				slog.Int("pid", c.pid),
				slog.Int("verdict", int(verdict)))
			continue
		}
		// The identity was captured before any of the above ran, and the tmux
		// query in particular can take seconds. softKillProcessChecked re-checks
		// it immediately before each signal, so a pid that changed hands while
		// the gauntlet was running is refused here rather than killed on the
		// strength of facts that belong to a process which no longer exists.
		usedSIGKILL, signalled := softKillProcessChecked(budget, c.pid, c.identity, controlClientKillGrace)
		if !signalled {
			notSignalled++
			pipeLog.Debug("orphan_sweep_candidate_not_signalled",
				slog.Int("pid", c.pid),
				slog.String("reason", "the pid no longer holds the identity it was judged under, "+
					"or was already gone"))
			continue
		}
		killed++
		pipeLog.Debug("reaped_orphaned_poll_client",
			slog.Int("pid", c.pid),
			slog.Bool("used_sigkill", usedSIGKILL))
	}
	return killed, unclassifiable, notSignalled, unknownParent, unexamined
}

// isControlClientOrphan reports whether the control-mode client pid is a
// stale orphan (its owning agent-deck TUI is gone) vs a live sibling TUI's
// active pipe.
//
// Signal: control clients are direct children of the TUI that spawned them
// via `exec.Command("tmux", "-C", "attach-session", ...)`. While the TUI is
// alive, the client's PPID equals that TUI's pid and that pid's executable
// path matches the agent-deck binary (== os.Executable() for any other
// running TUI on the same host, or the test binary in tests). When the TUI
// crashes the kernel reparents the client to PID 1 (init) or a session
// subreaper such as `systemd --user` / `launchd` — none of which match
// agent-deck.
//
// known=false means the parentage could not be established at all, and the
// caller MUST refuse to signal. This gate used to answer "orphan, sweep it" for
// every read failure, on the reasoning that the behaviour it replaced was "kill
// anything that isn't me" so falling back to it regressed nothing. That reads
// the failure the wrong way round: an unreadable parent is exactly as
// consistent with a live sibling TUI as with a dead one, and the sweeps break
// that tie by killing. It is also the same fail-closed rule the rest of this
// gauntlet already follows — isTruncatedTmuxComm for an unreadable comm,
// isLiveTmuxClientOrServer's ok=false for an unreachable server,
// stillSameIncarnation for an unreadable identity. Refusing costs a leaked
// client that the next sweep re-examines; guessing costs a live client.
//
// Two verdicts are determinations rather than read failures and stay orphan, or
// the #595 cleanup this function exists for goes inert:
//
//   - PPID <= 1: the kernel has already reparented the client to init.
//   - ESRCH from the liveness probe: the kernel confirming the parent is gone,
//     i.e. the client is being orphaned right now. Any OTHER probe error is a
//     read failure — EPERM in particular means the parent EXISTS and belongs to
//     someone else, which is the opposite of what this branch used to conclude.
//
// Why not a heartbeat file: would need TUI-startup wiring + a refresh
// goroutine + lifecycle cleanup. The PPID+exe check is zero-state and
// agrees on the same answer.
func isControlClientOrphan(pid int) parentageVerdict {
	return isControlClientOrphanFor(readParentPID, probeProcessAlive, readProcessExe, pid)
}

// parentageVerdict is what the parentage gate can conclude. Four answers rather
// than a bool, because three of them mean "do not signal" for reasons an
// operator must be able to tell apart: only one of them is a failure.
type parentageVerdict int

const (
	// parentageUnknown: the facts could not be read. Refuse, and say so loudly —
	// this is the one that means something is wrong with the host or with us.
	parentageUnknown parentageVerdict = iota
	// parentageOrphaned: the owning TUI is gone. The only verdict that
	// authorises a signal.
	parentageOrphaned
	// parentageOwned: a live agent-deck TUI (possibly a sibling) owns it.
	parentageOwned
	// parentageCandidateGone: the candidate itself no longer exists. Ordinary
	// churn — candidates die during the gauntlet all the time, because earlier
	// victims' grace periods run in between — and NOT a failure to establish
	// anything. Reporting it as unknown would put a safety-shaped Warn on
	// routine sweeps and bury the ones that matter.
	parentageCandidateGone
)

// reapable reports whether this verdict authorises a signal. Exactly one does.
func (v parentageVerdict) reapable() bool { return v == parentageOrphaned }

// controlClientOrphanOf is the seam both sweeps call through, so their handling
// of an indeterminate verdict can be tested without a host on which /proc reads
// genuinely fail. isControlClientOrphanFor covers the decision itself.
var controlClientOrphanOf = isControlClientOrphan

// probeProcessAlive asks the kernel whether pid still exists, without signalling
// it. Split out so isControlClientOrphanFor can be driven through every errno
// that matters.
func probeProcessAlive(pid int) error { return syscall.Kill(pid, 0) }

// isControlClientOrphanFor is the injectable core of isControlClientOrphan.
func isControlClientOrphanFor(
	parentPID func(int) (int, error),
	probeAlive func(int) error,
	processExe func(int) (string, error),
	pid int,
) parentageVerdict {
	ppid, err := parentPID(pid)
	if err != nil {
		// Before calling this a failure, ask the cheaper question: is the
		// CANDIDATE still there? A pid that exited has no parent to report, and
		// that is a determination about a process with nothing left to kill —
		// not a host that cannot answer.
		if errors.Is(probeAlive(pid), syscall.ESRCH) {
			return parentageCandidateGone
		}
		return parentageUnknown
	}
	if ppid <= 1 {
		// PPID == 1 means the kernel has already reparented the client to
		// init — definitively an orphan.
		return parentageOrphaned
	}
	// Liveness check on the parent. If the parent died between the list-clients
	// call and now, the client is in the process of being orphaned right now.
	if err := probeAlive(ppid); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return parentageOrphaned
		}
		// The parent is there and we cannot look at it (EPERM), or the probe
		// failed for a reason we cannot name. Neither says "orphan".
		return parentageUnknown
	}
	parentExe, err := processExe(ppid)
	if err != nil {
		// Linux without /proc-read permission, or macOS with `ps` failing. The
		// parent is alive; we simply cannot tell whose it is.
		return parentageUnknown
	}
	if looksLikeAgentDeckBinary(parentExe) {
		return parentageOwned
	}
	return parentageOrphaned
}

// looksLikeAgentDeckBinary returns true when exePath plausibly refers to an
// agent-deck process. Strongest signal: exact path match against
// os.Executable() (covers prod, where every TUI runs the same binary, and
// `go test`, where the parent's exe == the running test binary). Fallback:
// basename heuristic for renamed installations and the `go test` package
// binary name (`tmux.test` doesn't contain "agent-deck" but for production
// the basename will).
func looksLikeAgentDeckBinary(exePath string) bool {
	if exePath == "" {
		return false
	}
	if self, err := os.Executable(); err == nil {
		if exePath == self {
			return true
		}
		// Resolve symlinks in case one path is canonical and the other
		// isn't (e.g. /usr/local/bin/agent-deck -> /opt/...).
		if a, errA := filepath.EvalSymlinks(exePath); errA == nil {
			if b, errB := filepath.EvalSymlinks(self); errB == nil && a == b {
				return true
			}
		}
	}
	base := filepath.Base(exePath)
	return strings.Contains(base, "agent-deck")
}

// readParentPID returns the parent PID for pid. Prefers /proc/<pid>/stat on
// Linux (no fork); falls back to `ps -p <pid> -o ppid=` on macOS (or
// Linux without /proc access).
func readParentPID(pid int) (int, error) {
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
		// stat format: "pid (comm-with-possible-spaces-and-parens) state ppid ..."
		// The process name field may contain ')' so we split on the LAST one.
		idx := strings.LastIndex(string(data), ")")
		if idx < 0 {
			return 0, fmt.Errorf("malformed /proc/%d/stat", pid)
		}
		fields := strings.Fields(string(data[idx+1:]))
		if len(fields) < 2 {
			return 0, fmt.Errorf("/proc/%d/stat: too few fields", pid)
		}
		return strconv.Atoi(fields[1])
	}
	// #nosec G204 -- "ps" is a fixed binary; only arg is strconv.Itoa(int).
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "ppid=").Output()
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(out)))
}

// readProcessExe returns the executable path for pid. Prefers
// /proc/<pid>/exe readlink on Linux (full path, never truncated); falls back
// to `ps -p <pid> -o comm=` on macOS or when /proc is unavailable. The `ps`
// fallback may truncate to 16 chars on Linux but is full-width on macOS
// (the macOS comm column is the full path).
func readProcessExe(pid int) (string, error) {
	if exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid)); err == nil {
		return exe, nil
	}
	// #nosec G204 -- "ps" is a fixed binary; only arg is strconv.Itoa(int).
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// readProcessIdentity returns a token that distinguishes one incarnation of a
// PID from the next. A PID alone does not identify a process: the kernel
// reissues it once the number wraps, so a PID read out of a `list-clients`
// snapshot can belong to something else by the time it is signalled.
//
// Linux uses starttime (field 22 of /proc/<pid>/stat) — the boot-relative tick
// the process started on, which the kernel never rewrites. Two incarnations of
// the same PID cannot share it in practice: reuse requires wrapping the whole
// pid_max range, which takes far longer than the field's 10ms resolution.
// macOS (or a Linux host without /proc) falls back to `ps -o lstart=`, the same
// fact at second resolution — which is the fallback's one real weakness: a pid
// reused inside the same second reads as the same incarnation there, and the
// guard passes it through. That is still strictly better than the bare pid it
// replaces, and Linux, where this sweep's worst incident happened, gets the
// 10ms field.
//
// Read errors are returned rather than swallowed: an unreadable identity is
// the caller's signal that the guard cannot be armed, which is a different
// state from "the identity changed" and is handled differently — see
// softKillProcessChecked.
func readProcessIdentity(budget context.Context, pid int) (string, error) {
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
		// Same parse as readParentPID: the comm field can contain ')', so
		// split after the LAST one. Fields then start at state (field 3), so
		// starttime (field 22) sits at index 19.
		idx := strings.LastIndex(string(data), ")")
		if idx < 0 {
			return "", fmt.Errorf("malformed /proc/%d/stat", pid)
		}
		fields := strings.Fields(string(data[idx+1:]))
		const startTimeIndex = 19
		if len(fields) <= startTimeIndex {
			return "", fmt.Errorf("/proc/%d/stat: too few fields", pid)
		}
		return fields[startTimeIndex], nil
	}
	// Bounded, unlike the sibling ps probes, and bounded by the CALLER's budget
	// as well as by its own cap. A deadline miss surfaces as an error, which
	// fails closed: the pid is not signalled, and the caller reports it.
	ctx, cancel := context.WithTimeout(budget, processProbeTimeout)
	defer cancel()
	// #nosec G204 -- "ps" is a fixed binary; only arg is strconv.Itoa(int).
	out, err := exec.CommandContext(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "lstart=").Output()
	if err != nil {
		return "", err
	}
	identity := strings.TrimSpace(string(out))
	if identity == "" {
		return "", fmt.Errorf("no start time for pid %d", pid)
	}
	return identity, nil
}

// processIdentityOf is the seam the PID-reuse tests swap to make a pid appear
// to change hands mid-grace. Forcing a real reuse would take a full pid_max
// wrap inside a 100ms window.
var processIdentityOf = readProcessIdentity

// processProbeTimeout caps the `ps` fallback in readProcessIdentity. Like
// tmuxLiveQueryTimeout it nests inside whatever budget the caller passes — the
// sweep's single deadline, or a background context for callers that have none —
// so one wedged probe can spend at most this much of it. Matches
// staleControlSweepTimeout, which bounds the list-clients query feeding the
// same sweep.
var processProbeTimeout = 2 * time.Second

// controlClientKillGrace is how long softKillProcess waits after SIGTERM
// before escalating to SIGKILL. 500ms matches empirical clean-shutdown
// times for `tmux -C attach-session` on macOS + Linux.
const controlClientKillGrace = 500 * time.Millisecond

// softKillProcessHandle sends SIGTERM to proc, polls up to grace for it to
// exit, and escalates to SIGKILL if it doesn't. Returns true iff SIGKILL was
// ultimately used; an already-exited child is treated as done and returns false
// without escalation.
//
// It is for a process THIS program spawned and still holds an
// *os.Process for, which is a stable OS-level name for that one child: the
// handle carries the kernel's own reference (a pidfd where the runtime has one),
// and once Wait has reaped the child every further signal through it fails with
// ErrProcessDone instead of reaching whoever inherited the number. So it needs
// no start-time identity guard — it cannot signal a recycled pid by
// construction, which is strictly better than re-checking one.
//
// The pid-based sweeps have no handle, having found their victims in /proc or in
// tmux output; they capture an identity at decision time and go through
// softKillProcessChecked instead.
func softKillProcessHandle(proc *os.Process, grace time.Duration) bool {
	if proc == nil {
		return false
	}
	// Already reaped, or not ours to signal. Nothing was sent either way.
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return false
	}

	// Poll for exit. Signal(0) through the handle starts failing once the
	// child has been waited on, which is the same edge kill(pid, 0)'s ESRCH
	// marks for a pid — except it cannot be satisfied by a stranger holding
	// the number. The concurrent reaper is what turns the exit into that edge;
	// see reapWithEOFGrace.
	const pollInterval = 5 * time.Millisecond
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		time.Sleep(pollInterval)
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			return false
		}
	}

	// Still there after grace — escalate through the same handle.
	return proc.Kill() == nil
}

// stillSameIncarnation reports whether pid still refers to the process that
// identity was taken from, i.e. whether it is still the process the caller
// decided to kill.
//
// Fail-closed in both directions:
//
//   - An empty identity means the caller never established one. There is then
//     nothing tying this pid to the process the caller judged, so the signal is
//     refused rather than sent to an unidentified pid. An earlier revision of
//     this guard treated "" as "guard unarmed, proceed", on the argument that a
//     host unable to report start times should keep the #595 cleanup it had
//     before. That trade is the wrong way round for code whose failure mode is
//     SIGKILLing a live process: the sweep going inert is recoverable and
//     logged, a wrong kill is neither. Callers report the refusal (see
//     orphan_sweep_skipped_unidentifiable_pid) so an inert sweep is visible
//     rather than silent.
//   - A read that fails now means the process is gone or has become
//     unreadable, and there is nothing worth signalling either way.
func stillSameIncarnation(budget context.Context, pid int, identity string) bool {
	if identity == "" {
		return false
	}
	current, err := processIdentityOf(budget, pid)
	if err != nil {
		return false
	}
	return current == identity
}

// softKillProcessChecked is softKillProcess for a caller that decided to kill
// pid at some earlier point and captured its identity then. It re-checks that
// identity immediately before each signal, so a PID that changed hands between
// the decision and the signal is left alone.
//
// Both checks are load-bearing, and they close different windows:
//
//   - Before the SIGTERM, because the caller's decision can be arbitrarily
//     old. reapStaleControlClients works through a `list-clients` snapshot
//     sequentially and blocks up to grace per victim, so the last PID in a
//     burst is acted on N × grace after it was observed.
//   - Before the SIGKILL, because the victim may have exited cleanly inside
//     the grace period and had its number reissued. The poll below uses
//     kill(pid, 0), which answers "some process holds this number", not "the
//     one you signalled" — so without this check a well-behaved client that
//     obeyed SIGTERM promptly is indistinguishable from one that ignored it,
//     and the escalation lands on the new occupant.
//
// What this does NOT do is close the window — it narrows it. A pid can still
// change hands between the check returning and the kill(2) landing; the race is
// simply one syscall wide now instead of the ~500ms the escalation used to hold
// it open. Closing it outright needs a handle the kernel resolves atomically —
// pidfd_send_signal(2) on Linux 5.1+, which the host this surfaced on has —
// and that is a bigger change than the sweep is worth today.
//
// signalled reports whether anything was sent at all. Callers count kills and
// log one line per victim, and a pid the guard refused to touch is not a
// victim — reporting it as one would make the sweep's burst metric, which
// exists to observe the kill cascade, count kills that never happened.
func softKillProcessChecked(budget context.Context, pid int, identity string, grace time.Duration) (usedSIGKILL, signalled bool) {
	if !stillSameIncarnation(budget, pid, identity) {
		return false, false
	}

	// Initial SIGTERM. If the process is already gone, or is not ours to
	// signal, we're done — and in neither case may this report a kill.
	//
	// EPERM is reachable: the orphan sweep walks all of /proc, so it can select
	// another user's tmux client. Retrying with SIGKILL cannot succeed where
	// SIGTERM was refused (the permission check is per-signal-send, not
	// per-signal), and reporting the attempt as a kill would put a process that
	// is still running into kill_count and log reaped_orphaned_poll_client for
	// it — the burst metric exists to observe real cascades, so it must not
	// count kills that never happened.
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		if !errors.Is(err, syscall.ESRCH) {
			pipeLog.Warn("skipped_kill_signal_refused",
				slog.Int("pid", pid),
				slog.String("error", err.Error()))
		}
		return false, false
	}

	// Poll for exit. syscall.Kill(pid, 0) returns ESRCH once the process
	// is fully reaped; until then it returns nil (alive or zombie). The
	// poll is aggressive (5ms) so a clean SIGTERM→exit→reap chain in a test
	// environment, where the child is a process of the test binary and must
	// wait on the runtime's goroutine scheduler to pick up cmd.Wait(), has
	// plenty of chances to observe ESRCH within the grace window.
	const pollInterval = 5 * time.Millisecond
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		time.Sleep(pollInterval)
		if err := syscall.Kill(pid, 0); err != nil && errors.Is(err, syscall.ESRCH) {
			return false, true
		}
	}

	// Something still holds the number after grace. Escalate only if it is
	// still the process we signalled.
	if !stillSameIncarnation(budget, pid, identity) {
		return false, true
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	return true, true
}

// softKillProcessGroup is the process-group analogue of softKillProcess.
// It sends SIGTERM to the entire group (-pgid), polls every 5ms up to grace
// for the group to drain, and escalates to SIGKILL if any process in the
// group is still alive at the deadline. Returns true iff SIGKILL was
// ultimately used.
//
// A SIGTERM the kernel refuses ends the attempt; nothing is escalated and
// nothing is reported as killed:
//
//   - ESRCH — the group is empty, i.e. already dead.
//   - EPERM — not one member of that group is ours. This is what a recycled
//     pgid looks like from here. The old code read it as a reason to try
//     harder and sent a group-wide SIGKILL, which is the one response that
//     cannot help: kill(2)'s permission check is identical for both signals,
//     so the escalation either fails the same way or lands on a group this
//     process has no business signalling. It then returned true, reporting a
//     kill that never happened.
//
// Used by ControlPipe.Close() to tear down the agent-deck-owned
// `tmux -C attach-session` child without racing tmux's control-mode
// notify path. The original Close() implementation SIGKILL'd the group
// immediately, which on macOS Homebrew tmux 3.6a races the unfixed
// NULL-deref in tmux's notify path (tmux/tmux#4980) and crashes the
// server — wiping every agent-deck session. The mitigation in #739
// only covered killStaleControlClients (the post-restart cleanup path);
// the active-pipe close path still SIGKILL'd. This helper closes that gap.
// groupKillSyscall is the seam softKillProcessGroup signals through. A test
// swaps it to drive the errno branches, which otherwise need a second uid to
// reach. It is deliberately a package var rather than a parameter of an
// injectable core: a core-only test leaves the exported behaviour free to drift
// away from it, and the escalation this function refuses to make is exactly the
// kind of thing that gets pasted back in.
var groupKillSyscall = syscall.Kill

// stillOurs is re-asked immediately before the escalation. The pgid was
// captured when the child was certainly unreaped, but that was a full grace ago
// and the child can exit and be reaped inside it — by the concurrent reap this
// path exists to outrun — which frees its pid, and the pgid is that same
// number. softKillProcessChecked re-verifies incarnation before ITS escalation
// for the same reason; without this the group arm was the last signal on this
// path still resting on a name that may have changed hands.
func softKillProcessGroup(pgid int, grace time.Duration, stillOurs func() bool) bool {
	if err := groupKillSyscall(-pgid, syscall.SIGTERM); err != nil {
		// ESRCH (empty group) or EPERM (not ours) — either way there is
		// nothing here this process both may and should kill.
		return false
	}

	const pollInterval = 5 * time.Millisecond
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		time.Sleep(pollInterval)
		// kill(-pgid, 0) returns ESRCH only when no process in the group
		// remains; until then it returns nil (some member alive or zombie).
		if err := groupKillSyscall(-pgid, 0); err != nil && errors.Is(err, syscall.ESRCH) {
			return false
		}
		if !stillOurs() {
			// The child was reaped while we waited. Whatever answers to this
			// pgid now, we can no longer show it is ours. Its remaining members
			// are left behind — a leak the next sweep can see — rather than
			// SIGKILLing a group on the strength of a recycled number.
			pipeLog.Warn("skipped_group_escalation_pgid_changed_hands",
				slog.Int("pgid", pgid),
				slog.String("reason", "the child was reaped during the kill grace, so its pid — "+
					"and the process group id that is the same number — may name a stranger; "+
					"refusing the group-wide SIGKILL rather than guess"))
			return false
		}
	}

	_ = groupKillSyscall(-pgid, syscall.SIGKILL)
	return true
}

// tmuxSessionExistsOnSocket targets an explicit tmux server. socketName is the
// tmux `-L <name>` selector (Session.SocketName / Instance.TmuxSocketName);
// pass "" for the default server. All callers (watchPipe reconnect loop,
// public HasSession/HasSessionOnSocket in tmux.go) go through this.
//
// The probe is bounded by hasSessionProbeTimeout: a tmux server that is briefly
// busy can make `has-session` stall, and a stalled probe is indeterminate — we
// assume the session still exists (return true) rather than blocking the caller
// or reporting a live session as gone. A probe that completes is trusted, unless
// the client could not reach the server at all (see below).
func tmuxSessionExistsOnSocket(socketName, name string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), hasSessionProbeTimeout)
	defer cancel()
	err := tmuxExecContext(ctx, socketName, "has-session", "-t", name).Run()
	if ctx.Err() == context.DeadlineExceeded {
		return true // probe timed out: indeterminate, assume the session still exists
	}
	// Same mismatch caveat as Session.Exists: a client/server protocol version
	// mismatch refuses every command on this socket with a non-zero exit, which
	// is not evidence of absence — the refusal came FROM the server. This probe
	// backs the watchPipe reconnect loop and public HasSession/HasSessionOnSocket,
	// so reading a mismatch as absence here tears control pipes down for sessions
	// that are alive. The verdict is cached per socket, so consulting it adds no
	// subprocess per call.
	if err != nil && socketHasProtocolMismatch(socketName) {
		return true
	}
	return err == nil
}

// --- Global singleton ---

var (
	globalPipeManager   *PipeManager
	globalPipeManagerMu sync.RWMutex
)

// SetPipeManager sets the global PipeManager instance (called once at startup).
func SetPipeManager(pm *PipeManager) {
	globalPipeManagerMu.Lock()
	globalPipeManager = pm
	globalPipeManagerMu.Unlock()
}

// GetPipeManager returns the global PipeManager instance.
// Returns nil if not initialized (control pipes disabled or not yet started).
func GetPipeManager() *PipeManager {
	globalPipeManagerMu.RLock()
	defer globalPipeManagerMu.RUnlock()
	return globalPipeManager
}
