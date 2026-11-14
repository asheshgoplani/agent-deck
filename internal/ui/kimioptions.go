package ui

import (
	"github.com/asheshgoplani/agent-deck/internal/session"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type KimiOptionsPanel struct {
	mode    string
	focused bool
}

func NewKimiOptionsPanel() *KimiOptionsPanel {
	return &KimiOptionsPanel{}
}

func (p *KimiOptionsPanel) SetDefaults(mode string) {
	if mode == "" {
		mode = session.KimiPermissionAlwaysAsk
	}
	p.mode = mode
}

func (p *KimiOptionsPanel) SetFromOptions(opts *session.KimiOptions) {
	if opts != nil {
		p.mode = opts.PermissionMode
		if p.mode == "" {
			p.mode = session.KimiPermissionAlwaysAsk
		}
	}
}

func (p *KimiOptionsPanel) GetOptions() *session.KimiOptions {
	return &session.KimiOptions{PermissionMode: p.mode}
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
	case "left", "right", " ":
		if p.mode == session.KimiPermissionAskWhenNeeded {
			p.mode = session.KimiPermissionAlwaysAsk
		} else {
			p.mode = session.KimiPermissionAskWhenNeeded
		}
	case "y":
		p.mode = session.KimiPermissionAskWhenNeeded
	}
	return nil
}

func (p *KimiOptionsPanel) View() string {
	headerStyle := lipgloss.NewStyle().Foreground(ColorComment)
	activeStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	dimStyle := lipgloss.NewStyle().Foreground(ColorComment)

	label := "  YOLO mode: "
	if p.focused {
		label = activeStyle.Render("▶ YOLO mode: ")
	}
	box := "[ ]"
	if p.mode == session.KimiPermissionAskWhenNeeded {
		box = "[x]"
	}
	content := headerStyle.Render("─ Kimi Options ─") + "\n" + label + box + "\n"
	content += dimStyle.Render("    Auto-approve routine work; risky actions still ask (--yolo)") + "\n"
	return content
}
