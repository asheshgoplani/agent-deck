package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// renderSessionList renders the left panel with hierarchical session list
func (h *Home) renderSessionList(width, height int) string {
	var b strings.Builder

	if len(h.flatItems) == 0 {
		// Responsive empty state - adapts to available space
		// Account for border (2 chars each side) when calculating content area
		contentWidth := width - 4
		contentHeight := height - 2
		if contentWidth < 10 {
			contentWidth = 10
		}
		if contentHeight < 5 {
			contentHeight = 5
		}
		if config, filtered := h.filteredEmptyState(); filtered {
			return lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(ColorBorder).
				Render(renderEmptyStateResponsive(config, contentWidth, contentHeight))
		}

		// Group-scoped empty state
		if h.groupScope != "" {
			hints := []string{}
			if key := h.actionKey(hotkeyNewSession); key != "" {
				hints = append(hints, fmt.Sprintf("Press %s to create a session", key))
			}
			emptyContent := renderEmptyStateResponsive(EmptyStateConfig{
				Icon:     "⬡",
				Title:    "No sessions in " + h.groupScopeDisplayName(),
				Subtitle: "This group is empty",
				Hints:    hints,
			}, contentWidth, contentHeight)

			return lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(ColorBorder).
				Render(emptyContent)
		}

		hints := make([]string, 0, 3)
		if key := h.actionKey(hotkeyNewSession); key != "" {
			hints = append(hints, fmt.Sprintf("Press %s to create a new session", key))
		}
		if key := h.actionKey(hotkeyImport); key != "" {
			hints = append(hints, fmt.Sprintf("Press %s to import existing tmux sessions", key))
		}
		if key := h.actionKey(hotkeyCreateGroup); key != "" {
			hints = append(hints, fmt.Sprintf("Press %s to create a group", key))
		}
		if len(hints) == 0 {
			hints = append(hints, "Create or import sessions to get started")
		}

		emptyContent := renderEmptyStateResponsive(EmptyStateConfig{
			Icon:     "⬡",
			Title:    "No Sessions Yet",
			Subtitle: "Get started by creating your first session",
			Hints:    hints,
		}, contentWidth, contentHeight)

		return lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorBorder).
			Render(emptyContent)
	}

	// Render items starting from viewOffset. Local sessions use a three-line
	// Embedded card when the pane is wide enough; remote sessions use two lines and
	// group/divider/window rows stay one line.
	usedLines := 0
	lineBudget := height - 1 // Leave room for scrolling indicator
	if lineBudget < 1 {
		lineBudget = 1
	}

	// Show "more above" indicator if scrolled down
	if h.viewOffset > 0 {
		b.WriteString(DimStyle.Render(fmt.Sprintf("  ⋮ +%d above", h.viewOffset)))
		b.WriteString("\n")
		lineBudget-- // Account for the indicator line
	}

	snapshot := h.getSessionRenderSnapshot()
	groupStats := h.buildGroupRenderStats(snapshot)
	var jumpHintByItemIndex map[int]string
	if h.jumpMode {
		selectable := selectableItemIndices(h.flatItems)
		jumpHints := generateJumpHints(len(selectable))
		jumpHintByItemIndex = make(map[int]string, len(selectable))
		for hintIndex, itemIndex := range selectable {
			jumpHintByItemIndex[itemIndex] = jumpHints[hintIndex]
		}
	}

	nextItem := h.viewOffset
	rowLines := h.sidebarRowLines()
	refSrcID := h.sessionRef.srcID
	for i := h.viewOffset; i < len(h.flatItems); i++ {
		item := h.flatItems[i]
		rowHeight := h.sidebarItemRenderHeightAtWidthLines(item, width, rowLines)
		if usedLines > 0 && usedLines+rowHeight > lineBudget {
			break
		}
		if h.jumpMode && item.Type != session.ItemTypeDivider {
			hint, ok := jumpHintByItemIndex[i]
			if !ok {
				h.renderItem(&b, item, i == h.cursor, i, groupStats, snapshot, width)
				usedLines += rowHeight
				nextItem = i + 1
				continue
			}
			// Render item to temp buffer, then paint the fixed row gutter.
			var itemBuf strings.Builder
			h.renderItem(&itemBuf, item, i == h.cursor, i, groupStats, snapshot, width)
			raw := itemBuf.String()
			isMatch := h.jumpBuffer == "" || strings.HasPrefix(hint, h.jumpBuffer)

			if isMatch {
				// Overlay hint on the first line, preserve rest exactly
				if idx := strings.Index(raw, "\n"); idx >= 0 {
					b.WriteString(h.overlayJumpHintAtWidth(raw[:idx], hint, h.jumpBuffer, width))
					b.WriteString(raw[idx:]) // includes \n and any subsequent lines
				} else {
					b.WriteString(h.overlayJumpHintAtWidth(raw, hint, h.jumpBuffer, width))
				}
			} else {
				// Non-matching: render normally (no dimming to preserve layout)
				b.WriteString(raw)
			}
		} else if refSrcID != "" && i != h.cursor && item.Type == session.ItemTypeSession &&
			item.Session != nil && item.Session.ID == refSrcID {
			// The picked reference source A (ASK-111): ◆ in the gutter.
			var itemBuf strings.Builder
			h.renderItem(&itemBuf, item, false, i, groupStats, snapshot, width)
			raw := itemBuf.String()
			if idx := strings.Index(raw, "\n"); idx >= 0 {
				b.WriteString(overlaySessionRefMark(raw[:idx]))
				b.WriteString(raw[idx:])
			} else {
				b.WriteString(overlaySessionRefMark(raw))
			}
		} else {
			h.renderItem(&b, item, i == h.cursor, i, groupStats, snapshot, width)
		}
		usedLines += rowHeight
		nextItem = i + 1
	}

	// Show "more below" indicator if there are more items
	remaining := len(h.flatItems) - nextItem
	if remaining > 0 {
		b.WriteString(DimStyle.Render(fmt.Sprintf("  ⋮ +%d below", remaining)))
	}

	// Height padding is handled by ensureExactHeight() in View() for consistency
	return b.String()
}

type groupRenderStats struct {
	sessionCount int
	running      int
	waiting      int
	tinted       int
	tint         string
}

func (h *Home) buildGroupRenderStats(snapshot map[string]sessionRenderState) map[string]groupRenderStats {
	stats := make(map[string]groupRenderStats)
	if h.groupTree == nil {
		return stats
	}

	// #1987: count the partition being rendered, not the whole slice. A group's
	// Sessions holds active and archived rows together while the deck renders
	// exactly one partition at a time (rebuildFlatItems keeps only the rows whose
	// IsArchived matches the current view), so a raw len() reports sessions the
	// header is not heading — `demo (5)` above two rows, or `My Sessions (180)`
	// above 19. The running/waiting tallies are wrong for a second reason that
	// the same filter fixes: archiving does not reset Status and the status
	// updater skips archived sessions (see shouldPollStatusInLoop), so an archived
	// session contributes whatever it was doing when it was archived, forever.
	//
	// The rule is partition-aware rather than archive-excluding: in the archived
	// view (^) the header must count archived rows, because those are the rows
	// underneath it. Same shape as SameArchivePartition in the reorder path.
	viewArchived := h.statusFilter == FilterModeArchived

	for path, g := range h.groupTree.Groups {
		if g == nil {
			continue
		}

		directSessions := 0
		directRunning := 0
		directWaiting := 0
		directTinted := 0
		directTint := ""
		for _, sess := range g.Sessions {
			if sess.IsArchived() != viewArchived {
				continue
			}
			directSessions++
			// Same field the session row paints (renderSessionItem), so a
			// group name and its row can never disagree.
			if sess.Color != "" {
				directTinted++
				if directTint == "" || sess.Color < directTint {
					directTint = sess.Color
				}
			}
			state, ok := snapshot[sess.ID]
			status := sess.Status
			if ok {
				status = state.status
			}
			switch status {
			case session.StatusRunning:
				directRunning++
			case session.StatusWaiting:
				directWaiting++
			}
		}

		// Add direct totals to this group and all ancestors once,
		// avoiding repeated recursive scans per rendered row.
		ancestor := path
		for ancestor != "" {
			entry := stats[ancestor]
			entry.sessionCount += directSessions
			entry.running += directRunning
			entry.waiting += directWaiting
			entry.tinted += directTinted
			// Smallest colour string wins: the walk runs over a map, so a
			// fixed choice keeps the paint stable when colours differ.
			if directTint != "" && (entry.tint == "" || directTint < entry.tint) {
				entry.tint = directTint
			}
			stats[ancestor] = entry

			idx := strings.LastIndex(ancestor, "/")
			if idx == -1 {
				break
			}
			ancestor = ancestor[:idx]
		}
	}

	return stats
}

// renderItem renders a single item (group or session) for the left panel
func (h *Home) renderItem(
	b *strings.Builder,
	item session.Item,
	selected bool,
	itemIndex int,
	groupStats map[string]groupRenderStats,
	snapshot map[string]sessionRenderState,
	listWidth int,
) {
	switch item.Type {
	case session.ItemTypeGroup:
		h.renderGroupItem(b, item, selected, itemIndex, groupStats, listWidth)
	case session.ItemTypeSession:
		if item.CreatingID != "" {
			h.renderCreatingSessionItem(b, item, selected)
		} else {
			h.renderSessionItem(b, item, selected, snapshot, listWidth)
		}
	case session.ItemTypeWindow:
		h.renderWindowItem(b, item, selected)
	case session.ItemTypeRemoteGroup:
		h.renderRemoteGroupItem(b, item, selected, listWidth)
	case session.ItemTypeRemoteSession:
		h.renderRemoteSessionItemAtWidth(b, item, selected, listWidth)
	case session.ItemTypeDivider:
		h.renderDivider(b, item)
	}
}

// renderDivider renders the non-selectable separator between view-mode sections
// (e.g. running-on-top). It draws a dim horizontal rule with an optional caption.
func (h *Home) renderDivider(b *strings.Builder, item session.Item) {
	width := h.sessionsPaneWidth() - 4
	if width < 12 {
		width = 12
	}
	var line string
	if item.DividerLabel != "" {
		text := "─ " + item.DividerLabel + " "
		remaining := width - cellWidth(text)
		if remaining < 0 {
			remaining = 0
		}
		line = "  " + text + strings.Repeat("─", remaining)
	} else {
		line = "  " + strings.Repeat("─", width)
	}
	b.WriteString(DimStyle.Render(line))
	b.WriteString("\n")
}

// renderGroupItem renders a group header
// PERFORMANCE: Uses cached styles from styles.go to avoid allocations
func (h *Home) renderGroupItem(
	b *strings.Builder,
	item session.Item,
	selected bool,
	itemIndex int,
	groupStats map[string]groupRenderStats,
	listWidth int,
) {
	group := item.Group
	groupName := group.Name
	if h.groupViewMode == session.GroupViewActiveTop {
		for i := 0; i < itemIndex && i < len(h.flatItems); i++ {
			if h.flatItems[i].Type == session.ItemTypeGroup && h.flatItems[i].Path == item.Path {
				groupName += " (" + h.viewModeOpts.ActiveTopBottomLabel() + ")"
				break
			}
		}
	}

	// Fixed-width hotkey gutter, reserved on every row (see leftGutterWidth). It
	// holds the root group's hotkey number ("N·") when present; otherwise blanks.
	// Keeping it a constant width means the number no longer eats a level of
	// indentation, so a numbered root and its children stay properly nested.
	gutter := strings.Repeat(" ", leftGutterWidth)
	if selected {
		gutter = SessionSelectionPrefix.Render("▶ ")
	} else if item.Level == 0 && item.RootGroupNum >= 1 && item.RootGroupNum <= 9 {
		gutter = GroupHotkeyStyle.Render(fmt.Sprintf("%d·", item.RootGroupNum))
	}

	// Calculate indentation based on nesting level (no tree lines, just spaces).
	// Uses spacingNormal (2 chars) per level for consistent hierarchy
	// visualization, applied after the hotkey gutter.
	indent := strings.Repeat(strings.Repeat(" ", spacingNormal), max(0, item.Level))

	// Expand/collapse indicator with filled triangles (using cached styles)
	var expandIcon string
	if selected {
		if group.Expanded {
			expandIcon = GroupExpandSelStyle.Render("▾")
		} else {
			expandIcon = GroupExpandSelStyle.Render("▸")
		}
	} else {
		if group.Expanded {
			expandIcon = GroupExpandStyle.Render("▾") // Filled triangle for expanded
		} else {
			expandIcon = GroupExpandStyle.Render("▸") // Filled triangle for collapsed
		}
	}

	// Select appropriate cached styles based on selection state
	nameStyle := GroupNameStyle
	countStyle := GroupCountStyle
	if selected {
		nameStyle = GroupNameSelStyle
		countStyle = GroupCountSelStyle
	}

	// Use precomputed recursive stats (group + descendants) for this render pass.
	stats := groupStats[group.Path]
	// A collapsed group hides the session whose name is tinted; carry the tint
	// to the group name so the signal stays visible. Selected rows keep the
	// selected style, expanded groups already show the tinted row.
	if stats.tinted > 0 && !group.Expanded && !selected {
		nameStyle = nameStyle.Foreground(lipgloss.Color(stats.tint))
	}
	countStr := countStyle.Render(fmt.Sprintf(" (%d)", stats.sessionCount))
	if h.compactEmbeddedSidebar() {
		prefix := ""
		if selected {
			prefix = gutter
		}
		row := prefix + indent + expandIcon + " " + nameStyle.Render(groupName) + countStr
		row = fitCellWidth(row, max(1, listWidth))
		if selected {
			row = lipgloss.NewStyle().Foreground(ColorText).Background(ColorSurface).Render(row)
		}
		b.WriteString(row)
		b.WriteString("\n")
		return
	}

	statusStr := ""
	if stats.running > 0 {
		statusStr += " " + GroupStatusRunning.Render(fmt.Sprintf("● %d", stats.running))
	}
	if stats.waiting > 0 {
		statusStr += " " + GroupStatusWaiting.Render(fmt.Sprintf("◐ %d", stats.waiting))
	}

	// Build the row: [hotkey gutter][indent][expand] [name](count) [status]
	row := fmt.Sprintf(
		"%s%s%s %s%s%s",
		gutter,
		indent,
		expandIcon,
		nameStyle.Render(groupName),
		countStr,
		statusStr,
	)
	b.WriteString(row)
	b.WriteString("\n")
}

// Tree drawing characters for visual hierarchy
const (
	treeBranch = "├─" // Mid-level item (has siblings below)
	treeLast   = "└─" // Last item in group (no siblings below)
	treeLine   = "│ " // Continuation line
	treeEmpty  = "  " // Empty space (for alignment)
	// Sub-session connectors (nested under parent)
	subBranch = "├─" // Sub-session with siblings below
	subLast   = "└─" // Last sub-session
)

// renderSessionItem renders a single session item for the left panel
// PERFORMANCE: Uses cached styles from styles.go to avoid allocations
func (h *Home) renderCreatingPreview(creating *CreatingSession, width, height int) string {
	var b strings.Builder
	spinnerFrames := []string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"}
	spinner := spinnerFrames[h.animationFrame]

	centerStyle := lipgloss.NewStyle().
		Width(width - 4).
		Align(lipgloss.Center)

	// Spinner line
	spinnerStyle := lipgloss.NewStyle().
		Foreground(ColorPurple).
		Bold(true)
	spinnerLine := spinnerStyle.Render(spinner + "  " + spinner + "  " + spinner)
	b.WriteString("\n\n")
	b.WriteString(centerStyle.Render(spinnerLine))
	b.WriteString("\n\n")

	// Title
	titleStyle := lipgloss.NewStyle().
		Foreground(ColorPurple).
		Bold(true)
	b.WriteString(centerStyle.Render(titleStyle.Render("🔨 Creating Worktree")))
	b.WriteString("\n\n")

	// Description
	descStyle := lipgloss.NewStyle().
		Foreground(ColorText)
	b.WriteString(centerStyle.Render(descStyle.Render("Setting up " + creating.Title + "...")))
	b.WriteString("\n\n")

	// Elapsed time
	elapsed := time.Since(creating.StartTime).Truncate(time.Second)
	timeStyle := lipgloss.NewStyle().
		Foreground(ColorTextDim).
		Italic(true)
	b.WriteString(centerStyle.Render(timeStyle.Render(fmt.Sprintf("Elapsed: %s", elapsed))))
	b.WriteString("\n\n")

	// Progress dots animation
	dots := strings.Repeat("·", (h.animationFrame%4)+1) + strings.Repeat(" ", 3-h.animationFrame%4)
	dotStyle := lipgloss.NewStyle().Foreground(ColorPurple)
	b.WriteString(centerStyle.Render(dotStyle.Render(dots)))

	return b.String()
}

func (h *Home) renderCreatingSessionItem(
	b *strings.Builder,
	item session.Item,
	selected bool,
) {
	spinnerFrames := []string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"}
	spinner := spinnerFrames[h.animationFrame]

	// Leading hotkey gutter so creating rows align with group/session rows.
	b.WriteString(strings.Repeat(" ", leftGutterWidth))

	// Same selection styles as real session rows so a selected placeholder
	// reads as selected at a glance (highlight bar, not just a dim marker).
	treeStyle := TreeConnectorStyle
	selectionPrefix := "  "
	spinnerStyle := lipgloss.NewStyle().Foreground(ColorPurple)
	titleStyle := lipgloss.NewStyle().Foreground(ColorText).Italic(true)
	subtitleStyle := lipgloss.NewStyle().Foreground(ColorTextDim).Italic(true)
	if selected {
		selectionPrefix = SessionSelectionPrefix.Render("▶ ")
		treeStyle = TreeConnectorSelStyle
		titleStyle = SessionTitleSelStyle.Italic(true)
		subtitleStyle = SessionTitleSelStyle.Italic(true).Faint(true)
		spinnerStyle = SessionStatusSelStyle
	}

	b.WriteString(selectionPrefix)

	if item.Level > 0 {
		b.WriteString(treeStyle.Render("├── "))
	}

	b.WriteString(spinnerStyle.Render(spinner))
	b.WriteString(" ")
	b.WriteString(titleStyle.Render(item.CreatingTitle))
	b.WriteString(subtitleStyle.Render(" (creating worktree...)"))
	b.WriteString("\n")
}

// renderWindowItem renders a single window item (child of a session) for the left panel
func (h *Home) renderWindowItem(b *strings.Builder, item session.Item, selected bool) {
	treeStyle := TreeConnectorStyle

	// Base indent — windows are children of sessions.
	// Show │ continuation line at the parent session's level when it's not the
	// last item in the group. Extra space after │ so window ├─ aligns under
	// the parent session's ○ status bullet (position 4).
	baseIndent := ""
	if item.Level > 1 {
		groupIndent := strings.Repeat(treeEmpty, item.Level-2)
		if item.ParentIsLastInGroup {
			baseIndent = groupIndent + "   " // No continuation needed
		} else {
			baseIndent = groupIndent + " " + treeStyle.Render("│") + " "
		}
	}

	// Tree connector
	treeConnector := subBranch
	if item.IsLastWindow {
		treeConnector = subLast
	}

	// Selection
	selectionPrefix := " "
	nameStyle := lipgloss.NewStyle().Foreground(ColorTextDim)
	indexStyle := lipgloss.NewStyle().Foreground(ColorTextDim)
	if selected {
		selectionPrefix = SessionSelectionPrefix.Render("▶")
		nameStyle = SessionTitleSelStyle
		indexStyle = SessionStatusSelStyle
		treeStyle = TreeConnectorSelStyle
		// Rebuild baseIndent with selection styling
		if item.Level > 1 && !item.ParentIsLastInGroup {
			groupIndent := strings.Repeat(treeEmpty, max(0, item.Level-2))
			baseIndent = groupIndent + " " + treeStyle.Render("│") + " "
		}
	}

	winLabel := indexStyle.Render(fmt.Sprintf("[%d]", item.WindowIndex))
	winName := nameStyle.Render(" " + item.WindowName)

	// Tool badge (if detected)
	toolBadge := ""
	if item.WindowTool != "" {
		toolStyle := GetToolStyle(item.WindowTool)
		if selected {
			toolStyle = SessionStatusSelStyle
		}
		toolBadge = toolStyle.Render(" " + item.WindowTool)
	}

	row := fmt.Sprintf(
		"%s%s%s%s %s%s%s",
		strings.Repeat(" ", leftGutterWidth), // align with group/session hotkey gutter
		baseIndent,
		selectionPrefix,
		treeStyle.Render(treeConnector),
		winLabel,
		winName,
		toolBadge,
	)
	b.WriteString(row)
	b.WriteString("\n")
}

// renderLaunchingState renders the animated launching/resuming indicator for sessions
// renderRemotePreview renders the preview pane for a remote group or session
// remoteSessionsInView returns the remote's sessions on the current side of
// the archive partition (active view hides archived rows, the ^ view shows
// only them), matching the rows rebuildFlatItems actually emits. Header and
// preview counts must be taken from this slice, not h.remoteSessions, or the
// active and archived views both advertise the remote's grand total.
// Caller holds h.remoteSessionsMu (read).
func (h *Home) remoteSessionsInView(remoteName string) []session.RemoteSessionInfo {
	all := h.remoteSessions[remoteName]
	viewArchived := h.statusFilter == FilterModeArchived
	inView := make([]session.RemoteSessionInfo, 0, len(all))
	for _, rs := range all {
		if rs.Archived == viewArchived {
			inView = append(inView, rs)
		}
	}
	return inView
}

// remoteRowGutter returns the fixed-width left gutter for a remote row: the
// selection arrow "▶ " when selected, else leftGutterWidth blanks. Both are the
// same display width, so selecting a row never shifts it, and the Level-0 remote
// host header lands flush with local root groups (which likewise start their
// content right after the gutter). This replaces the separate selection-arrow
// column (selPrefix) that over-indented the whole remote subtree by one level
// (#1553 regression): the arrow now lives inside the gutter instead of after it.
func remoteRowGutter(selected bool) string {
	if selected {
		return "▶ "
	}
	return strings.Repeat(" ", leftGutterWidth)
}

// renderRemoteGroupItem renders a remote group header (e.g., "remotes/dev")
func (h *Home) renderRemoteGroupItem(b *strings.Builder, item session.Item, selected bool, listWidth int) {
	nameStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Bold(true) // yellow
	countStyle := DimStyle
	expandIcon := "▾"
	if h.isRemoteGroupCollapsed(item.Path) {
		expandIcon = "▸" // same glyph pair the local group rows use
	}
	if selected {
		nameStyle = GroupNameSelStyle
		countStyle = GroupCountSelStyle
	}

	// #1553: Level > 0 is a nested sub-group header ("remotes/<name>/<group>").
	// Render the last path segment with a subtree count, indented by level, and
	// no host-latency marker (latency is a host-level metric shown on Level 0).
	if item.Level > 0 {
		groupPath := strings.TrimPrefix(item.Path, "remotes/"+item.RemoteName+"/")
		counts := h.remoteHeaderCount(item)
		if h.remotePollUnavailable(item.RemoteName) {
			counts.running, counts.waiting = 0, 0
		}
		_, stale := h.remoteRowStale(item.RemoteName)

		segName := groupPath
		if idx := strings.LastIndex(groupPath, "/"); idx >= 0 {
			segName = groupPath[idx+1:]
		}

		line := fmt.Sprintf("%s%s%s %s%s%s",
			remoteRowGutter(selected),        // align with group hotkey gutter
			strings.Repeat("  ", item.Level), // nest under the remote header
			expandIcon,
			nameStyle.Render(segName),
			countStyle.Render(fmt.Sprintf(" (%d)", counts.total)),
			remoteStatusSuffix(counts.running, counts.waiting, stale),
		)
		b.WriteString(truncateRemoteGroupLine(line, listWidth))
		b.WriteString("\n")
		return
	}

	// Level 0: the remote host header. Count the sessions this view shows.
	counts := h.remoteHeaderCount(item)
	if h.remotePollUnavailable(item.RemoteName) {
		counts.running, counts.waiting = 0, 0
	}
	_, stale := h.remoteRowStale(item.RemoteName)
	h.remoteSessionsMu.RLock()
	fromCache := h.remoteFromCache[item.RemoteName]
	fetching := h.remotesFetchActive
	versionState := h.remoteVersions[item.RemoteName]
	poll, hasPoll := h.remotePolls[item.RemoteName]
	pollActive := h.remotePollActive[item.RemoteName]
	h.remoteSessionsMu.RUnlock()

	trailer := h.renderRemoteLatencyMarker(item.RemoteName, selected)
	if hasPoll && poll.LastPollError != "" {
		if listWidth > 0 && listWidth < 60 {
			trailer = " " + ErrorStyle.Render("· "+poll.LastPollError)
		} else {
			trailer = " " + DimStyle.Render("· unreachable: "+poll.LastPollError)
		}
		if poll.LastPollStatus == "auth_failed" {
			trailer += " " + DimStyle.Render("(paused; R retry)")
		}
	} else if hasPoll && poll.LastPollStatus == "unknown" && !fromCache {
		trailer = " " + DimStyle.Render("· unknown")
		if pollActive || fetching {
			trailer += " " + DimStyle.Render("· refreshing…")
		}
	} else if fromCache {
		// Honest staleness: this is the startup snapshot, not live state yet.
		trailer = " " + DimStyle.Render("· cached, refreshing…")
	} else if fetching {
		// A fetch is in flight: what is shown is the last answer, and the
		// header says so instead of leaving the user to guess.
		trailer += " " + DimStyle.Render("· refreshing…")
	}

	if hasPoll && poll.LastPollMS != nil && (poll.LastPollError == "" || listWidth == 0 || listWidth >= 60) {
		trailer += " " + DimStyle.Render(fmt.Sprintf("· poll %dms", *poll.LastPollMS))
	}
	if selected && (!hasPoll || poll.LastPollStatus != "auth_failed") {
		trailer += " " + DimStyle.Render("(R retry)")
	}

	line := fmt.Sprintf("%s%s %s%s%s%s%s",
		remoteRowGutter(selected), // align with group hotkey gutter (flush with local root groups)
		expandIcon,
		nameStyle.Render("remotes/"+item.RemoteName),
		countStyle.Render(fmt.Sprintf(" (%d)", counts.total)),
		renderRemoteVersionMarker(versionState, Version, selected), // #2164: drift marker, e.g. " v1.15.0 ↑"
		remoteStatusSuffix(counts.running, counts.waiting, stale),
		trailer,
	)
	b.WriteString(truncateRemoteGroupLine(line, listWidth))
	b.WriteString("\n")
}

// truncateRemoteGroupLine applies the same ellipsis truncation used for
// session titles (cellTruncate) to a remote-group header row. Without this,
// the row got hard-cut by whatever clips the final rendered line to the
// terminal width, with no "…" marker — unlike every session title, which
// truncates itself proactively with cellTruncate before assembly (finding
// 12, audit 2026-09-18).
func truncateRemoteGroupLine(line string, listWidth int) string {
	if listWidth <= 0 || cellWidth(line) <= listWidth {
		return line
	}
	return cellTruncate(line, listWidth, "…")
}

// remoteHeaderCount returns the counts for a remote header row: the ones
// rebuildFlatItems computed with the rows when it has them, otherwise (rows
// built by hand, as tests do) the same numbers scanned from the in-view
// sessions the way the renderer always did.
func (h *Home) remoteHeaderCount(item session.Item) remoteHeaderCount {
	if counts, ok := h.remoteHeaderCounts[item.Path]; ok {
		return counts
	}
	h.remoteSessionsMu.RLock()
	defer h.remoteSessionsMu.RUnlock()
	sessions := h.remoteSessionsInView(item.RemoteName)
	groupPath := ""
	if item.Level > 0 {
		groupPath = strings.TrimPrefix(item.Path, "remotes/"+item.RemoteName+"/")
	}
	counts := remoteHeaderCount{total: len(sessions)}
	if groupPath != "" {
		counts.total = remoteSubGroupCount(sessions, groupPath)
	}
	counts.running, counts.waiting = remoteStatusCounts(sessions, groupPath)
	return counts
}

// remoteStatusSuffix renders the same running/waiting glyph counts local
// group headers show, for remote host and sub-group headers. stale renders
// them dimmed, like the rows of a snapshot older than remoteRowStaleAge.
func remoteStatusSuffix(running, waiting int, stale bool) string {
	runningStyle, waitingStyle := GroupStatusRunning, GroupStatusWaiting
	if stale {
		runningStyle, waitingStyle = DimStyle, DimStyle
	}
	out := ""
	if running > 0 {
		out += " " + runningStyle.Render(fmt.Sprintf("● %d", running))
	}
	if waiting > 0 {
		out += " " + waitingStyle.Render(fmt.Sprintf("◐ %d", waiting))
	}
	return out
}

// renderRemoteLatencyMarker returns the colored ` · network Xms` (or ` · network offline`)
// suffix for a remote group header. Empty string when no measurement has
// been taken yet so the header doesn't jitter on first paint. See #1103.
//
// Color thresholds:
//   - green:  <  50ms        (lipgloss color 2)
//   - yellow: 50-200ms       (color 3)
//   - red:    > 200ms or offline (color 1)
func (h *Home) renderRemoteLatencyMarker(remoteName string, selected bool) string {
	h.remoteLatencyMu.RLock()
	lat, ok := h.remoteLatency[remoteName]
	h.remoteLatencyMu.RUnlock()
	if !ok || lat.MeasuredAt.IsZero() {
		return ""
	}

	var text string
	var color lipgloss.Color
	switch {
	case lat.Offline:
		text = " · network offline"
		color = lipgloss.Color("1") // red
	case lat.MS < 50:
		text = fmt.Sprintf(" · network %dms", lat.MS)
		color = lipgloss.Color("2") // green
	case lat.MS <= 200:
		text = fmt.Sprintf(" · network %dms", lat.MS)
		color = lipgloss.Color("3") // yellow
	default:
		text = fmt.Sprintf(" · network %dms", lat.MS)
		color = lipgloss.Color("1") // red
	}

	style := lipgloss.NewStyle().Foreground(color)
	if selected {
		// On the selected row, preserve color so the threshold signal
		// stays readable against the highlight background.
		style = style.Bold(true)
	}
	return style.Render(text)
}

// renderRemoteSessionItem renders a single remote session row
func (h *Home) renderRemoteSessionItem(b *strings.Builder, item session.Item, selected bool) {
	h.renderRemoteSessionItemAtWidth(b, item, selected, embeddedCardMinWidth-1)
}

func (h *Home) renderRemoteSessionItemAtWidth(b *strings.Builder, item session.Item, selected bool, listWidth int) {
	rs := item.RemoteSession
	if rs == nil {
		return
	}
	if h.embeddedLayout && listWidth >= embeddedCardMinWidth {
		statusIcon, statusStyle := remoteRowStatusGlyph(rs.Status, rs.Substate, rs.Archived)
		// Same freshness rule as the classic row below: a failed/cached poll
		// shows "?" and "last known"; a snapshot older than remoteRowStaleAge
		// keeps its glyph but dims it and says how old it is. A green ● here
		// was the one place a stale remote row still claimed a live session.
		staleNote := ""
		if h.remotePollUnavailable(item.RemoteName) {
			statusIcon, statusStyle = "?", DimStyle
			staleNote = " · last known"
		} else if age, stale := h.remoteRowStale(item.RemoteName); stale {
			statusStyle = DimStyle
			staleNote = " · status " + formatRemoteAge(age) + " old"
		}
		indent := strings.Repeat("  ", max(0, item.Level-1))
		marker := "  "
		if selected {
			marker = "▶ "
		}
		embedded := h.embeddedRemoteSessionIs(item.RemoteName, rs.ID)
		if embedded {
			marker = "┃ "
		}
		// A one-line rail carries the tool marker inline, like local rows.
		rowLines := h.sidebarRowLines()
		toolPrefix := ""
		if rowLines == 1 && rs.Tool != "" {
			toolPrefix = sidebarToolIcon(rs.Tool) + " "
		}
		firstPlain := indent + marker + statusIcon + " " + toolPrefix + rs.Title
		first := indent + marker + statusStyle.Render(statusIcon) + " "
		if toolPrefix != "" {
			first += GetToolStyle(strings.ToLower(rs.Tool)).Bold(true).Render(sidebarToolIcon(rs.Tool)) + " "
		}
		first = fitCellWidth(first+rs.Title, max(1, listWidth))
		secondText := strings.TrimSpace(rs.Status)
		if rs.Archived {
			secondText = "archived"
		}
		if secondText == "" {
			secondText = "idle"
		}
		if rs.Tool != "" {
			secondText += " · " + strings.ToLower(rs.DisplayTool())
		}
		if !h.compactSidebar {
			secondText += " · " + item.RemoteName
		}
		secondText += staleNote
		second := fitCellWidth(indent+"  ╰ "+secondText, max(1, listWidth))
		if selected || embedded {
			style := lipgloss.NewStyle().Foreground(ColorText).Background(ColorSurface)
			first = style.Render(fitCellWidth(firstPlain, max(1, listWidth)))
			second = style.Foreground(ColorTextDim).Render(second)
		} else {
			first = lipgloss.NewStyle().Foreground(ColorText).Render(first)
			second = lipgloss.NewStyle().Foreground(ColorTextDim).Render(second)
		}
		b.WriteString(first)
		b.WriteString("\n")
		// Gate on the resolved density so this row is exactly as tall as
		// sidebarItemRenderHeightDensity measured it, tool marker or not.
		if rowLines > 1 {
			b.WriteString(second)
			b.WriteString("\n")
		}
		return
	}

	statusIcon, sStyle := remoteRowStatusGlyph(rs.Status, rs.Substate, rs.Archived)
	unavailable := h.remotePollUnavailable(item.RemoteName)
	if unavailable {
		statusIcon, sStyle = "?", DimStyle
	}
	titleStyle := lipgloss.NewStyle().Foreground(ColorText)
	if selected {
		sStyle = SessionStatusSelStyle
		titleStyle = SessionStatusSelStyle
	}

	titleStr := rs.Title
	titleStr = truncateDisplay(titleStr, 25, "...")

	toolStr := ""
	if rs.Tool != "" && !h.toolBadgeHidden(rs.Tool) {
		// #1091: use brand-specific color (claude=orange, gemini=purple, …)
		// so SSH-remote rows match local rows. Falls back to ColorTextDim
		// for unknown/empty tool names via GetToolStyle.
		tStyle := GetToolStyle(rs.Tool)
		if selected {
			tStyle = SessionStatusSelStyle
		}
		toolStr = tStyle.Render(" " + rs.DisplayTool())
	}

	treeConnector := "├─"
	if item.IsSubSession {
		// #2450: a conductor's child closes its parent's subtree, not the
		// group, so it reads its own last-child flag.
		if item.IsLastSubSession {
			treeConnector = "└─"
		}
	} else if item.IsLastInGroup {
		treeConnector = "└─"
	}

	// #1553: indent by the item's level so sessions sit one step below their
	// owning group header. A session directly under a Level-1 group renders at
	// Level 2 -> `strings.Repeat("  ", 2)` == 4 spaces.
	indent := strings.Repeat("  ", item.Level)
	if item.Level == 0 {
		indent = "  " // defensive: never dedent past the old flat baseline
	}

	pendingStr := ""
	if unavailable {
		pendingStr = " " + DimStyle.Render("· last known")
	} else if age, stale := h.remoteRowStale(item.RemoteName); stale {
		// #2331: a poll that answers slowly (the remote's own status pass
		// ran long under load) is not "unavailable" — it succeeded — but
		// its snapshot can be tens of seconds old by the time this row
		// paints, with nothing above to say so. remotePollUnavailable only
		// catches an outright failed/paused poll; this catches the row that
		// is quietly stale despite the poll having gone fine. The glyph is
		// dimmed with it: a full-colour ● next to "status 47s old" still
		// read as "running now" (status-detection audit 2026-09-23).
		pendingStr = " " + DimStyle.Render(fmt.Sprintf("· status %s old", formatRemoteAge(age)))
		if !selected {
			sStyle = DimStyle
		}
	}
	if item.RemoteSession != nil {
		if verb, ok := h.remotePending[item.RemoteSession.ID]; ok {
			pendingStr = " " + DimStyle.Render("· "+verb)
		}
	}

	// Viewers badge, as on local rows; nothing when the remote did not say.
	remoteViewers, remoteViewersKnown := rs.ViewerList()
	remoteViewersBadge := renderViewersBadge(remoteViewers, remoteViewersKnown, selected)

	b.WriteString(fmt.Sprintf("%s%s%s %s %s%s%s%s\n",
		remoteRowGutter(selected), // align with group/session hotkey gutter
		indent,
		DimStyle.Render(treeConnector),
		sStyle.Render(statusIcon),
		titleStyle.Render(titleStr),
		toolStr,
		remoteViewersBadge,
		pendingStr,
	))
}
