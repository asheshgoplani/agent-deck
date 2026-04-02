package ui

import (
	"fmt"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/charmbracelet/lipgloss"
)

// TabStripLayout defines the rendering mode for the tab strip.
type TabStripLayout string

const (
	TabStripVertical   TabStripLayout = "vertical"
	TabStripHorizontal TabStripLayout = "horizontal"
)

// Braille spinner frames for running status.
var brailleSpinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Half-circle pulsation frames for waiting status.
var halfCircle = []string{"◐", "◓", "◑", "◒"}

// Dot matrix scale levels for transitions.
var dotScale = []string{"●", "•", "·", " "}

// tabTransition tracks a dot-matrix animation between status changes.
type tabTransition struct {
	frame    int
	maxFrame int
	fromActive bool // true = collapse (active→passive), false = expand
}

// TabStripModel manages the tab strip state and rendering.
type TabStripModel struct {
	instances    []*session.Instance
	selectedIdx  int
	animFrame    int
	unreadMap    map[string]bool
	layout       TabStripLayout
	width        int
	showHotkeys  bool
	transitions  map[string]*tabTransition
	prevStatuses map[string]session.Status
}

// NewTabStrip creates a new TabStripModel.
func NewTabStrip(layout string, width int, showHotkeys bool) *TabStripModel {
	l := TabStripVertical
	if layout == "horizontal" {
		l = TabStripHorizontal
	}
	return &TabStripModel{
		layout:       l,
		width:        width,
		showHotkeys:  showHotkeys,
		unreadMap:    make(map[string]bool),
		transitions:  make(map[string]*tabTransition),
		prevStatuses: make(map[string]session.Status),
	}
}

// UpdateInstances syncs the instance list and detects status changes for transitions.
func (ts *TabStripModel) UpdateInstances(instances []*session.Instance) {
	ts.instances = instances

	// Clamp selectedIdx
	if len(instances) == 0 {
		ts.selectedIdx = 0
	} else if ts.selectedIdx >= len(instances) {
		ts.selectedIdx = len(instances) - 1
	}

	// Detect status changes and create transitions
	for _, inst := range instances {
		prev, ok := ts.prevStatuses[inst.ID]
		if ok && prev != inst.Status {
			fromActive := isActiveStatus(prev)
			toActive := isActiveStatus(inst.Status)
			if fromActive != toActive {
				maxFrames := 5
				ts.transitions[inst.ID] = &tabTransition{
					frame:      0,
					maxFrame:   maxFrames,
					fromActive: fromActive,
				}
			}
		}
		ts.prevStatuses[inst.ID] = inst.Status
	}
}

func isActiveStatus(s session.Status) bool {
	return s == session.StatusRunning || s == session.StatusStarting
}

// UpdateUnreadState updates the unread map based on acknowledged instances.
func (ts *TabStripModel) UpdateUnreadState(acknowledgedByID map[string]bool) {
	ts.unreadMap = make(map[string]bool)
	for _, inst := range ts.instances {
		if !acknowledgedByID[inst.ID] {
			ts.unreadMap[inst.ID] = true
		}
	}
}

// SelectTab selects a tab by index, clamping to bounds.
func (ts *TabStripModel) SelectTab(idx int) {
	if len(ts.instances) == 0 {
		ts.selectedIdx = 0
		return
	}
	if idx < 0 {
		idx = 0
	}
	if idx >= len(ts.instances) {
		idx = len(ts.instances) - 1
	}
	ts.selectedIdx = idx
}

// NextTab moves to the next tab, wrapping around.
func (ts *TabStripModel) NextTab() {
	if len(ts.instances) == 0 {
		return
	}
	ts.selectedIdx = (ts.selectedIdx + 1) % len(ts.instances)
}

// PrevTab moves to the previous tab, wrapping around.
func (ts *TabStripModel) PrevTab() {
	if len(ts.instances) == 0 {
		return
	}
	ts.selectedIdx = (ts.selectedIdx - 1 + len(ts.instances)) % len(ts.instances)
}

// SelectedInstance returns the currently selected instance, or nil.
func (ts *TabStripModel) SelectedInstance() *session.Instance {
	if len(ts.instances) == 0 || ts.selectedIdx >= len(ts.instances) {
		return nil
	}
	return ts.instances[ts.selectedIdx]
}

// SelectedInstanceID returns the ID of the selected instance, or "".
func (ts *TabStripModel) SelectedInstanceID() string {
	inst := ts.SelectedInstance()
	if inst == nil {
		return ""
	}
	return inst.ID
}

// Tick advances the animation frame and transitions.
func (ts *TabStripModel) Tick() {
	ts.animFrame++
	for id, tr := range ts.transitions {
		tr.frame++
		if tr.frame >= tr.maxFrame {
			delete(ts.transitions, id)
		}
	}
}

// statusIcon returns the icon for a given status and instance ID.
func (ts *TabStripModel) statusIcon(status session.Status, id string) string {
	// Check unread
	if ts.unreadMap[id] && (status == session.StatusIdle || status == session.StatusWaiting) {
		return "✓"
	}

	switch status {
	case session.StatusRunning:
		return brailleSpinner[ts.animFrame%len(brailleSpinner)]
	case session.StatusWaiting:
		return "◐"
	case session.StatusStarting:
		return brailleSpinner[ts.animFrame%len(brailleSpinner)]
	case session.StatusIdle:
		return "○"
	case session.StatusError:
		return "✗"
	case session.StatusStopped:
		return "⏸"
	default:
		return "○"
	}
}

// statusColor returns the lipgloss color for a status.
func statusColor(status session.Status) lipgloss.Color {
	switch status {
	case session.StatusRunning:
		return lipgloss.Color("114")
	case session.StatusWaiting:
		return lipgloss.Color("221")
	case session.StatusStarting:
		return lipgloss.Color("117")
	case session.StatusError:
		return lipgloss.Color("196")
	case session.StatusIdle, session.StatusStopped:
		return lipgloss.Color("245")
	default:
		return lipgloss.Color("245")
	}
}

// View dispatches to the correct renderer based on layout.
func (ts *TabStripModel) View(size int) string {
	if ts.layout == TabStripHorizontal {
		return ts.viewHorizontal(size)
	}
	return ts.viewVertical(size)
}

// viewVertical renders a narrow sidebar view.
func (ts *TabStripModel) viewVertical(height int) string {
	if len(ts.instances) == 0 {
		return ""
	}

	w := ts.width
	if w < 10 {
		w = 15
	}

	var lines []string
	for i, inst := range ts.instances {
		if i >= height && height > 0 {
			break
		}

		selected := i == ts.selectedIdx
		icon := ts.statusIcon(inst.Status, inst.ID)
		color := statusColor(inst.Status)
		if ts.unreadMap[inst.ID] {
			color = lipgloss.Color("114")
		}

		iconStyle := lipgloss.NewStyle().Foreground(color)
		if ts.unreadMap[inst.ID] {
			iconStyle = iconStyle.Bold(true)
		}

		// Border char
		var border string
		if selected {
			border = lipgloss.NewStyle().Foreground(color).Render("▌")
		} else {
			border = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render("│")
		}

		// Truncate name
		nameWidth := w - 5 // border + icon + space + hotkey
		if ts.showHotkeys && i < 9 {
			nameWidth -= 3 // " ⌘N"
		}
		if nameWidth < 3 {
			nameWidth = 3
		}
		name := inst.Title
		if len(name) > nameWidth {
			name = name[:nameWidth]
		}

		// Build line
		line := border + iconStyle.Render(icon) + " " + name
		if ts.showHotkeys && i < 9 {
			// Pad and add hotkey
			padLen := w - lipgloss.Width(line) - 3
			if padLen < 0 {
				padLen = 0
			}
			line += strings.Repeat(" ", padLen) + fmt.Sprintf(" ⌘%d", i+1)
		}

		lines = append(lines, line)
	}

	return strings.Join(lines, "\n")
}

// viewHorizontal renders a single-line tab bar.
func (ts *TabStripModel) viewHorizontal(width int) string {
	if len(ts.instances) == 0 {
		return ""
	}

	n := len(ts.instances)
	tabWidth := width / n
	if tabWidth < 8 {
		tabWidth = 8
	}

	var tabs []string
	var underlines []string

	for i, inst := range ts.instances {
		selected := i == ts.selectedIdx
		icon := ts.statusIcon(inst.Status, inst.ID)
		color := statusColor(inst.Status)

		iconStyle := lipgloss.NewStyle().Foreground(color)

		nameWidth := tabWidth - 4 // "[ " + icon + " " + name + "]"
		if nameWidth < 3 {
			nameWidth = 3
		}
		name := inst.Title
		if len(name) > nameWidth {
			name = name[:nameWidth]
		}

		tab := "[" + iconStyle.Render(icon) + " " + name + "]"
		tabs = append(tabs, tab)

		// Underline
		ulWidth := lipgloss.Width(tab)
		if selected {
			ul := lipgloss.NewStyle().Foreground(color).Render(strings.Repeat("▀", ulWidth))
			underlines = append(underlines, ul)
		} else {
			underlines = append(underlines, strings.Repeat("─", ulWidth))
		}
	}

	return strings.Join(tabs, " ") + "\n" + strings.Join(underlines, " ")
}

// TmuxStatusFormat returns a tmux-formatted status string.
func (ts *TabStripModel) TmuxStatusFormat() string {
	if len(ts.instances) == 0 {
		return ""
	}

	var parts []string
	for _, inst := range ts.instances {
		icon := ts.statusIcon(inst.Status, inst.ID)
		color := statusColor(inst.Status)

		name := inst.Title
		if len(name) > 12 {
			name = name[:12]
		}

		part := fmt.Sprintf("#[fg=%s]%s %s#[default]", string(color), icon, name)
		parts = append(parts, part)
	}

	return strings.Join(parts, " │ ")
}
