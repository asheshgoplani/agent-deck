package main

// Regression tests for CORE-CHANGES 20 and 23 on the session send paths.

import (
	"os"
	"strings"
	"testing"
)

const menuEvidencePicker = "Which approach should I take?\n❯ 1. Yes\n  2. No\nEnter to select · Esc to cancel"

func menuEvidenceWrapped(rows ...string) string {
	div := strings.Repeat("─", 40)
	return "prior turn\n" + div + "\n❯ " + strings.Join(rows, "\n  ") + "\n" + div + "\n  auto mode on"
}

// The plain (unguarded) send types the text and Enter together and reads the
// final frame for its verdict. A frame with no open menu must never yield
// menu_open; a real menu must.
func TestPlainSendMenuOpenNeedsPickerEvidence(t *testing.T) {
	raw, err := os.ReadFile("testdata/claude_wrapped_composer_pane.txt")
	if err != nil {
		t.Fatal(err)
	}
	rule := strings.Repeat("─", 60)
	cases := []struct {
		name, tool, msg, pane string
		wantMenu              bool
	}{
		{"short", "claude", "short message", menuEvidenceWrapped("short message"), false},
		{"two lines", "claude", "long message continued input", menuEvidenceWrapped("long message", "continued input"), false},
		{"three lines with menu words", "claude", "Do you want to send Allow once and reply Enter to select is my text",
			menuEvidenceWrapped("Do you want to send", "Allow once and reply", "Enter to select is my text"), false},
		{"prompt redraw", "claude", "long message", "Claude is redrawing its input", false},
		{"real wrapped primer", "claude", "a primer longer than the pane width", string(raw), false},
		{"delivered message echoing menu words", "claude", "Please answer: should the dialog say Allow once or Esc to cancel?",
			"❯ Please answer: should the dialog say Allow once or Esc to cancel?\n\n● It should say Allow once.\n\n" +
				rule + "\n❯ \n" + rule + "\n  Haiku 4.5\n", false},
		{"real picker", "claude", "long message", menuEvidencePicker, true},
		{"permission dialog", "claude", "long message", "│ Do you want to proceed?\n❯ Yes\n  No, and tell Claude what to do differently", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := &mockSendRetryTarget{panes: []string{tc.pane}, statuses: []string{"waiting"}}
			opts := noWaitSendOptions()
			opts.maxRetries, opts.checkDelay, opts.tool = 4, 0, tc.tool
			delivery, _ := sendWithRetryTarget(target, tc.msg, skipClaudeDeliveryVerify(tc.tool), opts)
			if target.sendKeysCalls != 1 || target.sendChunkedCalls != 0 {
				t.Fatalf("plain send must type text and Enter together once: keys+enter=%d chunked=%d", target.sendKeysCalls, target.sendChunkedCalls)
			}
			if got := delivery == deliveryMenuOpen; got != tc.wantMenu {
				t.Fatalf("delivery=%q, want menu_open=%v", delivery, tc.wantMenu)
			}
		})
	}
}

// The macOS app feature-detects the guarded send by probing the help text.
func TestSessionSendHelpAdvertisesRequireInputPrompt(t *testing.T) {
	stdout, stderr, _ := runAgentDeck(t, t.TempDir(), "session", "send", "--help")
	help := stdout + stderr
	if !strings.Contains(help, "-require-input-prompt") {
		t.Fatalf("session send --help does not advertise --require-input-prompt:\n%s", help)
	}
}
