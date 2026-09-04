package ui

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestResolveShellSplitMode_AutoDetect verifies that resolveShellSplitMode
// detects iTerm2 via environment variables when [ui].shell_split is unset.
// Issue #1470.
func TestResolveShellSplitMode_AutoDetect(t *testing.T) {
	t.Run("LC_TERMINAL=iTerm2", func(t *testing.T) {
		t.Setenv("LC_TERMINAL", "iTerm2")
		t.Setenv("TERM_PROGRAM", "")
		if got := resolveShellSplitMode(); got != session.ShellSplitITerm {
			t.Errorf("resolveShellSplitMode() = %q, want %q", got, session.ShellSplitITerm)
		}
	})
	t.Run("TERM_PROGRAM=iTerm.app", func(t *testing.T) {
		t.Setenv("LC_TERMINAL", "")
		t.Setenv("TERM_PROGRAM", "iTerm.app")
		if got := resolveShellSplitMode(); got != session.ShellSplitITerm {
			t.Errorf("resolveShellSplitMode() = %q, want %q", got, session.ShellSplitITerm)
		}
	})
	t.Run("no_iterm_env", func(t *testing.T) {
		t.Setenv("LC_TERMINAL", "")
		t.Setenv("TERM_PROGRAM", "Apple_Terminal")
		if got := resolveShellSplitMode(); got != session.ShellSplitTmux {
			t.Errorf("resolveShellSplitMode() = %q, want %q (no iTerm env)", got, session.ShellSplitTmux)
		}
	})
}

// TestResolveShellSplitMode_ConfigWindow verifies that [ui].shell_split =
// "window" is honored, including over iTerm2 env auto-detection, so the
// open_shell_here hotkey opens a tmux window instead of a split pane.
func TestResolveShellSplitMode_ConfigWindow(t *testing.T) {
	home := setXDGTestHome(t)
	writeXDGTestConfig(t, home, "[ui]\nshell_split = \"window\"\n")
	t.Setenv("LC_TERMINAL", "iTerm2")
	t.Setenv("TERM_PROGRAM", "iTerm.app")

	if got := resolveShellSplitMode(); got != session.ShellSplitWindow {
		t.Errorf("resolveShellSplitMode() = %q, want %q", got, session.ShellSplitWindow)
	}
}

// TestResolveShellSplitMode_RemoteSessionNotApplicable documents that
// shell_split — including the "window" mode — is intentionally local-only.
// The open_shell_here hotkey handler acts solely on ItemTypeSession rows
// (item.Type == session.ItemTypeSession in the hotkeyOpenShellHere case), so
// ItemTypeRemoteSession rows never reach openShellHere or
// resolveShellSplitMode: the shell pane/window must land in the session's
// local worktree, which a remote SSH session does not have on this machine.
// If open_shell_here ever gains remote support, this skip should be replaced
// with real RemoteSession coverage.
func TestResolveShellSplitMode_RemoteSessionNotApplicable(t *testing.T) {
	t.Skip("shell_split is local-only by design: open_shell_here ignores RemoteSession rows (no local worktree to open a shell in)")
}

// TestResolveShellSplitMode_DefaultIsTmux verifies that the safe default (tmux)
// is returned when no config and no iTerm env vars are set. Issue #1470.
func TestResolveShellSplitMode_DefaultIsTmux(t *testing.T) {
	t.Setenv("LC_TERMINAL", "")
	t.Setenv("TERM_PROGRAM", "")

	if got := resolveShellSplitMode(); got != session.ShellSplitTmux {
		t.Errorf("resolveShellSplitMode() with no env = %q, want %q", got, session.ShellSplitTmux)
	}
}
