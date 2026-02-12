package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// HelpOverlay shows keyboard shortcuts in a modal
type HelpOverlay struct {
	visible      bool
	width        int
	height       int
	scrollOffset int // Current scroll position for small screens
}

// NewHelpOverlay creates a new help overlay
func NewHelpOverlay() *HelpOverlay {
	return &HelpOverlay{}
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
				{"N", "Quick create (auto name, smart defaults)"},
				{"r", "Rename session"},
				{"Shift+R", "Restart session"},
				{"d", "Delete session"},
				{"Ctrl+Z", "Undo delete"},
				{"m", "Move to group"},
				{"Shift+M", "MCP Manager (Claude)"},
				{"v", "Toggle preview mode (output/stats/both)"},
				{"u", "Mark unread"},
				{"K / J", "Reorder up/down"},
				{"f", "Quick fork (Claude only)"},
				{"F", "Fork with options (Claude only)"},
				{"c", "Copy output to clipboard"},
				{"x", "Send output to session"},
			},
		},
		{
			title: "WORKTREES",
			items: [][2]string{
				{"W", "Finish worktree (merge + cleanup)"},
				{"n → w", "Create session in worktree"},
				{"F → w", "Fork session into worktree"},
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

	// Styles
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorAccent)

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
			line := "  " + keyStyle.Render(item[0]) + descStyle.Render(item[1])
			lines = append(lines, line)
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

	// Clamp scroll offset
	maxScroll := totalLines - availableHeight
	if maxScroll < 0 {
		maxScroll = 0
	}
	if h.scrollOffset > maxScroll {
		h.scrollOffset = maxScroll
	}
	if h.scrollOffset < 0 {
		h.scrollOffset = 0
	}

	// Build visible content
	var content strings.Builder

	if needsScroll {
		// Show scroll indicator at top if not at beginning
		if h.scrollOffset > 0 {
			content.WriteString(scrollIndicatorStyle.Render("▲ more above"))
			content.WriteString("\n")
			availableHeight-- // Account for indicator line
		}

		// Determine end index
		endIdx := h.scrollOffset + availableHeight
		if h.scrollOffset > 0 {
			// Leave room for bottom indicator if needed
			if endIdx < totalLines {
				availableHeight--
				endIdx = h.scrollOffset + availableHeight
			}
		}
		if endIdx > totalLines {
			endIdx = totalLines
		}

		// Render visible lines
		for i := h.scrollOffset; i < endIdx; i++ {
			content.WriteString(lines[i])
			if i < endIdx-1 {
				content.WriteString("\n")
			}
		}

		// Show scroll indicator at bottom if more content below
		if endIdx < totalLines {
			content.WriteString("\n")
			content.WriteString(scrollIndicatorStyle.Render("▼ more below"))
		}
	} else {
		// No scrolling needed, render all lines
		for i, line := range lines {
			content.WriteString(line)
			if i < len(lines)-1 {
				content.WriteString("\n")
			}
		}
	}

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
