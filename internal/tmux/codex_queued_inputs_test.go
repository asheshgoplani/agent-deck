package tmux

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Codex 0.155 lists queued follow-up inputs between the live status line and
// the composer (rc feedback 2026-09-23). The live slot must look past that
// block, and only past that block.
func TestCodexLiveStatusLine_QueuedFollowUpInputs(t *testing.T) {
	const tail = "\n› Ask Codex to do anything\n\n" +
		"  gpt-6-sol · /private/tmp/exec-macapp-v4 · Context 25% left · Context 75% used · weekly 87% left · 258K window · Main [default]\n"
	const queue = "• Queued follow-up inputs\n" +
		"  ↳ xxx xxxx xxxxxx: xxxxxxx xxxxxx xxxxx\n" +
		"    xxxxxxxxxxx xx xxxxx xxxxxx xx xxxxxxx.xx.\n" +
		"  ↳ xxx xxxx xxxxxx\n" +
		"    shift + ← edit last queued message\n"
	cases := []struct {
		name  string
		frame string
		want  bool
	}{
		{"working-above-queue", "• Working (32m 32s • esc to interrupt) · 1 background terminal running · /ps to view · /stop to close\n\n" + queue + tail, true},
		{"compacting-above-queue", "• Compacting context (48s • esc to interrupt) · 1 background terminal running\n  └ Making room to continue.\n\n" + queue + tail, true},
		{"worked-for-rule-above-queue", "• xxxx xxxxx \"Working (9m 41s • esc to interrupt)\" xxxx\n\n─ Worked for 2m 07s ──────────\n\n" + queue + tail, false},
		{"header-without-entries", "• Working (1m 02s • esc to interrupt)\n\n• Queued follow-up inputs\n" + tail, false},
		{"indented-rows-without-header", "• Ran grep \"Working (9m 41s • esc to interrupt)\"\n    xxx xxxxx\n    xxxx\n" + tail, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := codexLiveStatusLine(c.frame); got != c.want {
				t.Fatalf("codexLiveStatusLine = %v, want %v\n%s", got, c.want, c.frame)
			}
		})
	}

	for _, name := range []string{
		"codex-local-macapp-v4-working-queued-inputs_af58e2b8",
		"codex-local-macapp-v4-compacting-queued-inputs_af58e2b8",
		"codex-local-macapp-v4-working-queued-later_af58e2b8",
	} {
		raw, err := os.ReadFile(filepath.Join("testdata", "status_corpus", name+".txt"))
		if err != nil {
			t.Fatal(err)
		}
		if got := ClassifyPaneFrame("codex", string(raw)); got != FrameActive {
			t.Fatalf("%s: ClassifyPaneFrame = %q, want active", name, got)
		}
	}
}

// Minimal sanitized layout from a captured queued-question frame. Only the
// structural status, queue and footer rows are retained; no transcript text.
func TestCodexLiveStatusLine_QueuedQuestions(t *testing.T) {
	const working = "◦ Working (4m 12s • esc to interrupt)\n\n"
	const header = "• Queued follow-up inputs\n"
	const summary = "  ? 1 question\n"
	const hint = "    shift+← to answer\n"
	const tail = "\n› Ask Codex to do anything\n\n  model · /tmp/project · Context 50% left\n  ? for shortcuts\n"
	cases := []struct {
		name, frame string
		busy        bool
	}{
		{"captured layout", working + header + summary + hint + tail, true},
		{"plural questions", working + header + "  ? 12 questions\n" + hint + tail, true},
		{"padded summary", working + header + "  ? 1 question   \n" + hint + tail, true},
		{"wrapped messages and hint", working + header + "  ↳ next task\n    wrapped continuation\n" + summary + "    shift+← to\n    answer\n" + tail, true},
		{"wide warning before composer", working + header + summary + hint + strings.Repeat(" ", 240) + "⚠ weekly limit: 20% left\n" + tail, true},
		{"questions alone are idle", header + summary + hint + tail, false},
		{"completed turn is idle", working + "─ Worked for 12s ─\n\n" + header + summary + hint + tail, false},
		{"quoted status with continuation", "• Example (4m 12s • esc to interrupt)\n  quoted status, no live turn\n" + header + summary + hint + tail, false},
		{"summary without header", working + summary + hint + tail, false},
		{"arbitrary question text", working + header + "  ? should we continue?\n" + hint + tail, false},
		{"zero questions", working + header + "  ? 0 questions\n" + hint + tail, false},
		{"invalid plural", working + header + "  ? 1 questions\n" + hint + tail, false},
		{"extra summary prose", working + header + "  ? 1 question is pending\n" + hint + tail, false},
		{"wrong summary indentation", working + header + "    ? 1 question\n" + hint + tail, false},
		{"footer is not a queue entry", working + header + hint + tail + summary, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := codexLiveStatusLine(c.frame); got != c.busy {
				t.Fatalf("codexLiveStatusLine = %v, want %v", got, c.busy)
			}
			if got := CodexTurnRunning(c.frame); got != c.busy {
				t.Errorf("CodexTurnRunning = %v, want %v", got, c.busy)
			}
			want := FrameWaiting
			if c.busy {
				want = FrameActive
			}
			if got := ClassifyPaneFrame("codex", c.frame); got != want {
				t.Errorf("ClassifyPaneFrame = %q, want %q", got, want)
			}
		})
	}
}
