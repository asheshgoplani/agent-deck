package ui

import (
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

type sidebarPresentation string

const (
	sidebarGrouped sidebarPresentation = "grouped"
	sidebarFlat    sidebarPresentation = "flat"

	embeddedCardMinWidth = 28
	// embeddedSessionRowLines is the tallest card: the identity line plus two
	// metadata lines. Densities resolve to this or fewer, never more.
	embeddedSessionRowLines = 3
)

func flattenSidebarItems(items []session.Item) []session.Item {
	flat := make([]session.Item, 0, len(items))
	for _, item := range items {
		switch item.Type {
		case session.ItemTypeGroup, session.ItemTypeRemoteGroup:
			continue
		case session.ItemTypeSession, session.ItemTypeWindow, session.ItemTypeRemoteSession:
			item.Level = 0
			item.IsLastInGroup = false
			item.ParentIsLastInGroup = false
		}
		flat = append(flat, item)
	}
	return flat
}

func flatSidebarItemsFromTree(tree *session.GroupTree) []session.Item {
	if tree == nil {
		return nil
	}
	items := make([]session.Item, 0, tree.SessionCount())
	for _, group := range tree.GroupList {
		for _, inst := range group.Sessions {
			items = append(items, session.Item{
				Type:         session.ItemTypeSession,
				Session:      inst,
				Path:         group.Path,
				Level:        0,
				IsSubSession: inst.IsSubSession(),
			})
		}
	}
	return items
}

// sidebarDensityRowLines maps a configured density to how many terminal lines
// one expanded session occupies. Everything that measures the sidebar — scroll
// windowing, click hit-testing, the renderer itself — reads this one number, so
// a density is described in exactly one place.
func sidebarDensityRowLines(density string) int {
	switch density {
	case session.SidebarDensityMinimal:
		return 1
	case session.SidebarDensityFull:
		return embeddedSessionRowLines
	default:
		return 2
	}
}

func sidebarItemRenderHeightDensity(item session.Item, sessionLines int) int {
	switch item.Type {
	case session.ItemTypeSession:
		if item.CreatingID != "" {
			return 1
		}
		return sessionLines
	case session.ItemTypeRemoteSession:
		// Remote rows carry one metadata line at most.
		return min(2, sessionLines)
	default:
		return 1
	}
}

func sidebarItemRenderHeightAtWidthDensity(item session.Item, width, sessionLines int) int {
	if width < embeddedCardMinWidth {
		return 1
	}
	return sidebarItemRenderHeightDensity(item, sessionLines)
}

func sidebarItemsHeightDensity(items []session.Item, start, end, width, sessionLines int) int {
	start = max(0, start)
	end = min(len(items), end)
	height := 0
	for i := start; i < end; i++ {
		height += sidebarItemRenderHeightAtWidthDensity(items[i], width, sessionLines)
	}
	return height
}

// sidebarAutoRowLinesFor resolves the "auto" density: the most height a row can
// have while the whole list still fits on screen at once. Scrolling a rail you
// could have read in one glance is the cost the extra metadata line was buying,
// so auto spends height only while there is height nobody is scrolling past.
//
// The choice reads the item list and the available height and nothing else —
// never the density it last returned — so a shrink cannot feed back into the
// measurement that caused it and oscillate.
func sidebarAutoRowLinesFor(items []session.Item, width, budget int) int {
	for lines := embeddedSessionRowLines; lines > 1; lines-- {
		if sidebarItemsHeightDensity(items, 0, len(items), width, lines) <= budget {
			return lines
		}
	}
	return 1
}

// sidebarAutoRowLines applies sidebarAutoRowLinesFor to the visible list.
// h.flatItems already omits the sessions inside collapsed groups, so opening a
// group is what makes the rail tighten and closing it is what gives the lines
// back.
func (h *Home) sidebarAutoRowLines() int {
	if h.height <= 0 {
		// No layout yet (a fresh Home, or a test that never sized it): there is
		// no budget to fit anything to, so fall back to the fixed default.
		return sidebarDensityRowLines(session.DefaultSidebarDensity)
	}
	budget, width := h.sidebarLineBudget()
	return sidebarAutoRowLinesFor(h.flatItems, width, budget)
}

// sidebarRowLines is how many lines this Home gives one expanded session.
func (h *Home) sidebarRowLines() int {
	if h.sidebarDensity == session.SidebarDensityAuto {
		return h.sidebarAutoRowLines()
	}
	return sidebarDensityRowLines(h.sidebarDensity)
}

func (h *Home) sidebarItemRenderHeightAtWidth(item session.Item, width int) int {
	return h.sidebarItemRenderHeightAtWidthLines(item, width, h.sidebarRowLines())
}

// sidebarItemRenderHeightAtWidthLines is sidebarItemRenderHeightAtWidth for a
// caller that walks many rows: resolve the density once (under "auto" that is
// a pass over the whole list) and hand it in, instead of once per row.
func (h *Home) sidebarItemRenderHeightAtWidthLines(item session.Item, width, rowLines int) int {
	if !h.embeddedLayout {
		return 1
	}
	return sidebarItemRenderHeightAtWidthDensity(item, width, rowLines)
}

func (h *Home) sidebarItemsHeight(items []session.Item, start, end, width int) int {
	return h.sidebarItemsHeightWithLines(items, start, end, width, h.sidebarRowLines())
}

// sidebarItemsHeightWithLines lets a caller that measures repeatedly (the
// viewport loops) resolve the density once instead of per call.
func (h *Home) sidebarItemsHeightWithLines(items []session.Item, start, end, width, rowLines int) int {
	if !h.embeddedLayout {
		start = max(0, start)
		end = min(len(items), end)
		return max(0, end-start)
	}
	return sidebarItemsHeightDensity(items, start, end, width, rowLines)
}

// pageStepItems converts a line budget into a number of rows for the vi-style
// page keys, walking from the cursor in dir (+1 down, -1 up) and charging each
// row its rendered height. In the classic layout every row is one line, so
// this is exactly the old lines-as-rows arithmetic; in the embedded layout a
// two- or three-line card counts for as many lines as it draws, so one Ctrl+F
// advances one visual page rather than two or three.
func (h *Home) pageStepItems(lines, dir int) int {
	lines = max(1, lines)
	if !h.embeddedLayout || dir == 0 {
		return lines
	}
	_, sidebarWidth := h.sidebarLineBudget()
	rowLines := h.sidebarRowLines()
	steps := 0
	for i := h.cursor + dir; i >= 0 && i < len(h.flatItems); i += dir {
		heightIndex := i
		if dir > 0 {
			heightIndex = i - dir
		}
		height := sidebarItemRenderHeightAtWidthDensity(h.flatItems[heightIndex], sidebarWidth, rowLines)
		if height > lines && steps > 0 {
			break
		}
		lines -= height
		// Charge dividers to the budget, but only commit selectable rows.
		// With no destination yet, allow the first selectable row to exceed
		// a tiny budget so paging still makes progress.
		if h.flatItems[i].Type != session.ItemTypeDivider {
			steps = (i - h.cursor) * dir
		}
		if lines <= 0 && steps > 0 {
			break
		}
	}
	return max(1, steps)
}

func (h *Home) compactEmbeddedSidebar() bool {
	return h.embeddedLayout && h.compactSidebar
}

func (h *Home) sidebarTitle() string {
	if !h.embeddedLayout {
		return "SESSIONS"
	}
	if h.sidebarMode == sidebarFlat {
		return "AGENTS  FLAT"
	}
	return "AGENTS  GROUPED"
}

func (h *Home) renderEmbeddedSessionItem(
	b *strings.Builder,
	item session.Item,
	selected bool,
	state sessionRenderState,
	listWidth int,
) {
	inst := item.Session
	if inst == nil {
		return
	}

	statusIcon, statusStyle := rowStatusGlyph(state.status, state.substate, inst.IsArchived())
	_, restarting := h.resumingSessions[inst.ID]
	if restarting {
		spinnerFrames := []string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"}
		statusIcon = spinnerFrames[h.animationFrame%len(spinnerFrames)]
		statusStyle = SessionStatusWaiting
	}

	statusText := string(state.status)
	if statusText == "" {
		statusText = string(session.StatusIdle)
	}
	tool := strings.ToLower(strings.TrimSpace(state.tool))
	if tool == "" {
		tool = strings.ToLower(strings.TrimSpace(inst.Tool))
	}
	toolIcon := ""
	if tool != "" {
		toolIcon = sidebarToolIcon(tool)
	}

	titleStyle := SessionTitleDefault
	switch state.status {
	case session.StatusRunning, session.StatusWaiting:
		titleStyle = SessionTitleActive
	case session.StatusError:
		titleStyle = SessionTitleError
	}
	if inst.Color != "" {
		titleStyle = titleStyle.Foreground(lipgloss.Color(inst.Color))
	}

	// Read the label from the snapshot, not Instance.Title: the snapshot exists
	// so View() never queues behind a mid-sweep UpdateStatus writer (#1753),
	// and it is where auto-named sessions get their task description promoted
	// in place of the generated handle, the same way the classic row does.
	displayTitle, _ := sessionDisplayLabelsFromState(state)
	label := strings.TrimSpace(displayTitle)
	if label == "" {
		label = inst.ID
	}
	if inst.Pin != session.PinNone {
		label = "📌 " + label
	}

	indent := strings.Repeat("  ", max(0, item.Level-1))
	gutter := strings.Repeat(" ", leftGutterWidth)
	if h.compactSidebar {
		gutter = ""
	}
	marker := "  "
	if selected {
		marker = "▶ "
	}
	embedded := h.embeddedSessionIs(inst.ID)
	if embedded {
		marker = "┃ "
	}
	chevron := ""
	if h.sessionHasWindows(item) {
		chevron = "▾ "
		if h.windowsCollapsed[inst.ID] {
			chevron = "▸ "
		}
	}
	rowLines := h.sidebarRowLines()

	// At one line per session the metadata lines are gone, and with them the
	// only place the sidebar says which agent this is. The tool marker moves
	// onto the identity line so Codex still reads differently from Claude —
	// that distinction is the whole reason a one-line rail stays usable.
	toolPrefix := ""
	if rowLines == 1 && toolIcon != "" {
		toolPrefix = toolIcon + " "
	}

	firstPlain := gutter + indent + marker + chevron + statusIcon + " " + toolPrefix + label
	first := gutter + indent + marker + chevron + statusStyle.Render(statusIcon) + " "
	if toolPrefix != "" {
		first += GetToolStyle(tool).Bold(true).Render(toolIcon) + " "
	}
	first += titleStyle.Render(label)

	metadataParts := make([]string, 0, 6)
	metadataParts = append(metadataParts, statusText)
	if tool != "" {
		metadataParts = append(metadataParts, tool)
	}
	if state.substate != "" {
		metadataParts = append(metadataParts, string(state.substate))
	}
	if restarting {
		metadataParts = append(metadataParts, "restarting…")
	}
	if !h.compactSidebar && state.paneTitle != "" && state.paneTitle != label {
		metadataParts = append(metadataParts, state.paneTitle)
	}

	metadataPrefix := gutter + indent + "  │ "
	metadataLastPrefix := gutter + indent + "  ╰ "
	metadataWidth := max(1, listWidth-cellWidth(metadataPrefix))
	metadataCount := max(0, rowLines-1)
	metadataLines := wrapSidebarMetadata(strings.Join(metadataParts, " · "), metadataWidth, metadataCount)
	for len(metadataLines) < metadataCount {
		metadataLines = append(metadataLines, "")
	}

	highlighted := selected || embedded
	first = fitCellWidth(first, max(1, listWidth))
	if highlighted {
		style := lipgloss.NewStyle().Foreground(ColorText).Background(ColorSurface)
		first = style.Render(fitCellWidth(firstPlain, max(1, listWidth)))
	} else {
		first = lipgloss.NewStyle().Foreground(ColorText).Render(first)
	}
	b.WriteString(first)
	b.WriteString("\n")

	// The last metadata line always carries the ╰ elbow so the card closes,
	// whether that is line 2 or line 3.
	for i := 0; i < metadataCount; i++ {
		prefix := metadataPrefix
		if i == metadataCount-1 {
			prefix = metadataLastPrefix
		}
		b.WriteString(renderSidebarMetadataLine(prefix, metadataLines[i], listWidth, highlighted))
		b.WriteString("\n")
	}
}

func wrapSidebarMetadata(text string, width, maxLines int) []string {
	text = strings.TrimSpace(text)
	if text == "" || width <= 0 || maxLines <= 0 {
		return nil
	}
	lines := strings.Split(ansi.Wrap(text, width, ""), "\n")
	if len(lines) <= maxLines {
		return lines
	}
	lines[maxLines-1] = cellTruncate(strings.Join(lines[maxLines-1:], " "), width, "…")
	return lines[:maxLines]
}

// sidebarToolIcon is the one-glyph tool marker the sidebar uses where a full
// tool name does not fit.
func sidebarToolIcon(tool string) string {
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "claude":
		return "✻"
	case "codex":
		return "⌬"
	case "pi":
		return "π"
	default:
		return session.GetToolIcon(tool)
	}
}

// renderSidebarMetadataLine renders one of a session's metadata lines.
func renderSidebarMetadataLine(prefix, text string, width int, highlighted bool) string {
	style := lipgloss.NewStyle().Foreground(ColorTextDim)
	if highlighted {
		style = style.Background(ColorSurface)
	}
	line := fitCellWidth(prefix+text, max(1, width))
	return style.Render(line)
}

func renderEmbeddedTerminal(title, status, tool, preview string, width, height, scrollOffset int) string {
	contentWidth := max(1, width-2)
	header := title
	if status != "" {
		header += "  " + status
	}
	header = lipgloss.NewStyle().Foreground(ColorAccent).Bold(true).Render(
		fitCellWidth(header, contentWidth),
	)
	meta := strings.TrimSpace(tool)
	if meta != "" {
		meta += "  ·  "
	}
	meta += "Ctrl+Q detach"
	meta = lipgloss.NewStyle().Foreground(ColorTextDim).Render(fitCellWidth(meta, contentWidth))

	lines := strings.Split(preview, "\n")
	for len(lines) > 0 && strings.TrimSpace(ansi.Strip(lines[len(lines)-1])) == "" {
		lines = lines[:len(lines)-1]
	}
	available := max(1, height-2)
	if len(lines) > available {
		maxOffset := len(lines) - available
		scrollOffset = min(max(0, scrollOffset), maxOffset)
		end := len(lines) - scrollOffset
		start := max(0, end-available)
		lines = lines[start:end]
	}
	for i, line := range lines {
		line = stripControlCharsPreserveANSI(line)
		line = stripDisplayErasingEscapes(line)
		if GetCurrentTheme() == ThemeLight {
			line = remapANSIBackground(line, previewSurfaceANSI())
		}
		if cellWidth(line) > contentWidth {
			line = cellTruncate(line, max(1, contentWidth-1), "…")
		}
		lines[i] = line
	}
	if len(lines) == 0 {
		lines = []string{lipgloss.NewStyle().Foreground(ColorTextDim).Render("Waiting for terminal output…")}
	}
	return strings.Join(append([]string{header, meta}, lines...), "\n")
}
