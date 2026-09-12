package ui

import tea "github.com/charmbracelet/bubbletea"

// OptionsPanel is the interface for tool-specific option panels in session dialogs.
// Implemented by ClaudeOptionsPanel and YoloOptionsPanel.
type OptionsPanel interface {
	Focus()
	Blur()
	IsFocused() bool
	AtTop() bool
	// AtBottom reports whether focus is on the panel's last control, so the
	// enclosing dialog knows when Tab/Enter/↓ should leave the panel instead
	// of moving within it.
	AtBottom() bool
	// FocusLast focuses the panel's last control, so a backward move (Shift+Tab
	// or ↑ from the row below) enters the panel at its bottom.
	FocusLast()
	// FocusedLine returns the zero-based logical line occupied by the focused
	// control in View, or -1 when the panel is not focused.
	FocusedLine() int
	Update(tea.Msg) tea.Cmd
	View() string
}
