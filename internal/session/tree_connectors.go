package session

// RecomputeTreeConnectors computes IsLastInGroup, IsLastSubSession and
// ParentIsLastInGroup from scratch on the final visible item list — the
// values GroupTree.Flatten set are stale by the time archived/status
// filtering and view-mode partitioning are done with the list (see the
// Item field comments in groups.go). Call it once, right before injecting
// tmux-window rows, on the list produced by PartitionByViewMode.
//
// Scope: only ItemTypeGroup and ItemTypeSession rows are read. Every other
// row type (dividers, remote rows, windows — none of which reach this call
// today) passes through with its fields untouched.
func RecomputeTreeConnectors(items []Item) []Item {
	segStart := 0
	haveLastPath := false
	var lastPath string

	flush := func(end int) {
		if end > segStart {
			recomputeSegment(items, segStart, end)
		}
	}

	for i := range items {
		switch items[i].Type {
		case ItemTypeGroup:
			// A group header always starts a fresh segment for its Path.
			flush(i)
			segStart = i + 1
			haveLastPath = false
		case ItemTypeSession:
			// No header separates consecutive rows of a default group, and a
			// subgroup header can be filtered out of the visible list — in
			// both cases a bare change in Path must also close the prior
			// segment, or its last row loses its closing state.
			if haveLastPath && items[i].Path != lastPath {
				flush(i)
				segStart = i
			}
			lastPath = items[i].Path
			haveLastPath = true
		}
	}
	flush(len(items))

	return items
}

// recomputeSegment assigns the three tree-connector flags for the
// ItemTypeSession rows in items[start:end), which all share one Path and are
// not split by a group header. Two passes are required: the first finds the
// index of the segment's last row that draws with top-level indent (needed to
// answer ParentIsLastInGroup for every child before any child's own flags can
// be assigned), the second assigns all three flags using that index.
func recomputeSegment(items []Item, start, end int) {
	// sessionIdx maps a session's ID to its row index, scoped to this segment
	// only: a parent that exists elsewhere in the flat list but not in this
	// segment must be treated as absent (D6, R2).
	sessionIdx := make(map[string]int, end-start)
	for i := start; i < end; i++ {
		it := &items[i]
		if it.Type != ItemTypeSession || it.Session == nil {
			continue
		}
		sessionIdx[it.Session.ID] = i
	}

	// topLevelForRendering reports whether a row draws with top-level indent:
	// either it structurally isn't a sub-session, or it is one but its parent
	// has no row of its own in this segment (a structural orphan, or a real
	// child separated from its parent by view-mode partitioning).
	topLevelForRendering := func(it *Item) bool {
		if !it.IsSubSession || it.Session == nil {
			return true
		}
		_, parentPresent := sessionIdx[it.Session.ParentSessionID]
		return !parentPresent
	}

	lastTopLevelIdx := -1
	for i := start; i < end; i++ {
		it := &items[i]
		if it.Type != ItemTypeSession || it.Session == nil {
			continue
		}
		if topLevelForRendering(it) {
			lastTopLevelIdx = i
		}
	}

	lastSubIdxByParent := make(map[string]int, end-start)
	for i := start; i < end; i++ {
		it := &items[i]
		if it.Type != ItemTypeSession || it.Session == nil || !it.IsSubSession {
			continue
		}
		if topLevelForRendering(it) {
			continue // orphaned chain of one row, handled via lastTopLevelIdx above
		}
		lastSubIdxByParent[it.Session.ParentSessionID] = i
	}

	for i := start; i < end; i++ {
		it := &items[i]
		if it.Type != ItemTypeSession || it.Session == nil {
			continue
		}

		if topLevelForRendering(it) {
			it.IsLastInGroup = i == lastTopLevelIdx
			it.IsLastSubSession = false
			it.ParentIsLastInGroup = false
			if it.IsSubSession {
				// Two independent defects share this branch, both gated on
				// IsSubSession in renderSessionItem — do not "simplify" this
				// into just one of the two assignments below.
				//
				// 1. Connector glyph: a structural orphan or a child cut off
				//    from its parent by view-mode partitioning still renders
				//    at top-level indent, but renderSessionItem branches on
				//    IsSubSession first and never reads IsLastInGroup for a
				//    sub-session row — its glyph comes from IsLastSubSession.
				it.IsLastSubSession = i == lastTopLevelIdx
				// 2. Gutter: the same branch reads ParentIsLastInGroup to
				//    decide the leading space vs. "│" continuation. This row's
				//    parent has no row in this segment at all, so there is
				//    nothing to continue a line from — the gutter must stay
				//    empty (D6) regardless of whether this particular row is
				//    the last one. Unconditional, not derived from the index.
				it.ParentIsLastInGroup = true
			}
			continue
		}

		parentIdx := sessionIdx[it.Session.ParentSessionID]
		it.IsLastInGroup = false
		it.ParentIsLastInGroup = parentIdx == lastTopLevelIdx
		it.IsLastSubSession = i == lastSubIdxByParent[it.Session.ParentSessionID]
	}
}
