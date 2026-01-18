package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// HelpOverlay shows keyboard shortcuts in a modal
type HelpOverlay struct {
	visible bool
	width   int
	height  int
}

// NewHelpOverlay creates a new help overlay
func NewHelpOverlay() *HelpOverlay {
	return &HelpOverlay{}
}

// Show makes the help overlay visible
func (h *HelpOverlay) Show() {
	h.visible = true
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

// Update handles messages for the help overlay
func (h *HelpOverlay) Update(msg tea.Msg) (*HelpOverlay, tea.Cmd) {
	if !h.visible {
		return h, nil
	}

	// Any key closes the help overlay
	if _, ok := msg.(tea.KeyMsg); ok {
		h.Hide()
	}
	return h, nil
}

// View renders the help overlay
func (h *HelpOverlay) View() string {
	if !h.visible {
		return ""
	}

	// Define help sections
	sections := []struct {
		title string
		items [][2]string // [key, description]
	}{
		{
			title: "NAVIGATION",
			items: [][2]string{
				{"j / Down", "Move down"},
				{"k / Up", "Move up"},
				{"Ctrl+u/d", "Half page up/down"},
				{"Ctrl+f/b", "Full page up/down"},
				{"gg / G", "Jump to top/bottom"},
				{"h / Left", "Collapse / parent"},
				{"l / Right", "Expand / toggle"},
				{"1-9", "Jump to group"},
				{"Enter", "Attach / toggle"},
			},
		},
		{
			title: "SESSIONS",
			items: [][2]string{
				{"n", "New session"},
				{"r", "Rename session"},
				{"Shift+R", "Restart session"},
				{"d", "Delete session"},
				{"m", "Move to group"},
				{"Shift+M", "MCP Manager (Claude)"},
				{"u", "Mark unread"},
				{"K / J", "Reorder up/down"},
				{"f", "Quick fork (Claude only)"},
				{"F", "Fork with options (Claude only)"},
			},
		},
		{
			title: "GROUPS",
			items: [][2]string{
				{"g", "New group"},
				{"e", "Rename group"},
				{"Tab", "Toggle expand"},
			},
		},
		{
			title: "SEARCH & FILTER",
			items: [][2]string{
				{"/", "Open search"},
				{"/waiting", "Filter waiting"},
				{"/running", "Filter running"},
				{"/idle", "Filter idle"},
			},
		},
		{
			title: "OTHER",
			items: [][2]string{
				{"S", "Settings"},
				{"Ctrl+R", "Reload from disk"},
				{"i", "Import tmux sessions"},
				{"Ctrl+Q", "Detach from session"},
				{"q", "Quit"},
				{"?", "This help"},
			},
		},
	}

	// Build content
	var content strings.Builder

	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorAccent).
		MarginBottom(1)

	sectionStyle := lipgloss.NewStyle().
		Foreground(ColorCyan).
		Bold(true)

	// Responsive dialog width
	dialogWidth := 48
	if h.width > 0 && h.width < dialogWidth+10 {
		dialogWidth = h.width - 10
		if dialogWidth < 35 {
			dialogWidth = 35
		}
	}
	keyWidth := 14
	if dialogWidth < 45 {
		keyWidth = 10 // Compact key column for small screens
	}

	keyStyle := lipgloss.NewStyle().
		Foreground(ColorPurple).
		Width(keyWidth)

	descStyle := lipgloss.NewStyle().
		Foreground(ColorText)

	content.WriteString(titleStyle.Render("KEYBOARD SHORTCUTS"))
	content.WriteString("\n\n")

	for i, section := range sections {
		content.WriteString(sectionStyle.Render(section.title))
		content.WriteString("\n")
		for _, item := range section.items {
			content.WriteString("  ")
			content.WriteString(keyStyle.Render(item[0]))
			content.WriteString(descStyle.Render(item[1]))
			content.WriteString("\n")
		}
		if i < len(sections)-1 {
			content.WriteString("\n")
		}
	}

	// Version info (subtle separator before footer)
	separatorStyle := lipgloss.NewStyle().Foreground(ColorBorder)
	versionStyle := lipgloss.NewStyle().
		Foreground(ColorComment).
		Italic(true)
	separatorWidth := dialogWidth - 8
	if separatorWidth < 20 {
		separatorWidth = 20
	}
	content.WriteString("\n")
	content.WriteString(separatorStyle.Render(strings.Repeat("─", separatorWidth)))
	content.WriteString("\n")
	content.WriteString(versionStyle.Render("Agent Deck v" + Version))
	content.WriteString("\n\n")

	footerStyle := lipgloss.NewStyle().
		Foreground(ColorComment).
		Italic(true)
	content.WriteString(footerStyle.Render("Press any key to close"))

	// Wrap in dialog box
	box := DialogBoxStyle.
		Width(dialogWidth).
		Render(content.String())

	return centerInScreen(box, h.width, h.height)
}
