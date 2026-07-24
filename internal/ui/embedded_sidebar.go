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

	embeddedCardMinWidth    = 28
	embeddedSessionRowLines = 2
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

func sidebarItemRenderHeight(item session.Item) int {
	switch item.Type {
	case session.ItemTypeSession, session.ItemTypeRemoteSession:
		if item.CreatingID != "" {
			return 1
		}
		return embeddedSessionRowLines
	default:
		return 1
	}
}

func sidebarItemRenderHeightAtWidth(item session.Item, width int) int {
	if width < embeddedCardMinWidth {
		return 1
	}
	return sidebarItemRenderHeight(item)
}

func sidebarItemsHeight(items []session.Item, start, end, width int) int {
	start = max(0, start)
	end = min(len(items), end)
	height := 0
	for i := start; i < end; i++ {
		height += sidebarItemRenderHeightAtWidth(items[i], width)
	}
	return height
}

func (h *Home) sidebarItemRenderHeightAtWidth(item session.Item, width int) int {
	if !h.embeddedLayout {
		return 1
	}
	return sidebarItemRenderHeightAtWidth(item, width)
}

func (h *Home) sidebarItemsHeight(items []session.Item, start, end, width int) int {
	if !h.embeddedLayout {
		start = max(0, start)
		end = min(len(items), end)
		return max(0, end-start)
	}
	return sidebarItemsHeight(items, start, end, width)
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

	statusText := string(state.status)
	if statusText == "" {
		statusText = string(session.StatusIdle)
	}
	tool := strings.ToLower(strings.TrimSpace(state.tool))
	if tool == "" {
		tool = strings.ToLower(strings.TrimSpace(inst.Tool))
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

	label := strings.TrimSpace(inst.Title)
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

	firstPlain := gutter + indent + marker + chevron + statusIcon + " " + label
	first := gutter + indent + marker + chevron + statusStyle.Render(statusIcon) + " " + titleStyle.Render(label)
	second := gutter + indent + "    " + statusText
	if tool != "" {
		second += " · " + tool
	}
	if state.substate != "" {
		second += " · " + string(state.substate)
	}
	if !h.compactSidebar && state.paneTitle != "" && state.paneTitle != label {
		second += " · " + state.paneTitle
	}

	first = fitCellWidth(first, max(1, listWidth))
	second = fitCellWidth(second, max(1, listWidth))
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
	b.WriteString(second)
	b.WriteString("\n")
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
