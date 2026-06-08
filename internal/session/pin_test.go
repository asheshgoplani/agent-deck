package session

import (
	"os"
	"testing"
	"time"
)

// TestSortInstancesByActionable_PinZones verifies the zoned sort: pin-top
// sessions first, then normal (status/recency), then pin-bottom, then archived
// — regardless of each session's status. The pin bands are "fully fixed".
func TestSortInstancesByActionable_PinZones(t *testing.T) {
	now := time.Now()
	// A pin-bottom session in error state would, under the old status sort,
	// jump to the very top. It must instead sink to the pin-bottom band.
	pinBottom := &Instance{ID: "pb", Status: StatusError, Pin: PinBottom, LastAccessedAt: now}
	// A pin-top session that is merely idle/stopped must still lead the list.
	pinTop := &Instance{ID: "pt", Status: StatusStopped, Pin: PinTop, LastAccessedAt: now.Add(-time.Hour)}
	normalErr := &Instance{ID: "ne", Status: StatusError, LastAccessedAt: now}
	normalIdle := &Instance{ID: "ni", Status: StatusIdle, LastAccessedAt: now}
	archived := &Instance{ID: "ar", Status: StatusWaiting, ArchivedAt: now}

	insts := []*Instance{normalIdle, archived, pinBottom, normalErr, pinTop}
	SortInstancesByActionable(insts)

	want := []string{"pt", "ne", "ni", "pb", "ar"}
	got := ids(insts)
	if !equalStrings(got, want) {
		t.Fatalf("pin zone order wrong:\n got  %v\n want %v", got, want)
	}
}

// TestSortInstancesByActionable_PinnedErrorStaysFixed proves a pinned session
// in an error state does not get surfaced by the actionable sort — it stays in
// its pin band ("fully fixed", requirement 3).
func TestSortInstancesByActionable_PinnedErrorStaysFixed(t *testing.T) {
	now := time.Now()
	normal := &Instance{ID: "n", Status: StatusIdle, LastAccessedAt: now}
	pinnedErr := &Instance{ID: "p", Status: StatusError, Pin: PinBottom, LastAccessedAt: now}

	insts := []*Instance{pinnedErr, normal}
	SortInstancesByActionable(insts)

	if insts[0].ID != "n" || insts[1].ID != "p" {
		t.Fatalf("pinned error session must stay in pin-bottom band; got %v", ids(insts))
	}
}

// TestSortInstancesByActionable_PinnedArchivedSinks proves archive is the
// stronger signal: a pinned-but-archived session loses its anchored slot and
// sinks to the archived zone (design point 4).
func TestSortInstancesByActionable_PinnedArchivedSinks(t *testing.T) {
	now := time.Now()
	pinTopArchived := &Instance{ID: "pa", Pin: PinTop, ArchivedAt: now, LastAccessedAt: now}
	normal := &Instance{ID: "n", Status: StatusIdle, LastAccessedAt: now}

	insts := []*Instance{pinTopArchived, normal}
	SortInstancesByActionable(insts)

	if insts[len(insts)-1].ID != "pa" {
		t.Fatalf("pinned+archived must sink to the archived zone (last); got %v", ids(insts))
	}
}

// TestSortInstancesByActionable_MultiplePinnedOrderedByOrder verifies that
// within a pin band, K/J reordering (Order) still controls relative position —
// status and recency are ignored inside the band.
func TestSortInstancesByActionable_MultiplePinnedOrderedByOrder(t *testing.T) {
	now := time.Now()
	// Higher Order should sort after lower Order, independent of status/recency.
	a := &Instance{ID: "a", Status: StatusError, Pin: PinTop, Order: 2, LastAccessedAt: now}
	b := &Instance{ID: "b", Status: StatusIdle, Pin: PinTop, Order: 0, LastAccessedAt: now.Add(-time.Hour)}
	c := &Instance{ID: "c", Status: StatusWaiting, Pin: PinTop, Order: 1, LastAccessedAt: now}

	insts := []*Instance{a, b, c}
	SortInstancesByActionable(insts)

	want := []string{"b", "c", "a"}
	if got := ids(insts); !equalStrings(got, want) {
		t.Fatalf("pin-top band must order by Order; got %v want %v", got, want)
	}
}

// TestSetField_Pin verifies the pin mutator accepts top/bottom/empty and
// rejects anything else.
func TestSetField_Pin(t *testing.T) {
	inst := &Instance{ID: "1", Tool: "shell"}

	for _, val := range []string{"top", "bottom", ""} {
		if _, _, err := SetField(inst, FieldPin, val, nil); err != nil {
			t.Fatalf("SetField pin=%q: unexpected error %v", val, err)
		}
		if string(inst.Pin) != val {
			t.Fatalf("SetField pin=%q: inst.Pin = %q", val, inst.Pin)
		}
	}

	if _, _, err := SetField(inst, FieldPin, "sideways", nil); err == nil {
		t.Fatal("SetField pin=sideways: expected validation error, got nil")
	}
	// A rejected value must not mutate the field.
	if inst.Pin != PinNone {
		t.Fatalf("rejected pin value mutated the field: %q", inst.Pin)
	}

	// pin is a live edit (no restart).
	if RestartPolicyFor(FieldPin) != FieldLive {
		t.Fatal("FieldPin should be a live edit (no restart required)")
	}
}

// TestPin_SurvivesSaveLoad confirms the pin column round-trips through
// SaveWithGroups/LoadWithGroups, and that an unpinned session defaults to
// PinNone after reload (the empty-string column default).
func TestPin_SurvivesSaveLoad(t *testing.T) {
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", t.TempDir())
	ClearUserConfigCache()
	t.Cleanup(func() {
		os.Setenv("HOME", origHome)
		ClearUserConfigCache()
	})

	storage, err := NewStorageWithProfile("_pin_roundtrip")
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	t.Cleanup(func() { storage.Close() })

	pinned := NewInstanceWithTool("launcher", "/tmp/p", "shell")
	pinned.Pin = PinTop
	plain := NewInstanceWithTool("worker", "/tmp/w", "shell")

	insts := []*Instance{pinned, plain}
	tree := NewGroupTree(insts)
	if err := storage.SaveWithGroups(insts, tree); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}

	loaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("LoadWithGroups: %v", err)
	}
	byID := map[string]*Instance{}
	for _, in := range loaded {
		byID[in.ID] = in
	}
	if got := byID[pinned.ID]; got == nil || got.Pin != PinTop {
		t.Fatalf("pinned session did not round-trip Pin=top; got %+v", got)
	}
	if got := byID[plain.ID]; got == nil || got.Pin != PinNone {
		t.Fatalf("unpinned session must default to PinNone; got pin=%q", got.Pin)
	}
}

// TestFlatten_LivePinMovesToTop is the regression test for the reported bug:
// pinning a session live (without rebuilding the group tree, as a dialog/CLI
// edit does) must move it to the top of its group immediately, not only after
// a restart. Flatten reads group.Sessions in slice order, so it must apply the
// pin partition itself.
func TestFlatten_LivePinMovesToTop(t *testing.T) {
	a := &Instance{ID: "a", Title: "a", GroupPath: "g", Status: StatusIdle}
	b := &Instance{ID: "b", Title: "b", GroupPath: "g", Status: StatusIdle}
	c := &Instance{ID: "c", Title: "c", GroupPath: "g", Status: StatusIdle}
	tree := NewGroupTree([]*Instance{a, b, c})

	// Simulate a live pin edit: mutate Pin without rebuilding the tree.
	c.Pin = PinTop

	got := sessionIDsInItems(tree.Flatten())
	if len(got) == 0 || got[0] != "c" {
		t.Fatalf("pinned session must lead its group live; got %v", got)
	}
}

// TestFlatten_LivePinBottomMovesToBottom is the mirror: a live pin-bottom edit
// must sink the session to the end of its group's session list.
func TestFlatten_LivePinBottomMovesToBottom(t *testing.T) {
	a := &Instance{ID: "a", Title: "a", GroupPath: "g", Status: StatusIdle}
	b := &Instance{ID: "b", Title: "b", GroupPath: "g", Status: StatusIdle}
	c := &Instance{ID: "c", Title: "c", GroupPath: "g", Status: StatusIdle}
	tree := NewGroupTree([]*Instance{a, b, c})

	a.Pin = PinBottom

	got := sessionIDsInItems(tree.Flatten())
	if len(got) == 0 || got[len(got)-1] != "a" {
		t.Fatalf("pin-bottom session must sink to the end of its group live; got %v", got)
	}
}

// TestPartitionByViewMode_PinOverridesActiveSplit verifies pin composes with
// the active-on-top view mode: a pin-top session that is merely idle still
// lands in the top section (fully fixed, requirement 3), and a pin-bottom
// active session is forced into the bottom section.
func TestPartitionByViewMode_PinOverridesActiveSplit(t *testing.T) {
	items := []Item{
		{Type: ItemTypeGroup, Path: "g"},
		{Type: ItemTypeSession, Path: "g", Session: &Instance{ID: "pinIdle", Status: StatusIdle, Pin: PinTop}},
		{Type: ItemTypeSession, Path: "g", Session: &Instance{ID: "idle", Status: StatusIdle}},
		{Type: ItemTypeSession, Path: "g", Session: &Instance{ID: "pinActive", Status: StatusRunning, Pin: PinBottom}},
	}
	activity := map[string]GroupActivity{"g": {HasAny: true}}

	out := PartitionByViewMode(items, GroupViewActiveTop, activity)

	dividerIdx := -1
	pos := map[string]int{}
	for i, it := range out {
		switch {
		case it.Type == ItemTypeDivider:
			dividerIdx = i
		case it.Type == ItemTypeSession && it.Session != nil:
			pos[it.Session.ID] = i
		}
	}
	if dividerIdx == -1 {
		t.Fatal("expected a divider between top and bottom sections")
	}
	if pos["pinIdle"] > dividerIdx {
		t.Errorf("pin-top idle session must be in the TOP section; divider=%d pinIdle=%d", dividerIdx, pos["pinIdle"])
	}
	if pos["pinActive"] < dividerIdx {
		t.Errorf("pin-bottom session must be in the BOTTOM section; divider=%d pinActive=%d", dividerIdx, pos["pinActive"])
	}
	if pos["idle"] < dividerIdx {
		t.Errorf("plain idle session must be in the BOTTOM section; divider=%d idle=%d", dividerIdx, pos["idle"])
	}
}

func sessionIDsInItems(items []Item) []string {
	var out []string
	for _, it := range items {
		if it.Type == ItemTypeSession && it.Session != nil {
			out = append(out, it.Session.ID)
		}
	}
	return out
}

func ids(insts []*Instance) []string {
	out := make([]string, len(insts))
	for i, in := range insts {
		out[i] = in.ID
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
