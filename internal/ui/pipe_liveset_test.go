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

func TestPipeLiveSet_AttachedMultipleAcrossSockets(t *testing.T) {
	s := newPipeLiveSet(2)
	s.touch("focused") // in the LRU, default socket
	// Two attached sessions on different sockets, neither one focused.
	s.setAttached("attached-default", "attached-iso")
	for _, n := range []string{"attached-default", "attached-iso", "focused"} {
		if !s.want(n) {
			t.Fatalf("%s should be live", n)
		}
	}
	m := s.members()
	if len(m) != 3 {
		t.Fatalf("expected 3 members, got %v", m)
	}
	// Attached sessions must precede LRU-only members.
	idx := map[string]int{}
	for i, n := range m {
		idx[n] = i
	}
	if idx["attached-default"] > idx["focused"] || idx["attached-iso"] > idx["focused"] {
		t.Fatalf("attached sessions must precede LRU members: %v", m)
	}
}

func TestPipeLiveSet_SetAttachedReplacesPreviousSet(t *testing.T) {
	s := newPipeLiveSet(2)
	s.setAttached("a", "b")
	s.setAttached("c") // replaces the set; a,b are not in the LRU
	if s.want("a") || s.want("b") {
		t.Fatal("setAttached must replace the previous pinned set, not append to it")
	}
	if !s.want("c") {
		t.Fatal("c should be pinned after setAttached")
	}
}

func TestPipeLiveSet_NilReceiverSafe(t *testing.T) {
	var s *pipeLiveSet // an alternate/test Home constructor never initializes liveSet
	// None of these may panic on a nil receiver.
	s.touch("x")
	s.setAttached("y")
	if s.want("x") {
		t.Fatal("nil live set wants nothing")
	}
	if s.members() != nil {
		t.Fatal("nil live set has nil members")
	}
}

// TestDesiredLivePipes_AttachedIsolatedSocketKept locks in the maintainer's
// item-1 invariant: a session attached on a non-default socket but NOT focused
// must stay in the desired live set (keeping its pipe), not get evicted to the
// 2s status poll.
func TestDesiredLivePipes_AttachedIsolatedSocketKept(t *testing.T) {
	ls := newPipeLiveSet(2)
	socketByName := map[string]string{
		"focused":      "",         // default socket, focused
		"attached-iso": "iso-sock", // isolated socket, attached but not focused
		"idle":         "",         // exists but neither focused nor attached
	}
	// attached-iso is reported attached (across sockets) but is not the focus.
	desired := desiredLivePipes(ls, "focused", []string{"attached-iso"}, socketByName)

	has := func(n string) bool {
		for _, d := range desired {
			if d == n {
				return true
			}
		}
		return false
	}
	if !has("attached-iso") {
		t.Fatalf("attached isolated-socket session must be in desired set, got %v", desired)
	}
	if !has("focused") {
		t.Fatalf("focused session must be in desired set, got %v", desired)
	}
	if has("idle") {
		t.Fatalf("idle (unfocused, unattached) session must not be live, got %v", desired)
	}
}

func TestDesiredLivePipes_DropsNamesWithNoInstance(t *testing.T) {
	ls := newPipeLiveSet(3)
	ls.touch("deleted") // was focused earlier; its instance is now gone
	socketByName := map[string]string{"live": ""}
	desired := desiredLivePipes(ls, "live", nil, socketByName)
	for _, d := range desired {
		if d == "deleted" {
			t.Fatalf("a session with no live instance must be filtered out, got %v", desired)
		}
	}
}

func TestOwnedSocketFilter(t *testing.T) {
	socketByName := map[string]string{"s-a": "", "s-b": "", "s-c": "iso"}
	owned := map[string]bool{"i-a": true}
	nameToID := map[string]string{"s-a": "i-a", "s-b": "i-b", "s-c": "i-c"}
	got := filterPipeCandidates(socketByName, nameToID, owned, "s-b", true)
	// owned s-a stays; focused s-b stays despite not owned; s-c dropped.
	if _, ok := got["s-a"]; !ok {
		t.Error("owned session dropped")
	}
	if _, ok := got["s-b"]; !ok {
		t.Error("focused session must keep its pipe")
	}
	if _, ok := got["s-c"]; ok {
		t.Error("non-owned non-focused session must be dropped")
	}
}

func TestOwnedSocketFilterFlagOff(t *testing.T) {
	socketByName := map[string]string{"s-a": "", "s-b": ""}
	got := filterPipeCandidates(socketByName, map[string]string{}, nil, "", false)
	if len(got) != 2 {
		t.Error("flag off must keep all candidates")
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

// TestReconcile_AttachedIsolatedSocketKeptAcrossTicks is the end-to-end version
// of the maintainer's item-3 ask: an attached isolated-socket session that is
// not focused must get a live pipe and KEEP it across reconcile ticks, never
// being disconnected.
func TestReconcile_AttachedIsolatedSocketKeptAcrossTicks(t *testing.T) {
	ls := newPipeLiveSet(2)
	socketByName := map[string]string{
		"focused":      "",
		"attached-iso": "iso-sock",
	}
	socketOf := func(name string) string { return socketByName[name] }

	f := newFakeConnector()

	// Tick 1: focus "focused"; "attached-iso" is attached on its isolated socket.
	desired := desiredLivePipes(ls, "focused", []string{"attached-iso"}, socketByName)
	reconcilePipes(f, desired, socketOf)
	if !f.IsConnected("attached-iso") {
		t.Fatalf("attached isolated-socket session must be connected on tick 1; connects=%v", f.connects)
	}

	// Tick 2: same focus + attach. The isolated-socket pipe must be retained.
	f.connects, f.disconns = nil, nil
	desired = desiredLivePipes(ls, "focused", []string{"attached-iso"}, socketByName)
	reconcilePipes(f, desired, socketOf)
	if len(f.disconns) != 0 {
		t.Fatalf("steady tick must not disconnect anything, got %v", f.disconns)
	}
	if !f.IsConnected("attached-iso") {
		t.Fatal("attached isolated-socket session must remain connected across ticks")
	}
}
