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
	openCodeFrame := "  ┃\n  ┃  Build · Big Pickle OpenCode\n  ╹▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀\n"
	openCodeFooter := "  ~/work    shift+tab agents  ctrl+p commands"
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
		{"OpenCode 2 home", "opencode2", "  ┃  Ask anything…\n" + openCodeFrame + openCodeFooter + "\n\n  2.3.2\n", true},
		{"OpenCode 2 session with draft and sidebar", "opencode", "  ┃  Existing draft\n" + openCodeFrame + openCodeFooter + "    ~/work\n\n", true},
		{"OpenCode 2 modal over the frame", "opencode", "  ┃  Exis    Search\n  ╹▀▀▀▀▀▀    Switch model    ▀▀▀▀▀▀\n" + openCodeFooter, false},
		{"OpenCode 2 modal above intact frame", "opencode", "                       Settings                   esc\n\n" +
			"                       Search\n\n" + openCodeFrame + openCodeFooter + "    ~/work\n\n", false},
		{"OpenCode 2 shell mode", "opencode", openCodeFrame + "  ~/work    esc exit shell mode    ~/work\n", false},
		{"OpenCode 2 exited to shell", "opencode", openCodeFrame + openCodeFooter + "\nuser@box:~/work$ ", false},
		{"OpenCode 1-style status text is not the V2 composer", "opencode", "Ask anything\npress enter to send\n", false},
	}
	for _, tc := range cases {
		if got := AgentComposerVisible(tc.tool, tc.pane); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
