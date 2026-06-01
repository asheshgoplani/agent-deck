package session

import "strings"

// GroupViewMode controls how the flattened session list is partitioned into a
// "top" section and a "bottom" section, separated by a divider. It is toggled
// from the TUI (cycled by a hotkey) and persisted across restarts.
type GroupViewMode int

const (
	// GroupViewNormal renders the list as-is (no partitioning).
	GroupViewNormal GroupViewMode = iota
	// GroupViewActiveTop hoists groups/subgroups that contain an active
	// (running/waiting/starting) session to the top, showing only their active
	// sessions there; the same group re-appears below the divider with its
	// remaining sessions.
	GroupViewActiveTop
	// GroupViewPopulatedTop hoists groups that contain any session to the top
	// (with all their sessions, unsplit) and sinks empty groups below the
	// divider.
	GroupViewPopulatedTop
)

// GroupViewModeCount is the number of cycle-able modes (used for "(mode+1)%N").
const GroupViewModeCount = 3

// Label returns a short human-readable name for the mode (for status hints).
func (m GroupViewMode) Label() string {
	switch m {
	case GroupViewActiveTop:
		return "Active on top"
	case GroupViewPopulatedTop:
		return "Populated on top"
	default:
		return "Normal"
	}
}

// dividerLabel returns the caption shown on the section divider for a mode.
func (m GroupViewMode) dividerLabel() string {
	switch m {
	case GroupViewActiveTop:
		return "idle / done"
	case GroupViewPopulatedTop:
		return "empty groups"
	default:
		return ""
	}
}

// GroupActivity summarizes, for a single group path, whether it contains any
// sessions and whether any of them are active — computed over the full tree
// (direct + descendant sessions), independent of expand/collapse state. This is
// what lets PartitionByViewMode place a *collapsed* group's header correctly:
// the flattened list omits a collapsed group's session rows, so header
// placement cannot be derived from rows alone.
type GroupActivity struct {
	HasAny    bool // group (or a descendant) has at least one session
	HasActive bool // group (or a descendant) has at least one active session
}

// GroupActivityMap returns per-group-path activity aggregated over all sessions
// in the tree (collapse-agnostic), propagated to ancestor paths.
func (t *GroupTree) GroupActivityMap() map[string]GroupActivity {
	m := make(map[string]GroupActivity)
	mark := func(path string, active bool) {
		if path == "" {
			return
		}
		parts := strings.Split(path, "/")
		for i := range parts {
			p := strings.Join(parts[:i+1], "/")
			a := m[p]
			a.HasAny = true
			if active {
				a.HasActive = true
			}
			m[p] = a
		}
	}
	for _, g := range t.Groups {
		for _, s := range g.Sessions {
			mark(g.Path, isActiveStatus(s.Status))
		}
	}
	return m
}

// isActiveStatus reports whether a status counts as "active" (attention-worthy)
// for GroupViewActiveTop: running, waiting, or starting.
func isActiveStatus(s Status) bool {
	switch s {
	case StatusRunning, StatusWaiting, StatusStarting:
		return true
	}
	return false
}

// markAncestorPaths marks path and every ancestor path (split on "/") in m.
func markAncestorPaths(m map[string]bool, path string) {
	if path == "" {
		return
	}
	parts := strings.Split(path, "/")
	for i := range parts {
		m[strings.Join(parts[:i+1], "/")] = true
	}
}

// hasMarkedAncestor reports whether any strict ancestor of path (its proper
// prefixes, split on "/") is marked in m. Excludes path itself.
func hasMarkedAncestor(m map[string]bool, path string) bool {
	parts := strings.Split(path, "/")
	for i := 1; i < len(parts); i++ {
		if m[strings.Join(parts[:i], "/")] {
			return true
		}
	}
	return false
}

// PartitionByViewMode re-orders an already-flattened item list into a top
// section and a bottom section separated by an ItemTypeDivider row.
//
// Session-row placement is derived from the flattened items (mirroring the
// post-flatten filtering the "!" status filter uses, so tree-connector
// rendering stays consistent). Group-header placement, however, is derived from
// `activity` — a collapse-agnostic per-group summary built from the full tree
// (see GroupTree.GroupActivityMap). This matters because a *collapsed* group
// contributes a header row but no session rows; without the tree view it would
// be misread as empty and sink to the bottom even when it holds running work.
//
// `activity` may be nil (e.g. in pure tests over fully-expanded lists): then
// header placement falls back to the visible session rows, and groups with no
// visible rows are treated as empty.
//
// If the mode is GroupViewNormal, or if either section would be empty, the
// original slice is returned unchanged (no divider).
func PartitionByViewMode(items []Item, mode GroupViewMode, activity map[string]GroupActivity) []Item {
	if mode == GroupViewNormal {
		return items
	}

	// sessionGoesTop classifies a single session item.
	sessionGoesTop := func(it Item) bool {
		if it.Session == nil {
			return true
		}
		switch mode {
		case GroupViewActiveTop:
			return isActiveStatus(it.Session.Status)
		case GroupViewPopulatedTop:
			return true // every real session is "top"; only empty groups sink
		}
		return true
	}

	// Pass 1: which group paths have a visible top/bottom *session row*.
	hasTopRow := make(map[string]bool)
	hasBottomRow := make(map[string]bool)
	for _, it := range items {
		if it.Type != ItemTypeSession || it.Session == nil {
			continue
		}
		if sessionGoesTop(it) {
			markAncestorPaths(hasTopRow, it.Path)
		} else {
			markAncestorPaths(hasBottomRow, it.Path)
		}
	}

	// Pass 2: split items into the two sections.
	top := make([]Item, 0, len(items))
	bottom := make([]Item, 0, len(items))
	for _, it := range items {
		switch it.Type {
		case ItemTypeGroup:
			inTop := hasTopRow[it.Path]
			inBottom := hasBottomRow[it.Path]
			if !inTop && !inBottom {
				// No visible session rows: the group is either collapsed (rows
				// hidden) or genuinely empty. Decide from the tree-wide activity.
				act := activity[it.Path]
				switch {
				case !act.HasAny:
					// Genuinely empty group (no sessions in its subtree). It must
					// render nested under a parent header, never orphaned with an
					// indent but nothing above it. Place it in whichever section its
					// nearest populated ancestor lives:
					//   - active-on-top: the parent re-appears in the BOTTOM with its
					//     idle remainder, so nest there (check bottom first).
					//   - populated-on-top: the parent is unsplit in the TOP ("all its
					//     contents, unsplit"), so nest there.
					//   - no populated ancestor (whole top-level subtree empty): sink
					//     to the bottom "empty groups" section.
					switch {
					case hasMarkedAncestor(hasBottomRow, it.Path):
						bottom = append(bottom, it)
					case hasMarkedAncestor(hasTopRow, it.Path):
						top = append(top, it)
					default:
						bottom = append(bottom, it)
					}
				case mode == GroupViewActiveTop && !act.HasActive:
					bottom = append(bottom, it) // collapsed, all-inactive -> sink
				default:
					top = append(top, it) // collapsed populated (active, or populated-top)
				}
				continue
			}
			if inTop {
				top = append(top, it)
			}
			if inBottom {
				bottom = append(bottom, it)
			}
		case ItemTypeSession:
			if sessionGoesTop(it) {
				top = append(top, it)
			} else {
				bottom = append(bottom, it)
			}
		default:
			// Windows/remote/etc. — keep with the top section to avoid dropping.
			top = append(top, it)
		}
	}

	// Nothing to partition: one side empty -> behave like normal view.
	if len(top) == 0 || len(bottom) == 0 {
		return items
	}

	out := make([]Item, 0, len(top)+1+len(bottom))
	out = append(out, top...)
	out = append(out, Item{Type: ItemTypeDivider, DividerLabel: mode.dividerLabel()})
	out = append(out, bottom...)
	return out
}
