package ui

import (
	"sort"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// preserveState captures current UI state before reload
func (h *Home) preserveState() reloadState {
	state := reloadState{
		expandedGroups: make(map[string]bool),
		viewOffset:     h.viewOffset,
	}

	// Capture cursor position (session ID or group path)
	if h.cursor < len(h.flatItems) {
		item := h.flatItems[h.cursor]
		switch item.Type {
		case session.ItemTypeSession:
			if item.Session != nil {
				state.cursorSessionID = item.Session.ID
			} else if item.CreatingID != "" {
				state.cursorCreatingID = item.CreatingID
			}
		case session.ItemTypeGroup:
			state.cursorGroupPath = item.Path
		}
	}

	// Capture expanded groups
	if h.groupTree != nil {
		for _, group := range h.groupTree.GroupList {
			if group.Expanded {
				state.expandedGroups[group.Path] = true
			}
		}
	}

	return state
}

// restoreState applies preserved UI state after reload
func (h *Home) restoreState(state reloadState) {
	// Restore expanded groups (only for groups present in the map;
	// new groups keep their default expanded state from storage)
	if h.groupTree != nil {
		for _, group := range h.groupTree.GroupList {
			if expanded, exists := state.expandedGroups[group.Path]; exists {
				group.Expanded = expanded
			}
		}
	}

	// Rebuild flat items with restored group states
	h.rebuildFlatItems()

	// Restore cursor position
	found := false

	// First, try to restore cursor to session if we had one selected
	if state.cursorSessionID != "" {
		for i, item := range h.flatItems {
			if item.Type == session.ItemTypeSession &&
				item.Session != nil &&
				item.Session.ID == state.cursorSessionID {
				h.cursor = i
				found = true
				break
			}
		}
	}

	// Creating-session placeholder: match by tempID so selection survives
	// reloads/rebuilds during the (long) worktree setup window.
	if !found && state.cursorCreatingID != "" {
		for i, item := range h.flatItems {
			if item.CreatingID == state.cursorCreatingID {
				h.cursor = i
				found = true
				break
			}
		}
	}

	// If session not found, try to restore cursor to group if we had one selected
	if !found && state.cursorGroupPath != "" {
		for i, item := range h.flatItems {
			if item.Type == session.ItemTypeGroup && item.Path == state.cursorGroupPath {
				h.cursor = i
				found = true
				break
			}
		}
	}

	// Fallback: clamp cursor to valid range if target not found or cursor out of bounds
	if !found || h.cursor >= len(h.flatItems) {
		if len(h.flatItems) > 0 {
			h.cursor = min(h.cursor, len(h.flatItems)-1)
			h.cursor = max(h.cursor, 0)
		} else {
			h.cursor = 0
		}
	}

	// The clamp above can land the cursor on a non-selectable divider row
	// (dividers carry a nil Session). Nudge off it so the initial preview shows
	// a real session rather than the empty state, mirroring j/k navigation.
	h.skipDivider(1)

	// Restore scroll position (clamped to valid range)
	if len(h.flatItems) > 0 {
		h.viewOffset = min(state.viewOffset, len(h.flatItems)-1)
		h.viewOffset = max(h.viewOffset, 0)
	} else {
		h.viewOffset = 0
	}
}

// sessionHasWindows returns true if the session item has 2+ cached tmux windows.
func (h *Home) sessionHasWindows(item session.Item) bool {
	if item.Session == nil {
		return false
	}
	tmuxSess := item.Session.GetTmuxSession()
	if tmuxSess == nil {
		return false
	}
	return len(tmux.GetCachedWindows(tmuxSess.TmuxName())) >= 2
}

// sessionHasChildren reports whether the row's session is a parent with
// sub-sessions nested under it in its group (#2631). Orphan sub-sessions
// whose parent lives in another group render top-level and never fold.
// In flat sidebar mode the parent-child fold is off entirely: the flat
// list emits every session by design, so a fold key or badge there would
// flip state nothing reads (CodeRabbit, #2636).
func (h *Home) sessionHasChildren(item session.Item) bool {
	if h.embeddedLayout && h.sidebarMode == sidebarFlat {
		return false
	}
	return h.sessionChildCount(item) > 0
}

// sessionChildCount counts the sub-sessions nested under a parent row that
// belong to the archive partition currently on screen. The deck renders one
// partition at a time (#1987: active or ^ archived), so a child in the other
// partition never appears under the parent and must not produce a fold badge
// or capture the fold keys (Tab, l/right, h/left) from the window toggle.
//
// A sub-session row never folds: the tree nests only one level, so a
// sub-session's own children render as orphan rows and FlattenWithParentFolds
// ignores a fold on it. Counting them put a badge on the row and took the
// fold keys from the window toggle for a fold that hides nothing.
func (h *Home) sessionChildCount(item session.Item) int {
	if item.Session == nil || h.groupTree == nil || item.Session.IsSubSession() {
		return 0
	}
	viewArchived := h.statusFilter == FilterModeArchived
	n := 0
	for _, group := range h.groupTree.GroupList {
		if group == nil || group.Path != item.Path {
			continue
		}
		for _, s := range group.Sessions {
			if s != nil && s.IsSubSession() && s.ParentSessionID == item.Session.ID && s.IsArchived() == viewArchived {
				n++
			}
		}
		break
	}
	return n
}

// partitionParentFolds returns the parent folds (#2631) that apply to the
// archive partition on screen. Flatten runs before the active/archived
// partition, so a fold must only hide children when its parent row is drawn
// in this view: a folded parent that was later archived would otherwise hide
// its still-active children from the active list, and a folded active parent
// would hide its archived children from the ^ view. In both cases the parent
// row (and its fold badge) is absent, so nothing on screen could unfold them.
//
// The same holds for the status and time filters, which also run after
// Flatten: a folded parent that the active filter hides must not take its
// matching children with it, or filtering by running/waiting would drop a
// child under a folded idle parent with no row left to unfold. filtered
// reports whether a fold was skipped only because of a filter, so the caller
// can rebuild if that filter is later cleared by the no-match fallback.
func (h *Home) partitionParentFolds(now time.Time) (folds map[string]bool, filtered bool) {
	if len(h.parentChildrenCollapsed) == 0 || h.groupTree == nil {
		return nil, false
	}
	viewArchived := h.statusFilter == FilterModeArchived
	folds = make(map[string]bool, len(h.parentChildrenCollapsed))
	for _, group := range h.groupTree.GroupList {
		if group == nil {
			continue
		}
		for _, s := range group.Sessions {
			if s == nil || !h.parentChildrenCollapsed[s.ID] || s.IsArchived() != viewArchived {
				continue
			}
			if h.statusFilter != "" && !viewArchived && !h.matchesStatusFilter(h.statusFilter, s.Status) {
				filtered = true
				continue
			}
			if h.timeFilter != session.TimeFilterAll && !h.timeFilter.Matches(s.DisplayLastActivityTime(), now) {
				filtered = true
				continue
			}
			folds[s.ID] = true
		}
	}
	return folds, filtered
}

// revealParentFold opens the folded child list (#2631) that would hide inst,
// so the reveal paths (search jump, focus request, session switch, new or
// restored session) land on the row instead of silently finding nothing.
// They already expand collapsed groups the same way.
func (h *Home) revealParentFold(inst *session.Instance) {
	if inst == nil || inst.ParentSessionID == "" || !h.parentChildrenCollapsed[inst.ParentSessionID] {
		return
	}
	delete(h.parentChildrenCollapsed, inst.ParentSessionID)
	_ = h.saveUIStateErr() // view preference; a dropped save self-heals next toggle
}

func (h *Home) moveCursorToSession(sessionID string) {
	for i, fi := range h.flatItems {
		if fi.Type == session.ItemTypeSession && fi.Session != nil && fi.Session.ID == sessionID {
			h.cursor = i
			return
		}
	}
}

// skipDivider nudges the cursor off a non-selectable divider row in the given
// direction (+1 = down, -1 = up). Dividers only ever sit between two non-empty
// sections, so they are never at a list edge nor adjacent to another divider;
// a single step in the travel direction always lands on a selectable row. The
// extra scan is defensive only.
func (h *Home) skipDivider(dir int) {
	n := len(h.flatItems)
	if n == 0 {
		return
	}
	if h.cursor < 0 {
		h.cursor = 0
	}
	if h.cursor >= n {
		h.cursor = n - 1
	}
	if h.flatItems[h.cursor].Type != session.ItemTypeDivider {
		return
	}
	h.cursor += dir
	if h.cursor < 0 {
		h.cursor = 0
	}
	if h.cursor >= n {
		h.cursor = n - 1
	}
	// Defensive: if somehow still on a divider, scan toward a selectable row.
	for h.cursor < n-1 && h.flatItems[h.cursor].Type == session.ItemTypeDivider {
		h.cursor++
	}
	for h.cursor > 0 && h.flatItems[h.cursor].Type == session.ItemTypeDivider {
		h.cursor--
	}
}

// moveCursorToGroup moves the cursor to the flat item matching the given group path.
func (h *Home) moveCursorToGroup(path string) {
	for i, fi := range h.flatItems {
		if fi.Type == session.ItemTypeGroup && fi.Path == path {
			h.cursor = i
			return
		}
	}
}

// moveCursorToRemoteGroup parks the cursor on a remote header after a rebuild.
// Remote headers are identified by RemoteName+Path, the same pair that
// cursor-identity restore matches on.
func (h *Home) moveCursorToRemoteGroup(remoteName, path string) {
	for i, fi := range h.flatItems {
		if fi.Type == session.ItemTypeRemoteGroup && fi.RemoteName == remoteName && fi.Path == path {
			h.cursor = i
			return
		}
	}
}

// isRemoteGroupCollapsed reports whether a remote header is folded shut.
func (h *Home) isRemoteGroupCollapsed(path string) bool {
	return h.remoteGroupsCollapsed[path]
}

// setRemoteGroupCollapsed folds a remote header open or shut, rebuilds the tree
// and keeps the cursor on the header the user acted on. Returns false when the
// state was already what was asked for, so callers can fall through to
// navigation instead of repainting for nothing.
func (h *Home) setRemoteGroupCollapsed(remoteName, path string, collapsed bool) bool {
	if h.remoteGroupsCollapsed == nil {
		h.remoteGroupsCollapsed = make(map[string]bool)
	}
	if h.remoteGroupsCollapsed[path] == collapsed {
		return false
	}
	if collapsed {
		h.remoteGroupsCollapsed[path] = true
	} else {
		delete(h.remoteGroupsCollapsed, path)
	}
	h.rebuildFlatItems()
	h.moveCursorToRemoteGroup(remoteName, path)
	h.saveUIState()
	return true
}

// toggleRemoteGroup flips a remote header's fold state.
func (h *Home) toggleRemoteGroup(remoteName, path string) {
	h.setRemoteGroupCollapsed(remoteName, path, !h.remoteGroupsCollapsed[path])
}

// remoteGroupParentPath returns the header one level up from a remote path, or
// "" for the Level-0 remote root. "remotes/<name>/a/b" -> "remotes/<name>/a",
// "remotes/<name>/a" -> "remotes/<name>".
func remoteGroupParentPath(path string) string {
	idx := strings.LastIndex(path, "/")
	if idx < 0 {
		return ""
	}
	parent := path[:idx]
	if parent == "remotes" {
		return ""
	}
	return parent
}

func (h *Home) captureSelectedItemIdentity() selectedItemIdentity {
	if h.cursor < 0 || h.cursor >= len(h.flatItems) {
		return selectedItemIdentity{windowIndex: -1}
	}

	item := h.flatItems[h.cursor]
	identity := selectedItemIdentity{windowIndex: -1}
	switch item.Type {
	case session.ItemTypeGroup:
		identity.groupPath = item.Path
	case session.ItemTypeSession:
		if item.Session != nil {
			identity.sessionID = item.Session.ID
		} else if item.CreatingID != "" {
			identity.creatingID = item.CreatingID
		}
	case session.ItemTypeWindow:
		identity.windowSessionID = item.WindowSessionID
		identity.windowIndex = item.WindowIndex
	case session.ItemTypeRemoteGroup:
		identity.remoteName = item.RemoteName
		identity.remoteGroupPath = item.Path
	case session.ItemTypeRemoteSession:
		if item.RemoteSession != nil {
			identity.remoteName = item.RemoteName
			identity.remoteSessionID = item.RemoteSession.ID
		}
	}
	return identity
}

func (h *Home) restoreSelectedItemIdentity(identity selectedItemIdentity) bool {
	for i, item := range h.flatItems {
		switch {
		case identity.windowSessionID != "" && item.Type == session.ItemTypeWindow && item.WindowSessionID == identity.windowSessionID && item.WindowIndex == identity.windowIndex:
			h.cursor = i
			return true
		case identity.sessionID != "" && item.Type == session.ItemTypeSession && item.Session != nil && item.Session.ID == identity.sessionID:
			h.cursor = i
			return true
		case identity.creatingID != "" && item.CreatingID == identity.creatingID:
			h.cursor = i
			return true
		case identity.groupPath != "" && item.Type == session.ItemTypeGroup && item.Path == identity.groupPath:
			h.cursor = i
			return true
		case identity.remoteSessionID != "" && item.Type == session.ItemTypeRemoteSession && item.RemoteSession != nil && item.RemoteName == identity.remoteName && item.RemoteSession.ID == identity.remoteSessionID:
			h.cursor = i
			return true
		case identity.remoteGroupPath != "" && item.Type == session.ItemTypeRemoteGroup && item.RemoteName == identity.remoteName && item.Path == identity.remoteGroupPath:
			h.cursor = i
			return true
		}
	}

	if identity.windowSessionID != "" {
		for i, item := range h.flatItems {
			if item.Type == session.ItemTypeSession && item.Session != nil && item.Session.ID == identity.windowSessionID {
				h.cursor = i
				return true
			}
		}
	}

	return false
}

func (h *Home) rebuildFlatItemsPreservingSelection(identity selectedItemIdentity) {
	h.rebuildFlatItemsPreservingSelectionAt(identity, time.Now())
}

func (h *Home) rebuildFlatItemsPreservingSelectionAt(identity selectedItemIdentity, now time.Time) {
	h.rebuildFlatItemsAt(now)
	if !h.restoreSelectedItemIdentity(identity) && len(h.flatItems) > 0 {
		h.cursor = min(h.cursor, len(h.flatItems)-1)
		h.cursor = max(h.cursor, 0)
	}
	h.syncViewport()
}

// rebuildFlatItems rebuilds the flattened view from group tree
func (h *Home) rebuildFlatItems() {
	h.rebuildFlatItemsAt(time.Now())
}

func (h *Home) rebuildFlatItemsAt(now time.Time) {
	h.nextTimeFilterExpiry = time.Time{}
	h.jumpMode = false
	h.jumpBuffer = ""

	startStatusFilter, startTimeFilter := h.statusFilter, h.timeFilter
	parentFolds, foldsFiltered := h.partitionParentFolds(now)
	allItems := h.groupTree.FlattenWithParentFolds(parentFolds)
	if h.embeddedLayout && h.sidebarMode == sidebarFlat {
		allItems = flatSidebarItemsFromTree(h.groupTree)
	}

	// Partition archived vs active before status filters. Group membership is
	// resolved from the full group tree — not the flattened view — so
	// collapsed groups still show their headers when they contain matches.
	viewArchived := h.statusFilter == FilterModeArchived
	if viewArchived || h.hasArchivedSessions() {
		groupsWithMatches := h.archiveGroupsWithMatches(viewArchived)
		partitioned := make([]session.Item, 0, len(allItems))
		for _, item := range allItems {
			if item.Type == session.ItemTypeGroup {
				// Archived view: only show groups that actually contain archived
				// sessions. Active view: keep every group header — groups are never
				// themselves archived, so empty groups and groups whose sessions are
				// all archived remain part of the active list (they render as empty
				// groups, same as before anything was archived) and can sink under
				// the view-mode divider instead of vanishing.
				if !viewArchived || groupsWithMatches[item.Path] {
					partitioned = append(partitioned, item)
				}
			} else if item.Type == session.ItemTypeSession && item.Session != nil {
				isArchived := item.Session.IsArchived()
				if viewArchived == isArchived {
					partitioned = append(partitioned, item)
				}
			}
		}
		allItems = partitioned
	}

	// Apply status filter if active (skip when browsing archived list).
	if h.statusFilter != "" && h.statusFilter != FilterModeArchived {
		// Use full membership so a collapsed match retains a header. The
		// recency pass below must not count sessions whose status header was
		// discarded merely because their group is folded.
		groupsWithMatches := make(map[string]bool)
		for _, group := range h.groupTree.GroupList {
			if group == nil {
				continue
			}
			for _, inst := range group.Sessions {
				if inst != nil && !inst.IsArchived() && h.matchesStatusFilter(h.statusFilter, inst.Status) {
					markGroupPathAndAncestors(groupsWithMatches, group.Path)
				}
			}
		}

		// Second pass: filter items
		filtered := make([]session.Item, 0, len(allItems))
		for _, item := range allItems {
			if item.Type == session.ItemTypeGroup {
				// Keep group if it has matching sessions
				if groupsWithMatches[item.Path] {
					filtered = append(filtered, item)
				}
			} else if item.Type == session.ItemTypeSession && item.Session != nil {
				// Keep session if it matches the filter
				if h.matchesStatusFilter(h.statusFilter, item.Session.Status) {
					filtered = append(filtered, item)
				}
			}
		}
		if len(filtered) == 0 && len(allItems) > 0 && !h.keepEmptyFilter && h.statusFilter != FilterModeActive {
			// #2627 (CodeRabbit): remote rows count toward the fallback too —
			// clearing the filter because no LOCAL row matches would bypass
			// the remote filtering this PR adds, showing every remote row.
			// Clear only when neither local nor remote rows match; keep the
			// local list empty-but-filtered when a remote match survives.
			// This branch never runs in the ^ archived view (the enclosing
			// block excludes FilterModeArchived), so only active-partition
			// remote rows can match. Hold the read lock like the remote
			// snapshot below does.
			remoteHasMatch := false
			h.remoteSessionsMu.RLock()
			for _, remoteSessions := range h.remoteSessions {
				for _, remote := range remoteSessions {
					if !remote.Archived && h.remoteMatchesStatus(h.statusFilter, remote) {
						remoteHasMatch = true
						break
					}
				}
				if remoteHasMatch {
					break
				}
			}
			h.remoteSessionsMu.RUnlock()
			if remoteHasMatch {
				h.flatItems = filtered
			} else {
				h.statusFilter = ""
				h.flatItems = allItems
			}
		} else {
			h.flatItems = filtered
		}
	} else {
		h.flatItems = allItems
	}

	// Use one remote snapshot for recency fallback and row construction.
	h.remoteSessionsMu.RLock()
	remoteNames := make([]string, 0, len(h.remoteSessions))
	remotes := make(map[string][]session.RemoteSessionInfo, len(h.remoteSessions))
	remoteGroupLists := make(map[string][]string, len(h.remoteGroups))
	for name, sessions := range h.remoteSessions {
		// Partition remote rows the way local ones were above: the active
		// view hides remote sessions the remote reports archived, and the ^
		// archived view shows only those. In the plain active view a remote
		// keeps its host header (and its empty groups) even with no active
		// session, like an empty local group renders "name (0)", so a fresh
		// remote or one whose last session was archived stays a target for
		// n, N and g. The archived view and any filter hide such a remote.
		partitioned := make([]session.RemoteSessionInfo, 0, len(sessions))
		for _, remote := range sessions {
			if remote.Archived == viewArchived {
				// #2627: remote rows honor the status filter the way local rows
				// do. In the archived view the Archived partition IS the filter
				// (same as local), so the predicate runs only outside it. Old
				// remotes omit substate/archived — those decode to the zero
				// status, degraded the same way the status glyph degrades it.
				if h.statusFilter != "" && !viewArchived && !h.remoteMatchesStatus(h.statusFilter, remote) {
					continue
				}
				partitioned = append(partitioned, remote)
			}
		}
		plainActiveView := !viewArchived && h.timeFilter == session.TimeFilterAll && h.statusFilter == ""
		if len(partitioned) == 0 && !plainActiveView {
			continue
		}
		remoteNames = append(remoteNames, name)
		remotes[name] = partitioned
		remoteGroupLists[name] = append([]string(nil), h.remoteGroups[name]...)
	}
	h.remoteSessionsMu.RUnlock()
	sort.Strings(remoteNames)

	// Decide fallback across eligible local and remote sessions before hiding
	// rows. Full group membership keeps collapsed matches available to reopen.
	remoteMatchesTime := func(remote session.RemoteSessionInfo) bool {
		activity, known := remote.LastActivity()
		return !known || h.timeFilter.Matches(activity, now)
	}
	if h.timeFilter != session.TimeFilterAll {
		groupsWithMatches := make(map[string]bool)
		hasCandidates, hasMatches := false, false
		for _, group := range h.groupTree.GroupList {
			if group == nil || !h.isInGroupScope(group.Path) {
				continue
			}
			for _, inst := range group.Sessions {
				if inst == nil || inst.IsArchived() != viewArchived {
					continue
				}
				if h.statusFilter != "" && !viewArchived && !h.matchesStatusFilter(h.statusFilter, inst.Status) {
					continue
				}
				hasCandidates = true
				if h.timeFilter.Matches(inst.DisplayLastActivityTime(), now) {
					hasMatches = true
					h.recordTimeFilterExpiry(inst.DisplayLastActivityTime(), now)
					markGroupPathAndAncestors(groupsWithMatches, group.Path)
				}
			}
		}
		for _, sessions := range remotes {
			for _, remote := range sessions {
				// #2627: the status filter narrows the time-fallback the same
				// way it narrows local sessions above — a row the filter hides
				// is neither a candidate nor a match.
				if h.statusFilter != "" && !viewArchived && !h.remoteMatchesStatus(h.statusFilter, remote) {
					continue
				}
				hasCandidates = true
				if remoteMatchesTime(remote) {
					hasMatches = true
					if activity, known := remote.LastActivity(); known {
						h.recordTimeFilterExpiry(activity, now)
					}
				}
			}
		}
		if hasCandidates && !hasMatches && !h.keepEmptyFilter {
			h.timeFilter = session.TimeFilterAll
		} else {
			filtered := make([]session.Item, 0, len(h.flatItems))
			for _, item := range h.flatItems {
				if item.Type == session.ItemTypeGroup && groupsWithMatches[item.Path] {
					filtered = append(filtered, item)
				} else if item.Type == session.ItemTypeSession && item.Session != nil && h.timeFilter.Matches(item.Session.DisplayLastActivityTime(), now) {
					filtered = append(filtered, item)
				}
			}
			h.flatItems = filtered
		}
	}

	// A no-match fallback above cleared a filter that had kept some parent
	// folds out of Flatten. The parent rows are drawn again now, so rebuild
	// with the folds applied. Filters only ever clear here, so this recurses
	// at most once per filter.
	if foldsFiltered && (h.statusFilter != startStatusFilter || h.timeFilter != startTimeFilter) {
		h.rebuildFlatItemsAt(now)
		return
	}

	// Apply group scope filter (composes with status filter above)
	if h.groupScope != "" {
		scoped := make([]session.Item, 0, len(h.flatItems))
		for _, item := range h.flatItems {
			if h.isInGroupScope(item.Path) {
				scoped = append(scoped, item)
			}
		}
		h.flatItems = scoped
	}

	// Partition into top/bottom sections by view mode (active-on-top / populated-on-top).
	// Runs after filtering/scoping but before window injection so windows follow
	// their parent session into whichever section it lands in.
	if h.groupViewMode != session.GroupViewNormal {
		// Activity is computed from the full tree (collapse-agnostic) so a
		// collapsed-but-populated group's header is placed by its real contents,
		// not by the (absent) session rows under a collapsed header. It honors the
		// archive view so a group whose sessions are all archived counts as empty
		// in the active view and sinks below the divider.
		activity := h.groupTree.GroupActivityMapWith(viewArchived, h.viewModeOpts)
		h.flatItems = session.PartitionByViewModeWith(h.flatItems, h.groupViewMode, activity, h.viewModeOpts)
	}

	// Recompute IsLastInGroup, IsLastSubSession and ParentIsLastInGroup on the
	// final visible list. GroupTree.Flatten sets them over a group's full
	// session list (archived sessions included), but the archived/status
	// filtering and view-mode partitioning above can drop or reorder the
	// trailing rows. Runs before window injection so injected windows inherit
	// the corrected flags.
	h.flatItems = session.RecomputeTreeConnectors(h.flatItems)

	// Inject window items after sessions that have 2+ windows
	if len(h.flatItems) > 0 {
		expanded := make([]session.Item, 0, len(h.flatItems)+8)
		for _, item := range h.flatItems {
			expanded = append(expanded, item)

			if item.Type != session.ItemTypeSession || item.Session == nil {
				continue
			}
			tmuxSess := item.Session.GetTmuxSession()
			if tmuxSess == nil {
				continue
			}
			wins := tmux.GetCachedWindows(tmuxSess.TmuxName())
			if len(wins) < 2 || h.windowsCollapsed[item.Session.ID] {
				continue
			}

			// A window hangs off the row above it, so it takes that row's own
			// "am I last" flag: IsLastSubSession for a sub-session row,
			// IsLastInGroup for a top-level row. Reading IsLastInGroup
			// unconditionally left a dangling │ under the last visible
			// sub-session's windows (R4).
			parentIsLast := item.IsLastInGroup
			if item.IsSubSession {
				parentIsLast = item.IsLastSubSession
			}
			for winIdx, win := range wins {
				expanded = append(expanded, session.Item{
					Type:                session.ItemTypeWindow,
					WindowIndex:         win.Index,
					WindowID:            win.ID,
					WindowName:          win.Name,
					WindowTool:          win.Tool,
					WindowSessionID:     item.Session.ID,
					Level:               item.Level + 1,
					Path:                item.Path,
					IsWindow:            true,
					IsLastWindow:        winIdx == len(wins)-1,
					IsLastInGroup:       parentIsLast && winIdx == len(wins)-1,
					ParentIsLastInGroup: parentIsLast,
				})
			}
		}
		h.flatItems = expanded
	}

	// Header counts are taken once here from the same in-view slices the
	// renderer used to rescan per header per frame (finding 11).
	h.remoteHeaderCounts = make(map[string]remoteHeaderCount)
	if len(remotes) > 0 {
		collapsed := h.remoteGroupsCollapsed
		if h.embeddedLayout && h.sidebarMode == sidebarFlat {
			// Flat presentation has no headers to reopen hidden descendants.
			// Keep the saved folds for the next switch to grouped presentation.
			collapsed = nil
		}
		for _, remoteName := range remoteNames {
			sessions := remotes[remoteName]
			for path, counts := range remoteHeaderCounts(remoteName, sessions) {
				h.remoteHeaderCounts[path] = counts
			}
			if h.timeFilter != session.TimeFilterAll {
				filtered := make([]session.RemoteSessionInfo, 0, len(sessions))
				for _, remote := range sessions {
					if remoteMatchesTime(remote) {
						filtered = append(filtered, remote)
					}
				}
				if len(filtered) == 0 {
					continue
				}
				sessions = filtered
			}
			// Filter before building rows so collapsed headers and connectors
			// describe the surviving sessions.
			// #1553: nest each remote's sessions under their Group paths
			// instead of dumping them flat at Level 1.
			// #1875: apply the user's manual row order for this remote, and
			// the remote's own group order to the group headers. In the plain
			// active view, empty remote groups get a header row too, like an
			// empty local group; any filter or the archived view hides them.
			showEmptyGroups := !viewArchived && h.timeFilter == session.TimeFilterAll && h.statusFilter == ""
			rows := buildRemoteFlatItemsWithEmptyGroups(remoteName, sessions, collapsed, h.remoteSessionOrder.forRemote(remoteName), remoteGroupLists[remoteName], showEmptyGroups)
			for _, row := range rows {
				// An empty group's header has no session to count; give it
				// a zero entry so the renderer never falls back to a scan.
				if row.Type == session.ItemTypeRemoteGroup {
					if _, ok := h.remoteHeaderCounts[row.Path]; !ok {
						h.remoteHeaderCounts[row.Path] = remoteHeaderCount{}
					}
				}
			}
			h.flatItems = append(h.flatItems, rows...)
		}
	}

	if h.embeddedLayout && h.sidebarMode == sidebarFlat {
		h.flatItems = flattenSidebarItems(h.flatItems)
	}

	// Pre-compute root group numbers for O(1) hotkey lookup (replaces O(n) loop in renderGroupItem).
	// View-mode partitioning can duplicate root headers; only the first copy
	// owns the logical root's digit.
	rootNum := 0
	seenRoots := make(map[string]bool)
	for i := range h.flatItems {
		if h.flatItems[i].Type == session.ItemTypeGroup && h.flatItems[i].Level == 0 {
			rootKey := h.flatItems[i].Path
			if rootKey == "" && h.flatItems[i].Group != nil {
				rootKey = h.flatItems[i].Group.Path
			}
			if seenRoots[rootKey] {
				h.flatItems[i].RootGroupNum = 0
				continue
			}
			rootNum++
			seenRoots[rootKey] = true
			h.flatItems[i].RootGroupNum = rootNum
		}
	}

	// Invalidate mouse double-click tracking (item indices may have shifted)
	h.lastClickIndex = -1

	// Inject creating session placeholders (worktree creation in progress)
	for _, creating := range h.creatingSessions {
		item := session.Item{
			Type:          session.ItemTypeSession,
			Level:         1,
			Path:          creating.GroupPath,
			CreatingID:    creating.ID,
			CreatingTitle: creating.Title,
			CreatingTool:  creating.Tool,
		}
		// Insert at the appropriate group position
		inserted := false
		if creating.GroupPath != "" {
			for i := len(h.flatItems) - 1; i >= 0; i-- {
				fi := h.flatItems[i]
				if fi.Type == session.ItemTypeGroup && fi.Path == creating.GroupPath {
					// Insert after the group header
					h.flatItems = append(h.flatItems[:i+1], append([]session.Item{item}, h.flatItems[i+1:]...)...)
					inserted = true
					break
				}
				if fi.Path == creating.GroupPath && (fi.Type == session.ItemTypeSession || fi.CreatingID != "") {
					// Insert after the last session in this group
					h.flatItems = append(h.flatItems[:i+1], append([]session.Item{item}, h.flatItems[i+1:]...)...)
					inserted = true
					break
				}
			}
		}
		if !inserted {
			// No group found or no group — append at end (before remotes)
			h.flatItems = append(h.flatItems, item)
		}
	}

	// Ensure cursor is valid
	if h.cursor >= len(h.flatItems) {
		h.cursor = len(h.flatItems) - 1
	}
	if h.cursor < 0 {
		h.cursor = 0
	}
	// Adjust viewport if cursor is out of view
	h.syncViewport()

	h.checkFlatItemsUnique()
	h.dumpFlatItems()

	// Publish an updated web snapshot when menu structure/session list changes.
	h.publishWebMenuSnapshot()
}

// sidebarLineBudget reports how many terminal lines the session list may draw
// into, and the width it draws at. Scroll windowing, the auto sidebar density,
// and click hit-testing all measure the sidebar against this one budget, so
// none of them can disagree about how much room a row has.
//
// MUST match the calculation in View() exactly!
//
// Layout breakdown:
// - Header: 1 line
// - Filter bar: 1 line (always shown)
// - Update banner: 0 or 1 line (when update available)
// - Maintenance banner: 0 or 1 line (when maintenance completed)
// - Main content: contentHeight lines
// - Help bar: 2 lines (border + content)
// Panel title within content: 2 lines (title + underline)
// Panel content: contentHeight - 2 lines
func (h *Home) sidebarLineBudget() (lineBudget int, sidebarWidth int) {
	helpBarHeight := 2
	panelTitleLines := 2 // SESSIONS title + underline (matches View())

	// Filter bar is always shown for consistent layout (matches View())
	filterBarHeight := 1
	updateBannerHeight := 0
	if h.shouldRenderUpdateBanner() {
		updateBannerHeight = 1
	}
	maintenanceBannerHeight := 0
	if h.maintenanceMsg != "" {
		maintenanceBannerHeight = 1
	}
	debugBarHeight := 0
	if h.debugMode {
		debugBarHeight = 1
	}

	// contentHeight = total height for main content area
	// MUST match View(): subtract debugBarHeight when the debug footer is rendered.
	contentHeight := h.height - 1 - helpBarHeight - updateBannerHeight - maintenanceBannerHeight - filterBarHeight - debugBarHeight - h.promptInputDialog.ReservedHeight()

	// CRITICAL: Calculate panelContentHeight based on current layout mode
	// This MUST match the calculations in renderStackedLayout/renderDualColumnLayout/renderSingleColumnLayout
	var panelContentHeight int
	layoutMode := h.getLayoutMode()
	switch layoutMode {
	case LayoutModeStacked:
		// Stacked layout: list gets (100-previewPct)% of height, minus title.
		// Must match renderStackedLayout via the shared stackedListHeight helper.
		listHeight := h.stackedListHeight(contentHeight)
		panelContentHeight = listHeight - panelTitleLines
	case LayoutModeSingle:
		// Single column: list gets full height minus title
		// Must match: listHeight := totalHeight - 2
		panelContentHeight = contentHeight - panelTitleLines
	default: // LayoutModeDual
		// Dual layout: list panel gets full contentHeight minus title
		panelContentHeight = contentHeight - panelTitleLines
	}

	sidebarWidth = h.width
	if layoutMode == LayoutModeDual {
		sidebarWidth = h.sessionsPaneWidth()
	}
	return max(1, panelContentHeight-1), sidebarWidth
}

// syncViewport ensures the cursor is visible within the viewport
// Call this after any cursor movement
func (h *Home) syncViewport() {
	if len(h.flatItems) == 0 {
		h.viewOffset = 0
		return
	}

	lineBudget, sidebarWidth := h.sidebarLineBudget()
	rowLines := h.sidebarRowLines()

	if h.cursor < h.viewOffset {
		h.viewOffset = h.cursor
	}
	if h.viewOffset >= len(h.flatItems) {
		h.viewOffset = len(h.flatItems) - 1
	}
	if h.viewOffset < 0 {
		h.viewOffset = 0
	}

	// Measure once, then remove one row as the viewport advances. Recounting
	// the entire remaining span at every offset made a jump to the end of a
	// large embedded list quadratic in the number of sessions.
	toCursorHeight := h.sidebarItemsHeightWithLines(h.flatItems, h.viewOffset, h.cursor+1, sidebarWidth, rowLines)
	for h.viewOffset < h.cursor {
		effectiveBudget := lineBudget
		if h.viewOffset > 0 {
			effectiveBudget--
		}
		effectiveBudget = max(1, effectiveBudget)
		if toCursorHeight <= effectiveBudget {
			break
		}
		toCursorHeight -= h.sidebarItemRenderHeightAtWidthLines(h.flatItems[h.viewOffset], sidebarWidth, rowLines)
		h.viewOffset++
	}

	// Scroll back up only to reclaim blank space at the end of the list
	// (after removals or a resize). Measuring to the end of the list — not
	// the cursor — keeps the viewport still while the cursor moves within it.
	toEndHeight := h.sidebarItemsHeightWithLines(h.flatItems, h.viewOffset, len(h.flatItems), sidebarWidth, rowLines)
	for h.viewOffset > 0 {
		candidate := h.viewOffset - 1
		effectiveBudget := lineBudget
		if candidate > 0 {
			effectiveBudget--
		}
		effectiveBudget = max(1, effectiveBudget)
		candidateHeight := toEndHeight + h.sidebarItemRenderHeightAtWidthLines(h.flatItems[candidate], sidebarWidth, rowLines)
		if candidateHeight > effectiveBudget {
			break
		}
		h.viewOffset = candidate
		toEndHeight = candidateHeight
	}
}

// getVisibleHeight returns the number of visible items in the session list
// Used for vi-style pagination (Ctrl+u/d/f/b)
func (h *Home) getVisibleHeight() int {
	helpBarHeight := 2
	panelTitleLines := 2
	filterBarHeight := 1
	updateBannerHeight := 0
	if h.shouldRenderUpdateBanner() {
		updateBannerHeight = 1
	}
	maintenanceBannerHeight := 0
	if h.maintenanceMsg != "" {
		maintenanceBannerHeight = 1
	}
	debugBarHeight := 0
	if h.debugMode {
		debugBarHeight = 1
	}

	contentHeight := h.height - 1 - helpBarHeight - updateBannerHeight - maintenanceBannerHeight - filterBarHeight - debugBarHeight - h.promptInputDialog.ReservedHeight()

	var panelContentHeight int
	layoutMode := h.getLayoutMode()
	switch layoutMode {
	case LayoutModeStacked:
		listHeight := h.stackedListHeight(contentHeight)
		panelContentHeight = listHeight - panelTitleLines
	case LayoutModeSingle:
		panelContentHeight = contentHeight - panelTitleLines
	default: // LayoutModeDual
		panelContentHeight = contentHeight - panelTitleLines
	}

	maxVisible := panelContentHeight - 1
	if maxVisible < 1 {
		maxVisible = 1
	}
	return maxVisible
}

// jumpToRootGroup jumps to the root header carrying the requested digit.
func (h *Home) jumpToRootGroup(n int) {
	if n < 1 || n > 9 {
		return
	}

	for i, item := range h.flatItems {
		if item.Type == session.ItemTypeGroup && item.Level == 0 && item.RootGroupNum == n {
			h.cursor = i
			h.syncViewport()
			return
		}
	}
}

// getCurrentGroupPath returns the group path of the currently selected item
func (h *Home) getCurrentGroupPath() string {
	if h.cursor >= 0 && h.cursor < len(h.flatItems) {
		item := h.flatItems[h.cursor]
		if item.Type == session.ItemTypeGroup && item.Group != nil {
			return item.Group.Path
		}
		if item.Type == session.ItemTypeSession && item.Session != nil {
			return item.Session.GroupPath
		}
	}
	return ""
}

func (h *Home) hasArchivedSessions() bool {
	if h == nil || h.groupTree == nil {
		return false
	}
	for _, group := range h.groupTree.GroupList {
		if group == nil {
			continue
		}
		for _, sess := range group.Sessions {
			if sess != nil && sess.IsArchived() {
				return true
			}
		}
	}
	return false
}

// archiveGroupsWithMatches returns group paths that contain at least one
// session matching the archive partition (active vs archived-only view).
func (h *Home) archiveGroupsWithMatches(viewArchived bool) map[string]bool {
	groupsWithMatches := make(map[string]bool)
	if h == nil || h.groupTree == nil {
		return groupsWithMatches
	}
	for _, group := range h.groupTree.GroupList {
		if group == nil {
			continue
		}
		for _, sess := range group.Sessions {
			if sess == nil {
				continue
			}
			if viewArchived != sess.IsArchived() {
				continue
			}
			markGroupPathAndAncestors(groupsWithMatches, group.Path)
		}
	}
	return groupsWithMatches
}

func markGroupPathAndAncestors(groupsWithMatches map[string]bool, groupPath string) {
	if groupsWithMatches == nil || groupPath == "" {
		return
	}
	parts := strings.Split(groupPath, "/")
	for i := range parts {
		groupsWithMatches[strings.Join(parts[:i+1], "/")] = true
	}
}

// matchesStatusFilter reports whether status passes the current filter.
// FilterModeActive consults [display].active_filter_excludes (a status is
// excluded when it or its statusBucket is); concrete filters match the
// status's statusBucket, the bucket its row glyph and the pill count show.
func (h *Home) matchesStatusFilter(filter, status session.Status) bool {
	if filter == FilterModeActive {
		return !h.activeFilterExcludes[status] && !h.activeFilterExcludes[statusBucket(status)] &&
			!(h.activeFilterHideStopped && statusBucket(status) == session.StatusStopped)
	}
	return statusBucket(status) == filter
}

// remoteMatchesStatus applies the status-filter predicate to a remote row
// (#2627). RemoteSessionInfo.Status is the wire string; converting it to
// session.Status puts remote rows through the same statusBucket mapping the
// local predicate uses. Old remotes omit substate/archived, and the wire
// Status for those still decodes to a valid bucket — the row keeps the
// coarse behavior its glyph already shows, as the issue requires.
func (h *Home) remoteMatchesStatus(filter session.Status, remote session.RemoteSessionInfo) bool {
	return h.matchesStatusFilter(filter, session.Status(remote.Status))
}
