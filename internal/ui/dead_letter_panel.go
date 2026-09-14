package ui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// DeadLetterPanel is the TUI counterpart of `inbox dead-letter`: it lists and
// inspects bounded metadata, retries one record, and purges one record only
// after an in-panel confirmation.
type DeadLetterPanel struct {
	visible      bool
	width        int
	height       int
	cursor       int
	detail       bool
	confirmPurge bool
	records      []session.DeadLetterRecord
	status       string
	loadErr      string
}

func NewDeadLetterPanel() *DeadLetterPanel { return &DeadLetterPanel{} }

func (p *DeadLetterPanel) Show() {
	if p == nil {
		return
	}
	p.visible = true
	p.detail = false
	p.confirmPurge = false
	p.status = ""
	p.refresh()
}

func (p *DeadLetterPanel) Hide() {
	if p != nil {
		p.visible = false
	}
}

func (p *DeadLetterPanel) IsVisible() bool { return p != nil && p.visible }

func (p *DeadLetterPanel) SetSize(width, height int) {
	if p != nil {
		p.width, p.height = width, height
	}
}

func (p *DeadLetterPanel) refresh() {
	records, err := session.ListDeadLetters()
	if err != nil {
		p.loadErr = err.Error()
		return
	}
	p.loadErr = ""
	p.records = records
	if p.cursor >= len(p.records) {
		p.cursor = len(p.records) - 1
	}
	if p.cursor < 0 {
		p.cursor = 0
	}
}

func (p *DeadLetterPanel) selected() *session.DeadLetterRecord {
	if p == nil || p.cursor < 0 || p.cursor >= len(p.records) {
		return nil
	}
	return &p.records[p.cursor]
}

func (p *DeadLetterPanel) Update(msg tea.Msg) (*DeadLetterPanel, tea.Cmd) {
	if p == nil || !p.visible {
		return p, nil
	}
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return p, nil
	}
	if p.confirmPurge {
		switch key.String() {
		case "y", "Y":
			record := p.selected()
			p.confirmPurge = false
			if record == nil {
				return p, nil
			}
			if err := session.PurgeDeadLetter(record.ID); err != nil {
				p.status = "Purge failed: " + err.Error()
			} else {
				p.status = "Purged " + record.ID
				p.detail = false
				p.refresh()
			}
		case "n", "N", "esc":
			p.confirmPurge = false
		}
		return p, nil
	}

	switch key.String() {
	case "esc", "q", "alt+d":
		if p.detail {
			p.detail = false
		} else {
			p.Hide()
		}
	case "j", "down", "ctrl+n":
		if !p.detail && p.cursor+1 < len(p.records) {
			p.cursor++
		}
	case "k", "up", "ctrl+p":
		if !p.detail && p.cursor > 0 {
			p.cursor--
		}
	case "enter", "l":
		if p.selected() != nil {
			p.detail = true
		}
	case "h", "backspace":
		p.detail = false
	case "r":
		record := p.selected()
		if record == nil {
			return p, nil
		}
		target, err := session.RetryDeadLetter(record.ID)
		if err != nil {
			p.status = err.Error()
		} else {
			p.status = fmt.Sprintf("Delivered %s to %s", record.ID, target)
			p.detail = false
			p.refresh()
		}
	case "d":
		if p.selected() != nil {
			p.confirmPurge = true
		}
	}
	return p, nil
}

func (p *DeadLetterPanel) View() string {
	if p == nil || !p.visible {
		return ""
	}
	width := p.width - 6
	if width < 48 {
		width = 48
	}
	title := lipgloss.NewStyle().Bold(true).Foreground(ColorYellow).Render("Dead-letter events")
	dim := lipgloss.NewStyle().Foreground(ColorTextDim)
	var b strings.Builder
	b.WriteString(title)
	b.WriteString("\n")
	b.WriteString(dim.Render("Bounded metadata only — prompt and output content are never shown."))
	b.WriteString("\n\n")
	if p.loadErr != "" {
		b.WriteString(lipgloss.NewStyle().Foreground(ColorRed).Render("Unable to read records: " + p.loadErr))
	} else if len(p.records) == 0 {
		b.WriteString("No dead-lettered events.\n")
	} else if p.detail {
		b.WriteString(p.renderDetail())
	} else {
		for i, record := range p.records {
			cursor := "  "
			if i == p.cursor {
				cursor = "> "
			}
			age := "now"
			if record.AgeSeconds > 0 {
				age = (time.Duration(record.AgeSeconds) * time.Second).Round(time.Second).String()
			}
			line := fmt.Sprintf("%s%-16s %-15s %-9s %s", cursor, record.ID, record.Reason, age, record.ChildSessionID)
			b.WriteString(truncateStr(line, width))
			b.WriteByte('\n')
		}
	}
	if p.status != "" {
		b.WriteString("\n")
		b.WriteString(truncateStr(p.status, width))
		b.WriteString("\n")
	}
	if p.confirmPurge {
		b.WriteString("\nPurge only this record? y confirm / n cancel\n")
	}
	b.WriteString("\n")
	b.WriteString(dim.Render("j/k navigate  enter show  r retry  d purge  esc close"))

	box := lipgloss.NewStyle().
		Width(width).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorBorder).
		Padding(1, 2).
		Render(b.String())
	return lipgloss.Place(p.width, p.height, lipgloss.Center, lipgloss.Center, box)
}

func (p *DeadLetterPanel) renderDetail() string {
	record := p.selected()
	if record == nil {
		return "No record selected.\n"
	}
	return fmt.Sprintf("ID: %s\nStore: %s\nChild: %s\nTitle: %s\nTarget: %s\nProfile: %s\nReason: %s\nAge: %ds\nAttempts: %d\nPayload: %s\n",
		record.ID, record.Store, record.ChildSessionID, record.ChildTitle,
		record.TargetSessionID, record.Profile, record.Reason, record.AgeSeconds,
		record.Attempts, record.PayloadSummary)
}
