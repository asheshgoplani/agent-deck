package tmux

// Regression tests for CORE-CHANGES 23: an interactive menu needs picker
// evidence. Every pane below except the ones marked as menus has no open
// menu, yet the classifier used to read menu chrome words anywhere in the
// last 15 rows (including the user's own composer text) as one. The same
// classifier feeds `session show` substate and the end-of-budget menu_open
// verdict of `session send`.

import (
	"os"
	"strings"
	"testing"
)

const pickerEvidenceMenu = "Which approach should I take?\n❯ 1. Yes\n  2. No\nEnter to select · Esc to cancel"

func pickerEvidenceWrapped(rows ...string) string {
	div := strings.Repeat("─", 40)
	return "prior turn\n" + div + "\n❯ " + strings.Join(rows, "\n  ") + "\n" + div + "\n  auto mode on"
}

func realWrappedClaudePane(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../cmd/agent-deck/testdata/claude_wrapped_composer_pane.txt")
	if err != nil {
		t.Fatal(err)
	}
	return StripANSI(string(raw))
}

func TestMenuNeedsPickerEvidenceMatrix(t *testing.T) {
	rule := strings.Repeat("─", 60)
	cases := []struct {
		name, tool, pane string
		wantMenu         bool
	}{
		{"short draft", "claude", pickerEvidenceWrapped("short message"), false},
		{"two line draft", "claude", pickerEvidenceWrapped("long message", "continued input"), false},
		{"three line draft with menu words", "claude", pickerEvidenceWrapped("Do you want to send", "Allow once and reply", "Enter to select is my text"), false},
		{"prompt redraw", "claude", "Claude is redrawing its input", false},
		{"real wrapped primer", "claude", realWrappedClaudePane(t), false},
		{"delivered message echoing menu words", "claude",
			"❯ Please answer: should the dialog say Allow once or Esc to cancel?\n\n● It should say Allow once.\n\n" +
				rule + "\n❯ \n" + rule + "\n  Haiku 4.5\n", false},
		{"reply rows starting with now and nothing", "claude",
			"● Here is the plan.\n  Now the dialog shows Allow once.\n  Nothing else changes; Esc to cancel still works.\n" +
				rule + "\n❯ \n" + rule + "\n  Haiku 4.5\n", false},
		{"question prose without choices", "claude",
			"● Do you want to review the summary? I can help.\n" + rule + "\n❯ \n" + rule + "\n  Haiku 4.5\n", false},
		{"reply explaining how to navigate", "claude",
			"● Use the sidebar to navigate; the dialog says Allow once.\n" + rule + "\n❯ \n" + rule + "\n  Haiku 4.5\n", false},
		{"permission footer without visible choices", "claude", " Bash command\n\n Esc to cancel · Tab to amend\n", true},
		{"real picker", "claude", pickerEvidenceMenu, true},
		{"permission dialog", "claude", "│ Do you want to proceed?\n❯ Yes\n  No, and tell Claude what to do differently", true},
		{"permission dialog with allow choices", "claude", "│ Bash command\n│ Do you want to proceed?\n│ ❯ 1. Yes\n│   2. Yes, and don't ask again\n│   3. No, and tell Claude what to do differently (esc)\n", true},
		{"Codex update prompt", "codex", "Update available\n❯ Update now\n  Later\nPress enter to confirm or esc to go back", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NewPromptDetector(tc.tool).ClassifySubstate(tc.pane)
			if (got == SubstateInteractiveMenu) != tc.wantMenu {
				t.Fatalf("substate=%q, want interactive-menu=%v", got, tc.wantMenu)
			}
		})
	}
}

// A typed draft that wraps onto two-space-indented rows is still Claude's
// live input box: the guarded send must be able to press Enter on it.
func TestRealWrappedClaudeDraftHasPrompt(t *testing.T) {
	if !NewPromptDetector("claude").HasPrompt(realWrappedClaudePane(t)) {
		t.Fatal("real wrapped Claude composer has no prompt verdict")
	}
}
