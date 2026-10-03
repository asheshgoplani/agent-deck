package ui

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Issue #2473: the preview's one-line background-work detail.
func TestBackgroundWorkLine(t *testing.T) {
	cases := []struct {
		work tmux.BackgroundWork
		want string
	}{
		{tmux.BackgroundWork{}, ""},
		{tmux.BackgroundWork{Kind: tmux.BackgroundKindWorkflow, Task: "comms-followon-round3", Step: 3, Steps: 5, Elapsed: "18m32s"}, "background: comms-followon-round3 3/5 · 18m32s"},
		{tmux.BackgroundWork{Kind: tmux.BackgroundKindBash, Task: "2 shells, 1 monitor"}, "background: 2 shells, 1 monitor"},
		{tmux.BackgroundWork{Kind: tmux.BackgroundKindAgent}, "background: agent"},
	}
	for _, c := range cases {
		if got := backgroundWorkLine(c.work); got != c.want {
			t.Errorf("backgroundWorkLine(%+v) = %q, want %q", c.work, got, c.want)
		}
	}
}
