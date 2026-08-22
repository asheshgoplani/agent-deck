package ui

import tea "github.com/charmbracelet/bubbletea"

// OptionsPanel is the interface for tool-specific option panels in session dialogs.
// Implemented by ClaudeOptionsPanel and YoloOptionsPanel.
type OptionsPanel interface {
	Focus()
	Blur()
	IsFocused() bool
	AtTop() bool
	// FocusedLine returns the zero-based logical line occupied by the focused
	// control in View, or -1 when the panel is not focused.
	FocusedLine() int
	Update(tea.Msg) tea.Cmd
	View() string
}
