package ui

import (
	"sort"
	"testing"
)

func TestPipeLiveSet_TouchLRUEviction(t *testing.T) {
	s := newPipeLiveSet(3)
	for _, n := range []string{"a", "b", "c", "d"} { // d evicts a
		s.touch(n)
	}
	if s.want("a") {
		t.Fatal("a should have been evicted from a capacity-3 LRU")
	}
	for _, n := range []string{"b", "c", "d"} {
		if !s.want(n) {
			t.Fatalf("%s should be live", n)
		}
	}
}

func TestPipeLiveSet_TouchPromotesAndDedupes(t *testing.T) {
	s := newPipeLiveSet(3)
	s.touch("a")
	s.touch("b")
	s.touch("c")
	s.touch("a") // promote a to front; now order a,c,b
	s.touch("d") // evicts the tail (b), not a
	if !s.want("a") {
		t.Fatal("a was just touched; must survive")
	}
	if s.want("b") {
		t.Fatal("b was the LRU tail and should be evicted")
	}
}

func TestPipeLiveSet_EmptyTouchIsNoop(t *testing.T) {
	s := newPipeLiveSet(3)
	s.touch("")
	if len(s.members()) != 0 {
		t.Fatalf("empty touch must not add a member, got %v", s.members())
	}
}

func TestPipeLiveSet_AttachedPinnedAndDeduped(t *testing.T) {
	s := newPipeLiveSet(2)
	s.touch("a")
	s.touch("b")
	s.setAttached("z")
	if !s.want("z") {
		t.Fatal("attached session must be live")
	}
	// attached also present in LRU must not appear twice in members
	s.setAttached("a")
	got := s.members()
	count := 0
	for _, m := range got {
		if m == "a" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("member 'a' duplicated: %v", got)
	}
	if got[0] != "a" {
		t.Fatalf("attached session must be first in members, got %v", got)
	}
}

func TestPipeLiveSet_MembersEmptyIsNonNil(t *testing.T) {
	s := newPipeLiveSet(3)
	got := s.members()
	if got == nil {
		t.Fatal("members() on empty set should return non-nil empty slice")
	}
	if len(got) != 0 {
		t.Fatalf("members() on empty set should be empty, got %v", got)
	}
}

func TestPipeLiveSet_SetAttachedEmptyClearsPin(t *testing.T) {
	s := newPipeLiveSet(2)
	s.setAttached("z")
	s.setAttached("")
	if s.want("z") {
		t.Fatal("clearing attached pin should drop z (not in LRU)")
	}
}

// fakeConnector records connect/disconnect calls for reconcilePipes tests.
type fakeConnector struct {
	connected map[string]bool
	connects  []string
	disconns  []string
}

func newFakeConnector(initial ...string) *fakeConnector {
	f := &fakeConnector{connected: map[string]bool{}}
	for _, n := range initial {
		f.connected[n] = true
	}
	return f
}
func (f *fakeConnector) IsConnected(name string) bool { return f.connected[name] }
func (f *fakeConnector) Connect(name, socket string) error {
	f.connected[name] = true
	f.connects = append(f.connects, name)
	return nil
}
func (f *fakeConnector) Disconnect(name string) {
	delete(f.connected, name)
	f.disconns = append(f.disconns, name)
}
func (f *fakeConnector) ConnectedSessions() []string {
	out := make([]string, 0, len(f.connected))
	for n := range f.connected {
		out = append(out, n)
	}
	return out
}

func TestReconcilePipes_ConnectsAndDisconnects(t *testing.T) {
	f := newFakeConnector("old1", "old2", "keep")
	reconcilePipes(f, []string{"keep", "new1"}, func(string) string { return "" })

	sort.Strings(f.connects)
	sort.Strings(f.disconns)
	if len(f.connects) != 1 || f.connects[0] != "new1" {
		t.Fatalf("expected to connect [new1], got %v", f.connects)
	}
	want := []string{"old1", "old2"}
	if len(f.disconns) != 2 || f.disconns[0] != want[0] || f.disconns[1] != want[1] {
		t.Fatalf("expected to disconnect %v, got %v", want, f.disconns)
	}
}

func TestReconcilePipes_IgnoresEmptyDesired(t *testing.T) {
	f := newFakeConnector()
	reconcilePipes(f, []string{"", "a"}, func(string) string { return "sock" })
	if len(f.connects) != 1 || f.connects[0] != "a" {
		t.Fatalf("empty desired entry must be skipped; connects=%v", f.connects)
	}
}
