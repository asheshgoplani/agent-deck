package ui

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/history/model"
)

// Catppuccin Mocha
const (
	brBase     = "#1e1e2e"
	brSurface0 = "#313244"
	brOverlay0 = "#6c7086"
	brText     = "#cdd6f4"
	brSubtext1 = "#bac2de"
	brSubtext0 = "#a6adc8"
	brLavender = "#b4befe"
	brBlue     = "#89b4fa"
	brSapphire = "#74c7ec"
	brMauve    = "#cba6f7"
	brGreen    = "#a6e3a1"
	brYellow   = "#f9e2af"
	brSky      = "#89dceb"
	brPeach    = "#fab387"
	brRed      = "#f38ba8"
)

func brFg(hex string) lipgloss.Style { return lipgloss.NewStyle().Foreground(lipgloss.Color(hex)) }

var (
	// One quiet chrome border for both panes: focus never moves off the sidebar,
	// so an active/idle color split carried no info. The gutter + selection bar
	// mark where the cursor is.
	brPaneBorder = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(brOverlay0)).Padding(0, 1)

	brSelectedBar = lipgloss.NewStyle().Background(lipgloss.Color(brSurface0)).Foreground(lipgloss.Color(brText)).Bold(true)
	brGutterStyle = brFg(brLavender)

	brFolderStyle   = brFg(brBlue).Bold(true)  // folder (path container) rows
	brProjectStyle  = brFg(brMauve).Bold(true) // project rows
	brCountStyle    = brFg(brOverlay0)
	brTitleStyle    = brFg(brSubtext1)
	brClosedTitle   = brFg(brOverlay0)
	brTimeStyle     = brFg(brOverlay0)
	brLoadMoreStyle = brFg(brOverlay0).Italic(true)
)

var brStatusStyles = map[model.SessionStatus]lipgloss.Style{
	model.StatusWaiting:     brFg(brRed).Bold(true),
	model.StatusRunningBusy: brFg(brGreen),
	model.StatusRunningIdle: brFg(brYellow),
	model.StatusRecent:      brFg(brSky),
	model.StatusClosed:      brFg(brOverlay0),
}
