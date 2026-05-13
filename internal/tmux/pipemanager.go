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
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// PipeManager manages ControlPipes for all active tmux sessions.
// It provides zero-subprocess CapturePane and event-driven output detection.
// Falls back to subprocess execution when pipes are unavailable.
type PipeManager struct {
	pipes map[string]*ControlPipe // sessionName -> pipe
	mu    sync.RWMutex

	// Callback for output events (invoked when %output detected from a session)
	onOutput func(sessionName string)

	// Callback for window change events (invoked when %window-add or %window-close detected)
	onWindowChange func()

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
	pipeLog.Debug("pipe_disconnected", slog.String("session", sessionName))
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

// RefreshAllActivities sends a single list-windows command through any available
// pipe to get activity timestamps for ALL sessions. This replaces the subprocess
// call in RefreshSessionCache.
func (pm *PipeManager) RefreshAllActivities() (map[string]int64, map[string][]WindowInfo, error) {
	pm.mu.RLock()
	// Find any alive pipe to send the command through
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

	// tmux control mode requires double-quoted format strings containing special chars
	output, err := pipe.SendCommand(`list-windows -a -F "#{session_name}\t#{window_activity}\t#{window_index}\t#{window_name}"`)
	if err != nil {
		return nil, nil, fmt.Errorf("list-windows via pipe: %w", err)
	}

	sessionCache, windowCache := parseListWindowsOutput(output)
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

	output, err := pipe.SendCommand(`list-panes -a -F "#{session_name}\t#{pane_title}\t#{pane_current_command}\t#{pane_dead}\t#{window_index}\t#{pane_index}"`)
	if err != nil {
		return nil, nil, fmt.Errorf("list-panes via pipe: %w", err)
	}

	result := make(map[string]PaneInfo)
	windowTools := make(map[string]map[int]string)
	seenWindowTool := make(map[string]bool) // "session\twinIdx" -> already processed
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 6)
		if len(parts) != 6 {
			continue
		}
		name := parts[0]

		// Collect tool info for the first pane of each window (handles any base-index).
		windowKey := name + "\t" + parts[4]
		if !seenWindowTool[windowKey] {
			seenWindowTool[windowKey] = true
			var winIdx int
			_, _ = fmt.Sscanf(parts[4], "%d", &winIdx)
			tool := detectToolFromCommand(parts[2])
			if tool == "" {
				tool = detectToolFromCommand(parts[1])
			}
			if tool != "" {
				if windowTools[name] == nil {
					windowTools[name] = make(map[int]string)
				}
				windowTools[name][winIdx] = tool
			}
		}

		// Cache the first pane seen per session (primary window+pane).
		if _, seen := result[name]; !seen {
			result[name] = PaneInfo{
				Title:          parts[1],
				CurrentCommand: parts[2],
				Dead:           parts[3] == "1",
			}
		}
	}
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

		// Check if session still exists before trying to reconnect.
		// Avoids infinite reconnect loops for deleted/non-existent sessions.
		// Target the same socket the original pipe lived on — checking the
		// default server for a session that lives on an isolated agent-deck
		// socket would answer "no" and silently delete a healthy pipe.
		reconnectSocket := pipe.socketName
		if !tmuxSessionExistsOnSocket(reconnectSocket, sessionName) {
			pipeLog.Debug("pipe_reconnect_session_gone",
				slog.String("session", sessionName),
				slog.String("socket", reconnectSocket))
			pm.mu.Lock()
			delete(pm.pipes, sessionName)
			pm.mu.Unlock()
			return
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
	myPID := os.Getpid()

	out, err := tmuxExec(socketName,
		"list-clients", "-t", sessionName,
		"-F", "#{client_control_mode} #{client_pid}",
	).Output()
	if err != nil {
		return // session may not exist or no clients attached
	}

	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
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
		if !isControlClientOrphan(pid) {
			// Live sibling TUI — leave its pipe alone. Without this guard
			// two concurrent agent-deck TUIs (allow_multiple=true) would
			// SIGTERM each other's control clients on every reconnect (#927).
			pipeLog.Debug("preserved_live_sibling_control_client",
				slog.String("session", sessionName),
				slog.Int("pid", pid))
			continue
		}
		// Soft-kill the stale control-mode client process.
		// On macOS Homebrew tmux 3.6a there is an unfixed NULL-deref in the
		// control-mode notify path that races with client teardown (#737).
		// SIGKILL'ing a TUI while it holds an active control client can crash
		// the entire tmux server, wiping every agent-deck session. A SIGTERM
		// lets the client drain and exit cleanly; SIGKILL is retained as a
		// 500ms fallback for clients that ignore TERM.
		usedSIGKILL := softKillProcess(pid, controlClientKillGrace)
		pipeLog.Debug("killed_stale_control_client",
			slog.String("session", sessionName),
			slog.Int("pid", pid),
			slog.Bool("used_sigkill", usedSIGKILL))
	}
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
// Conservative: any error reading parent metadata returns true (treat as
// orphan, sweep it). The prior behaviour was "kill anything that isn't me",
// so falling back to that on metadata-read failures preserves cleanup
// behaviour for #595's stale-client class without regressing.
//
// Why not a heartbeat file: would need TUI-startup wiring + a refresh
// goroutine + lifecycle cleanup. The PPID+exe check is zero-state and
// agrees on the same answer.
func isControlClientOrphan(pid int) bool {
	ppid, err := readParentPID(pid)
	if err != nil || ppid <= 1 {
		// PPID == 1 means the kernel has already reparented the client to
		// init — definitively an orphan.
		return true
	}
	// Liveness double-check on the parent. If the parent died between the
	// list-clients call and now, the client is in the process of being
	// orphaned right now — sweep it.
	if err := syscall.Kill(ppid, 0); err != nil {
		return true
	}
	parentExe, err := readProcessExe(ppid)
	if err != nil {
		// On Linux without /proc-read permission, or macOS with `ps`
		// failing, we can't verify the parent. Fall back to "treat as
		// orphan" so #595 cleanup still happens; the cost is that this
		// regresses the #927 behaviour only on hosts where /proc is
		// inaccessible AND `ps` doesn't work — which is essentially never.
		return true
	}
	return !looksLikeAgentDeckBinary(parentExe)
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
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// controlClientKillGrace is how long softKillProcess waits after SIGTERM
// before escalating to SIGKILL. 500ms matches empirical clean-shutdown
// times for `tmux -C attach-session` on macOS + Linux.
const controlClientKillGrace = 500 * time.Millisecond

// softKillProcess sends SIGTERM to pid, polls every 25ms up to grace for the
// process to exit, and escalates to SIGKILL if it doesn't. Returns true iff
// SIGKILL was ultimately used. A non-existent pid (ESRCH) is treated as
// already-dead and returns false without escalation.
func softKillProcess(pid int, grace time.Duration) bool {
	// Initial SIGTERM. If the process is already gone, we're done.
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return false
		}
		// Permission or other error — try SIGKILL as last resort.
		_ = syscall.Kill(pid, syscall.SIGKILL)
		return true
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
			return false
		}
	}

	// Still alive after grace — escalate.
	_ = syscall.Kill(pid, syscall.SIGKILL)
	return true
}

// softKillProcessGroup is the process-group analogue of softKillProcess.
// It sends SIGTERM to the entire group (-pgid), polls every 5ms up to grace
// for the group to drain, and escalates to SIGKILL if any process in the
// group is still alive at the deadline. Returns true iff SIGKILL was
// ultimately used. An empty group (ESRCH on initial SIGTERM) is treated as
// already-dead and returns false without escalation.
//
// Used by ControlPipe.Close() to tear down the agent-deck-owned
// `tmux -C attach-session` child without racing tmux's control-mode
// notify path. The original Close() implementation SIGKILL'd the group
// immediately, which on macOS Homebrew tmux 3.6a races the unfixed
// NULL-deref in tmux's notify path (tmux/tmux#4980) and crashes the
// server — wiping every agent-deck session. The mitigation in #739
// only covered killStaleControlClients (the post-restart cleanup path);
// the active-pipe close path still SIGKILL'd. This helper closes that gap.
func softKillProcessGroup(pgid int, grace time.Duration) bool {
	if err := syscall.Kill(-pgid, syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return false
		}
		// Permission or other error — fall back to SIGKILL on the group.
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		return true
	}

	const pollInterval = 5 * time.Millisecond
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		time.Sleep(pollInterval)
		// kill(-pgid, 0) returns ESRCH only when no process in the group
		// remains; until then it returns nil (some member alive or zombie).
		if err := syscall.Kill(-pgid, 0); err != nil && errors.Is(err, syscall.ESRCH) {
			return false
		}
	}

	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	return true
}

// tmuxSessionExistsOnSocket targets an explicit tmux server. socketName is the
// tmux `-L <name>` selector (Session.SocketName / Instance.TmuxSocketName);
// pass "" for the default server. All callers (watchPipe reconnect loop,
// public HasSession/HasSessionOnSocket in tmux.go) go through this.
func tmuxSessionExistsOnSocket(socketName, name string) bool {
	cmd := tmuxExec(socketName, "has-session", "-t", name)
	return cmd.Run() == nil
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
