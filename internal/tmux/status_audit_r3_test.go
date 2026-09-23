package tmux

import (
	"os"
	"path/filepath"
	"testing"
)

// Status-detection audit, review round 3 (2026-09-23).

// Review r2 P1: "delegate_task" was a plain, unanchored pi busy string, so a
// finished answer that merely names it kept an idle pi pane RUNNING for as
// long as the text stayed on screen. Neither pi nor pi-subagents prints that
// string; a live pi subagent runs inside a turn and pi draws its Working
// loader for it.
func TestPiDelegateTaskProseIsNotBusy(t *testing.T) {
	cases := []struct {
		file string
		want FrameVerdict
	}{
		{"pi-synth-idle-delegate-task-prose", FrameWaiting},
		{"pi-synth-subagent-working", FrameActive},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", "status_corpus", c.file+".txt"))
			if err != nil {
				t.Fatal(err)
			}
			if got := ClassifyPaneFrame("pi", string(raw)); got != c.want {
				t.Fatalf("ClassifyPaneFrame = %s, want %s", got, c.want)
			}
		})
	}
}
