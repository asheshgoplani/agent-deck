package ui

import (
	"fmt"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	searchBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorAccent).
			Padding(0, 1)

	resultItemStyle = lipgloss.NewStyle().
			Padding(0, 2)

	selectedResultStyle = lipgloss.NewStyle().
				Padding(0, 2).
				Background(ColorAccent).
				Foreground(ColorBg)

	overlayStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorAccent).
			Padding(1, 2)
)

// Search represents the search overlay component
type Search struct {
	input          textinput.Model
	results        []*session.Instance
	cursor         int
	width          int
	height         int
	visible        bool
	allItems       []*session.Instance
	switchToGlobal bool   // Flag to signal switch to global search
	scopedGroup    string // Non-empty => filter items to this exact GroupPath (v1.7.60)
}

// NewSearch creates a new search overlay
func NewSearch() *Search {
	ti := textinput.New()
	ti.Placeholder = "Search sessions..."
	ti.Focus()
	ti.CharLimit = 100
	ti.Width = 50

	return &Search{
		input:   ti,
		results: []*session.Instance{},
		cursor:  0,
		visible: false,
	}
}

// SetItems sets the full list of items to search through. When a group scope
// has been set via SetScopedGroup, items are filtered to that group before
// storage so background reloads do not leak out-of-group sessions into a
// scoped in-group search session.
func (s *Search) SetItems(items []*session.Instance) {
	if s.scopedGroup != "" {
		filtered := make([]*session.Instance, 0, len(items))
		for _, it := range items {
			if it != nil && it.GroupPath == s.scopedGroup {
				filtered = append(filtered, it)
			}
		}
		s.allItems = filtered
	} else {
		s.allItems = items
	}
	s.updateResults()
}

// SetScopedGroup restricts SetItems to a single group path. Pass "" to clear.
func (s *Search) SetScopedGroup(groupPath string) {
	s.scopedGroup = groupPath
}

// SetSize sets the dimensions of the search overlay
func (s *Search) SetSize(width, height int) {
	s.width = width
	s.height = height
}

// Show makes the search overlay visible
func (s *Search) Show() {
	s.visible = true
	s.input.Focus()
	s.switchToGlobal = false
}

// WantsSwitchToGlobal returns true if user pressed Tab to switch to global search
func (s *Search) WantsSwitchToGlobal() bool {
	if s.switchToGlobal {
		s.switchToGlobal = false
		return true
	}
	return false
}

// Hide hides the search overlay and clears any group scope.
func (s *Search) Hide() {
	s.visible = false
	s.input.Blur()
	s.scopedGroup = ""
}

// IsVisible returns whether the search overlay is visible
func (s *Search) IsVisible() bool {
	return s.visible
}

// Selected returns the currently selected item
func (s *Search) Selected() *session.Instance {
	if len(s.results) == 0 {
		return nil
	}
	if s.cursor >= len(s.results) {
		s.cursor = len(s.results) - 1
	}
	return s.results[s.cursor]
}

// Update handles messages for the search overlay
// Returns the updated Search and any command to execute
func (s *Search) Update(msg tea.Msg) (*Search, tea.Cmd) {
	if !s.visible {
		return s, nil
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc":
			s.Hide()
			return s, nil

		case "enter":
			if len(s.results) > 0 {
				s.Hide()
				// Parent should handle the selection
			}
			return s, nil

		case "up", "ctrl+k":
			if s.cursor > 0 {
				s.cursor--
			}
			return s, nil

		case "down", "ctrl+j":
			if s.cursor < len(s.results)-1 {
				s.cursor++
			}
			return s, nil

		case "tab":
			// Signal to switch to global search
			s.switchToGlobal = true
			s.Hide()
			return s, nil

		default:
			// Update text input
			var cmd tea.Cmd
			s.input, cmd = s.input.Update(msg)
			s.updateResults()
			return s, cmd
		}
	}

	return s, nil
}

// updateResults filters the items based on the current input
func (s *Search) updateResults() {
	query := s.input.Value()
	s.results = session.FilterByQuery(s.allItems, query)
	s.cursor = 0
}

// View renders the search overlay
func (s *Search) View() string {
	if !s.visible {
		return ""
	}

	// Header
	header := lipgloss.NewStyle().
		Foreground(ColorAccent).
		Bold(true).
		Render("🔍 Local Search (Agent Deck sessions)")

	// Build search input box
	searchBox := searchBoxStyle.Render(s.input.View())

	// Build results list
	var resultsStr strings.Builder
	maxResults := 10
	if len(s.results) > maxResults {
		s.results = s.results[:maxResults]
	}

	for i, item := range s.results {
		var line string
		if i == s.cursor {
			line = selectedResultStyle.Render("› " + item.Title + " (" + item.Tool + ")")
		} else {
			line = resultItemStyle.Render("  " + item.Title + " (" + item.Tool + ")")
		}
		resultsStr.WriteString(line)
		if i < len(s.results)-1 {
			resultsStr.WriteString("\n")
		}
	}

	// Show count
	countStr := lipgloss.NewStyle().
		Foreground(lipgloss.Color("241")).
		Render("  " + formatCount(len(s.results)))

	// Show filter hint when search is empty
	hintStr := ""
	if s.input.Value() == "" {
		hintStr = lipgloss.NewStyle().
			Foreground(ColorComment).
			Italic(true).
			Render("  Tip: waiting / running / idle to filter by status")
	}

	// Keyboard shortcuts hint
	keysHint := lipgloss.NewStyle().
		Foreground(ColorComment).
		Render("  [Enter] Select  [↑↓] Navigate  [Tab] Global  [Esc] Cancel")

	// Combine everything
	var content string
	if hintStr != "" {
		content = header + "\n\n" + searchBox + "\n" + hintStr + "\n\n" + resultsStr.String() + "\n" + countStr + "\n" + keysHint
	} else {
		content = header + "\n\n" + searchBox + "\n\n" + resultsStr.String() + "\n" + countStr + "\n" + keysHint
	}

	// Wrap in overlay box - responsive width
	overlayWidth := 60
	if s.width > 0 && s.width < overlayWidth+10 {
		overlayWidth = s.width - 10
		if overlayWidth < 30 {
			overlayWidth = 30
		}
	}
	overlay := overlayStyle.Width(overlayWidth).Render(content)

	// Center in the screen
	return centerInScreen(overlay, s.width, s.height)
}

// formatCount formats the result count
func formatCount(count int) string {
	if count == 0 {
		return "No results"
	}
	if count == 1 {
		return "1 result"
	}
	return lipgloss.NewStyle().Bold(true).Render(fmt.Sprintf("%d", count)) + " results"
}

// centerInScreen centers content in the terminal
func centerInScreen(content string, screenWidth, screenHeight int) string {
	lines := strings.Split(content, "\n")
	contentHeight := len(lines)
	contentWidth := 0
	for _, line := range lines {
		// cellWidth (not len): the box lines carry ANSI styling, so byte length
		// over-counts and the dialog ends up shifted left of true center.
		if w := cellWidth(line); w > contentWidth {
			contentWidth = w
		}
	}

	// Calculate vertical padding
	verticalPad := (screenHeight - contentHeight) / 2
	if verticalPad < 0 {
		verticalPad = 0
	}

	// Calculate horizontal padding
	horizontalPad := (screenWidth - contentWidth) / 2
	if horizontalPad < 0 {
		horizontalPad = 0
	}

	// Add vertical padding
	var result strings.Builder
	for i := 0; i < verticalPad; i++ {
		result.WriteString("\n")
	}

	// Add horizontal padding and content
	padding := strings.Repeat(" ", horizontalPad)
	for _, line := range lines {
		result.WriteString(padding + line + "\n")
	}

	return result.String()
}
