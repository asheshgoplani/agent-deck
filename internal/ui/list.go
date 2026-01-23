package ui

import (
	"fmt"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// List manages session list display
type List struct {
	items    []*session.Instance
	cursor   int
	width    int
	height   int
	tree     *Tree
	viewMode string // "flat" or "tree"
}

// NewList creates a new list
func NewList() *List {
	return &List{
		items:    []*session.Instance{},
		cursor:   0,
		tree:     NewTree(),
		viewMode: "tree",
	}
}

// SetItems sets the list items
func (l *List) SetItems(items []*session.Instance) {
	l.items = items
	if l.cursor >= len(items) && len(items) > 0 {
		l.cursor = len(items) - 1
	}

	// Update tree
	l.tree.Clear()
	groups := session.GroupByProject(items)
	for name, group := range groups {
		l.tree.AddFolder(name)
		l.tree.SetFolderCount(name, len(group))
	}
}

// Len returns number of items
func (l *List) Len() int {
	return len(l.items)
}

// Cursor returns current cursor position
func (l *List) Cursor() int {
	return l.cursor
}

// Selected returns the currently selected item
func (l *List) Selected() *session.Instance {
	if len(l.items) == 0 || l.cursor >= len(l.items) {
		return nil
	}
	return l.items[l.cursor]
}

// MoveUp moves cursor up
func (l *List) MoveUp() {
	if l.cursor > 0 {
		l.cursor--
	}
}

// MoveDown moves cursor down
func (l *List) MoveDown() {
	if l.cursor < len(l.items)-1 {
		l.cursor++
	}
}

// SetSize sets the list dimensions
func (l *List) SetSize(width, height int) {
	l.width = width
	l.height = height
}

// ToggleFolder toggles the current folder
func (l *List) ToggleFolder(name string) {
	l.tree.ToggleFolder(name)
}

// View renders the list
func (l *List) View() string {
	var b strings.Builder

	// Group by project
	groups := session.GroupByProject(l.items)

	itemIndex := 0
	for _, folderName := range l.tree.GetFolders() {
		folder := l.tree.GetFolder(folderName)
		if folder == nil {
			continue
		}

		// Folder header
		arrow := "▼"
		folderStyle := FolderStyle
		if !folder.Expanded {
			arrow = "▶"
			folderStyle = FolderCollapsedStyle
		}

		b.WriteString(folderStyle.Render(fmt.Sprintf("%s %s (%d)", arrow, folderName, folder.Count)))
		b.WriteString("\n")

		// Sessions in folder
		if folder.Expanded {
			sessions := groups[folderName]
			for _, inst := range sessions {
				style := SessionItemStyle
				prefix := "  "
				if itemIndex == l.cursor {
					style = SessionItemSelectedStyle
					prefix = "▶ "
				}

				status := StatusIndicator(string(inst.Status))
				icon := ToolIcon(inst.Tool)

				line := style.Render(prefix + icon + " " + inst.Title + " " + status)
				b.WriteString(line)
				b.WriteString("\n")
				itemIndex++
			}
		} else {
			// Skip items when collapsed but still count them
			itemIndex += len(groups[folderName])
		}
	}

	return b.String()
}
