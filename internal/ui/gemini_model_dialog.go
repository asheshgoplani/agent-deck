package ui

import (
	"fmt"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type GeminiModelDialog struct {
	visible   bool
	width     int
	height    int
	sessionID string
	models    []string
	cursor    int
	err       error
	loading   bool
}

func NewGeminiModelDialog() *GeminiModelDialog {
	return &GeminiModelDialog{}
}

func (d *GeminiModelDialog) Show(sessionID string) tea.Cmd {
	d.sessionID = sessionID
	d.visible = true
	d.loading = true
	d.models = nil
	d.cursor = 0
	d.err = nil

	return func() tea.Msg {
		models, err := session.GetAvailableGeminiModels()
		return modelsFetchedMsg{models: models, err: err}
	}
}

type modelsFetchedMsg struct {
	models []string
	err    error
}

func (d *GeminiModelDialog) Hide() {
	d.visible = false
}

func (d *GeminiModelDialog) IsVisible() bool {
	return d.visible
}

func (d *GeminiModelDialog) Update(msg tea.Msg) (*GeminiModelDialog, tea.Cmd) {
	if !d.visible {
		return d, nil
	}

	switch msg := msg.(type) {
	case modelsFetchedMsg:
		d.loading = false
		if msg.err != nil {
			d.err = msg.err
		} else {
			d.models = msg.models
		}
		return d, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "esc":
			d.Hide()
			return d, nil

		case "up", "k":
			if d.cursor > 0 {
				d.cursor--
			}

		case "down", "j":
			if d.cursor < len(d.models)-1 {
				d.cursor++
			}

		case "enter":
			if len(d.models) > 0 {
				model := d.models[d.cursor]
				d.Hide()
				return d, func() tea.Msg {
					return modelSelectedMsg{sessionID: d.sessionID, model: model}
				}
			}
		}
	}

	return d, nil
}

type modelSelectedMsg struct {
	sessionID string
	model     string
}

func (d *GeminiModelDialog) View() string {
	if !d.visible {
		return ""
	}

	var content strings.Builder
	titleStyle := DialogTitleStyle.Width(40)
	content.WriteString(titleStyle.Render("Change Gemini Model"))
	content.WriteString("\n\n")

	if d.loading {
		content.WriteString(lipgloss.NewStyle().Italic(true).Render("  Fetching available models..."))
	} else if d.err != nil {
		content.WriteString(lipgloss.NewStyle().Foreground(ColorRed).Render(fmt.Sprintf("  Error: %v", d.err)))
	} else if len(d.models) == 0 {
		content.WriteString(lipgloss.NewStyle().Italic(true).Render("  No models found."))
	} else {
		for i, model := range d.models {
			prefix := "  "
			style := lipgloss.NewStyle().Foreground(ColorText)
			if i == d.cursor {
				prefix = "> "
				style = lipgloss.NewStyle().Background(ColorAccent).Foreground(ColorBg).Bold(true)
			}
			content.WriteString(style.Width(38).Render(prefix + model) + "\n")
		}
	}

	content.WriteString("\n")
	hintStyle := lipgloss.NewStyle().Foreground(ColorComment)
	content.WriteString(hintStyle.Render("  [Enter] Select  [Esc] Cancel"))

	dialog := DialogBoxStyle.Width(44).Render(content.String())

	return lipgloss.Place(
		d.width,
		d.height,
		lipgloss.Center,
		lipgloss.Center,
		dialog,
	)
}

func (d *GeminiModelDialog) SetSize(width, height int) {
	d.width = width
	d.height = height
}
