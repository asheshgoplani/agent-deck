package ui

import (
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// OMPOptionsPanel exposes OMP's harness controls without requiring raw commands.
type OMPOptionsPanel struct {
	inputs                                                       []textinput.Model
	cursor                                                       int
	focused                                                      bool
	mode                                                         int
	noSession, printThoughts, autoApprove, fromClaude, fromCodex bool
}

var ompInputLabels = []string{"Resume ID", "Models", "Smol model", "Slow model", "Plan model", "Approval mode", "Max time", "Profile"}
var ompModes = []string{"continue", "new", "resume"}

func NewOMPOptionsPanel() *OMPOptionsPanel {
	p := &OMPOptionsPanel{}
	for _, placeholder := range []string{"session id", "model-a,model-b", "fast model", "reasoning model", "planning model", "always-ask | write | yolo", "10m | 1h", "profile name"} {
		in := textinput.New()
		in.Placeholder = placeholder
		in.CharLimit = 256
		in.Width = 42
		p.inputs = append(p.inputs, in)
	}
	return p
}

func (p *OMPOptionsPanel) SetDefaults(opts *session.OMPOptions) {
	p.mode = 0
	p.noSession, p.printThoughts, p.autoApprove, p.fromClaude, p.fromCodex = false, false, false, false, false
	for idx := range p.inputs {
		p.inputs[idx].SetValue("")
	}
	if opts == nil {
		return
	}
	for idx, mode := range ompModes {
		if mode == opts.SessionMode {
			p.mode = idx
		}
	}
	values := []string{opts.ResumeID, strings.Join(opts.Models, ","), opts.SmolModel, opts.SlowModel, opts.PlanModel, opts.ApprovalMode, opts.MaxTime, opts.Profile}
	for idx, value := range values {
		p.inputs[idx].SetValue(value)
	}
	p.noSession, p.printThoughts, p.autoApprove, p.fromClaude, p.fromCodex = opts.NoSession, opts.PrintThoughts, opts.AutoApprove, opts.FromClaude, opts.FromCodex
}

func (p *OMPOptionsPanel) GetOptions(model string) *session.OMPOptions {
	var models []string
	for _, value := range strings.Split(p.inputs[1].Value(), ",") {
		if value = strings.TrimSpace(value); value != "" {
			models = append(models, value)
		}
	}
	return &session.OMPOptions{SessionMode: ompModes[p.mode], ResumeID: strings.TrimSpace(p.inputs[0].Value()), NoSession: p.noSession, Model: strings.TrimSpace(model), Models: models, SmolModel: strings.TrimSpace(p.inputs[2].Value()), SlowModel: strings.TrimSpace(p.inputs[3].Value()), PlanModel: strings.TrimSpace(p.inputs[4].Value()), PrintThoughts: p.printThoughts, ApprovalMode: strings.TrimSpace(p.inputs[5].Value()), AutoApprove: p.autoApprove, MaxTime: strings.TrimSpace(p.inputs[6].Value()), Profile: strings.TrimSpace(p.inputs[7].Value()), FromClaude: p.fromClaude, FromCodex: p.fromCodex}
}

func (p *OMPOptionsPanel) Focus() { p.focused = true; p.syncFocus() }
func (p *OMPOptionsPanel) Blur() {
	p.focused = false
	for idx := range p.inputs {
		p.inputs[idx].Blur()
	}
}
func (p *OMPOptionsPanel) IsFocused() bool { return p.focused }
func (p *OMPOptionsPanel) AtTop() bool     { return p.cursor == 0 }
func (p *OMPOptionsPanel) FocusedLine() int {
	if !p.focused {
		return -1
	}
	return p.cursor + 1
}
func (p *OMPOptionsPanel) syncFocus() {
	for idx := range p.inputs {
		if p.focused && p.cursor == idx+2 {
			p.inputs[idx].Focus()
		} else {
			p.inputs[idx].Blur()
		}
	}
}

func (p *OMPOptionsPanel) Update(msg tea.Msg) tea.Cmd {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	if p.cursor >= 2 && p.cursor <= 9 {
		switch key.String() {
		case "up", "shift+tab":
			p.cursor--
			p.syncFocus()
			return nil
		case "down", "tab", "enter":
			p.cursor++
			p.syncFocus()
			return nil
		}
		var cmd tea.Cmd
		p.inputs[p.cursor-2], cmd = p.inputs[p.cursor-2].Update(msg)
		return cmd
	}
	switch key.String() {
	case "up", "shift+tab":
		if p.cursor > 0 {
			p.cursor--
		}
	case "down", "tab", "enter":
		if p.cursor < 13 {
			p.cursor++
		}
	case "left":
		if p.cursor == 0 {
			p.mode = (p.mode + len(ompModes) - 1) % len(ompModes)
		}
	case "right", " ":
		switch p.cursor {
		case 0:
			p.mode = (p.mode + 1) % len(ompModes)
		case 1:
			p.noSession = !p.noSession
		case 10:
			p.printThoughts = !p.printThoughts
		case 11:
			p.autoApprove = !p.autoApprove
		case 12:
			p.fromClaude = !p.fromClaude
		case 13:
			p.fromCodex = !p.fromCodex
		}
	}
	p.syncFocus()
	return nil
}

func (p *OMPOptionsPanel) View() string {
	header := lipgloss.NewStyle().Foreground(ColorComment).Render("─ OMP Harness Options ─") + "\n"
	line := func(idx int, value string) string {
		marker := "  "
		if p.focused && p.cursor == idx {
			marker = "▶ "
		}
		return marker + value + "\n"
	}
	out := header + line(0, "Session mode: "+ompModes[p.mode]+"  (←/→)") + line(1, ompCheckbox("No session (ephemeral)", p.noSession))
	for idx := range p.inputs {
		out += line(idx+2, ompInputLabels[idx]+": "+p.inputs[idx].View())
	}
	for idx, item := range []struct {
		label string
		value bool
	}{{"Print thoughts", p.printThoughts}, {"Auto approve", p.autoApprove}, {"Import from Claude", p.fromClaude}, {"Import from Codex", p.fromCodex}} {
		out += line(idx+10, ompCheckbox(item.label, item.value))
	}
	return strings.TrimSuffix(out, "\n")
}

func ompCheckbox(label string, checked bool) string {
	mark := " "
	if checked {
		mark = "x"
	}
	return "[" + mark + "] " + label
}
