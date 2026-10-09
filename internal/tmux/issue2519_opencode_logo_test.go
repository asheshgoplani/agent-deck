package tmux

import (
	"testing"
	"time"
)

const issue2519Logo = "█▀▀█ █▀▀█ █▀▀█ █▀▀▄ █▀▀▀ █▀▀█ █▀▀█ █▀▀█\n█  █ █  █ █▀▀▀ █  █ █    █  █ █  █ █▀▀▀\n▀▀▀▀ █▀▀▀ ▀▀▀▀ ▀▀▀▀ ▀▀▀▀ ▀▀▀▀ ▀▀▀▀ ▀▀▀▀"

func TestIssue2519OpenCodeLogo(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		busy          bool
	}{
		{"fresh prompt", issue2519Logo + "\nAsk anything…", false},
		{"logo without prompt", issue2519Logo, false},
		{"colored logo", "\x1b[36m" + issue2519Logo + "\x1b[0m\nAsk anything…", false},
		{"busy hint with prompt", issue2519Logo + "\nAsk anything…\nesc interrupt", true},
		{"busy task with prompt", issue2519Logo + "\n█ Thinking...\nAsk anything…", true},
		{"spinner above logo", "▓ Running checks\n" + issue2519Logo + "\nAsk anything…", true},
		{"bare pulse", "█\nAsk anything…", true},
		{"solid pulse", "██\nAsk anything…", true},
		{"spaced solid pulse", "█ █\nAsk anything…", true},
		{"shaded pulse", "█▓▒░\nAsk anything…", true},
		{"task spinner", "█ Running checks\nAsk anything…", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewSession("issue2519", t.TempDir())
			s.Command = "opencode"
			if got := s.hasBusyIndicator(tc.content); got != tc.busy {
				t.Fatalf("busy=%v, want %v", got, tc.busy)
			}
		})
	}
}

func TestIssue2519OpenCodeBusyThenIdle(t *testing.T) {
	s := NewSession("issue2519", t.TempDir())
	s.Command = "opencode"
	if !s.hasBusyIndicator("█ Working\nesc interrupt") {
		t.Fatal("busy work missed")
	}
	idle := issue2519Logo + "\nAsk anything…"
	if !s.hasBusyIndicator(idle) {
		t.Fatal("transition grace lost")
	}
	s.stateTracker.spinnerTracker.lastBusyTime = time.Now().Add(-time.Hour)
	if s.hasBusyIndicator(idle) {
		t.Fatal("logo keeps completed work busy")
	}
}

// A busy OpenCode pane keeps its composer placeholder visible, so prompt
// text must not hide a live pulse; the question UI help bar still does.
func TestIssue2519OpenCodeBusyWithComposer(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		busy          bool
	}{
		{"pulse frame with composer and bare footer",
			"┃ fix the failing test\n\n█ Running go test ./...\n\n┃ Ask anything…\n⬝⬝■■■■⬝⬝  esc interrupt", true},
		{"shaded pulse frame with composer",
			"▒ Reading internal/tmux/tmux.go\n┃ Ask anything…", true},
		{"logo with composer is idle",
			issue2519Logo + "\n┃ Ask anything… \"Fix a TODO in the codebase\"", false},
		{"question UI progress bar is idle",
			"░ Progress: 100%\nenter submit     esc dismiss", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewSession("issue2523", t.TempDir())
			s.Command = "opencode"
			if got := s.hasBusyIndicator(tc.content); got != tc.busy {
				t.Fatalf("busy=%v, want %v", got, tc.busy)
			}
		})
	}
}
