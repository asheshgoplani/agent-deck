package main

import (
	"os"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

type enterRecordingTarget struct {
	*mockSendRetryTarget
	entered bool
}

func (m *enterRecordingTarget) SendKeysAndEnterChecked(_ string, capture func() (string, error), check tmux.PostPasteCheck) error {
	pane, err := capture()
	ok, checkErr := check(pane, err)
	if !ok {
		return checkErr
	}
	m.entered = true
	return nil
}

// Captured from a real Claude pane after a long primer was
// pasted and wrapped but before Enter. The bytes include Claude's ANSI UI.
func TestGuardedRealWrappedClaudeComposerRemainsSendable(t *testing.T) {
	raw, err := os.ReadFile("testdata/claude_wrapped_composer_pane.txt")
	if err != nil {
		t.Fatal(err)
	}
	pane := string(raw)
	if !strings.Contains(tmux.StripANSI(pane), "\n  reply BUSY-DONE") {
		t.Fatal("fixture lost the wrapped input line")
	}
	guard := &promptGuardTarget{tool: "claude"}
	if err := guard.check(pane, nil); err != nil {
		t.Fatalf("real wrapped Claude composer was refused: %v; delivery=%s", err, func() string { d, _ := guard.refusal(); return d }())
	}
	target := &enterRecordingTarget{mockSendRetryTarget: &mockSendRetryTarget{panes: []string{claudeComposer(""), pane}}}
	guard = &promptGuardTarget{sendRetryTarget: target, tool: "claude"}
	if err := guard.SendKeysAndEnterChecked("a primer longer than the pane width", nil, nil); err != nil {
		t.Fatalf("guarded transport refused Enter after wrapped paste: %v", err)
	}
	if !target.entered {
		t.Fatal("guarded transport withheld Enter after wrapped paste")
	}
}

func TestGuardedRefusalAfterTypingReportsTypedNotSubmitted(t *testing.T) {
	guard := &promptGuardTarget{tool: "claude", typedBatches: 1}
	if err := guard.check(guardedMenu, nil); err == nil {
		t.Fatal("real menu should refuse Enter")
	}
	delivery, reason := guard.refusal()
	if delivery != deliveryTypedNotSubmitted || !strings.Contains(reason.Error(), "Which approach") {
		t.Fatalf("delivery=%q reason=%v", delivery, reason)
	}
}

func TestGuardedPostPasteMatrix(t *testing.T) {
	div := strings.Repeat("─", 40)
	wrapped := func(rows ...string) string {
		return "prior turn\n" + div + "\n❯ " + strings.Join(rows, "\n  ") + "\n" + div + "\n  auto mode on"
	}
	cases := []struct {
		name, tool, after string
		wantEnter         bool
	}{
		{"short", "claude", wrapped("short message"), true},
		{"two lines", "claude", wrapped("long message", "continued input"), true},
		{"three lines with menu words", "claude", wrapped("Do you want to send", "Allow once and reply", "Enter to select is my text"), true},
		{"prompt disappears after typing", "claude", "Claude is redrawing its input", true},
		{"real picker", "claude", guardedMenu, false},
		{"permission dialog", "claude", "│ Do you want to proceed?\n❯ Yes\n  No, and tell Claude what to do differently", false},
		{"Codex update prompt", "codex", "Update available\n❯ Update now\n  Later\nPress enter to confirm or esc to go back", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := claudeComposer("")
			if tc.tool == "codex" {
				before = "› Write a message\n  gpt-6-luna · /tmp/project"
			}
			target := &enterRecordingTarget{mockSendRetryTarget: &mockSendRetryTarget{panes: []string{before, tc.after}}}
			guard := &promptGuardTarget{sendRetryTarget: target, tool: tc.tool}
			err := guard.SendKeysAndEnterChecked("long message", nil, nil)
			if target.entered != tc.wantEnter {
				t.Fatalf("entered=%v want=%v err=%v", target.entered, tc.wantEnter, err)
			}
			if tc.wantEnter && err != nil {
				t.Fatal(err)
			}
			if !tc.wantEnter {
				delivery, reason := guard.refusal()
				if err == nil || delivery != deliveryTypedNotSubmitted || !strings.Contains(reason.Error(), strings.Split(tc.after, "\n")[0]) {
					t.Fatalf("err=%v delivery=%q reason=%v", err, delivery, reason)
				}
			}
		})
	}
}
