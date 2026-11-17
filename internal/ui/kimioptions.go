package ui

import (
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var kimiPermissionModes = []string{
	session.KimiPermissionAlwaysAsk,
	session.KimiPermissionAskWhenNeeded,
	session.KimiPermissionNeverAsk,
}

var kimiPermissionModeLabels = []string{"Always Ask", "Ask When Needed", "Never Ask"}

func kimiPermissionIndex(mode string) int {
	for i, m := range kimiPermissionModes {
		if mode == m {
			return i
		}
	}
	return 0
}

type KimiOptionsPanel struct {
	mode    string
	focused bool
}

func NewKimiOptionsPanel() *KimiOptionsPanel {
	return &KimiOptionsPanel{}
}

func (p *KimiOptionsPanel) SetDefaults(mode string) {
	p.mode = session.NormalizeKimiPermissionMode(mode)
}

func (p *KimiOptionsPanel) SetFromOptions(opts *session.KimiOptions) {
	if opts != nil {
		p.SetDefaults(opts.PermissionMode)
	}
}

func (p *KimiOptionsPanel) GetOptions() *session.KimiOptions {
	return &session.KimiOptions{PermissionMode: p.mode}
}

func (p *KimiOptionsPanel) Focus()          { p.focused = true }
func (p *KimiOptionsPanel) Blur()           { p.focused = false }
func (p *KimiOptionsPanel) IsFocused() bool { return p.focused }
func (p *KimiOptionsPanel) AtTop() bool     { return true }
func (p *KimiOptionsPanel) AtBottom() bool  { return true }
func (p *KimiOptionsPanel) FocusLast()      { p.focused = true }

func (p *KimiOptionsPanel) FocusedLine() int {
	if !p.focused {
		return -1
	}
	return 1
}

func (p *KimiOptionsPanel) Update(msg tea.Msg) tea.Cmd {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	switch key.String() {
	case "left":
		p.mode = kimiPermissionModes[(kimiPermissionIndex(p.mode)+2)%3]
	case "right", " ":
		p.mode = kimiPermissionModes[(kimiPermissionIndex(p.mode)+1)%3]
	case "y":
		p.mode = session.KimiPermissionAskWhenNeeded
	}
	return nil
}

func (p *KimiOptionsPanel) View() string {
	headerStyle := lipgloss.NewStyle().Foreground(ColorComment)
	activeStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	dimStyle := lipgloss.NewStyle().Foreground(ColorComment)

	content := headerStyle.Render("─ Kimi Options ─") + "\n"
	content += "  Permission mode: "
	parts := make([]string, 0, len(kimiPermissionModeLabels))
	for i, label := range kimiPermissionModeLabels {
		if i == kimiPermissionIndex(p.mode) {
			parts = append(parts, activeStyle.Render(">"+label))
		} else {
			parts = append(parts, dimStyle.Render(" "+label))
		}
	}
	content += strings.Join(parts, "  ") + "\n"
	content += dimStyle.Render("    --yolo: routine auto, risky asks; --auto: everything auto") + "\n"
	return content
}
