package ui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// YoloOptionsPanel is a UI panel for YOLO/dangerous mode options.
// Used for Gemini and Codex in NewDialog, matching ClaudeOptionsPanel's visual style.
type YoloOptionsPanel struct {
	toolName   string // "Gemini" or "Codex"
	label      string // Checkbox label text
	yoloMode   bool
	useHappy   bool
	showHappy  bool
	focusIndex int
	focused    bool
}

// NewYoloOptionsPanel creates a new options panel for a tool with a YOLO checkbox
// and an optional happy checkbox.
func NewYoloOptionsPanel(toolName, label string, showHappy bool) *YoloOptionsPanel {
	return &YoloOptionsPanel{
		toolName:  toolName,
		label:     label,
		showHappy: showHappy,
	}
}

// SetDefaults applies default value from config.
func (p *YoloOptionsPanel) SetDefaults(yoloMode bool, useHappy ...bool) {
	p.yoloMode = yoloMode
	p.useHappy = false
	if len(useHappy) > 0 {
		p.useHappy = useHappy[0]
	}
}

// Focus sets focus to this panel.
func (p *YoloOptionsPanel) Focus() {
	p.focused = true
	p.focusIndex = 0
}

// Blur removes focus from this panel.
func (p *YoloOptionsPanel) Blur() {
	p.focused = false
	p.focusIndex = -1
}

// IsFocused returns true if the panel has focus.
func (p *YoloOptionsPanel) IsFocused() bool {
	return p.focused
}

// GetYoloMode returns the current YOLO mode state.
func (p *YoloOptionsPanel) GetYoloMode() bool {
	return p.yoloMode
}

// GetUseHappy returns the current happy state.
func (p *YoloOptionsPanel) GetUseHappy() bool {
	return p.useHappy
}

// AtTop returns true when focus is on the first checkbox.
func (p *YoloOptionsPanel) AtTop() bool {
	return p.focusIndex <= 0
}

// Update handles key events.
func (p *YoloOptionsPanel) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "down", "tab":
			if p.showHappy && p.focusIndex < 1 {
				p.focusIndex++
			}
			return nil
		case "up", "shift+tab":
			if p.showHappy && p.focusIndex > 0 {
				p.focusIndex--
			}
			return nil
		case " ":
			if p.showHappy && p.focusIndex == 0 {
				p.useHappy = !p.useHappy
				return nil
			}
			p.yoloMode = !p.yoloMode
			return nil
		case "y":
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
	if p.showHappy {
		content += renderCheckboxLine("Use happy wrapper", p.useHappy, p.focused && p.focusIndex == 0)
	}
	yoloFocusIndex := 0
	if p.showHappy {
		yoloFocusIndex = 1
	}
	content += renderCheckboxLine(p.label, p.yoloMode, p.focused && p.focusIndex == yoloFocusIndex)
	return content
}
