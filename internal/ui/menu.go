package ui

import (
	"strings"
)

// Menu shows bottom menu bar
type Menu struct {
	width int
}

// NewMenu creates a new menu
func NewMenu() *Menu {
	return &Menu{}
}

// SetWidth sets menu width
func (m *Menu) SetWidth(width int) {
	m.width = width
}

// View renders the menu
func (m *Menu) View() string {
	items := []string{
		MenuKey("↑↓", "Navigate"),
		MenuKey("Enter", "Attach"),
		MenuKey("/", "Search"),
		MenuKey("n", "New"),
		MenuKey("Tab", "Fold"),
		MenuKey("d", "Delete"),
		MenuKey("r", "Rename"),
		MenuKey("D", "Discover"),
		MenuKey("q", "Quit"),
	}

	content := strings.Join(items, "  ")

	style := MenuStyle.Width(m.width)
	return style.Render(content)
}
