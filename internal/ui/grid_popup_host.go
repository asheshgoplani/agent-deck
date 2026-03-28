package ui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// GridPopupHost is a thin Bubble Tea model wrapping GridView for standalone popup usage.
// Instead of tea.Exec (tmux attach), it records the target session and quits.
type GridPopupHost struct {
	grid     *GridView
	switchTo string // tmux session name to switch to on exit
}

// NewGridPopupHost creates a standalone grid host for the given group.
// sourceSession is the session the popup was launched from (for send output).
func NewGridPopupHost(group *session.Group, sourceSession *session.Instance) *GridPopupHost {
	InitTheme("dark")
	grid := NewGridView()
	grid.SetStandaloneMode(true)
	grid.SetSourceSession(sourceSession)
	grid.Show(group)
	return &GridPopupHost{grid: grid}
}

// SwitchTo returns the tmux session name to switch to, or empty if none.
func (h *GridPopupHost) SwitchTo() string {
	return h.switchTo
}

// Init starts the grid tick and fetches all cell content.
func (h *GridPopupHost) Init() tea.Cmd {
	return tea.Batch(h.grid.fetchAllCells(), h.grid.startTick())
}

// Update routes messages to the grid, intercepting gridAttachMsg for switch behavior.
func (h *GridPopupHost) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		h.grid.SetSize(msg.Width, msg.Height)
		return h, nil

	case gridAttachMsg:
		// In standalone mode: record target and quit instead of tea.Exec
		if msg.instance != nil {
			tmuxSess := msg.instance.GetTmuxSession()
			if tmuxSess != nil {
				h.switchTo = tmuxSess.Name
			}
		}
		return h, tea.Quit

	case gridCellCaptureMsg, gridTickMsg, gridPopupTickMsg, gridPopupCaptureMsg, gridSendOutputMsg:
		var cmd tea.Cmd
		h.grid, cmd = h.grid.Update(msg)
		return h, cmd

	case tea.KeyMsg:
		var cmd tea.Cmd
		h.grid, cmd = h.grid.Update(msg)
		// If grid was hidden (user pressed q/Esc), quit the program
		if !h.grid.IsVisible() {
			return h, tea.Quit
		}
		return h, cmd

	case tea.MouseMsg:
		// Forward mouse events to grid (scroll support)
		var cmd tea.Cmd
		h.grid, cmd = h.grid.Update(msg)
		return h, cmd
	}

	// Ignore other messages (focus events, etc.)
	return h, nil
}

// View delegates rendering to the grid.
func (h *GridPopupHost) View() string {
	return h.grid.View()
}
