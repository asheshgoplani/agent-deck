package session_test

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// itemByID returns the item for the session with the given ID, or fails.
func itemByID(t *testing.T, items []session.Item, id string) session.Item {
	t.Helper()
	for _, it := range items {
		if it.Session != nil && it.Session.ID == id {
			return it
		}
	}
	t.Fatalf("no item for session id %q", id)
	return session.Item{}
}

func newSession(t *testing.T, title string) *session.Instance {
	t.Helper()
	return session.NewInstanceWithTool(title, "/tmp/"+title, "claude")
}

// TestLastTopLevelWithChildren_RendersLast: the last top-level session in a
// group must render └─ even when it has children (defect 1).
func TestLastTopLevelWithChildren_RendersLast(t *testing.T) {
	p1 := newSession(t, "p1")
	p2 := newSession(t, "p2")
	s1 := newSession(t, "s1")
	s1.SetParent(p2.ID)

	items := []session.Item{
		{Type: session.ItemTypeGroup, Path: "alpha"},
		{Type: session.ItemTypeSession, Session: p1, Path: "alpha"},
		{Type: session.ItemTypeSession, Session: p2, Path: "alpha"},
		{Type: session.ItemTypeSession, Session: s1, Path: "alpha", IsSubSession: true},
	}

	out := session.RecomputeTreeConnectors(items)

	if !itemByID(t, out, p2.ID).IsLastInGroup {
		t.Errorf("p2 is the last top-level session in its group but has a child, so IsLastInGroup=false")
	}
	if itemByID(t, out, p1.ID).IsLastInGroup {
		t.Errorf("p1 is not last but IsLastInGroup=true")
	}
}

// TestSoleTopLevelInSubgroup_RendersLast: the same defect-1 shape inside a
// nested subgroup path, not just a root group.
func TestSoleTopLevelInSubgroup_RendersLast(t *testing.T) {
	p := newSession(t, "p")
	s1 := newSession(t, "s1")
	s1.SetParent(p.ID)

	items := []session.Item{
		{Type: session.ItemTypeGroup, Path: "trovy/anarlog"},
		{Type: session.ItemTypeSession, Session: p, Path: "trovy/anarlog"},
		{Type: session.ItemTypeSession, Session: s1, Path: "trovy/anarlog", IsSubSession: true},
	}

	out := session.RecomputeTreeConnectors(items)

	if !itemByID(t, out, p.ID).IsLastInGroup {
		t.Errorf("p is the sole top-level session in its subgroup but has a child, so IsLastInGroup=false")
	}
}

// TestSoleVisibleSubSession_ArchivedSiblings_RendersLast: the only LIVE child
// of a parent must render └─ (defect 2: IsLastSubSession). Archived siblings
// are already filtered out of the list before RecomputeTreeConnectors ever
// sees it, so the pure-function shape is simply "one visible child".
func TestSoleVisibleSubSession_ArchivedSiblings_RendersLast(t *testing.T) {
	p := newSession(t, "p")
	s1 := newSession(t, "s1")
	s1.SetParent(p.ID)

	items := []session.Item{
		{Type: session.ItemTypeGroup, Path: "alpha"},
		{Type: session.ItemTypeSession, Session: p, Path: "alpha"},
		{Type: session.ItemTypeSession, Session: s1, Path: "alpha", IsSubSession: true},
	}

	out := session.RecomputeTreeConnectors(items)

	if !itemByID(t, out, s1.ID).IsLastSubSession {
		t.Errorf("s1 is the only visible child but IsLastSubSession=false")
	}
}

// TestParentIsLast_ArchivedTopLevelTrailing_NoVerticalBar: a child's │ prefix
// must disappear when its parent is the last VISIBLE top-level session
// (defect 2: ParentIsLastInGroup). A trailing archived sibling of the parent
// is already filtered out upstream, so the pure-function shape is simply "the
// parent is the last top-level row in its segment".
func TestParentIsLast_ArchivedTopLevelTrailing_NoVerticalBar(t *testing.T) {
	p1 := newSession(t, "p1")
	s1 := newSession(t, "s1")
	s1.SetParent(p1.ID)

	items := []session.Item{
		{Type: session.ItemTypeGroup, Path: "alpha"},
		{Type: session.ItemTypeSession, Session: p1, Path: "alpha"},
		{Type: session.ItemTypeSession, Session: s1, Path: "alpha", IsSubSession: true},
	}

	out := session.RecomputeTreeConnectors(items)

	if !itemByID(t, out, s1.ID).ParentIsLastInGroup {
		t.Errorf("s1's parent p1 is the last visible top-level session, so ParentIsLastInGroup should be true (no │), but is false")
	}
}

// TestParentNotLast_ChildKeepsVerticalBar: inverse regression guard — when a
// LIVE session follows the parent, the │ prefix must stay.
func TestParentNotLast_ChildKeepsVerticalBar(t *testing.T) {
	p1 := newSession(t, "p1")
	s1 := newSession(t, "s1")
	s1.SetParent(p1.ID)
	p2 := newSession(t, "p2") // trailing, LIVE top-level

	items := []session.Item{
		{Type: session.ItemTypeGroup, Path: "alpha"},
		{Type: session.ItemTypeSession, Session: p1, Path: "alpha"},
		{Type: session.ItemTypeSession, Session: s1, Path: "alpha", IsSubSession: true},
		{Type: session.ItemTypeSession, Session: p2, Path: "alpha"},
	}

	out := session.RecomputeTreeConnectors(items)

	if itemByID(t, out, s1.ID).ParentIsLastInGroup {
		t.Errorf("s1's parent p1 is NOT the last top-level session (p2 follows it live), so ParentIsLastInGroup must stay false but is true")
	}
}

// TestMultipleVisibleSubSessions_OnlyLastRendersLast: regression guard — with
// 2+ live children, only the actual last one gets └─.
func TestMultipleVisibleSubSessions_OnlyLastRendersLast(t *testing.T) {
	p := newSession(t, "p")
	s1 := newSession(t, "s1")
	s2 := newSession(t, "s2")
	s3 := newSession(t, "s3")
	for _, s := range []*session.Instance{s1, s2, s3} {
		s.SetParent(p.ID)
	}

	items := []session.Item{
		{Type: session.ItemTypeGroup, Path: "alpha"},
		{Type: session.ItemTypeSession, Session: p, Path: "alpha"},
		{Type: session.ItemTypeSession, Session: s1, Path: "alpha", IsSubSession: true},
		{Type: session.ItemTypeSession, Session: s2, Path: "alpha", IsSubSession: true},
		{Type: session.ItemTypeSession, Session: s3, Path: "alpha", IsSubSession: true},
	}

	out := session.RecomputeTreeConnectors(items)

	if !itemByID(t, out, s3.ID).IsLastSubSession {
		t.Errorf("s3 is the last of 3 visible children but IsLastSubSession=false")
	}
	for _, id := range []string{s1.ID, s2.ID} {
		if itemByID(t, out, id).IsLastSubSession {
			t.Errorf("%s is not the last child but IsLastSubSession=true", id)
		}
	}
}

// TestViewModeSplit_SubSessionsAcrossSections_EachSectionEndsWithLast: a
// parent with children split across the ActiveTop divider by status — each
// section's own last child must get └─. The parent (idle) lands in the bottom
// section with s2; s1 and s3 (running) land in the top section without their
// parent, so within the top segment they are the "parent absent" case.
func TestViewModeSplit_SubSessionsAcrossSections_EachSectionEndsWithLast(t *testing.T) {
	p := newSession(t, "p")
	s1 := newSession(t, "s1")
	s2 := newSession(t, "s2")
	s3 := newSession(t, "s3")
	for _, s := range []*session.Instance{s1, s2, s3} {
		s.SetParent(p.ID)
	}

	items := []session.Item{
		// Top section: s1, s3 (running), no p — header duplicated per real
		// PartitionByViewMode output.
		{Type: session.ItemTypeGroup, Path: "alpha"},
		{Type: session.ItemTypeSession, Session: s1, Path: "alpha", IsSubSession: true},
		{Type: session.ItemTypeSession, Session: s3, Path: "alpha", IsSubSession: true},
		{Type: session.ItemTypeDivider},
		// Bottom section: p (idle) with s2 (idle).
		{Type: session.ItemTypeGroup, Path: "alpha"},
		{Type: session.ItemTypeSession, Session: p, Path: "alpha"},
		{Type: session.ItemTypeSession, Session: s2, Path: "alpha", IsSubSession: true},
	}

	out := session.RecomputeTreeConnectors(items)

	if !itemByID(t, out, s3.ID).IsLastSubSession {
		t.Errorf("s3 is the last child in the top view-mode section but IsLastSubSession=false")
	}
	if itemByID(t, out, s1.ID).IsLastSubSession {
		t.Errorf("s1 is not last in the top section but IsLastSubSession=true")
	}
	if !itemByID(t, out, s2.ID).IsLastSubSession {
		t.Errorf("s2 is the only (and last) child in the bottom view-mode section but IsLastSubSession=false")
	}
}

// TestStatusFilter_MiddleChildHidden_LastVisibleRendersLast: a status filter
// hiding a parent's raw-last child (leaving a gap in the middle of the
// overall flat list, not at its tail) must not strand the new last-visible
// child with a stale IsLastSubSession=false.
func TestStatusFilter_MiddleChildHidden_LastVisibleRendersLast(t *testing.T) {
	p := newSession(t, "p")
	s1 := newSession(t, "s1")
	s2 := newSession(t, "s2")
	// s3 (raw-last child) is already filtered out by the status filter
	// upstream — it never reaches RecomputeTreeConnectors.
	for _, s := range []*session.Instance{s1, s2} {
		s.SetParent(p.ID)
	}
	z := newSession(t, "z") // a later group's session, so the gap is in the middle

	items := []session.Item{
		{Type: session.ItemTypeGroup, Path: "alpha"},
		{Type: session.ItemTypeSession, Session: p, Path: "alpha"},
		{Type: session.ItemTypeSession, Session: s1, Path: "alpha", IsSubSession: true},
		{Type: session.ItemTypeSession, Session: s2, Path: "alpha", IsSubSession: true},
		{Type: session.ItemTypeGroup, Path: "beta"},
		{Type: session.ItemTypeSession, Session: z, Path: "beta"},
	}

	out := session.RecomputeTreeConnectors(items)

	if !itemByID(t, out, s2.ID).IsLastSubSession {
		t.Errorf("s2 is the last VISIBLE child of p after the status filter hides s3, so IsLastSubSession should be true, but is false")
	}
}

// TestOrphanSubSession_CountsAsTopLevelSibling: a structural orphan (parent in
// a different group) counts as a top-level sibling when deciding whether a
// preceding real top-level row is last.
func TestOrphanSubSession_CountsAsTopLevelSibling(t *testing.T) {
	p1 := newSession(t, "p1") // real top-level, not last: the orphan follows it
	otherParent := newSession(t, "other-parent")
	orphan := newSession(t, "orphan")
	orphan.SetParent(otherParent.ID) // otherParent is not in this segment at all

	items := []session.Item{
		{Type: session.ItemTypeGroup, Path: "alpha"},
		{Type: session.ItemTypeSession, Session: p1, Path: "alpha"},
		{Type: session.ItemTypeSession, Session: orphan, Path: "alpha", IsSubSession: true},
	}

	out := session.RecomputeTreeConnectors(items)

	if itemByID(t, out, p1.ID).IsLastInGroup {
		t.Errorf("p1 is not last (the orphan follows it as a top-level sibling) but IsLastInGroup=true")
	}
}

// TestChildSection_NoParentInSection_EmptyGutter: a real child whose parent is
// in a different view-mode section gets an empty gutter (no │), D6.
func TestChildSection_NoParentInSection_EmptyGutter(t *testing.T) {
	p := newSession(t, "p")
	s1 := newSession(t, "s1")
	s1.SetParent(p.ID)

	items := []session.Item{
		// Top section: s1 alone, parent p is not here.
		{Type: session.ItemTypeGroup, Path: "alpha"},
		{Type: session.ItemTypeSession, Session: s1, Path: "alpha", IsSubSession: true},
		{Type: session.ItemTypeDivider},
		// Bottom section: p alone.
		{Type: session.ItemTypeGroup, Path: "alpha"},
		{Type: session.ItemTypeSession, Session: p, Path: "alpha"},
	}

	out := session.RecomputeTreeConnectors(items)

	if !itemByID(t, out, s1.ID).ParentIsLastInGroup {
		t.Errorf("s1's parent is in a different section (not present here), so ParentIsLastInGroup should be true (empty gutter), but is false")
	}
}

// TestTwoChildren_AcrossTwoSections_EachLastInOwnSection: two children of one
// parent land in two different sections (parent in neither) — each is last in
// its own section.
func TestTwoChildren_AcrossTwoSections_EachLastInOwnSection(t *testing.T) {
	p := newSession(t, "p")
	sa := newSession(t, "sa")
	sb := newSession(t, "sb")
	sa.SetParent(p.ID)
	sb.SetParent(p.ID)

	items := []session.Item{
		{Type: session.ItemTypeGroup, Path: "alpha"},
		{Type: session.ItemTypeSession, Session: sa, Path: "alpha", IsSubSession: true},
		{Type: session.ItemTypeDivider},
		{Type: session.ItemTypeGroup, Path: "alpha"},
		{Type: session.ItemTypeSession, Session: sb, Path: "alpha", IsSubSession: true},
	}

	out := session.RecomputeTreeConnectors(items)

	if !itemByID(t, out, sa.ID).IsLastSubSession {
		t.Errorf("sa is the only (and last) row of its own section but IsLastSubSession=false")
	}
	if !itemByID(t, out, sb.ID).IsLastSubSession {
		t.Errorf("sb is the only (and last) row of its own section but IsLastSubSession=false")
	}
}

// TestSegmentBoundary_PathChangeWithoutHeader_ClosesPriorSegment: the default
// group emits no header row at all, so a bare change in Path (not a header)
// must still close the prior segment (R3).
func TestSegmentBoundary_PathChangeWithoutHeader_ClosesPriorSegment(t *testing.T) {
	p1 := newSession(t, "p1")
	p2 := newSession(t, "p2") // last row of the default group's segment
	q1 := newSession(t, "q1") // different Path, no header row between them

	items := []session.Item{
		{Type: session.ItemTypeSession, Session: p1, Path: "my-sessions"},
		{Type: session.ItemTypeSession, Session: p2, Path: "my-sessions"},
		{Type: session.ItemTypeSession, Session: q1, Path: "beta"},
	}

	out := session.RecomputeTreeConnectors(items)

	if !itemByID(t, out, p2.ID).IsLastInGroup {
		t.Errorf("p2 closes the header-less default-group segment but IsLastInGroup=false")
	}
	if !itemByID(t, out, q1.ID).IsLastInGroup {
		t.Errorf("q1 opens its own segment on the bare Path change but IsLastInGroup=false")
	}
}

// TestOrphanSubSession_LastInSegment_RendersLast: a structural orphan (parent
// in a different group) closing a segment gets IsLastSubSession=true — not
// IsLastInGroup, which renderSessionItem never reads for a sub-session row
// (ledger D17) — plus an empty gutter (ParentIsLastInGroup=true), since its
// parent has no row in this segment to continue a line from.
func TestOrphanSubSession_LastInSegment_RendersLast(t *testing.T) {
	otherParent := newSession(t, "other-parent")
	orphan := newSession(t, "orphan")
	orphan.SetParent(otherParent.ID)

	items := []session.Item{
		{Type: session.ItemTypeGroup, Path: "alpha"},
		{Type: session.ItemTypeSession, Session: orphan, Path: "alpha", IsSubSession: true},
	}

	out := session.RecomputeTreeConnectors(items)

	got := itemByID(t, out, orphan.ID)
	if !got.IsLastSubSession {
		t.Errorf("orphan is the last row of its segment but IsLastSubSession=false")
	}
	if !got.ParentIsLastInGroup {
		t.Errorf("orphan's parent has no row in this segment, so ParentIsLastInGroup should be true (empty gutter), but is false")
	}
}

// TestOrphanSubSession_NotLastInSegment_EmptyGutterButNotLast: a structural
// orphan that is NOT the last row of its segment gets IsLastSubSession=false,
// but the gutter stays empty regardless — proving the empty gutter is its own
// fix (D17), not a side effect of also being last.
func TestOrphanSubSession_NotLastInSegment_EmptyGutterButNotLast(t *testing.T) {
	otherParent := newSession(t, "other-parent")
	orphan := newSession(t, "orphan")
	orphan.SetParent(otherParent.ID)
	trailing := newSession(t, "trailing") // real top-level row after the orphan

	items := []session.Item{
		{Type: session.ItemTypeGroup, Path: "alpha"},
		{Type: session.ItemTypeSession, Session: orphan, Path: "alpha", IsSubSession: true},
		{Type: session.ItemTypeSession, Session: trailing, Path: "alpha"},
	}

	out := session.RecomputeTreeConnectors(items)

	got := itemByID(t, out, orphan.ID)
	if got.IsLastSubSession {
		t.Errorf("orphan is not the last row of its segment (trailing follows it) but IsLastSubSession=true")
	}
	if !got.ParentIsLastInGroup {
		t.Errorf("orphan's parent still has no row in this segment, so ParentIsLastInGroup should stay true even though the orphan isn't last")
	}
}

// TestRemoteItemTypes_UnaffectedByRecompute: ItemTypeRemoteGroup/RemoteSession
// rows pass through unchanged — they never reach this function's real call
// site in home.go (verified: remote rows append after window injection), but
// the function documents and enforces that contract regardless.
func TestRemoteItemTypes_UnaffectedByRecompute(t *testing.T) {
	items := []session.Item{
		{Type: session.ItemTypeRemoteGroup, Path: "remote-host", IsLastInGroup: true},
		{Type: session.ItemTypeRemoteSession, Path: "remote-host", IsLastInGroup: false},
	}

	out := session.RecomputeTreeConnectors(items)

	if !out[0].IsLastInGroup {
		t.Errorf("ItemTypeRemoteGroup row's IsLastInGroup was changed from true")
	}
	if out[1].IsLastInGroup {
		t.Errorf("ItemTypeRemoteSession row's IsLastInGroup was changed from false")
	}
}
