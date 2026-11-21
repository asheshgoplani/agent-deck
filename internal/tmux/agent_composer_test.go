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
	openCodeFrame := "  ┃\n  ┃  Reviewing the change\n  ┃\n  ┃  Build · Big Pickle OpenCode\n  ╹▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀\n"
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
		{"OpenCode home", "opencode2", "  ┃  Ask anything…\n" + openCodeFrame + openCodeFooter + "\n\n  2.3.2\n", true},
		{"OpenCode home with a prerelease version", "opencode2", "  ┃  Ask anything…\n" + openCodeFrame + openCodeFooter + "\n\n  0.0.0-beta-19271\n", true},
		{"OpenCode session with draft and sidebar", "opencode", "  ┃  Existing draft\n" + openCodeFrame + openCodeFooter + "    ~/work\n\n", true},
		{"OpenCode narrow pane hides the shortcut hints", "opencode", "  ┃  Existing draft\n" + openCodeFrame + "\n", true},
		{"OpenCode usage readout replaces the hints", "opencode", "  ┃  Existing draft\n" + openCodeFrame + "  ~/work    12% · $0.05\n", true},
		{"OpenCode rebound command palette shortcut", "opencode", "  ┃  Existing draft\n" + openCodeFrame + "  ~/work    shift+tab agents  ctrl+k commands\n", true},
		{"OpenCode dialog over a full-width transcript row", "opencode", "  ┃  line 06 yyyyyyyyyyyyyyyyy    Commands                                         esc    yyyyy\n" + openCodeFrame + openCodeFooter, false},
		{"OpenCode help dialog", "opencode", "\n                       Help                   esc/enter\n\n", false},
		{"OpenCode modal over the frame", "opencode", "  ┃  Exis    Search\n  ╹▀▀▀▀▀▀    Switch model    ▀▀▀▀▀▀\n" + openCodeFooter, false},
		{"OpenCode modal above intact frame", "opencode", "                       Settings                   esc\n\n" +
			"                       Search\n\n" + openCodeFrame + openCodeFooter + "    ~/work\n\n", false},
		{"OpenCode shell mode", "opencode", openCodeFrame + "  ~/work    esc exit shell mode    ~/work\n", false},
		{"OpenCode narrow shell mode", "opencode", openCodeFrame + "  esc shell\n", false},
		{"OpenCode busy footer", "opencode", "  ┃  Existing draft\n" + openCodeFrame + "  esc interrupt\n", false},
		{"OpenCode armed interrupt footer", "opencode", "  ┃  Existing draft\n" + openCodeFrame + "  esc again to interrupt\n", false},
		{"OpenCode exited to shell", "opencode", openCodeFrame + openCodeFooter + "\nuser@box:~/work$ ", false},
		{"OpenCode exited to a footerless prompt", "opencode", openCodeFrame + "❯ ", false},
		{"OpenCode exited to a footerless bash prompt", "opencode", openCodeFrame + "bash-5.2$ ", false},
		{"OpenCode exited to a footerless starship prompt", "opencode", openCodeFrame + "➜  repo git:(main) ", false},
		{"OpenCode frame with only two spine rows", "opencode", "  ┃\n  ┃  Build · Big Pickle OpenCode\n  ╹▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀\n", false},
		{"OpenCode footerless frame with the minimum spine rows", "opencode", "  ┃\n  ┃  Draft\n  ┃  Build · Big Pickle OpenCode\n  ╹▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀\n", true},
		{"OpenCode sessions dialog on a narrow pane", "opencode",
			"     Sessionfor work-a-really-long-project-directory-sc\n" +
				"     s     name-for-review-3232\n" +
				"     Search\n" +
				"     No sessions available\n" +
				"     delete ctrl+d  rename ctrl+r     all projects ctrl+a\n" +
				"  ┃\n" +
				"  ┃  Build · Big Pickle OpenCode\n" +
				"  ╹▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀\n" +
				"  /…/work-a-really-lon…  shift+tab agents  ctrl+p commands\n", false},
		{"OpenCode 1-style status text is not the frame composer", "opencode", "Ask anything\npress enter to send\n", false},
	}
	for _, tc := range cases {
		if got := AgentComposerVisible(tc.tool, tc.pane); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
