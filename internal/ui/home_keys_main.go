package ui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/asheshgoplani/agent-deck/internal/recall/query"
	"github.com/asheshgoplani/agent-deck/internal/recall/reader"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// collapseOrNavUp implements the "h"/"left" collapse-or-parent navigation:
// collapses an open group/session-windows, or moves the cursor to the parent
// group of the focused item. Issue #1470.
func (h *Home) collapseOrNavUp() {
	if h.cursor >= len(h.flatItems) {
		return
	}
	item := h.flatItems[h.cursor]
	collapsed := false
	if item.Type == session.ItemTypeGroup {
		groupPath := item.Path
		h.groupTree.CollapseGroup(groupPath)
		h.rebuildFlatItems()
		for i, fi := range h.flatItems {
			if fi.Type == session.ItemTypeGroup && fi.Path == groupPath {
				h.cursor = i
				break
			}
		}
		collapsed = true
	} else if item.Type == session.ItemTypeWindow {
		sid := item.WindowSessionID
		h.windowsCollapsed[sid] = true
		h.rebuildFlatItems()
		h.moveCursorToSession(sid)
	} else if item.Type == session.ItemTypeSession && h.sessionHasChildren(item) && !h.parentChildrenCollapsed[item.Session.ID] {
		// Fold the child list shut (#2631) — children are checked BEFORE
		// window sub-items, mirroring the toggle handler: the fold hides the
		// whole subtree. An already-folded parent falls through to the
		// windows/group-collapse branches below (collapse-or-parent).
		h.parentChildrenCollapsed[item.Session.ID] = true
		h.rebuildFlatItems()
		_ = h.saveUIStateErr()
	} else if item.Type == session.ItemTypeSession && h.sessionHasWindows(item) && !h.windowsCollapsed[item.Session.ID] {
		h.windowsCollapsed[item.Session.ID] = true
		h.rebuildFlatItems()
	} else if item.Type == session.ItemTypeSession {
		h.groupTree.CollapseGroup(item.Path)
		h.rebuildFlatItems()
		for i, fi := range h.flatItems {
			if fi.Type == session.ItemTypeGroup && fi.Path == item.Path {
				h.cursor = i
				break
			}
		}
		collapsed = true
	} else if item.Type == session.ItemTypeRemoteGroup {
		// Fold shut if open; if already shut, walk to the parent header so the
		// key keeps behaving like "collapse or go up" on the remote side too.
		if !h.setRemoteGroupCollapsed(item.RemoteName, item.Path, true) {
			if parent := remoteGroupParentPath(item.Path); parent != "" {
				h.moveCursorToRemoteGroup(item.RemoteName, parent)
			}
		}
	} else if item.Type == session.ItemTypeRemoteSession {
		// Item.Path is the owning group header's path.
		h.setRemoteGroupCollapsed(item.RemoteName, item.Path, true)
	}
	if collapsed {
		h.saveGroupState()
	}
}

func (h *Home) normalizeMainKey(pressed string) string {
	// Shift+Enter relay: csiuReader emits the Private-Use rune
	// shiftEnterMarker (U+E5E5) when it sees a Shift+Enter CSI u or
	// modifyOtherKeys sequence (issue #1093). Bubble Tea v1.3.10 has no
	// native shift+enter string, so we rewrite the rune to the canonical
	// label here, before any hotkey lookup, so the dispatch arm at
	// `case "shift+enter":` is reachable.
	if pressed == string(shiftEnterMarker) {
		pressed = "shift+enter"
	}
	if canonical, ok := h.hotkeyLookup[pressed]; ok {
		return canonical
	}
	if h.blockedHotkeys[pressed] {
		return ""
	}
	return pressed
}

func (h *Home) actionKey(action string) string {
	return actionHotkey(h.hotkeys, action)
}

// handleSearchKey handles keys when search is visible
func (h *Home) handleSearchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		selected := h.search.Selected()
		if selected != nil {
			// Unfold the selected child's parent first so the jump lands on a
			// visible row (#2631); the jump then expands groups and clears
			// filters as needed (#2655).
			h.revealParentFold(selected)
			if !h.jumpToSessionByID(selected.ID, selected.GroupPath) {
				h.setError(fmt.Errorf("'%s' not visible after clearing filters (session may be gone)", selected.Title))
			}
		}
		h.search.Hide()
		return h, nil
	case "esc":
		h.search.Hide()
		return h, nil
	}

	var cmd tea.Cmd
	h.search, cmd = h.search.Update(msg)

	// Check if user wants to switch to global search
	if h.search.WantsSwitchToGlobal() && h.globalSearch.HasSource() {
		h.globalSearch.SetSize(h.width, h.height)
		cmd = tea.Batch(cmd, h.globalSearch.Show())
	}

	return h, cmd
}

// jumpToSessionByID moves the cursor to the session with the given ID,
// expanding its group first. When the session is hidden by the active
// status or time filter (the search indexes every instance, not the
// filtered view), it clears those filters the same way the empty-view
// fallbacks do and retries; the filters are restored untouched only when
// the session is genuinely not present. Returns false in that last case.
// Issue #2655.
func (h *Home) jumpToSessionByID(id, groupPath string) bool {
	find := func() bool {
		for i, item := range h.flatItems {
			if item.Type == session.ItemTypeSession && item.Session != nil && item.Session.ID == id {
				h.cursor = i
				h.syncViewport()
				return true
			}
		}
		return false
	}

	// Capture the pre-jump fold state of the target group and its ancestors
	// so a failed jump restores them (#2656, CodeRabbit). Expansion runs
	// once here; the retry path below only rebuilds.
	prevExpanded := map[string]bool{}
	if groupPath != "" {
		for _, g := range h.groupTree.GroupList {
			if g == nil {
				continue
			}
			if g.Path == groupPath || strings.HasPrefix(groupPath, g.Path+"/") {
				prevExpanded[g.Path] = g.Expanded
			}
		}
		h.groupTree.ExpandGroupWithParents(groupPath)
	}
	restoreFolds := func() {
		for path, expanded := range prevExpanded {
			if !expanded {
				h.groupTree.CollapseGroup(path)
			}
		}
	}

	h.rebuildFlatItems()
	if find() {
		return true
	}
	if h.statusFilter == "" && h.timeFilter == session.TimeFilterAll {
		restoreFolds()
		h.rebuildFlatItems()
		return false
	}
	prevFilter, prevTime := h.statusFilter, h.timeFilter
	h.statusFilter = ""
	h.timeFilter = session.TimeFilterAll
	h.rebuildFlatItems()
	if find() {
		return true
	}
	h.statusFilter, h.timeFilter = prevFilter, prevTime
	restoreFolds()
	h.rebuildFlatItems()
	return false
}

// handleGlobalSearchKey handles keys when global search is visible
func (h *Home) handleGlobalSearchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		selected := h.globalSearch.Selected()
		if selected != nil {
			h.globalSearch.Hide()
			return h, h.handleGlobalSearchSelection(selected)
		}
		h.globalSearch.Hide()
		return h, nil
	case "esc":
		h.globalSearch.Hide()
		return h, nil
	}

	var cmd tea.Cmd
	h.globalSearch, cmd = h.globalSearch.Update(msg)

	// Check if user wants to switch to local search
	if h.globalSearch.WantsSwitchToLocal() {
		h.search.SetItems(h.instances)
		h.search.Show()
	}

	return h, cmd
}

// recallOffNotice is what G says when the index is not open because
// [recall] is disabled (an open error is quoted instead, see NewHome).
const recallOffNotice = "Recall is off ([recall] enabled = false in config.toml)"

// openGlobalSearch opens the Recall overlay when the index is available;
// otherwise the local title search, with a line inside it saying why
// (the footer is hidden behind the overlay for as long as it is open).
func (h *Home) openGlobalSearch() tea.Cmd {
	if !h.globalSearch.HasSource() {
		notice := h.recallOff
		if notice == "" {
			notice = recallOffNotice
		}
		h.search.SetNotice(notice + "; showing the local title search instead")
		h.search.Show()
		return nil
	}
	h.globalSearch.SetSize(h.width, h.height)
	return h.globalSearch.Show()
}

// handleGlobalSearchSelection opens a Recall hit the way `recall open`
// does: jump to the registered session that owns the conversation (the
// bound deck id, or the Claude session id an instance carries); otherwise
// register a new Claude session that resumes it. Other harnesses and
// subagent transcripts are not resumable from the index; the footer says
// how to read them.
func (h *Home) handleGlobalSearchSelection(result *GlobalSearchResult) tea.Cmd {
	h.instancesMu.RLock()
	for _, inst := range h.instances {
		if (result.DeckID != "" && inst.ID == result.DeckID) || (result.Harness == reader.HarnessClaude && inst.ClaudeSessionID == result.SessionID) {
			h.instancesMu.RUnlock()
			h.jumpToSession(inst)
			return nil
		}
	}
	h.instancesMu.RUnlock()
	switch {
	case result.Sidechain:
		h.setError(fmt.Errorf("a subagent transcript cannot be resumed; open its parent session (agent-deck recall show %s)", query.Ref(result.SessID)))
		return nil
	case result.Harness != reader.HarnessClaude:
		h.setError(fmt.Errorf("%s conversations are searchable but not resumable yet: agent-deck recall show %s", result.Harness, query.Ref(result.SessID)))
		return nil
	case result.Missing:
		h.setError(fmt.Errorf("the transcript file is gone; its text is still in the index: agent-deck recall show %s", query.Ref(result.SessID)))
		return nil
	}
	return h.createSessionFromGlobalSearch(result)
}

// jumpToSession jumps the cursor to the specified session
func (h *Home) jumpToSession(inst *session.Instance) {
	// Ensure the session's group is expanded
	if inst.GroupPath != "" {
		h.groupTree.ExpandGroupWithParents(inst.GroupPath)
	}
	h.revealParentFold(inst)
	h.rebuildFlatItems()

	// Find and select the session
	for i, item := range h.flatItems {
		if item.Type == session.ItemTypeSession && item.Session != nil && item.Session.ID == inst.ID {
			h.cursor = i
			h.syncViewport()
			break
		}
	}
}

func (h *Home) beginNotesEditing(inst *session.Instance) {
	if inst == nil {
		return
	}
	h.notesEditing = true
	h.notesEditingSessionID = inst.ID
	h.notesEditor.SetValue(inst.Notes)
	h.notesEditor.Focus()
}

func (h *Home) stopNotesEditing() {
	h.notesEditing = false
	h.notesEditingSessionID = ""
	h.notesEditor.Blur()
}

func (h *Home) handleNotesEditorKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	selected := h.getSelectedSession()
	if selected == nil || selected.ID != h.notesEditingSessionID {
		h.stopNotesEditing()
		return h, nil
	}

	switch msg.String() {
	case "esc":
		h.stopNotesEditing()
		return h, nil
	case "ctrl+s":
		notes := strings.TrimRight(h.notesEditor.Value(), "\n")
		h.instancesMu.RLock()
		inst := h.instanceByID[h.notesEditingSessionID]
		h.instancesMu.RUnlock()
		if inst != nil {
			inst.Notes = notes
			h.forceSaveInstances()
		}
		h.stopNotesEditing()
		return h, nil
	}

	var cmd tea.Cmd
	h.notesEditor, cmd = h.notesEditor.Update(msg)
	return h, cmd
}

// overlayJumpHint paints the hint in the row gutter.
func (h *Home) overlayJumpHint(line string, hint string, buffer string) string {
	return h.overlayJumpHintAtWidth(line, hint, buffer, h.width)
}

func (h *Home) overlayJumpHintAtWidth(line string, hint string, buffer string, width int) string {
	if hint == "" {
		return line
	}
	if cellWidth(line) < leftGutterWidth {
		return line
	}

	var hintRendered string
	for i, ch := range hint {
		s := string(ch)
		if i < len(buffer) {
			// Already typed: dimmed badge
			hintRendered += lipgloss.NewStyle().Foreground(ColorBg).Background(ColorComment).Bold(true).Render(s)
		} else {
			// Remaining: bright yellow badge
			hintRendered += lipgloss.NewStyle().Foreground(ColorBg).Background(ColorYellow).Bold(true).Render(s)
		}
	}

	if len(hint) > leftGutterWidth {
		return cellTruncate(hintRendered+" "+ansi.Cut(line, leftGutterWidth, cellWidth(line)), width, "")
	}
	return replaceVisibleRange(line, 0, leftGutterWidth, hintRendered+strings.Repeat(" ", leftGutterWidth-len(hint)))
}

// jumpItemName returns the display name for an item, used to locate hint badge position.
func jumpItemName(item session.Item) string {
	switch item.Type {
	case session.ItemTypeGroup:
		if item.Group != nil {
			return item.Group.Name
		}
	case session.ItemTypeSession:
		if item.Session != nil {
			return item.Session.Title
		}
	case session.ItemTypeWindow:
		return item.WindowName
	case session.ItemTypeRemoteGroup:
		return "remotes/" + item.RemoteName
	case session.ItemTypeRemoteSession:
		if item.RemoteSession != nil {
			return item.RemoteSession.Title
		}
	}
	return ""
}

func selectableItemIndices(items []session.Item) []int {
	indices := make([]int, 0, len(items))
	for i, item := range items {
		if item.Type != session.ItemTypeDivider {
			indices = append(indices, i)
		}
	}
	return indices
}

// handleJumpKey processes key input during jump mode.
func (h *Home) handleJumpKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	switch {
	case key == "esc":
		h.jumpMode = false
		h.jumpBuffer = ""
		return h, nil

	case len(key) == 1 && key[0] >= 'a' && key[0] <= 'z':
		h.jumpBuffer += key
		selectable := selectableItemIndices(h.flatItems)
		hints := generateJumpHints(len(selectable))
		result := matchJumpHint(hints, h.jumpBuffer)

		if result.matched {
			h.cursor = selectable[result.index]
			h.skipDivider(1) // never land on a non-selectable divider
			h.syncViewport()
			h.jumpMode = false
			h.jumpBuffer = ""
			// For sessions/windows/remotes: attach directly.
			// For groups: just move cursor (user can press Enter to toggle).
			item := h.flatItems[h.cursor]
			if item.Type != session.ItemTypeGroup && item.Type != session.ItemTypeRemoteGroup {
				return h.handleMainKey(tea.KeyMsg{Type: tea.KeyEnter})
			}
			return h, nil
		}
		if !result.isPrefix {
			h.jumpMode = false
			h.jumpBuffer = ""
		}
		return h, nil

	default:
		// Non-letter key — exit jump mode, pass key through
		h.jumpMode = false
		h.jumpBuffer = ""
		return h.handleMainKey(msg)
	}
}

// hasModalVisible returns true if any modal dialog or overlay is currently visible
func (h *Home) hasModalVisible() bool {
	return h.initialLoading || h.isQuitting || h.notesEditing || h.jumpMode ||
		h.setupWizard.IsVisible() || h.settingsPanel.IsVisible() ||
		(h.toolVisibilityPanel != nil && h.toolVisibilityPanel.IsVisible()) ||
		h.watcherPanel.IsVisible() || // hotkeyWatcherPanel overlay
		h.agentsPanel.IsVisible() || // hotkeyAgentsPanel overlay
		(h.deadLetterPanel != nil && h.deadLetterPanel.IsVisible()) || // hotkeyDeadLetters overlay
		h.triggersPanel.IsVisible() || // hotkeyTriggers overlay
		h.helpOverlay.IsVisible() || h.search.IsVisible() || h.globalSearch.IsVisible() ||
		h.newDialog.IsVisible() || h.groupDialog.IsVisible() || h.forkDialog.IsVisible() ||
		h.confirmDialog.IsVisible() || h.hookTrustDialog.IsVisible() || h.mcpDialog.IsVisible() || h.pluginDialog.IsVisible() || h.skillDialog.IsVisible() ||
		h.geminiModelDialog.IsVisible() || h.promptInputDialog.IsVisible() || h.sessionPickerDialog.IsVisible() ||
		h.codeBlockDialog.IsVisible() ||
		h.sessionSwitcher.IsVisible() || h.scrollbackPager.IsVisible() || h.contextPager.IsVisible() ||
		h.worktreeFinishDialog.IsVisible() || h.editPathsDialog.IsVisible() ||
		h.editSessionDialog.IsVisible() ||
		(h.telemetryDialog != nil && h.telemetryDialog.IsVisible()) ||
		(h.feedbackDialog != nil && h.feedbackDialog.IsVisible()) ||
		h.reportDialog.IsVisible() ||
		h.zoxidePicker.IsVisible()
}

// markNavigationAndFetchPreview sets navigation tracking state and returns a debounced preview command
func (h *Home) markNavigationAndFetchPreview() tea.Cmd {
	h.lastNavigationTime = time.Now()
	h.isNavigating = true
	return h.fetchSelectedPreview()
}

// clickedItemID returns a stable identifier for the item at the given flatItems index
func (h *Home) clickedItemID(index int) string {
	if index < 0 || index >= len(h.flatItems) {
		return ""
	}
	item := h.flatItems[index]
	if item.Type == session.ItemTypeSession && item.Session != nil {
		return "session:" + item.Session.ID
	}
	if item.Type == session.ItemTypeGroup {
		return "group:" + item.Path
	}
	return ""
}

// endDividerDrag releases the divider and persists whatever ratio it was left
// at. Every path out of a drag goes through here so a drag can never end
// without its result being saved.
func (h *Home) endDividerDrag() {
	if !h.draggingDivider {
		return
	}
	h.draggingDivider = false
	persistPreviewPct(h.getPreviewPct())
	h.saveUIState()
}

// handleDividerDrag processes mouse events for the Sessions/Preview divider
// resize drag and reports whether it consumed the event. The lifecycle is:
// left-press on the separator grabs it, motion resizes the split live, and
// release persists the new ratio. It keys off draggingDivider + msg.Action
// rather than msg.Button because X10 terminals report a drag release as
// MouseButtonNone, not MouseButtonLeft.
//
// The drag also has to survive a release it never sees. Under all-motion
// reporting a terminal that is moving the mouse at the instant the button comes
// up encodes that release with the motion bit set (SGR `<32;x;ym`), and Bubble
// Tea's parser refuses to promote a motion report to a release — so the event
// arrives as one more drag step and the divider keeps following a mouse nobody
// is holding. That is the "click and release stays dragging" bug. The next
// event gives the state away for free: a genuine drag reports motion with the
// left button still set, while an unheld cursor reports motion with
// MouseButtonNone. So a button-less motion ends the drag rather than continuing
// it, and the divider unsticks on the very next mouse move.
func (h *Home) handleDividerDrag(msg tea.MouseMsg) bool {
	if h.draggingDivider {
		switch msg.Action {
		case tea.MouseActionMotion:
			if msg.Button != tea.MouseButtonLeft || !h.dividerResizeEnabled() {
				// The button is not down any more (or the divider went inert
				// under us); either way the drag is over.
				h.endDividerDrag()
				return true
			}
			h.lastUserInputTime = time.Now()
			h.setPreviewPctFromMouseX(msg.X)
		case tea.MouseActionRelease:
			h.endDividerDrag()
		case tea.MouseActionPress:
			// A fresh press while still "dragging" means the previous release
			// never landed. Close out the old drag and let this press start a
			// clean one if it is on the divider.
			h.endDividerDrag()
			if msg.Button == tea.MouseButtonLeft && h.hoverDivider {
				h.draggingDivider = true
				h.lastUserInputTime = time.Now()
			}
		}
		return true
	}

	// Grab the divider only on a left-press over the separator, and only while
	// it is live. hoverDivider is already current and already carries that
	// condition: syncDividerHover ran for this event.
	if msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress && h.hoverDivider {
		h.draggingDivider = true
		h.lastUserInputTime = time.Now()
		return true
	}
	return false
}

// dividerResizeEnabled reports whether the Sessions/Preview divider accepts a
// resize right now. It requires the dual layout AND a detached dashboard.
//
// Attached is the disqualifier that matters. While a session is embedded, the
// input router forwards every mouse event that lands inside the session pane
// straight to tmux, so the dashboard sees the pointer arrive at the divider and
// then never hears about it leaving — the highlight and the grab cursor outlive
// the mouse by whatever the expiry window is. There is no event to fix that
// with, because the events are going somewhere else by design. Resizing the
// frame around a terminal you are typing into is also not what the mouse is for
// in that mode. So the divider is simply inert while attached: detach and it
// comes back.
func (h *Home) dividerResizeEnabled() bool {
	return h.getLayoutMode() == LayoutModeDual && !h.embeddedMode
}

// syncDividerHover recomputes whether the mouse is resting on the divider's
// grab zone. Hover is derived state with exactly one input — the last known
// mouse position — so it is recomputed from every mouse event rather than
// updated from some of them. Anything else strands the highlight: an event the
// drag handler never sees leaves the divider lit under a mouse that moved away.
func (h *Home) syncDividerHover(x int) {
	h.hoverDivider = !h.hasModalVisible() &&
		h.dividerResizeEnabled() &&
		h.isOnDivider(x)
	if h.hoverDivider {
		h.hoverDividerAt = time.Now()
	}
	h.syncPointerShape()
}

// expireDividerHover is the backstop for every way a mouse can stop reporting
// that we cannot observe directly. The one that actually bites: in embedded
// mode the input router forwards events inside the session pane straight to
// tmux, so moving the pointer off the divider and into the terminal next to it
// delivers nothing to the dashboard at all — the divider would stay lit under a
// mouse that is demonstrably somewhere else.
//
// Rather than enumerate those channels, hover simply expires if no mouse event
// has confirmed it recently. The window is deliberately generous: approaching
// the divider and pausing before you click must never lose the highlight, and
// losing it costs nothing anyway — the click re-establishes hover before the
// grab check, and any movement brings it straight back.
func (h *Home) expireDividerHover(now time.Time) {
	if h.hoverDivider && now.Sub(h.hoverDividerAt) > hoverDividerTTL {
		h.clearDividerHover()
	}
}

// clearDividerHover drops the highlight and hands the mouse cursor back. Hover
// is a claim about where the mouse is, and the moment the mouse stops reporting
// — the user went to the keyboard, the window lost focus, the layout moved
// under it — that claim has no evidence behind it and must not survive on
// screen. The next mouse move re-establishes it in a frame.
func (h *Home) clearDividerHover() {
	if !h.hoverDivider {
		// Still resync the pointer: a stale grab cursor outlives its highlight.
		h.syncPointerShape()
		return
	}
	h.hoverDivider = false
	h.syncPointerShape()
}

// syncPointerShape asks the terminal for a grab cursor over the divider, so the
// handle looks draggable before you find out by dragging it. Terminals without
// OSC 22 ignore it and fall back to the widened hit zone and the hover
// highlight, which need no terminal cooperation.
func (h *Home) syncPointerShape() {
	if h.sessionOutput == nil {
		return
	}
	switch {
	case h.draggingDivider:
		h.sessionOutput.SetPointerShape(pointerShapeGrabbing)
	case h.hoverDivider:
		h.sessionOutput.SetPointerShape(pointerShapeGrab)
	default:
		h.sessionOutput.SetPointerShape(pointerShapeDefault)
	}
}

// getListContentStartY returns the Y coordinate where list items begin rendering
func (h *Home) getListContentStartY() int {
	// Header: 1 line, Filter bar: 1 line
	startY := 2
	if h.shouldRenderUpdateBanner() {
		startY++ // Update banner
	}
	if h.maintenanceMsg != "" {
		startY++ // Maintenance banner
	}
	// Panel title: 2 lines (title + underline)
	startY += 2
	return startY
}

// mouseYToItemIndex maps a mouse Y coordinate to a flatItems index, or -1 if not on an item
func (h *Home) mouseYToItemIndex(y int) int {
	if len(h.flatItems) == 0 {
		return -1
	}

	lineInList := y - h.getListContentStartY()
	if lineInList < 0 {
		return -1
	}

	// Account for "more above" indicator
	if h.viewOffset > 0 {
		if lineInList == 0 {
			return -1 // Clicked on the "more above" indicator itself
		}
		lineInList-- // Shift down past the indicator
	}

	// Reject clicks beyond the visible list area (e.g. in the preview section of stacked layout)
	// When scrolled, the "more above" indicator takes 1 render line, reducing visible items by 1.
	maxVisible := h.getVisibleHeight()
	if h.viewOffset > 0 {
		maxVisible--
	}
	if lineInList >= maxVisible {
		return -1
	}

	sidebarWidth := h.width
	if h.getLayoutMode() == LayoutModeDual {
		sidebarWidth = h.sessionsPaneWidth()
	}
	rowLines := h.sidebarRowLines()
	for itemIndex := h.viewOffset; itemIndex < len(h.flatItems); itemIndex++ {
		rowHeight := h.sidebarItemRenderHeightAtWidthLines(h.flatItems[itemIndex], sidebarWidth, rowLines)
		if lineInList < rowHeight {
			return itemIndex
		}
		lineInList -= rowHeight
	}
	return -1
}

func (h *Home) changeStatusFilter(filter session.Status) tea.Cmd {
	selectedBefore := h.captureSelectedItemIdentity()
	h.keepEmptyFilter = true
	// % cycles All -> Open -> Open+stopped hidden -> All when the configured
	// exclude set keeps stopped sessions visible; otherwise it is a toggle.
	hideStopped := filter == FilterModeActive && h.statusFilter == FilterModeActive &&
		!h.activeFilterHideStopped && !h.activeFilterExcludes[session.StatusStopped]
	h.activeFilterHideStopped = hideStopped
	switch {
	case hideStopped:
		// stay on the active filter; the flag now hides stopped too
	case filter != "" && h.statusFilter == filter:
		h.statusFilter = ""
	default:
		h.statusFilter = filter
	}
	h.rebuildFlatItemsPreservingSelection(selectedBefore)
	return h.fetchSelectedPreview()
}
