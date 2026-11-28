package ui

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/terminal"
	tea "github.com/charmbracelet/bubbletea"
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

func TestOpenShellHereRemote(t *testing.T) {
	for _, mode := range []string{"tmux", "window", "iterm"} {
		t.Run(mode, func(t *testing.T) {
			home := setXDGTestHome(t)
			writeXDGTestConfig(t, home, "[ui]\nshell_split = \""+mode+"\"\n[remotes.lab]\nhost = \"alice@lab\"\n")

			h := &Home{
				profile:   "work",
				groupTree: session.NewGroupTree(nil),
				flatItems: []session.Item{{
					Type:          session.ItemTypeRemoteSession,
					RemoteName:    "lab",
					RemoteSession: &session.RemoteSessionInfo{ID: "remote-id"},
				}},
			}
			h.setHotkeys(resolveHotkeys(nil))
			var splitCommand string
			h.openInSplitPaneSink = func(req terminal.AttachRequest) error {
				splitCommand = terminal.BuildAttachCommand(req)
				return nil
			}
			_, cmd := h.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'H'}})
			if mode == "iterm" {
				if cmd != nil || !strings.Contains(splitCommand, "-p 'work' remote attach --shell='tmux' 'lab' 'remote-id'") {
					t.Fatalf("iTerm2 split command = %q; want remote attach that opens a tmux split", splitCommand)
				}
			} else if cmd == nil || !h.isAttaching.Load() || splitCommand != "" {
				t.Fatalf("expected inline remote attach for %s", mode)
			}
		})
	}
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
