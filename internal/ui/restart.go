package ui

import (
	"errors"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

// In-place restart of the TUI.
//
// The maintainer's complaint: after `agent-deck update` (or the auto-update
// job) replaces the binary, the open TUI keeps running the old code and has
// to be closed and reopened by hand. The restart_deck hotkey (default
// ctrl+t) runs the normal shutdown sequence (state flushed, watchers and
// pipes closed, primary claim released) and then, once Bubble Tea has
// restored the terminal, main() replaces this process with the executable at
// the same path, with the same args and env, so the new build comes up
// exactly the way the user launched the old one.
//
// The key is only reachable from the home screen: while attached, Bubble Tea
// is parked inside tea.Exec and keystrokes go to tmux, so there is never an
// attached pane to detach from here. Modal dialogs and in-flight session
// actions block the restart with a footer message instead of losing work.

// errRestartBlocked wraps the reason a restart was refused.
var errRestartBlocked = errors.New("restart blocked")

// sessionActionInFlight reports whether a create/resume/fork/setup/remote
// restart is still running, or a tmux attach is being set up.
func (h *Home) sessionActionInFlight() bool {
	return len(h.launchingSessions) > 0 ||
		len(h.resumingSessions) > 0 ||
		len(h.forkingSessions) > 0 ||
		len(h.creatingSessions) > 0 ||
		len(h.setupRunningSessions) > 0 ||
		len(h.remoteRestarting) > 0 ||
		h.isAttaching.Load()
}

// restartBlockReason returns "" when a restart may proceed, otherwise a
// short reason for the footer.
func (h *Home) restartBlockReason() string {
	switch {
	case h.restartRequested:
		return "restart already in progress"
	case h.hasModalVisible() || h.insertMode:
		return "close the open dialog first"
	case h.sessionActionInFlight():
		return "a session action is still running, try again in a moment"
	}
	return ""
}

// tryRestartDeck is the restart_deck key handler. It either refuses with a
// footer message or arms the restart and starts the regular quit sequence.
// The MCP pool is left running (performQuit(false)) so the new process can
// reconnect to it instead of cold-starting every MCP.
func (h *Home) tryRestartDeck() (tea.Model, tea.Cmd) {
	if reason := h.restartBlockReason(); reason != "" {
		h.setError(fmt.Errorf("%w: %s", errRestartBlocked, reason))
		return h, nil
	}
	h.restartRequested = true
	h.isQuitting = true
	uiLog.Info("tui_restart_requested",
		"running", Version,
		"installed", h.installedUpdateVersion(),
		"exe", h.restartExecutable())
	return h, h.performQuit(false)
}

// restartExecutable is the path to exec: the path fingerprinted at startup
// when available (it is the file the installer replaced), else whatever
// os.Executable says now.
func (h *Home) restartExecutable() string {
	if h.binaryWatch != nil && h.binaryWatch.execPath != "" {
		return h.binaryWatch.execPath
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return exe
}

// RestartTarget reports whether the user asked for an in-place restart and,
// if so, which executable main() should exec after tea.Program.Run returns.
func (h *Home) RestartTarget() (string, bool) {
	if !h.restartRequested {
		return "", false
	}
	exe := h.restartExecutable()
	return exe, exe != ""
}

// ExecSelf replaces the current process with exe, keeping os.Args and the
// environment. It only returns on failure (or on platforms without exec,
// see restart_windows.go). Call it after the TUI has restored the terminal.
func ExecSelf(exe string) error {
	if exe == "" {
		return errors.New("executable path unknown")
	}
	return execSelf(exe, os.Args, os.Environ())
}
