package ui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// pickerToolNames lists built-in tools shown in the new-session picker (shell excluded).
var pickerToolNames = []string{
	"claude", "gemini", "opencode", "codex", "pi", "copilot", "crush", "cursor", "hermes",
}

// ToolVisibilityPanel edits [ui].hidden_tools via a checklist overlay.
type ToolVisibilityPanel struct {
	visible      bool
	width        int
	height       int
	cursor       int
	scrollOffset int
	toolNames    []string
	visibleTools map[string]bool // name -> shown in picker
	loadOK       bool            // false when config load failed; blocks persist
}

// NewToolVisibilityPanel creates a tool visibility editor.
func NewToolVisibilityPanel() *ToolVisibilityPanel {
	return &ToolVisibilityPanel{
		visibleTools: make(map[string]bool),
	}
}

// Show displays the panel and loads config from disk.
func (p *ToolVisibilityPanel) Show() {
	p.visible = true
	p.cursor = 0
	p.scrollOffset = 0
	config, err := session.LoadUserConfig()
	if err != nil {
		p.loadOK = false
		return
	}
	p.loadOK = true
	p.LoadConfig(config)
}

// Hide closes the panel.
func (p *ToolVisibilityPanel) Hide() {
	p.visible = false
}

// IsVisible reports whether the panel is open.
func (p *ToolVisibilityPanel) IsVisible() bool {
	return p.visible
}

// SetSize sets render dimensions.
func (p *ToolVisibilityPanel) SetSize(width, height int) {
	p.width = width
	p.height = height
}

// LoadConfig populates tool rows from config.
func (p *ToolVisibilityPanel) LoadConfig(config *session.UserConfig) {
	hidden := make(map[string]bool)
	if config != nil {
		for _, name := range config.UI.HiddenTools {
			hidden[name] = true
		}
	}

	names := append([]string{}, pickerToolNames...)
	if config != nil && len(config.Tools) > 0 {
		builtins := map[string]bool{
			"claude": true, "gemini": true, "opencode": true, "codex": true,
			"pi": true, "copilot": true, "crush": true, "cursor": true, "hermes": true,
			"shell": true, "aider": true,
		}
		var custom []string
		for name := range config.Tools {
			if !builtins[name] {
				custom = append(custom, name)
			}
		}
		sort.Strings(custom)
		names = append(names, custom...)
	}

	p.toolNames = names
	p.visibleTools = make(map[string]bool, len(names))
	for _, name := range names {
		p.visibleTools[name] = !hidden[name]
	}
	if p.cursor >= len(p.toolNames) {
		p.cursor = 0
	}
}

// HiddenTools returns the denylist for [ui].hidden_tools.
func (p *ToolVisibilityPanel) HiddenTools() []string {
	out := make([]string, 0)
	for _, name := range p.toolNames {
		if !p.visibleTools[name] {
			out = append(out, name)
		}
	}
	return out
}

// Update handles keyboard input. The third return is true when the panel should persist.
func (p *ToolVisibilityPanel) Update(msg tea.KeyMsg) (*ToolVisibilityPanel, tea.Cmd, bool) {
	if !p.visible {
		return p, nil, false
	}

	switch msg.String() {
	case "esc", "enter":
		p.Hide()
		return p, nil, p.loadOK
	case "up", "k":
		if p.cursor > 0 {
			p.cursor--
		}
	case "down", "j":
		if p.cursor < len(p.toolNames)-1 {
			p.cursor++
		}
	case " ":
		if len(p.toolNames) > 0 {
			name := p.toolNames[p.cursor]
			p.visibleTools[name] = !p.visibleTools[name]
		}
	}

	return p, nil, false
}

// View renders the checklist overlay.
func (p *ToolVisibilityPanel) View() string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary)
	sectionStyle := lipgloss.NewStyle().Foreground(ColorTextDim).Bold(true)
	dimStyle := lipgloss.NewStyle().Foreground(ColorTextDim)
	highlightStyle := lipgloss.NewStyle().Foreground(ColorPrimary).Bold(true)

	var b strings.Builder
	b.WriteString(titleStyle.Render("Visible tools"))
	b.WriteString("\n")
	b.WriteString(dimStyle.Render("Space toggles · Enter/Esc save · shell always shown"))
	b.WriteString("\n\n")
	b.WriteString(sectionStyle.Render("NEW-SESSION PICKER"))
	b.WriteString("\n")

	for i, name := range p.toolNames {
		check := "[ ]"
		if p.visibleTools[name] {
			check = "[x]"
		}
		line := fmt.Sprintf("  %s %s", check, name)
		if i == p.cursor {
			line = highlightStyle.Render(line)
		}
		b.WriteString(line)
		b.WriteString("\n")
	}

	if len(p.toolNames) == 0 {
		b.WriteString(dimStyle.Render("  No tools to configure."))
		b.WriteString("\n")
	}

	return b.String()
}
