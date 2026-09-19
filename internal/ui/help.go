package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// wrapWithHangingIndent wraps text to fit within width, indenting continuation
// lines with the given indent string so wrapped descriptions stay aligned under
// their column instead of bleeding back to column 0.
//
// width is the visible character budget for each line (excluding the indent on
// continuation lines). If width <= 0 the text is returned unchanged.
func wrapWithHangingIndent(text string, width int, indent string) string {
	if text == "" || width <= 0 {
		return text
	}
	lines := wrapLines(text, width)
	for i := 1; i < len(lines); i++ {
		lines[i] = indent + lines[i]
	}
	return strings.Join(lines, "\n")
}

// wrapLines wraps text at word boundaries and returns each visual row as its
// own slice element, so callers that build a scrollable []string of real
// screen rows count and align wrapped content correctly.
func wrapLines(text string, width int) []string {
	words := strings.Fields(text)
	if width <= 0 || len(words) == 0 {
		return []string{text}
	}
	var lines []string
	current := words[0]
	for _, w := range words[1:] {
		if len(current)+1+len(w) <= width {
			current += " " + w
			continue
		}
		lines = append(lines, current)
		current = w
	}
	return append(lines, current)
}

// renderHelpRow renders one key/description help entry as a set of fully
// styled, individually countable screen rows.
//
// The two columns are wrapped independently and then paired row by row, so a
// three-alternative key label (e.g. "+ / K / Shift+↑") wraps into an aligned
// column instead of being hard wrapped by keyStyle's fixed Width(). The
// literal " " between the columns guarantees a separator even when the key
// label exactly fills keyWidth (e.g. "--group <name>").
func renderHelpRow(key, desc string, keyWidth, descWidth int, keyStyle, descStyle lipgloss.Style) []string {
	keyLines := wrapLines(key, keyWidth)
	descLines := wrapLines(desc, descWidth)

	rowCount := max(len(keyLines), len(descLines))
	rows := make([]string, 0, rowCount)
	for i := 0; i < rowCount; i++ {
		k := ""
		if i < len(keyLines) {
			k = keyLines[i]
		}
		if i >= len(descLines) || descLines[i] == "" {
			// No description cell: don't leave the column gap dangling.
			rows = append(rows, strings.TrimRight("  "+keyStyle.Render(k), " "))
			continue
		}
		rows = append(rows, "  "+keyStyle.Render(k)+" "+descStyle.Render(descLines[i]))
	}
	return rows
}

// HelpOverlay shows keyboard shortcuts in a modal
type HelpOverlay struct {
	visible      bool
	width        int
	height       int
	scrollOffset int // Current scroll position for small screens
	hotkeys      map[string]string
	// hasAgents gates the Agents row. The feature is opt-in by presence, and
	// this overlay is part of the TUI: a user who has adopted nothing must not
	// find a key here for a surface that does not exist for them.
	hasAgents bool

	// Runtime shortcuts follow the layout selected at startup. A saved
	// opposite preference only takes effect after restart.
	embeddedLayout bool
}

// SetHasAgents records whether anything has been adopted.
func (h *HelpOverlay) SetHasAgents(has bool) {
	if h == nil {
		return
	}
	h.hasAgents = has
}

// SetEmbeddedLayout records the layout used by the running dashboard.
func (h *HelpOverlay) SetEmbeddedLayout(enabled bool) {
	h.embeddedLayout = enabled
}

// NewHelpOverlay creates a new help overlay
func NewHelpOverlay() *HelpOverlay {
	return &HelpOverlay{hotkeys: resolveHotkeys(nil)}
}

// Show makes the help overlay visible
func (h *HelpOverlay) Show() {
	h.visible = true
	h.scrollOffset = 0
}

// Hide hides the help overlay
func (h *HelpOverlay) Hide() {
	h.visible = false
}

// IsVisible returns whether the help overlay is visible
func (h *HelpOverlay) IsVisible() bool {
	return h.visible
}

// SetSize sets the dimensions for centering
func (h *HelpOverlay) SetSize(width, height int) {
	h.width = width
	h.height = height
}

// SetHotkeys updates displayed hotkeys for dynamic help rendering.
func (h *HelpOverlay) SetHotkeys(bindings map[string]string) {
	h.hotkeys = make(map[string]string, len(bindings))
	for action, key := range bindings {
		h.hotkeys[action] = key
	}
}

func (h *HelpOverlay) key(action, fallback string) string {
	if h.hotkeys == nil {
		return fallback
	}
	if key, ok := h.hotkeys[action]; ok {
		trimmed := strings.TrimSpace(key)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func (h *HelpOverlay) keyPair(a, b, fallback string) string {
	if h.hotkeys == nil {
		return fallback
	}
	joined := joinHotkeyLabels(actionHotkey(h.hotkeys, a), actionHotkey(h.hotkeys, b))
	if joined != "" {
		return joined
	}
	return ""
}

// Update handles messages for the help overlay
func (h *HelpOverlay) Update(msg tea.Msg) (*HelpOverlay, tea.Cmd) {
	if !h.visible {
		return h, nil
	}

	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "j", "down":
			h.scrollOffset++
			return h, nil
		case "k", "up":
			if h.scrollOffset > 0 {
				h.scrollOffset--
			}
			return h, nil
		case "ctrl+d", "pgdown":
			h.scrollOffset += 10
			return h, nil
		case "ctrl+u", "pgup":
			if h.scrollOffset > 10 {
				h.scrollOffset -= 10
			} else {
				h.scrollOffset = 0
			}
			return h, nil
		case "home":
			h.scrollOffset = 0
			return h, nil
		case "end":
			h.scrollOffset = 9999 // Will be clamped in View()
			return h, nil
		case "g":
			h.scrollOffset = 0
			return h, nil
		case "G":
			h.scrollOffset = 9999 // Will be clamped in View()
			return h, nil
		default:
			// Any other key closes the help overlay
			h.Hide()
		}
	}
	return h, nil
}

// View renders the help overlay
func (h *HelpOverlay) View() string {
	if !h.visible {
		return ""
	}

	// Define help sections
	newKeys := h.keyPair(hotkeyNewSession, hotkeyQuickCreate, "n/N")
	forkKeys := h.keyPair(hotkeyQuickFork, hotkeyForkWithOptions, "f/F")
	reorderUpKeys := "+ / K / Shift+↑"
	reorderDownKeys := "- / J / Shift+↓"
	indentKeys := "Shift+→/←"
	pinKeys := ","
	searchKey := h.key(hotkeySearch, "/")
	settingsKey := h.key(hotkeySettings, "S")
	helpKey := h.key(hotkeyHelp, "?")
	quitKey := h.key(hotkeyQuit, "q")
	importKey := h.key(hotkeyImport, "i")
	reloadKey := h.key(hotkeyReload, "Ctrl+R")
	restartDeckKey := h.key(hotkeyRestartDeck, "Ctrl+T")
	installUpdateKey := h.key(hotkeyInstallUpdate, "Ctrl+Y")
	deleteKey := h.key(hotkeyDelete, "d")
	closeKey := h.key(hotkeyCloseSession, "D")
	restartKey := h.key(hotkeyRestart, "Shift+R")
	restartFreshKey := h.key(hotkeyRestartFresh, "Shift+T")
	renameKey := h.key(hotkeyRename, "r")
	moveKey := h.key(hotkeyMoveToGroup, "M")
	mcpKey := h.key(hotkeyMCPManager, "m")
	pluginKey := h.key(hotkeyPluginManager, "L")
	skillsKey := h.key(hotkeySkillsManager, "s")
	previewKey := h.key(hotkeyTogglePreview, "v")
	groupViewKey := h.key(hotkeyCycleGroupView, "t")
	timeFilterKey := h.key(hotkeyCycleTimeFilter, "*")
	// Opt-in: empty when switch_session is unbound, so the filter drops the row.
	switchKey := h.key(hotkeySwitchSession, "")
	// In-attach scrollback pager (#1491). Its trigger is resolved directly (it is
	// not a home-screen key); empty label when disabled drops the row.
	scrollbackKey := ResolvedScrollbackTrigger(session.GetHotkeyOverrides()).Label()
	unreadKey := h.key(hotkeyMarkUnread, "u")
	quickApproveKey := h.key(hotkeyQuickApprove, "a")
	promptSessionKey := h.key(hotkeyPromptSession, "o")
	copyKey := h.key(hotkeyCopyOutput, "c")
	copyPaneKey := h.key(hotkeyCopyPane, "V")
	yoloKey := h.key(hotkeyToggleYolo, "y")
	copyInfoKey := h.key(hotkeyCopyInfo, "B")
	contextInspectorKey := h.key(hotkeyContextInspector, "C")
	sendKey := h.key(hotkeySendOutput, "x")
	execShellKey := h.key(hotkeyExecShell, "E")
	openShellHereKey := h.key(hotkeyOpenShellHere, "h")
	notesKey := h.key(hotkeyEditNotes, "e")
	if cfg, _ := session.LoadUserConfig(); cfg != nil && !cfg.GetShowNotes() {
		notesKey = ""
	}
	editPathsKey := h.key(hotkeyEditPaths, "p")
	editSessionKey := h.key(hotkeyEditSession, "P")
	worktreeSetupKey := h.key(hotkeyWorktreeSetup, "b")
	worktreeKey := h.key(hotkeyWorktreeFinish, "W")
	watcherPanelKey := h.key(hotkeyWatcherPanel, "w")
	deadLettersKey := h.key(hotkeyDeadLetters, "alt+d")
	agentsPanelKey := h.key(hotkeyAgentsPanel, "alt+a")
	groupKey := h.key(hotkeyCreateGroup, "g")
	undoKey := h.key(hotkeyUndoDelete, "Ctrl+Z")
	archiveKey := h.key(hotkeyArchiveSession, "A")
	unarchiveKey := h.key(hotkeyUnarchiveSession, "Shift+U")
	viewArchivedKey := h.key(hotkeyViewArchived, "^")
	detachKey := DetachByteLabel(DetachByteFromBinding(h.key(hotkeyDetach, "ctrl+q")))
	altSessionKey := h.key(hotkeyAltSession, "`")
	mruBackKey := h.key(hotkeyMRUBack, "alt+left")
	mruForwardKey := h.key(hotkeyMRUForward, "alt+right")
	navigationItems := [][2]string{
		{"j / Down", "Move down"},
		{"k / Up", "Move up"},
		{"Ctrl+u/d", "Half page up/down"},
		{"PgUp / PgDn", "Half page up/down"},
		{"Ctrl+f/b", "Full page up/down"},
		{"Home / End", "Jump to first / last item"},
		{"G", "Global search"},
		{"h / Left", "Collapse / parent"},
		{"l / Right", "Expand / toggle"},
		{"1-9", "Jump to root group"},
		{"Space", "Jump mode"},
		{altSessionKey, "Alternate session (swap with the previous one, vim-style)"},
		{mruBackKey, "Walk back through recently used sessions"},
		{mruForwardKey, "Walk forward through recently used sessions"},
	}
	quickStartEnter := "Attach to selected session"
	if h.embeddedLayout {
		quickStartEnter = "Focus embedded terminal for selected session"
		navigationItems = append(navigationItems,
			[2]string{"Enter", "Focus embedded terminal / toggle group"},
			[2]string{"Alt+Enter", "Full-screen attach"},
			[2]string{"Shift+Enter", "Open session in new iTerm window (macOS)"},
			[2]string{"Alt+V", "Toggle grouped / flat agent sidebar"},
		)
	} else {
		navigationItems = append(navigationItems,
			[2]string{"Enter", "Attach to session / toggle group"},
			[2]string{"Shift+Enter", "Open session in new iTerm window (macOS)"},
		)
	}

	sections := []struct {
		title string
		items [][2]string // [key, description]
	}{
		{
			title: "QUICK START",
			items: [][2]string{
				{"Enter", quickStartEnter},
				{restartKey, "Restart selected session"},
				{detachKey, "Detach from session"},
				{helpKey, "Open this help"},
			},
		},
		{
			title: "NAVIGATION",
			items: navigationItems,
		},
		{
			title: "GROUP NAVIGATION (v1.7.60)",
			items: [][2]string{
				{"Alt+j / Alt+k", "Next / prev session in group"},
				{"Alt+1 - Alt+9", "Jump to Nth session in group"},
				{"Alt+g / Alt+G", "First / last in group"},
				{"Alt+/", "Filter search in group"},
			},
		},
		{
			title: "SESSIONS",
			items: [][2]string{
				{newKeys, "New / quick create"},
				{renameKey, "Rename session"},
				{restartKey, "Restart session"},
				{restartFreshKey, "Restart with new session ID"},
				{deleteKey, "Delete session"},
				{closeKey, "Close session process"},
				{undoKey, "Undo delete"},
				{archiveKey, "Archive session"},
				{unarchiveKey, "Unarchive session"},
				{viewArchivedKey, "Toggle archived view"},
				{moveKey, "Move to group"},
				{mcpKey, "MCP Manager (Claude/Gemini/Cursor)"},
				{pluginKey, "Plugin Manager (Claude — RFC PLUGIN_ATTACH.md)"},
				{skillsKey, "Skills Manager"},
				{CostDashboardKey, "Cost Dashboard"},
				{previewKey, "Toggle preview mode (output/stats/both)"},
				{"O", "Toggle preview orientation (right / below — portrait monitors)"},
				{"< / >", "Shrink / grow preview pane by 5% (drag divider with mouse; vertical in below-orientation)"},
				{unreadKey, "Mark unread"},
				{quickApproveKey, "Quick approve (send '1' to Claude)"},
				{promptSessionKey, "Prompt session (send a one-line prompt without attaching)"},
				{reorderUpKeys, "Reorder up (auto-promote at edge)"},
				{reorderDownKeys, "Reorder down (auto-promote at edge)"},
				{indentKeys, "Indent / outdent (in group)"},
				{pinKeys, "Pin (cycle off→top→bottom→off)"},
				{forkKeys, "Fork session (Claude/Pi)"},
				{yoloKey, "Toggle YOLO mode"},
				{contextInspectorKey, "Inspect full context (everything the agent is being sent, ranked by cost)"},
				{sendKey, "Send output to session"},
				{execShellKey, "Exec shell in sandbox container"},
				{openShellHereKey, "Open shell in session's worktree (split pane / window)"},
				{editPathsKey, "Edit multi-repo paths"},
				{editSessionKey, "Edit session settings (title/color/...)"},
				{notesKey, "Edit notes"},
			},
		},
		{
			// The copy family used to sit mid-way down the 30-row SESSIONS list,
			// where it went unfound: the recurring user question was "why can't I
			// select text?" rather than "which key copies?". Its own section, with
			// the Shift+drag row, answers that question where people look for it.
			title: "COPY & TEXT SELECTION",
			items: [][2]string{
				{copyKey, "Copy last AI response"},
				{copyInfoKey, "Copy session info (repo / path / branch)"},
				{copyPaneKey, "Copy visible terminal text, including links"},
				{"Y", "Copy a fenced code block (picker if several)"},
				// Not a binding: agent-deck holds the terminal in mouse mode 1002
				// (tea.WithMouseCellMotion) so drags arrive as events and the
				// terminal never renders a selection. Shift/Option bypasses that.
				{"Shift+drag", "Native terminal selection (Option+drag in iTerm2)"},
			},
		},
		{
			title: "WORKTREES",
			items: [][2]string{
				{worktreeSetupKey, "Re-run worktree setup script"},
				{worktreeKey, "Finish worktree (merge + cleanup)"},
				{"n → w", "Create session in worktree"},
				{"F → w", "Fork session into worktree"},
			},
		},
		{
			title: "WATCHERS",
			items: [][2]string{
				{watcherPanelKey, "Watcher panel"},
				{deadLettersKey, "Dead-letter events"},
			},
		},
		{
			title: "GROUPS",
			items: [][2]string{
				{groupKey, "New group"},
				{renameKey, "Rename group"},
				{"Tab", "Toggle expand"},
			},
		},
		{
			title: "SEARCH & FILTER",
			items: [][2]string{
				{searchKey, "Open search"},
				{FilterKeyError, "Filter errors"},
				{FilterKeyActive, "Filter open (hide errors)"},
				{"/waiting", "Filter waiting"},
				{"/running", "Filter running"},
				{"/idle", "Filter idle"},
				{groupViewKey, "Cycle view: active-on-top / populated-on-top"},
				{timeFilterKey, "Cycle time filter: today / 3 days / 7 days / all"},
			},
		},
		{
			title: "OTHER",
			items: [][2]string{
				{settingsKey, "Settings"},
				{reloadKey, "Reload from disk"},
				{installUpdateKey, "Install the available update (runs agent-deck update)"},
				{restartDeckKey, "Restart agent-deck in place (picks up an installed update)"},
				{importKey, "Import tmux sessions"},
				{switchKey, "Switch session (here or attached)"},
				{scrollbackKey, "Scrollback pager (while attached)"},
				{quitKey, "Quit"},
				{helpKey, "This help"},
			},
		},
		{
			title: "STARTUP FLAGS",
			items: [][2]string{
				{"--group <name>", "Launch scoped to a group"},
				{"--profile <name>", "Use specific profile"},
			},
		},
	}

	// The Agents row appears only once something has been adopted. The whole
	// feature is opt-in by presence, and the help overlay is part of the TUI:
	// a user with no agents must not find a key here for a surface that does
	// not exist for them.
	if h.hasAgents {
		for i := range sections {
			if sections[i].title == "WATCHERS" {
				sections[i].items = append(sections[i].items, [2]string{agentsPanelKey, "Agents"})
				break
			}
		}
	}

	for i := range sections {
		filtered := sections[i].items[:0]
		for _, item := range sections[i].items {
			if strings.TrimSpace(item[0]) == "" {
				continue
			}
			filtered = append(filtered, item)
		}
		sections[i].items = filtered
	}

	// Styles
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorAccent)

	sectionStyle := lipgloss.NewStyle().
		Foreground(ColorCyan).
		Bold(true)

	// Responsive dialog width: prefer wider so descriptions don't wrap
	// awkwardly. Default 70, scale up to ~80 when the terminal is roomy; the
	// shared helper handles shrinking (and the never-overflow clamp) on narrow
	// terminals.
	preferred := 70
	if h.width >= 100 {
		preferred = 80
	}
	dialogWidth := fitDialogWidth(preferred, 35, h.width)
	keyWidth := 14
	if dialogWidth < 45 {
		keyWidth = 10 // Compact key column for small screens
	}
	// Description column budget: dialogWidth minus border (2) + padding (4)
	// + leading "  " (2) + key column + the 1-space key/description gap
	// renderHelpRow always emits.
	descWidth := dialogWidth - 2 - 4 - 2 - keyWidth - 1
	if descWidth < 10 {
		descWidth = 10
	}

	keyStyle := lipgloss.NewStyle().
		Foreground(ColorPurple).
		Width(keyWidth)

	descStyle := lipgloss.NewStyle().
		Foreground(ColorText)

	separatorStyle := lipgloss.NewStyle().Foreground(ColorBorder)
	versionStyle := lipgloss.NewStyle().
		Foreground(ColorComment).
		Italic(true)
	footerStyle := lipgloss.NewStyle().
		Foreground(ColorComment).
		Italic(true)
	scrollIndicatorStyle := lipgloss.NewStyle().
		Foreground(ColorYellow).
		Bold(true)

	// Build content as lines for scrolling support
	var lines []string

	lines = append(lines, titleStyle.Render("KEYBOARD SHORTCUTS"))
	lines = append(lines, "")

	for i, section := range sections {
		lines = append(lines, sectionStyle.Render(section.title))
		for _, item := range section.items {
			lines = append(lines, renderHelpRow(item[0], item[1], keyWidth, descWidth, keyStyle, descStyle)...)
		}
		if i < len(sections)-1 {
			lines = append(lines, "")
		}
	}

	// Version info
	separatorWidth := dialogWidth - 8
	if separatorWidth < 20 {
		separatorWidth = 20
	}
	lines = append(lines, "")
	lines = append(lines, separatorStyle.Render(strings.Repeat("─", separatorWidth)))
	lines = append(lines, versionStyle.Render("Agent Deck v"+Version))

	totalLines := len(lines)

	// Calculate available height for content (screen height minus dialog borders, padding, footer)
	// Dialog box has 2 lines for border (top/bottom) + 1 padding each side + 2 for footer area
	availableHeight := h.height - 8
	if availableHeight < 10 {
		availableHeight = 10
	}

	// Check if scrolling is needed
	needsScroll := totalLines > availableHeight

	// When scrolling, reserve one line each for the top and bottom indicator
	// slots unconditionally — even on a page where one stays blank — so the
	// clamp below and the render below share one content budget. Reserving
	// them only when shown made maxScroll larger than any page could
	// actually render, so the last page was unreachable and "▼ more below"
	// stayed lit forever.
	contentHeight := availableHeight
	if needsScroll {
		contentHeight = max(availableHeight-2, 1)
	}

	// Clamp scroll offset
	maxScroll := totalLines - contentHeight
	if maxScroll < 0 {
		maxScroll = 0
	}
	if h.scrollOffset > maxScroll {
		h.scrollOffset = maxScroll
	}
	if h.scrollOffset < 0 {
		h.scrollOffset = 0
	}

	// Build visible content as discrete rows, then join once — no ad hoc
	// newline bookkeeping that could leave a stray blank line before the
	// footer.
	visibleRows := lines
	if needsScroll {
		// The top indicator always occupies a row, blank when at the top, so
		// the content budget stays fixed across every scroll position.
		topIndicator := ""
		if h.scrollOffset > 0 {
			topIndicator = scrollIndicatorStyle.Render("▲ more above")
		}
		endIdx := min(h.scrollOffset+contentHeight, totalLines)

		visibleRows = append([]string{topIndicator}, lines[h.scrollOffset:endIdx]...)
		if endIdx < totalLines {
			visibleRows = append(visibleRows, scrollIndicatorStyle.Render("▼ more below"))
		}
	}

	var content strings.Builder
	content.WriteString(strings.Join(visibleRows, "\n"))

	// Footer with appropriate hint
	content.WriteString("\n\n")
	if needsScroll {
		content.WriteString(footerStyle.Render("j/k scroll • any other key to close"))
	} else {
		content.WriteString(footerStyle.Render("Press any key to close"))
	}

	// Wrap in dialog box
	box := DialogBoxStyle.
		Width(dialogWidth).
		Render(content.String())

	return centerInScreen(box, h.width, h.height)
}
