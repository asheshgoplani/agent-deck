package tmux

import (
	"strings"
	"testing"
)

// Lander review of #2775: the remote attach takes Ctrl+V only over an agent's composer.
func TestAgentComposerVisible(t *testing.T) {
	claude := "⏺ Done.\n\n\x1b[38;5;244m────────────────────────── work ─\n\x1b[38;5;246m❯ \x1b[39m\n" +
		"\x1b[38;5;244m──────────────────────────────────\n  Model X | Context 41.0% | 5h 24.0%\n  ⏵⏵ bypass permissions on\n\n\n"
	draft := "──────────────────────────────────\n❯ look at this and then fix the\n  wrapped draft line\n──────────────────────────────────\n  ? for shortcuts\n"
	cases := []struct {
		name, tool, pane string
		want             bool
	}{
		{"Claude composer, empty", "claude", claude, true},
		{"Claude composer with a wrapped draft", "claude", draft, true},
		{"vim", "claude", "package main\n~\n~\n\"main.go\" 4L, 29B      1,1   All\n", false},
		{"a shell", "claude", "user@box:~$ ls\nREADME.md\nuser@box:~$ \n", false},
		{"a permission dialog", "claude", "╭────────────╮\n│ Do you want to proceed? │\n│ ❯ 1. Yes │\n╰────────────╯\n", false},
		{"a composer far above the bottom (scrolled transcript)", "claude",
			"──────────────\n❯ old\n──────────────\n" + strings.Repeat("output line\n", 20), false},
		{"a shell tool never", "shell", claude, false},
		{"codex never", "codex", claude, false},
		{"empty", "claude", "", false},
	}
	for _, tc := range cases {
		if got := AgentComposerVisible(tc.tool, tc.pane); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
