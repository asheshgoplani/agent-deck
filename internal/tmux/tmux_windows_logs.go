package tmux

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"al.essio.dev/pkg/shellescape"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
)

// LogFile returns the path to this session's log file
// Logs are stored under the XDG data directory, falling back to legacy logs.
func (s *Session) LogFile() string {
	return filepath.Join(LogDir(), s.TmuxName()+".log")
}

// LogDir returns the directory containing all session logs
func LogDir() string {
	logDir, err := agentpaths.EffectiveDataPath("logs", "logs")
	if err != nil {
		return filepath.Join(os.TempDir(), "agent-deck", "logs")
	}
	return logDir
}

// SplitShellPane adds a vertical split pane to this session running shell
// in workdir. If workdir is empty the pane inherits the session's current
// working directory. Issue #1470.
//
// #2214: when workdir is given, the shell itself asserts it via cd rather than
// trusting split-window's own -c alone — see cwdAssertCommand.
func (s *Session) SplitShellPane(workdir string) error {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	args := []string{"split-window", "-h", "-t", s.Name}
	if workdir != "" {
		args = append(args, "-c", workdir, bashBinary, "-c", cwdAssertCommand(workdir, "exec "+shellescape.Quote(shell)))
	} else {
		args = append(args, shell)
	}
	return commandRun(tmuxExec(s.SocketName, args...))
}

// NewShellWindow adds a new window (tab) to this session running shell in
// workdir, instead of splitting the current window. If workdir is empty the
// window inherits the session's current working directory.
//
// #2214: when workdir is given, the shell itself asserts it via cd rather than
// trusting new-window's own -c alone — see cwdAssertCommand.
func (s *Session) NewShellWindow(workdir string) error {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	args := []string{"new-window", "-P", "-F", "#{window_id}", "-t", s.Name}
	if workdir != "" {
		args = append(args, "-c", workdir, bashBinary, "-c", cwdAssertCommand(workdir, "exec "+shellescape.Quote(shell)))
	} else {
		args = append(args, shell)
	}
	out, err := tmuxExec(s.SocketName, args...).Output()
	if err != nil {
		return err
	}
	// new-window prints its ID before running after-new-window hooks, which
	// may print more output or select another window. Configure that exact ID.
	windowID, _, _ := strings.Cut(string(out), "\n")
	// Creation succeeded. Like Start, apply Deck's window policy
	// (windowPolicyOptions), but preserve any local option a user's
	// after-new-window hook explicitly installed (-o). Option configuration
	// is best effort: an invalid override applies nothing (value logs it)
	// and must not report failure and invite a duplicate tab.
	args = windowPolicyArgs([]string{windowID}, s.OptionOverrides, hostTmuxVersionString(), "set-window-option", "-oq")
	if len(args) == 0 {
		return nil
	}
	if err := s.runBoundedMutation(args...); err != nil {
		statusLog.Warn("shell_window_options_failed", slog.String("window", windowID), slog.String("error", err.Error()))
	}
	return nil
}

// ErrLastWindow is returned by KillWindow when the target is the session's
// only remaining window; killing it would kill the whole session.
var ErrLastWindow = errors.New("window is the session's last window")

// ErrWindowChanged is returned by KillWindow when the window the caller
// selected is no longer what it was selected as: it is gone from the session
// (closed, or moved elsewhere), or it has since been renamed. Ids are never
// reused by a tmux server, so the check by id can never pick a different
// window; the name check catches the "same window, no longer what the user
// read on screen" case.
var ErrWindowChanged = errors.New("window changed since it was selected")

// listWindows returns the session's live windows (index, id, name) from the
// tmux server. It enumerates all windows and matches in Go rather than using
// `display-message -t session:index`: tmux's target resolution is lenient
// about a window index that does not exist (it silently falls back to another
// window in the session instead of erroring), which would make a
// nonexistent/closed index look like a valid, different window.
func (s *Session) listWindows() ([]WindowInfo, error) {
	out, err := runBoundedOutput(s.SocketName,
		"list-windows", "-t", s.TmuxName(), "-F", tmuxFmt("#{window_index}", "#{window_id}", "#{window_name}"))
	if err != nil {
		return nil, err
	}
	var wins []WindowInfo
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.SplitN(line, tmuxFieldSep, 3)
		if len(fields) != 3 {
			continue
		}
		idx, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		wins = append(wins, WindowInfo{Index: idx, ID: fields[1], Name: fields[2]})
	}
	return wins, nil
}

// WindowID returns the stable tmux window id (e.g. "@12") of the window
// currently at the given index in this session. The CLI uses it to accept an
// index; the TUI carries the id from the window cache instead, so the row the
// user read is the window that gets killed even if tmux renumbered since.
func (s *Session) WindowID(index int) (string, error) {
	wins, err := s.listWindows()
	if err != nil {
		return "", err
	}
	for _, w := range wins {
		if w.Index == index {
			return w.ID, nil
		}
	}
	return "", fmt.Errorf("no window at index %d in session %s", index, s.Name)
}

// Window returns the live index and name of the window with the given
// stable id in this session, or ErrWindowChanged if the session has no such
// window (any more).
func (s *Session) Window(windowID string) (WindowInfo, error) {
	wins, err := s.listWindows()
	if err != nil {
		return WindowInfo{}, err
	}
	for _, w := range wins {
		if w.ID == windowID {
			return w, nil
		}
	}
	return WindowInfo{}, ErrWindowChanged
}

// KillWindow kills the window with the stable id windowID in this session,
// leaving the session's other windows intact. expectedName is the window
// name the user read when they selected it; the kill is refused with
// ErrWindowChanged if the window is gone from the session or its live name
// differs, and with ErrLastWindow if it is the session's last window. The
// id/last-window check and the kill happen in a single server-side command
// (if-shell -F) so neither condition can change between check and kill.
// Bounded (runBoundedOutput) so a wedged server cannot hang the UI.
func (s *Session) KillWindow(windowID, expectedName string) error {
	live, err := s.Window(windowID)
	if err != nil {
		return err
	}
	if live.Name != expectedName {
		return ErrWindowChanged
	}
	// Target the window by session:@id: tmux verifies the id belongs to the
	// session, so a window that moved to another session does not resolve.
	target := s.Name + ":" + windowID
	// The nested command is a tmux command string; s.Name is an agent-deck
	// generated session name (sanitized charset) and windowID is a
	// tmux-generated id (@<digits>), so embedding them is safe. The name is
	// free text and is compared in Go above, never embedded in a format.
	cond := "#{&&:#{==:#{window_id}," + windowID + "},#{>:#{session_windows},1}}"
	out, err := runBoundedOutput(s.SocketName,
		"if-shell", "-F", "-t", target, cond,
		"kill-window -t \""+target+"\"",
		"display-message -p refused")
	if err != nil {
		// The target no longer resolves (window gone from the session).
		return ErrWindowChanged
	}
	if !strings.Contains(string(out), "refused") {
		return nil
	}
	// Refused: figure out why for a clearer error. This second query is not
	// part of the atomic decision above (nothing was killed either way), so
	// a race here only affects the message text, never correctness.
	if _, err := s.Window(windowID); err != nil {
		return err
	}
	return ErrLastWindow
}

// ListAllSessions returns all Agent Deck tmux sessions
func ListAllSessions() ([]*Session, error) {
	socket := DefaultSocketName()
	// Bounded — see tmuxPollTimeout.
	output, err := runBoundedOutput(socket, "list-sessions", "-F", "#{session_name}")
	if err != nil {
		// No sessions exist
		if strings.Contains(err.Error(), "no server running") ||
			strings.Contains(err.Error(), "no sessions") {
			return []*Session{}, nil
		}
		return nil, fmt.Errorf("failed to list sessions: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	var sessions []*Session

	for _, line := range lines {
		if strings.HasPrefix(line, SessionPrefix) {
			displayName := strings.TrimPrefix(line, SessionPrefix)
			// Get session info. Sessions discovered by ListAllSessions live on
			// the installation-wide default socket by construction — a non-default
			// socket is reached only via Instance.TmuxSocketName, which the caller
			// plugs in after loading the Instance record.
			sess := &Session{
				Name:        line,
				DisplayName: displayName,
				SocketName:  socket,
			}
			// Try to get working directory (bounded — see tmuxPollTimeout)
			if workDirOutput, err := runBoundedOutput(socket, "display-message", "-t", line, "-p", "#{pane_current_path}"); err == nil {
				sess.WorkDir = strings.TrimSpace(string(workDirOutput))
			}
			sessions = append(sessions, sess)
		}
	}

	return sessions, nil
}

// ═══════════════════════════════════════════════════════════════════════════
// Log Management Functions
// ═══════════════════════════════════════════════════════════════════════════

// TruncateLogFile truncates a log file to keep only the last maxLines lines
// This is called when a log file exceeds maxSizeBytes
func TruncateLogFile(logPath string, maxLines int) error {
	// Read the file
	data, err := os.ReadFile(logPath)
	if err != nil {
		return fmt.Errorf("failed to read log file: %w", err)
	}

	// Split into lines
	lines := strings.Split(string(data), "\n")

	// If already under limit, nothing to do
	if len(lines) <= maxLines {
		return nil
	}

	// Keep only the last maxLines
	start := len(lines) - maxLines
	truncatedLines := lines[start:]

	// Write back
	truncatedData := strings.Join(truncatedLines, "\n")
	if err := os.WriteFile(logPath, []byte(truncatedData), 0o644); err != nil {
		return fmt.Errorf("failed to write truncated log: %w", err)
	}

	statusLog.Debug(
		"log_truncated",
		slog.String("file", filepath.Base(logPath)),
		slog.Int("from_lines", len(lines)),
		slog.Int("to_lines", len(truncatedLines)),
	)
	return nil
}

// isSessionLogName reports whether name is a per-session pane log
// (<SessionPrefix>...log, see Session.LogFile). The logs directory is shared
// with agent-deck's own logs (transition-notifier.log, notifier-missed.log,
// ...), which no tmux session owns: log maintenance must never truncate or
// delete those as "orphans" (issue #2481 item 7, the transition log was lost on
// 4 of 5 hosts this way).
func isSessionLogName(name string) bool {
	return strings.HasPrefix(name, SessionPrefix) && strings.HasSuffix(name, ".log")
}

// TruncateLargeLogFiles checks the per-session log files and truncates any that
// exceed maxSizeMB
func TruncateLargeLogFiles(maxSizeMB int, maxLines int) (truncated int, err error) {
	logDir := LogDir()

	entries, err := os.ReadDir(logDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil // No logs directory yet
		}
		return 0, fmt.Errorf("failed to read log directory: %w", err)
	}

	maxSizeBytes := int64(maxSizeMB * 1024 * 1024)

	for _, entry := range entries {
		if entry.IsDir() || !isSessionLogName(entry.Name()) {
			continue
		}

		logPath := filepath.Join(logDir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			continue
		}

		if info.Size() > maxSizeBytes {
			if err := TruncateLogFile(logPath, maxLines); err != nil {
				statusLog.Debug("truncate_failed", slog.String("file", entry.Name()), slog.String("error", err.Error()))
				continue
			}
			truncated++
		}
	}

	return truncated, nil
}

// CleanupOrphanedLogs removes per-session log files for sessions that no
// longer exist. Only <SessionPrefix>*.log files are candidates. A log is
// considered orphaned if:
// 1. No tmux session with matching name exists
// 2. The log file is older than 1 hour (to avoid race conditions during session creation)
func CleanupOrphanedLogs() (removed int, freedBytes int64, err error) {
	logDir := LogDir()

	entries, err := os.ReadDir(logDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil // No logs directory yet
		}
		return 0, 0, fmt.Errorf("failed to read log directory: %w", err)
	}

	// Get list of existing tmux sessions
	sessions, err := ListAllSessions()
	if err != nil {
		// If tmux server isn't running, we can't determine orphans safely
		return 0, 0, nil
	}

	// Build a set of active session names
	activeNames := make(map[string]bool)
	for _, sess := range sessions {
		activeNames[sess.Name] = true
	}

	now := time.Now()
	minAge := 1 * time.Hour // Only cleanup logs older than 1 hour

	for _, entry := range entries {
		if entry.IsDir() || !isSessionLogName(entry.Name()) {
			continue
		}

		sessionName := strings.TrimSuffix(entry.Name(), ".log")
		logPath := filepath.Join(logDir, entry.Name())

		// Check if session exists
		if activeNames[sessionName] {
			continue // Session still exists
		}

		// Check age
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) < minAge {
			continue // Too recent, might be in process of creation
		}

		// Remove orphaned log
		size := info.Size()
		if err := os.Remove(logPath); err != nil {
			statusLog.Debug(
				"orphan_remove_failed",
				slog.String("file", entry.Name()),
				slog.String("error", err.Error()),
			)
			continue
		}

		removed++
		freedBytes += size
		statusLog.Debug(
			"orphan_removed",
			slog.String("file", entry.Name()),
			slog.Float64("size_kb", float64(size)/1024),
		)
	}

	return removed, freedBytes, nil
}

// RunLogMaintenance performs all log maintenance tasks based on settings
// This should be called once at startup and optionally periodically
func RunLogMaintenance(maxSizeMB int, maxLines int, removeOrphans bool) {
	// Truncate large files
	truncated, err := TruncateLargeLogFiles(maxSizeMB, maxLines)
	if err != nil {
		statusLog.Debug("log_truncation_error", slog.String("error", err.Error()))
	} else if truncated > 0 {
		statusLog.Debug("log_truncation_complete", slog.Int("count", truncated))
	}

	// Remove orphaned logs
	if removeOrphans {
		removed, freed, err := CleanupOrphanedLogs()
		if err != nil {
			statusLog.Debug("orphan_cleanup_error", slog.String("error", err.Error()))
		} else if removed > 0 {
			statusLog.Debug("orphan_cleanup_complete", slog.Int("count", removed), slog.Float64("freed_mb", float64(freed)/(1024*1024)))
		}
	}
}

// ═══════════════════════════════════════════════════════════════════════════

// ═══════════════════════════════════════════════════════════════════════════
// Notification Bar Helper Functions
// ═══════════════════════════════════════════════════════════════════════════

// ListAgentDeckSessionsOnSocket lists managed sessions on the specified server.
// No prod caller: scripts/runtime-health/red/codex_exclusion_cache.go.txt
// (the performance.sh red-side overlay) builds against it.
func ListAgentDeckSessionsOnSocket(socket string) ([]string, error) {
	// Bounded — see tmuxPollTimeout. Drives the cross-profile notification-bar
	// refresh, i.e. it runs on a timer for every session.
	output, err := runBoundedOutput(socket, "list-sessions", "-F", "#{session_name}")
	if err != nil {
		// No sessions exist
		if strings.Contains(err.Error(), "no server running") ||
			strings.Contains(err.Error(), "no sessions") {
			return []string{}, nil
		}
		return nil, fmt.Errorf("failed to list sessions: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	var sessions []string

	for _, line := range lines {
		if strings.HasPrefix(line, SessionPrefix) {
			sessions = append(sessions, line)
		}
	}

	return sessions, nil
}

// ListAgentDeckCodexSessionIDsOnSocket reads all managed session bindings in
// one tmux call. Session environment variables expand in list-sessions format
// on the named server, so ownership refresh does not fork once per session.
//
// A format looks a bare variable up in the session environment first and then
// in the server's GLOBAL environment (tmux FORMATS: "or the name of an
// environment variable"; verified on 3.3a, 3.6a and 3.7b). Only the session
// environment is a binding: a CODEX_SESSION_ID the server inherited from the
// client that started it (a Codex tool shell, a Codex conductor) would
// otherwise be reported for every unbound session and exclude the real
// owner. So the global value is read once, and every row that merely echoes
// it is resolved with the per-session read the old code used for all rows.
func ListAgentDeckCodexSessionIDsOnSocket(socket string) (map[string]string, error) {
	output, err := runBoundedOutput(socket, "list-sessions", "-F", "#{session_name}\t#{CODEX_SESSION_ID}")
	if err != nil {
		if strings.Contains(err.Error(), "no server running") || strings.Contains(err.Error(), "no sessions") {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("failed to list Codex session bindings: %w", err)
	}
	bindings := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSuffix(string(output), "\n"), "\n") {
		if line == "" {
			continue
		}
		name, id, ok := strings.Cut(line, "\t")
		if !ok {
			return nil, fmt.Errorf("malformed Codex session binding row")
		}
		if strings.HasPrefix(name, SessionPrefix) && id != "" {
			bindings[name] = id
		}
	}
	if len(bindings) == 0 {
		return bindings, nil
	}
	global, err := globalEnvironmentValue(socket, "CODEX_SESSION_ID")
	if err != nil {
		return nil, fmt.Errorf("failed to read global Codex session binding: %w", err)
	}
	if global == "" {
		return bindings, nil
	}
	for name, id := range bindings {
		if id != global {
			continue
		}
		// Ambiguous: the session's own environment may hold the same value
		// (bound by the process that also started the server) or nothing.
		peer := &Session{Name: name, SocketName: socket}
		own, err := peer.ReadEnvironment("CODEX_SESSION_ID")
		if err != nil {
			return nil, fmt.Errorf("failed to resolve Codex session binding for %s: %w", name, err)
		}
		if own == "" {
			delete(bindings, name)
		} else {
			bindings[name] = own
		}
	}
	return bindings, nil
}

// globalEnvironmentValue reads one variable from the server's global
// environment. An unset variable (tmux prints "unknown variable" and exits 1)
// and an unset marker ("-NAME") both read as empty.
func globalEnvironmentValue(socket, key string) (string, error) {
	output, err := runBoundedOutput(socket, "show-environment", "-g", key)
	if err != nil {
		if strings.Contains(err.Error(), "unknown variable") {
			return "", nil
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && strings.Contains(string(exitErr.Stderr), "unknown variable") {
			return "", nil
		}
		return "", err
	}
	line := strings.TrimSpace(string(output))
	if value, ok := strings.CutPrefix(line, key+"="); ok {
		return value, nil
	}
	return "", nil
}

// GetActiveSession returns the session name the user is currently attached to.
// Returns empty string and error if not attached to any session.
func GetActiveSession() (string, error) {
	// Bounded — see tmuxPollTimeout.
	out, err := runBoundedOutput(DefaultSocketName(), "display-message", "-p", "#{client_session}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ═══════════════════════════════════════════════════════════════════════════

// DiscoverAllTmuxSessions returns all tmux sessions (including non-Agent Deck ones)
func DiscoverAllTmuxSessions() ([]*Session, error) {
	// Bounded — see tmuxPollTimeout. pane_current_path goes LAST: it is the
	// one field that can legitimately contain a colon (a path component),
	// and SplitN below relies on that so the path is never truncated at an
	// embedded colon.
	output, err := runBoundedOutput(DefaultSocketName(), "list-sessions", "-F",
		"#{session_name}:#{session_created}:#{pane_current_command}:#{pane_current_path}")
	if err != nil {
		// No sessions exist
		if strings.Contains(err.Error(), "no server running") ||
			strings.Contains(err.Error(), "no sessions") {
			return []*Session{}, nil
		}
		return nil, fmt.Errorf("failed to list sessions: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	var sessions []*Session

	for _, line := range lines {
		if line == "" {
			continue
		}

		parts := strings.SplitN(line, ":", 4)
		sessionName := parts[0]
		var created time.Time
		if len(parts) > 1 {
			if epoch, convErr := strconv.ParseInt(parts[1], 10, 64); convErr == nil {
				created = time.Unix(epoch, 0)
			}
		}
		command := ""
		if len(parts) > 2 {
			command = parts[2]
		}
		workDir := ""
		if len(parts) > 3 {
			workDir = parts[3]
		}

		// Create session object
		sess := &Session{
			Name:        sessionName,
			DisplayName: sessionName,
			WorkDir:     workDir,
			Created:     created,
			Command:     command,
		}

		// If it's an agent-deck session, clean up the display name
		if strings.HasPrefix(sessionName, SessionPrefix) {
			sess.DisplayName = strings.TrimPrefix(sessionName, SessionPrefix)
		}

		sessions = append(sessions, sess)
	}

	return sessions, nil
}
