package tmux

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"al.essio.dev/pkg/shellescape"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
)

// GetAttachedSessions returns the names of tmux sessions that have real clients
// attached on the default socket. Used to detect which session the user is
// currently viewing. Filters out control mode clients (from PipeManager) which
// are not real user sessions.
func GetAttachedSessions() ([]string, error) {
	return attachedSessionsOnSocket(DefaultSocketName())
}

// GetAttachedSessionsOnSockets returns the union of attached (non-control)
// session names across the given sockets. The default socket is always
// consulted, then each distinct extra socket; "" denotes the default socket.
// Sockets with no running server (or any list-clients error) are skipped
// silently. Order is unspecified and duplicates are removed.
//
// This is the socket-aware counterpart to GetAttachedSessions: a session
// attached on an isolated agent-deck socket (TmuxSocketName != "") is invisible
// to a default-socket-only query, so callers that must pin every attached
// session regardless of socket use this instead.
func GetAttachedSessionsOnSockets(sockets ...string) []string {
	socketSeen := make(map[string]bool, len(sockets)+1)
	nameSeen := map[string]bool{}
	var out []string
	for _, sock := range append([]string{DefaultSocketName()}, sockets...) {
		sock = strings.TrimSpace(sock)
		if socketSeen[sock] {
			continue
		}
		socketSeen[sock] = true
		names, err := attachedSessionsOnSocket(sock)
		if err != nil {
			continue
		}
		for _, n := range names {
			if !nameSeen[n] {
				nameSeen[n] = true
				out = append(out, n)
			}
		}
	}
	return out
}

// attachedSessionsOnSocket lists the non-control-mode sessions with a client
// attached on a single tmux socket ("" = default server).
func attachedSessionsOnSocket(socket string) ([]string, error) {
	// session_name is sanitized to [A-Za-z0-9-], so it never contains
	// tmuxFieldSep; no reordering needed here.
	// Bounded — see tmuxPollTimeout.
	output, err := runBoundedOutput(socket, "list-clients", "-F", tmuxFmt("#{session_name}", "#{client_control_mode}"))
	if err != nil {
		return nil, err
	}

	var sessions []string
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		parts := strings.SplitN(line, tmuxFieldSep, 2)
		if len(parts) != 2 || parts[0] == "" {
			continue
		}
		// Skip control mode clients
		if parts[1] == "1" {
			continue
		}
		sessions = append(sessions, parts[0])
	}
	return sessions, nil
}

// BindSwitchKey binds a number key to switch to target session.
// Uses prefix table (default) so Ctrl+b N works.
// The key should be a single character like "1", "2", etc.
// Deprecated: Use BindSwitchKeyWithAck for notification bar integration.
func BindSwitchKey(key, targetSession string) error {
	// Bounded — see tmuxMutationTimeout. agent-deck rebinds these keys every 2s.
	return runBoundedMutation(DefaultSocketName(), "bind-key", key, "switch-client", "-t", targetSession)
}

// BindSwitchKeyWithAck binds a number key to switch to target session AND
// writes a signal file so agent-deck can acknowledge the session was selected.
// This enables proper acknowledgment when user presses Ctrl+b 1-6 shortcuts.
func BindSwitchKeyWithAck(key, targetSession, sessionID string) error {
	// Get signal file path
	signalFile, err := GetAckSignalPath()
	if err != nil {
		// Fall back to simple binding if we can't get the path
		return BindSwitchKey(key, targetSession)
	}

	// Ensure the signal directory exists at bind time as defense-in-depth.
	// On the XDG layout the data dir (~/.local/share/agent-deck) may not exist
	// yet, unlike the legacy ~/.agent-deck which was always present.
	_ = os.MkdirAll(filepath.Dir(signalFile), 0o700)

	script := buildAckSwitchScript(signalFile, sessionID, targetSession)
	// Bounded — see tmuxMutationTimeout. agent-deck rebinds these keys every 2s.
	return runBoundedMutation(DefaultSocketName(), "bind-key", key, "run-shell", script)
}

// buildAckSwitchScript builds the run-shell command bound to a quick-switch key.
//
// It must:
//  1. Ensure the signal directory exists. On the XDG layout the data dir
//     (~/.local/share/agent-deck) may not exist when the key fires, unlike the
//     legacy ~/.agent-deck which was always present. Without this mkdir the
//     echo below fails and the `&&` short-circuits, so `tmux switch-client`
//     never runs and the user sees "...returned 1" with no switch (#1327).
//  2. Write the session ID to the signal file (for agent-deck to acknowledge).
//  3. Switch to the target session.
//
// The inner `tmux switch-client` runs inside the tmux server that fired the
// run-shell hook, so it targets the correct socket automatically — no need to
// thread -L through the shell string.
//
// Every interpolated value is shell-escaped via shellescape.Quote. targetSession
// derives from the user-controlled session title, so raw single-quote wrapping
// ('%s') would break — or be exploited — by a title containing a quote, space,
// or shell metacharacter. The dir is created 0700 (matching the bind-time
// os.MkdirAll) so the ack-signal dir/file is not exposed to other local users.
func buildAckSwitchScript(signalFile, sessionID, targetSession string) string {
	return fmt.Sprintf("mkdir -p -m 700 %s && echo %s > %s && tmux switch-client -t %s",
		shellescape.Quote(filepath.Dir(signalFile)),
		shellescape.Quote(sessionID),
		shellescape.Quote(signalFile),
		shellescape.Quote(targetSession))
}

const ackSignalLegacyMarker = ".ack-signal-legacy"

// GetAckSignalPath returns the path to the acknowledgment signal file
func GetAckSignalPath() (string, error) {
	return agentpaths.EffectiveDataPath("ack-signal", "ack-signal", ackSignalLegacyMarker)
}

func preserveLegacyAckSignalPath(signalFile string) {
	legacyDir, err := agentpaths.LegacyDir()
	if err != nil {
		return
	}
	if filepath.Clean(signalFile) != filepath.Join(legacyDir, "ack-signal") {
		return
	}
	_ = os.WriteFile(filepath.Join(legacyDir, ackSignalLegacyMarker), []byte{}, 0o600)
}

// ReadAndClearAckSignal reads the session ID from the signal file and deletes it.
// Returns empty string if no signal file exists or on error.
func ReadAndClearAckSignal() string {
	signalFile, err := GetAckSignalPath()
	if err != nil {
		return ""
	}

	data, err := os.ReadFile(signalFile)
	if err != nil {
		return "" // File doesn't exist or can't be read
	}

	// Delete the file immediately after reading
	preserveLegacyAckSignalPath(signalFile)
	_ = os.Remove(signalFile)

	return strings.TrimSpace(string(data))
}

// WriteAckSignal writes sessionID to the ack-signal file so the TUI's background
// sync (which runs even while the TUI is paused inside tea.Exec) acknowledges the
// session, updates the notification bar, and records the switch for detach
// cursor-sync. This is the programmatic equivalent of the `echo <id> > signal`
// step in buildAckSwitchScript; pairs with SwitchAttachedClients.
func WriteAckSignal(sessionID string) error {
	signalFile, err := GetAckSignalPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(signalFile), 0o700); err != nil {
		return err
	}
	// Write atomically: the reader does ReadFile-then-Remove, so a plain
	// truncating WriteFile could be observed mid-write as an empty/partial file
	// and the ack would be lost. Stage to a temp file and rename into place.
	tmp := signalFile + ".tmp"
	if err := os.WriteFile(tmp, []byte(sessionID+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, signalFile); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// attachedClientNames lists the names of non-control clients attached on socket
// ("" = default server). Control-mode clients (PipeManager pipes) are excluded.
// Returns nil when no server is running or no client is attached.
func attachedClientNames(socket string) []string {
	// client_name is free-text (a pts path) so it goes LAST, after the 0/1
	// control-mode flag, to stay collision-safe under tmuxFieldSep.
	// Bounded — see tmuxPollTimeout. list-clients against a wedged server was
	// one of the observed spin sources.
	output, err := runBoundedOutput(socket, "list-clients", "-F", tmuxFmt("#{client_control_mode}", "#{client_name}"))
	if err != nil {
		return nil
	}
	var names []string
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		parts := strings.SplitN(line, tmuxFieldSep, 2)
		if len(parts) != 2 || parts[1] == "" {
			continue
		}
		if parts[0] == "1" { // control mode
			continue
		}
		names = append(names, parts[1])
	}
	return names
}

// SwitchAttachedClients moves every non-control client attached on socket into
// targetSession and writes the ack-signal for sessionID. Returns switched=true
// iff at least one client was attached and switched.
//
// This is the programmatic equivalent of the Ctrl+b N quick-switch: like that
// path it works while the TUI is suspended inside tea.Exec (tmux drives the
// switch, not Bubble Tea). And like that path it only works when the attached
// client and target session share a tmux server — querying clients on the
// target's own socket means a client attached elsewhere yields switched=false,
// so the caller falls back to a focus_request rather than mis-switching.
func SwitchAttachedClients(socket, targetSession, sessionID string) (bool, error) {
	clients := attachedClientNames(socket)
	if len(clients) == 0 {
		return false, nil
	}
	// Best-effort: the bar/cursor sync degrades without it, but the switch below
	// is what the user actually asked for, so a failed ack must not abort it.
	_ = WriteAckSignal(sessionID)

	switched := false
	var firstErr error
	for _, c := range clients {
		// Bounded — see tmuxMutationTimeout. One client per iteration, so an
		// unbounded wedge here stalls the whole sweep.
		if err := runBoundedMutation(socket, "switch-client", "-c", c, "-t", targetSession); err != nil {
			// A timeout is NOT a tmux failure, and the difference decides the
			// caller's behaviour: routeFocus returns on any non-nil error and
			// only reaches the focus_request fallback for (false, nil). Report a
			// client we gave up on as simply not switched, so the slower path —
			// write the row, let the TUI consume it on the next tick — still
			// runs. Reporting it as an error instead would turn a wedged client
			// into a hard `session focus --attach` failure with nothing written.
			if errors.Is(err, errTmuxTimeout) {
				statusLog.Warn("switch_client_timed_out",
					slog.String("client", c),
					slog.String("target", targetSession),
					slog.String("error", err.Error()))
				continue
			}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		switched = true
	}
	return switched, firstErr
}

// DetachClientsOnSockets detaches every non-control client attached on any of
// the given sockets ("" = default server). Returns detached=true iff at least
// one client was detached.
//
// This is the cross-server companion to SwitchAttachedClients: switch-client
// cannot move a client between tmux servers, so when a notification target lives
// on a different socket than the attached session, detaching that client makes
// agent-deck's paused attach (tea.Exec) return. The TUI then resumes and
// consumes the focus_request to attach the target on its OWN socket — a
// detach-and-reattach, which is the only way to "switch while attached" across
// servers. Control-mode clients (PipeManager pipes) are left alone.
func DetachClientsOnSockets(sockets ...string) (bool, error) {
	detached := false
	var firstErr error
	for _, socket := range sockets {
		for _, c := range attachedClientNames(socket) {
			// Bounded — see tmuxMutationTimeout. Same shape as the switch sweep
			// above: per-client, and a timeout degrades to detached=false.
			if err := runBoundedMutation(socket, "detach-client", "-c", c); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			detached = true
		}
	}
	return detached, firstErr
}

// DetachClientByPID detaches the one non-control client on socket whose
// process id is pid ("" = default server). Returns detached=false (no error)
// when no such client is attached, e.g. a pid recorded by a TUI that has
// since left that attach (#2704).
func DetachClientByPID(socket string, pid int) (bool, error) {
	if pid <= 0 {
		return false, nil
	}
	output, err := runBoundedOutput(socket, "list-clients", "-F", tmuxFmt("#{client_control_mode}", "#{client_pid}", "#{client_name}"))
	if err != nil {
		return false, nil
	}
	name := clientNameForPID(string(output), pid)
	if name == "" {
		return false, nil
	}
	if err := runBoundedMutation(socket, "detach-client", "-c", name); err != nil {
		return false, err
	}
	return true, nil
}

// clientNameForPID parses list-clients output of control-mode flag, client
// pid and client name, and returns the name of the non-control client with
// that pid, or "". client_name goes last: it is free text.
func clientNameForPID(output string, pid int) string {
	want := strconv.Itoa(pid)
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		parts := strings.SplitN(line, tmuxFieldSep, 3)
		if len(parts) != 3 || parts[0] == "1" || parts[2] == "" {
			continue
		}
		if parts[1] == want {
			return parts[2]
		}
	}
	return ""
}

// UnbindKey removes a key binding and restores default behavior.
// After unbinding, attempts to restore the default behavior where number keys
// select windows. The restore is best-effort since it may fail in environments
// without windows (e.g., CI) and agent-deck rebinds keys every 2s anyway.
func UnbindKey(key string) error {
	socket := DefaultSocketName()
	// First unbind our custom binding
	// Bounded — see tmuxMutationTimeout. Both are best-effort already.
	_ = runBoundedMutation(socket, "unbind-key", key)

	// Best-effort restore default: number keys select windows
	// bind-key 1 select-window -t :1
	_ = runBoundedMutation(socket, "bind-key", key, "select-window", "-t", ":"+key)
	return nil
}

// ctrlQDetachIfShellFormat returns the tmux FORMAT used to guard the
// socket-wide (server-wide) "-n -T root C-q" binding in Session.Start: true
// (non-"0", non-empty) exactly when the invoking client's current session
// name starts with SessionPrefix.
//
// #{m:pattern,string} is evaluated entirely inside the tmux server's own
// format engine at keypress time, against whichever session the invoking
// client is currently attached to — never handed to a shell, so a session
// name can never break out of any quoting (see the #1820 doc comment on the
// call site in Session.Start for the injection this replaced). Same
// prefix-match pattern as BindMouseStatusRightDetach below.
func ctrlQDetachIfShellFormat() string {
	return fmt.Sprintf("#{m:%s*,#{session_name}}", SessionPrefix)
}

// ctrlQDetachBindArgs returns the bind-key arguments (after "C-q") that
// install the Ctrl+Q detach guard: a pure `if-shell -F ... detach-client`
// command, never a run-shell/embedded-shell-script form. Kept as its own
// function so tests can assert on the exact args Session.Start installs.
func ctrlQDetachBindArgs() []string {
	return []string{"if-shell", "-F", ctrlQDetachIfShellFormat(), "detach-client", ""}
}

// BindMouseStatusRightDetach binds a mouse click on the status-right area to detach.
// Only fires inside agentdeck sessions (guards against detaching the user's outer tmux).
func BindMouseStatusRightDetach() error {
	// Guard: only detach if current session is an agentdeck-managed session.
	// #{m:pattern,string} is evaluated entirely inside tmux's format engine
	// (see ctrlQDetachIfShellFormat) — never handed to a shell, so a session
	// name containing shell metacharacters can't break out of anything.
	return commandRun(tmuxExec(DefaultSocketName(), "bind", "-n", "MouseDown1StatusRight",
		"if-shell", "-F", ctrlQDetachIfShellFormat(), "detach-client", ""))
}

// UnbindMouseStatusClicks removes mouse click bindings from the status bar.
func UnbindMouseStatusClicks() {
	_ = commandRun(tmuxExec(DefaultSocketName(), "unbind", "-n", "MouseDown1StatusRight"))
}
