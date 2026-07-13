package ui

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestPathInScope(t *testing.T) {
	cases := []struct {
		path, scope string
		want        bool
	}{
		{"fitstars", "", true},                 // empty scope matches all
		{"fitstars", "fitstars", true},         // exact
		{"fitstars/starfit", "fitstars", true}, // child
		{"fitstars2", "fitstars", false},       // prefix but not path-boundary
		{"base", "fitstars", false},
	}
	for _, c := range cases {
		if got := pathInScope(c.path, c.scope); got != c.want {
			t.Errorf("pathInScope(%q,%q) = %v, want %v", c.path, c.scope, got, c.want)
		}
	}
}

func TestSplitInstancesByScope(t *testing.T) {
	mk := func(id, group string) *session.Instance {
		return &session.Instance{ID: id, GroupPath: group}
	}
	in, out := splitInstancesByScope(
		[]*session.Instance{mk("a", "fitstars"), mk("b", "fitstars/starfit"), mk("c", "base")},
		"fitstars",
	)
	if len(in) != 2 || len(out) != 1 || out[0].ID != "c" {
		t.Errorf("split wrong: in=%v out=%v", len(in), len(out))
	}
}

func TestIsOwnedFlagOff(t *testing.T) {
	h := &Home{claimPolling: false}
	if !h.isOwned("anything") {
		t.Error("flag off must own everything (today's behavior)")
	}
}

func TestIsOwnedFlagOn(t *testing.T) {
	h := &Home{claimPolling: true, ownedSessions: map[string]bool{"a": true}}
	if !h.isOwned("a") || h.isOwned("b") {
		t.Error("owned-set gating broken")
	}
}
