package session

// Maestro fleet-supervisor presentation (TUI Phase 1).
//
// Maestro is the orchestrator-of-orchestrators: it sits one level above
// conductors (see ~/.agent-deck/conductor/agent-deck/maestro-design.md).
// Phase-1 identification is by EXACT session title — the same convention
// conductors used before graduating to the is_conductor column in the v4
// schema migration. Exact match is load-bearing: regular worker sessions
// titled "maestro-<something>" must NOT be detected as the supervisor.

import (
	"testing"
	"time"
)

func TestIsMaestro_ExactTitleOnly(t *testing.T) {
	cases := []struct {
		title string
		want  bool
	}{
		{"conductor-maestro", true},
		{"conductor-agent-deck", false},
		{"maestro-user-test", false}, // worker session, not the supervisor
		{"maestro", false},
		{"", false},
		{"conductor-maestro-2", false},
	}
	for _, c := range cases {
		inst := &Instance{Title: c.title}
		if got := inst.IsMaestro(); got != c.want {
			t.Errorf("IsMaestro() for title %q = %v, want %v", c.title, got, c.want)
		}
	}
}

// The maestro must surface first within its group regardless of status:
// a stopped supervisor still outranks a running worker, because the row is
// a fixed point of reference (the fleet supervisor), not an activity item.
func TestSortInstancesByActionable_MaestroFirst(t *testing.T) {
	SetGroupSortMode("actionable")
	t.Cleanup(func() { SetGroupSortMode("creation") })
	now := time.Now()
	maestro := &Instance{Title: "conductor-maestro", Status: StatusStopped, LastAccessedAt: now.Add(-24 * time.Hour), Order: 5}
	worker := &Instance{Title: "busy-worker", Status: StatusRunning, LastAccessedAt: now, Order: 0}
	conductor := &Instance{Title: "conductor-agent-deck", Status: StatusWaiting, LastAccessedAt: now, Order: 1}

	insts := []*Instance{worker, conductor, maestro}
	SortInstancesByActionable(insts)

	if insts[0].Title != "conductor-maestro" {
		t.Fatalf("maestro must sort first within its group; got order: %s, %s, %s",
			insts[0].Title, insts[1].Title, insts[2].Title)
	}
	// Non-maestro relative order must keep issue #857 semantics
	// (error/waiting before running? no — waiting outranks running).
	if insts[1].Title != "conductor-agent-deck" || insts[2].Title != "busy-worker" {
		t.Fatalf("non-maestro rows must keep actionable sort; got: %s, %s",
			insts[1].Title, insts[2].Title)
	}
}

// In the default "creation" mode the maestro still surfaces first within its
// group (it is band -1, exempt from the normal-band sort), and the non-maestro
// rows follow in creation Order — not status/recency. Guards the pin/Maestro
// invariant for the group_sort default.
func TestSortInstancesByActionable_MaestroFirst_CreationMode(t *testing.T) {
	SetGroupSortMode("creation")
	t.Cleanup(func() { SetGroupSortMode("creation") })
	now := time.Now()
	maestro := &Instance{Title: "conductor-maestro", Status: StatusStopped, LastAccessedAt: now.Add(-24 * time.Hour), Order: 5}
	worker := &Instance{Title: "busy-worker", Status: StatusRunning, LastAccessedAt: now, Order: 0}
	conductor := &Instance{Title: "conductor-agent-deck", Status: StatusWaiting, LastAccessedAt: now, Order: 1}

	insts := []*Instance{worker, conductor, maestro}
	SortInstancesByActionable(insts)

	if insts[0].Title != "conductor-maestro" {
		t.Fatalf("maestro must sort first even in creation mode; got: %s, %s, %s",
			insts[0].Title, insts[1].Title, insts[2].Title)
	}
	// Non-maestro rows follow by creation Order (worker=0 before conductor=1),
	// NOT by actionable status (which would put the waiting conductor first).
	if insts[1].Title != "busy-worker" || insts[2].Title != "conductor-agent-deck" {
		t.Fatalf("non-maestro rows must order by creation Order; got: %s, %s",
			insts[1].Title, insts[2].Title)
	}
}

// The group containing the maestro must be pinned to the very top of the
// group list — above even the legacy "conductor" group pin (Order=-1).
func TestGroupTree_MaestroGroupPinnedFirst(t *testing.T) {
	insts := []*Instance{
		{Title: "some-app", GroupPath: "alpha"},
		{Title: "conductor-legacy", GroupPath: "conductor"},
		{Title: "conductor-maestro", GroupPath: "conductors"},
		{Title: "conductor-agent-deck", GroupPath: "conductors"},
	}
	tree := NewGroupTree(insts)

	if len(tree.GroupList) < 3 {
		t.Fatalf("expected at least 3 groups, got %d", len(tree.GroupList))
	}
	if tree.GroupList[0].Path != "conductors" {
		var order []string
		for _, g := range tree.GroupList {
			order = append(order, g.Path)
		}
		t.Fatalf("group containing the maestro must be first; got group order: %v", order)
	}
	if tree.GroupList[1].Path != "conductor" {
		t.Fatalf("legacy conductor group must keep its pin right below the maestro group; got %q", tree.GroupList[1].Path)
	}
}

// Without a maestro anywhere, group ordering must be unchanged
// (legacy "conductor" pin still first).
func TestGroupTree_NoMaestro_LegacyPinUnchanged(t *testing.T) {
	insts := []*Instance{
		{Title: "some-app", GroupPath: "alpha"},
		{Title: "conductor-legacy", GroupPath: "conductor"},
		{Title: "maestro-user-test", GroupPath: "workers"}, // worker, not supervisor
	}
	tree := NewGroupTree(insts)

	if tree.GroupList[0].Path != "conductor" {
		var order []string
		for _, g := range tree.GroupList {
			order = append(order, g.Path)
		}
		t.Fatalf("legacy conductor pin regressed; got group order: %v", order)
	}
}
