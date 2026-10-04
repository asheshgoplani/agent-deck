package main

import (
	"slices"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/events"
)

// TestFollowDemandKinds (#2481 item 6): `events follow` takes a tmux.output
// lease only when its --kind filter would print tmux.output frames.
func TestFollowDemandKinds(t *testing.T) {
	cases := []struct {
		kinds []string
		want  bool
	}{
		{nil, true},
		{[]string{"tmux"}, true},
		{[]string{"tmux.output"}, true},
		{[]string{"session.status", "tmux."}, true},
		{[]string{"session.status"}, false},
		{[]string{"session.transition", "session.status", "session.turn", "macapp."}, false},
		{[]string{"macapp."}, false},
	}
	for _, c := range cases {
		got := slices.Contains(followDemandKinds(c.kinds), events.KindTmuxOutput)
		if got != c.want {
			t.Errorf("--kind %v: takes tmux.output lease = %v, want %v", c.kinds, got, c.want)
		}
	}
}
