package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// The outer-tmux refusal (#560) keeps its exit but reads like the quick
// check page, and names the opt-in line exactly (ASK-211).
func TestOuterTmuxRefusalText_CheckPageStyle(t *testing.T) {
	got := outerTmuxRefusalText("tmux 3.5a", "1.16.36")
	for _, want := range []string{
		"Agent Deck · quick check",
		"You are inside tmux.",
		"✓ tmux         tmux 3.5a · you are inside it",
		"✓ agent-deck   v1.16.36",
		"Detach (Ctrl+B d) and run agent-deck from a plain shell.",
		"\n  AGENT_DECK_ALLOW_OUTER_TMUX=1 agent-deck\n",
		"agent-deck list",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("refusal lacks %q:\n%s", want, got)
		}
	}
	for _, gone := range []string{"Error:", "designed to run OUTSIDE", "Run anyway"} {
		if strings.Contains(got, gone) {
			t.Errorf("refusal still says %q:\n%s", gone, got)
		}
	}
	// Short and plain: fits an 80-column terminal, no line over 72.
	for _, l := range strings.Split(got, "\n") {
		if n := utf8.RuneCountInString(l); n > 72 {
			t.Errorf("line is %d wide (max 72): %q", n, l)
		}
	}
}

func TestOuterTmuxRefusalText_UnknownTmuxVersion(t *testing.T) {
	got := outerTmuxRefusalText("", "1.16.36")
	if !strings.Contains(got, "✓ tmux         you are inside it\n") {
		t.Fatalf("no-version tmux row wrong:\n%s", got)
	}
}

func TestTmuxMissingCheckText_PerOS(t *testing.T) {
	cases := []struct {
		goos string
		want []string
	}{
		{"darwin", []string{"Mac:    brew install tmux"}},
		{"linux", []string{"sudo apt install tmux", "sudo dnf install tmux", "sudo pacman -S tmux"}},
		{"freebsd", []string{"https://github.com/tmux/tmux/wiki/Installing"}},
	}
	for _, tc := range cases {
		t.Run(tc.goos, func(t *testing.T) {
			got := tmuxMissingCheckText(tc.goos, "1.16.36", "/usr/bin:/bin")
			for _, want := range append([]string{
				"Agent Deck · quick check",
				"✗ tmux         not found. Every session runs inside tmux.",
				"✓ agent-deck   v1.16.36",
				"Install tmux, then run agent-deck again.",
				"Searched PATH: /usr/bin:/bin",
			}, tc.want...) {
				if !strings.Contains(got, want) {
					t.Errorf("missing-tmux text lacks %q:\n%s", want, got)
				}
			}
			if tc.goos == "darwin" && strings.Contains(got, "apt") {
				t.Errorf("Mac text shows a Linux install line:\n%s", got)
			}
		})
	}
}
