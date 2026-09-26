package tmux

import (
	"os"
	"path/filepath"
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
