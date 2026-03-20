package ui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// YoloOptionsPanel is a UI panel for YOLO/dangerous mode options.
// Used for Gemini and Codex in NewDialog, matching ClaudeOptionsPanel's visual style.
type YoloOptionsPanel struct {
	toolName string // "Gemini" or "Codex"
	label    string // Checkbox label text
	yoloMode bool
	focused  bool
}

// NewYoloOptionsPanel creates a new options panel for a tool with a single YOLO checkbox.
func NewYoloOptionsPanel(toolName, label string) *YoloOptionsPanel {
	return &YoloOptionsPanel{
		toolName: toolName,
		label:    label,
	}
}

// SetDefaults applies default value from config.
func (p *YoloOptionsPanel) SetDefaults(yoloMode bool) {
	p.yoloMode = yoloMode
}

// Focus sets focus to this panel.
func (p *YoloOptionsPanel) Focus() {
	p.focused = true
}

// Blur removes focus from this panel.
func (p *YoloOptionsPanel) Blur() {
	p.focused = false
}

// IsFocused returns true if the panel has focus.
func (p *YoloOptionsPanel) IsFocused() bool {
	return p.focused
}

// GetYoloMode returns the current YOLO mode state.
func (p *YoloOptionsPanel) GetYoloMode() bool {
	return p.yoloMode
}

// AtTop returns true (single element, always at top).
func (p *YoloOptionsPanel) AtTop() bool {
	return true
}

// Update handles key events.
func (p *YoloOptionsPanel) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case " ", "y":
			p.yoloMode = !p.yoloMode
			return nil
		}
	}
	return nil
}

// View renders the options panel matching ClaudeOptionsPanel's visual style.
func (p *YoloOptionsPanel) View() string {
	headerStyle := lipgloss.NewStyle().Foreground(ColorComment)

	var content string
	content += headerStyle.Render("─ "+p.toolName+" Options ─") + "\n"
	content += renderCheckboxLine(p.label, p.yoloMode, p.focused)
	return content
}
