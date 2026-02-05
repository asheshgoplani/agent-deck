package ui

import tea "github.com/charmbracelet/bubbletea"

// OptionsPanel is the interface for tool-specific option panels in session dialogs.
// Implemented by ClaudeOptionsPanel and YoloOptionsPanel.
type OptionsPanel interface {
	Focus()
	Blur()
	IsFocused() bool
	AtTop() bool
	Update(tea.Msg) tea.Cmd
	View() string
}
