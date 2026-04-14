package ui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// CopilotOptionsPanel is a UI panel for Copilot-specific launch options.
// Provides two toggles: YOLO mode (allow-all) and Autopilot mode (auto-approve tools).
type CopilotOptionsPanel struct {
	yoloMode      bool
	autopilotMode bool
	focused       bool
	focusIndex    int // 0=yolo, 1=autopilot
}

// NewCopilotOptionsPanel creates a new Copilot options panel.
func NewCopilotOptionsPanel() *CopilotOptionsPanel {
	return &CopilotOptionsPanel{}
}

// SetDefaults applies default values from config.
func (p *CopilotOptionsPanel) SetDefaults(yolo, autopilot bool) {
	p.yoloMode = yolo
	p.autopilotMode = autopilot
}

// Focus sets focus to this panel.
func (p *CopilotOptionsPanel) Focus() {
	p.focused = true
	p.focusIndex = 0
}

// Blur removes focus from this panel.
func (p *CopilotOptionsPanel) Blur() {
	p.focused = false
	p.focusIndex = -1
}

// IsFocused returns true if the panel has focus.
func (p *CopilotOptionsPanel) IsFocused() bool {
	return p.focused
}

// AtTop returns true if focus is on the first element.
func (p *CopilotOptionsPanel) AtTop() bool {
	return p.focusIndex <= 0
}

// GetYoloMode returns the current YOLO mode state.
func (p *CopilotOptionsPanel) GetYoloMode() bool {
	return p.yoloMode
}

// GetAutopilotMode returns the current autopilot mode state.
func (p *CopilotOptionsPanel) GetAutopilotMode() bool {
	return p.autopilotMode
}

// Update handles key events.
func (p *CopilotOptionsPanel) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up":
			if p.focusIndex > 0 {
				p.focusIndex--
			}
			return nil
		case "down":
			if p.focusIndex < 1 {
				p.focusIndex++
			}
			return nil
		case " ", "y":
			switch p.focusIndex {
			case 0:
				p.yoloMode = !p.yoloMode
			case 1:
				p.autopilotMode = !p.autopilotMode
			}
			return nil
		}
	}
	return nil
}

// View renders the options panel.
func (p *CopilotOptionsPanel) View() string {
	headerStyle := lipgloss.NewStyle().Foreground(ColorComment)
	dimStyle := lipgloss.NewStyle().Foreground(ColorComment)

	var content string
	content += headerStyle.Render("─ Copilot Options ─") + "\n"
	content += renderCheckboxLine("YOLO mode (allow-all)", p.yoloMode, p.focused && p.focusIndex == 0)
	content += renderCheckboxLine("Autopilot (auto-approve tools)", p.autopilotMode, p.focused && p.focusIndex == 1)
	if p.yoloMode && p.autopilotMode {
		content += dimStyle.Render("    ↑ overridden by YOLO mode") + "\n"
	}
	return content
}
