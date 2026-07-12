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

// TestResolveShellSplitMode_DefaultIsTmux verifies that the safe default (tmux)
// is returned when no config and no iTerm env vars are set. Issue #1470.
func TestResolveShellSplitMode_DefaultIsTmux(t *testing.T) {
	t.Setenv("LC_TERMINAL", "")
	t.Setenv("TERM_PROGRAM", "")

	if got := resolveShellSplitMode(); got != session.ShellSplitTmux {
		t.Errorf("resolveShellSplitMode() with no env = %q, want %q", got, session.ShellSplitTmux)
	}
}
