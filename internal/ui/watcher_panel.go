package ui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// WatcherDisplayItem is a watcher entry suitable for TUI list rendering.
type WatcherDisplayItem struct {
	ID            string
	Name          string
	Type          string
	Status        string // "running", "stopped"
	HealthStatus  string // "healthy", "warning", "error", "stopped"
	EventsPerHour float64
	Conductor     string
}

// WatcherEventDisplay is an event row suitable for TUI detail rendering.
type WatcherEventDisplay struct {
	Timestamp time.Time
	Sender    string
	Subject   string
	RoutedTo  string
	SessionID string
}

// WatcherActionMsg is the tea.Msg returned when the user triggers a quick action.
type WatcherActionMsg struct {
	Action      string // "start", "stop", "test"
	WatcherID   string
	WatcherName string
}

// WatcherPanel is an overlay panel that shows watcher list and detail views.
// It follows the same pattern as SettingsPanel: Show/Hide/IsVisible/SetSize/Update/View.
type WatcherPanel struct {
	visible      bool
	width        int
	height       int
	cursor       int  // selected watcher index in list view
	scrollOffset int  // scroll offset within list view
	detailMode   bool // false = list view, true = detail view
	detailCursor int  // cursor within detail view actions

	// Data set externally
	watchers []WatcherDisplayItem
	events   []WatcherEventDisplay // events for currently selected watcher
}

// NewWatcherPanel creates a new WatcherPanel.
func NewWatcherPanel() *WatcherPanel {
	return &WatcherPanel{}
}

// Show makes the panel visible and resets navigation state.
func (wp *WatcherPanel) Show() {
	wp.visible = true
	wp.cursor = 0
	wp.scrollOffset = 0
	wp.detailMode = false
	wp.detailCursor = 0
}

// Hide hides the panel.
func (wp *WatcherPanel) Hide() {
	wp.visible = false
}

// IsVisible returns whether the panel is currently shown.
func (wp *WatcherPanel) IsVisible() bool {
	return wp.visible
}

// SetSize sets the terminal dimensions used for rendering.
func (wp *WatcherPanel) SetSize(w, h int) {
	wp.width = w
	wp.height = h
}

// SetWatchers replaces the displayed watcher list.
func (wp *WatcherPanel) SetWatchers(items []WatcherDisplayItem) {
	wp.watchers = items
	// Clamp cursor so it stays valid after the list changes.
	if len(wp.watchers) == 0 {
		wp.cursor = 0
	} else if wp.cursor >= len(wp.watchers) {
		wp.cursor = len(wp.watchers) - 1
	}
}

// SetEvents replaces the event list shown in detail view.
func (wp *WatcherPanel) SetEvents(events []WatcherEventDisplay) {
	wp.events = events
}

// SelectedWatcher returns the currently highlighted watcher or nil when the list is empty.
func (wp *WatcherPanel) SelectedWatcher() *WatcherDisplayItem {
	if len(wp.watchers) == 0 || wp.cursor < 0 || wp.cursor >= len(wp.watchers) {
		return nil
	}
	item := wp.watchers[wp.cursor]
	return &item
}

// Update processes keyboard input for the watcher panel.
// Returns the updated panel, an optional tea.Cmd, and (for forward compatibility) true.
func (wp *WatcherPanel) Update(msg tea.Msg) (*WatcherPanel, tea.Cmd) {
	if !wp.visible {
		return wp, nil
	}

	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return wp, nil
	}

	switch key.String() {
	case "esc", "w":
		if wp.detailMode {
			// Back to list view
			wp.detailMode = false
			wp.detailCursor = 0
		} else {
			wp.Hide()
		}

	case "j", "down", "ctrl+n":
		if wp.detailMode {
			wp.detailCursor++
		} else {
			if wp.cursor < len(wp.watchers)-1 {
				wp.cursor++
			}
		}

	case "k", "up", "ctrl+p":
		if wp.detailMode {
			if wp.detailCursor > 0 {
				wp.detailCursor--
			}
		} else {
			if wp.cursor > 0 {
				wp.cursor--
			}
		}

	case "enter", "l":
		if !wp.detailMode && len(wp.watchers) > 0 {
			wp.detailMode = true
			wp.detailCursor = 0
		}

	case "h", "backspace":
		if wp.detailMode {
			wp.detailMode = false
			wp.detailCursor = 0
		}

	case "s":
		if sel := wp.SelectedWatcher(); sel != nil {
			return wp, func() tea.Msg {
				return WatcherActionMsg{Action: "start", WatcherID: sel.ID, WatcherName: sel.Name}
			}
		}

	case "x":
		if sel := wp.SelectedWatcher(); sel != nil {
			return wp, func() tea.Msg {
				return WatcherActionMsg{Action: "stop", WatcherID: sel.ID, WatcherName: sel.Name}
			}
		}

	case "t":
		if sel := wp.SelectedWatcher(); sel != nil {
			return wp, func() tea.Msg {
				return WatcherActionMsg{Action: "test", WatcherID: sel.ID, WatcherName: sel.Name}
			}
		}
	}

	return wp, nil
}

// View renders the panel as an overlay string.
// Returns empty string when not visible.
func (wp *WatcherPanel) View() string {
	if !wp.visible {
		return ""
	}

	dialogWidth := 60
	if wp.width > 0 && wp.width < dialogWidth+10 {
		dialogWidth = wp.width - 4
		if dialogWidth < 30 {
			dialogWidth = 30
		}
	}

	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorAccent)

	borderStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorBorder).
		Padding(0, 1).
		Width(dialogWidth)

	if wp.detailMode {
		return wp.renderDetail(dialogWidth, titleStyle, borderStyle)
	}
	return wp.renderList(dialogWidth, titleStyle, borderStyle)
}

// renderList renders the list view of all watchers.
func (wp *WatcherPanel) renderList(dialogWidth int, titleStyle, borderStyle lipgloss.Style) string {
	var sb strings.Builder

	// Title
	title := titleStyle.Render("WATCHERS")
	sb.WriteString(title)
	sb.WriteString("\n")
	sb.WriteString(strings.Repeat("─", dialogWidth))
	sb.WriteString("\n")

	if len(wp.watchers) == 0 {
		dimStyle := lipgloss.NewStyle().Foreground(ColorTextDim)
		sb.WriteString(dimStyle.Render("  No watchers configured."))
		sb.WriteString("\n")
	} else {
		selectedStyle := lipgloss.NewStyle().
			Background(ColorSurface).
			Foreground(ColorText).
			Bold(true)
		normalStyle := lipgloss.NewStyle().Foreground(ColorText)

		nameWidth := dialogWidth - 30
		if nameWidth < 10 {
			nameWidth = 10
		}

		for i, w := range wp.watchers {
			dot := wp.statusDot(w.HealthStatus)
			name := truncateStr(w.Name, nameWidth)
			rate := fmt.Sprintf("%.1f/hr", w.EventsPerHour)
			row := fmt.Sprintf(" %s %-*s (%s)  %s", dot, nameWidth, name, w.Type, rate)

			if i == wp.cursor {
				sb.WriteString(selectedStyle.Render(row))
			} else {
				sb.WriteString(normalStyle.Render(row))
			}
			sb.WriteString("\n")
		}
	}

	sb.WriteString(strings.Repeat("─", dialogWidth))
	sb.WriteString("\n")

	footerStyle := lipgloss.NewStyle().Foreground(ColorTextDim)
	sb.WriteString(footerStyle.Render("[Enter] Details  [s] Start  [x] Stop  [t] Test  [w/Esc] Close"))

	return borderStyle.Render(sb.String())
}

// renderDetail renders the detail view for the selected watcher.
func (wp *WatcherPanel) renderDetail(dialogWidth int, titleStyle, borderStyle lipgloss.Style) string {
	sel := wp.SelectedWatcher()
	if sel == nil {
		return wp.renderList(dialogWidth, titleStyle, borderStyle)
	}

	var sb strings.Builder

	// Header
	dot := wp.statusDot(sel.HealthStatus)
	header := fmt.Sprintf("%s %s (%s) — %s", dot, sel.Name, sel.Type, sel.Status)
	sb.WriteString(titleStyle.Render(header))
	sb.WriteString("\n")
	sb.WriteString(strings.Repeat("─", dialogWidth))
	sb.WriteString("\n")

	// Recent Events section
	sectionStyle := lipgloss.NewStyle().Foreground(ColorCyan).Bold(true)
	sb.WriteString(sectionStyle.Render("Recent Events"))
	sb.WriteString("\n")

	if len(wp.events) == 0 {
		dimStyle := lipgloss.NewStyle().Foreground(ColorTextDim)
		sb.WriteString(dimStyle.Render("  No events recorded yet."))
		sb.WriteString("\n")
	} else {
		headerStyle := lipgloss.NewStyle().Foreground(ColorTextDim)
		colW := (dialogWidth - 4) / 4
		if colW < 8 {
			colW = 8
		}
		colHdr := fmt.Sprintf(" %-12s %-*s %-*s %-*s",
			"TIME", colW, "SENDER", colW, "SUBJECT", colW, "ROUTED TO")
		sb.WriteString(headerStyle.Render(colHdr))
		sb.WriteString("\n")

		limit := 10
		if len(wp.events) < limit {
			limit = len(wp.events)
		}
		rowStyle := lipgloss.NewStyle().Foreground(ColorText)
		for _, ev := range wp.events[:limit] {
			ts := ev.Timestamp.Format("01-02 15:04")
			sender := truncateStr(ev.Sender, colW)
			subject := truncateStr(ev.Subject, colW)
			routedTo := truncateStr(ev.RoutedTo, colW)
			row := fmt.Sprintf(" %-12s %-*s %-*s %-*s", ts, colW, sender, colW, subject, colW, routedTo)
			sb.WriteString(rowStyle.Render(row))
			sb.WriteString("\n")
		}
	}

	sb.WriteString(strings.Repeat("─", dialogWidth))
	sb.WriteString("\n")

	// Quick Actions section
	sectionStyle2 := lipgloss.NewStyle().Foreground(ColorCyan).Bold(true)
	sb.WriteString(sectionStyle2.Render("Quick Actions"))
	sb.WriteString("\n")
	actStyle := lipgloss.NewStyle().Foreground(ColorText)
	sb.WriteString(actStyle.Render("  [s] Start   [x] Stop   [t] Test"))
	sb.WriteString("\n")

	sb.WriteString(strings.Repeat("─", dialogWidth))
	sb.WriteString("\n")

	footerStyle := lipgloss.NewStyle().Foreground(ColorTextDim)
	sb.WriteString(footerStyle.Render("[h/Esc] Back to list"))

	return borderStyle.Render(sb.String())
}

// statusDot returns a colored status indicator character.
func (wp *WatcherPanel) statusDot(healthStatus string) string {
	switch healthStatus {
	case "healthy":
		return lipgloss.NewStyle().Foreground(ColorGreen).Render("●")
	case "warning":
		return lipgloss.NewStyle().Foreground(ColorYellow).Render("●")
	case "error":
		return lipgloss.NewStyle().Foreground(ColorRed).Render("●")
	default: // "stopped" or unknown
		return lipgloss.NewStyle().Foreground(ColorTextDim).Render("●")
	}
}

// truncateStr truncates s to max runes, appending "…" if needed (T-16-06).
func truncateStr(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max <= 1 {
		return "…"
	}
	return string(runes[:max-1]) + "…"
}
