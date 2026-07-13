package ui

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
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

func TestShouldSweepInstance(t *testing.T) {
	inst := &session.Instance{ID: "a"}
	hOff := &Home{claimPolling: false}
	if !hOff.shouldSweepInstance(inst) {
		t.Error("flag off must sweep everything non-archived")
	}
	hOn := &Home{claimPolling: true, ownedSessions: map[string]bool{"a": true}}
	if !hOn.shouldSweepInstance(inst) {
		t.Error("owned instance must be swept")
	}
	other := &session.Instance{ID: "b"}
	if hOn.shouldSweepInstance(other) {
		t.Error("non-owned instance must not be swept")
	}
}

func TestOrphanIDs(t *testing.T) {
	now := time.Now().Unix()
	claims := map[string]statedb.ClaimRow{
		"live":  {SessionID: "live", OwnerPID: 1, Heartbeat: now},
		"stale": {SessionID: "stale", OwnerPID: 2, Heartbeat: now - 120},
	}
	all := []string{"live", "stale", "unclaimed"}
	got := orphanIDs(all, claims, 15*time.Second)
	want := map[string]bool{"stale": true, "unclaimed": true}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for _, id := range got {
		if !want[id] {
			t.Errorf("unexpected orphan %q", id)
		}
	}
}
