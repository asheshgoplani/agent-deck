package ui

import (
	"github.com/asheshgoplani/agent-deck/internal/session"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	kimiPermissionLabels = []string{"Kimi default", "Ask When Needed", "Never Ask"}
	kimiPermissionModes  = []string{session.KimiPermissionDefault, session.KimiPermissionYolo, session.KimiPermissionAuto}
)

type KimiOptionsPanel struct {
	permissionIndex int
	focused         bool
}

func NewKimiOptionsPanel() *KimiOptionsPanel {
	return &KimiOptionsPanel{}
}

func (p *KimiOptionsPanel) SetDefaults(permissionMode string) {
	p.permissionIndex = kimiPermissionIndex(permissionMode)
}

func (p *KimiOptionsPanel) SetFromOptions(opts *session.KimiOptions) {
	if opts != nil {
		p.SetDefaults(opts.PermissionMode)
	}
}

func (p *KimiOptionsPanel) GetOptions() *session.KimiOptions {
	return &session.KimiOptions{PermissionMode: kimiPermissionModes[p.permissionIndex]}
}

func (p *KimiOptionsPanel) Focus() {
	p.focused = true
}

func (p *KimiOptionsPanel) Blur() {
	p.focused = false
}

func (p *KimiOptionsPanel) IsFocused() bool {
	return p.focused
}

func (p *KimiOptionsPanel) AtTop() bool {
	return true
}

func (p *KimiOptionsPanel) AtBottom() bool {
	return true
}

func (p *KimiOptionsPanel) FocusLast() {
	p.focused = true
}

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
		p.cycle(-1)
	case "right", " ":
		p.cycle(1)
	case "y":
		p.SetDefaults(session.KimiPermissionYolo)
	}
	return nil
}

func (p *KimiOptionsPanel) cycle(delta int) {
	count := len(kimiPermissionModes)
	p.permissionIndex = ((p.permissionIndex+delta)%count + count) % count
}

func (p *KimiOptionsPanel) View() string {
	headerStyle := lipgloss.NewStyle().Foreground(ColorComment)
	activeStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	dimStyle := lipgloss.NewStyle().Foreground(ColorComment)

	label := "  Permissions: "
	if p.focused {
		label = activeStyle.Render("▶ Permissions: ")
	}
	content := headerStyle.Render("─ Kimi Options ─") + "\n" + label
	for i, choice := range kimiPermissionLabels {
		if i > 0 {
			content += "  "
		}
		content += renderKimiPermissionRadio(choice, i == p.permissionIndex, p.focused)
	}
	content += "\n" + dimStyle.Render("    "+kimiPermissionHelp(kimiPermissionModes[p.permissionIndex])) + "\n"
	return content
}

func kimiPermissionIndex(mode string) int {
	for i, candidate := range kimiPermissionModes {
		if mode == candidate {
			return i
		}
	}
	return 0
}

func kimiPermissionHelp(mode string) string {
	switch mode {
	case session.KimiPermissionYolo:
		return "Routine work runs; risky actions and questions still ask (--yolo)"
	case session.KimiPermissionAuto:
		return "Fully unattended; plan reviews still ask (--auto)"
	default:
		return "Use Kimi's configured or saved permission mode"
	}
}

func renderKimiPermissionRadio(label string, selected, focused bool) string {
	style := lipgloss.NewStyle()
	if focused && selected {
		style = style.Foreground(ColorAccent).Bold(true)
	} else if selected {
		style = style.Foreground(ColorCyan)
	} else {
		style = style.Foreground(ColorComment)
	}
	if selected {
		return style.Render("(•) " + label)
	}
	return style.Render("( ) " + label)
}
