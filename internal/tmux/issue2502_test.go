package tmux

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIssue2502_PublishedCaptures(t *testing.T) {
	for _, name := range []string{"pair-006", "pair-066", "pair-088"} {
		t.Run(name, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join("testdata", "issue2502", name+".txt"))
			if err != nil {
				t.Fatal(err)
			}
			d := NewPromptDetector("claude")
			if got := d.ClassifySubstate(string(b)); got != SubstateRunning {
				t.Errorf("substate = %q, want running", got)
			}
			if d.HasPrompt(string(b)) {
				t.Error("live spinner must not be an idle prompt")
			}
			if d.CompletedTurnAtIdlePrompt(string(b)) {
				t.Error("live spinner must not confirm hook lag")
			}
		})
	}
}

// Synthetic adversarial cases, distinct from the three published excerpts.
func TestIssue2502_SpinnerBoundaries(t *testing.T) {
	const composer = "\n────────────────────────\n❯\n────────────────────────\n  Haiku 4.5\n"
	cases := []struct {
		name, frame string
		want        Substate
	}{
		{"completed", "✻ Simmered for 2s · done 8:34 PM" + composer, SubstateIdleAtEmptyPrompt},
		{"old turn", "✻ Simmering…\n❯ next task\n⏺ Done.\n✻ Worked for 2s" + composer, SubstateIdleAtEmptyPrompt},
		{"quoted tool result", "⎿ ✻ Simmering…" + composer, SubstateIdleAtEmptyPrompt},
		{"prose", "⏺ The status is ✻ Simmering…" + composer, SubstateIdleAtEmptyPrompt},
		{"completed after spinner", "✻ Simmering…\n✻ Worked for 2s" + composer, SubstateIdleAtEmptyPrompt},
		{"question menu", "✻ Simmering…\nWhich option?\n❯ 1. Option A\n  2. Option B\nEnter to select · Tab/Arrow keys to navigate · Esc to cancel", SubstateInteractiveMenu},
		{"timed live", "✻ Simmering… (2s · ↓ 100 tokens)" + composer, SubstateRunning},
		{"ansi", "\x1b[33m✻ Simmering…\x1b[0m" + composer, SubstateRunning},
		{"tip", "✽ Kerfuffling…\n  ⎿  Tip: Use /voice to enable push-to-talk dictation" + composer, SubstateRunning},
	}
	d := NewPromptDetector("claude")
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := d.ClassifySubstate(c.frame); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestIssue2502_AllSpinnerGlyphs(t *testing.T) {
	for _, glyph := range []string{"✳", "✽", "✶", "✻", "✢", "·"} {
		frame := glyph + " Simmering…\n────────────────────────\n❯\n────────────────────────\n"
		if got := NewPromptDetector("claude").ClassifySubstate(frame); got != SubstateRunning {
			t.Errorf("%s: %q, want running", glyph, got)
		}
	}
}
