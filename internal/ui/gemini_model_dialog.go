package ui

import (
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// modelsFetchedMsg is sent when async model list fetch completes
type modelsFetchedMsg struct {
	models []string
	err    error
}

// modelSelectedMsg is sent when user selects a model
type modelSelectedMsg struct {
	model      string
	instanceID string
}

// GeminiModelDialog allows selecting a Gemini model for the current session
type GeminiModelDialog struct {
	visible    bool
	width      int
	height     int
	cursor     int
	models     []string
	loading    bool
	err        error
	instanceID string // ID of the session to change model for
	current    string // Currently active model
}

// NewGeminiModelDialog creates a new model selection dialog
func NewGeminiModelDialog() *GeminiModelDialog {
	return &GeminiModelDialog{}
}

// Show opens the dialog and triggers async model fetching
func (d *GeminiModelDialog) Show(instanceID, currentModel string) tea.Cmd {
	d.visible = true
	d.cursor = 0
	d.models = nil
	d.loading = true
	d.err = nil
	d.instanceID = instanceID
	d.current = currentModel

	return func() tea.Msg {
		models, err := session.GetAvailableGeminiModels()
		return modelsFetchedMsg{models: models, err: err}
	}
}

// Hide closes the dialog
func (d *GeminiModelDialog) Hide() {
	d.visible = false
	d.loading = false
}

// IsVisible returns whether the dialog is visible
func (d *GeminiModelDialog) IsVisible() bool {
	return d.visible
}

// SetSize updates the dialog dimensions
func (d *GeminiModelDialog) SetSize(width, height int) {
	d.width = width
	d.height = height
}

// HandleModelsFetched processes the async model fetch result
func (d *GeminiModelDialog) HandleModelsFetched(msg modelsFetchedMsg) {
	d.loading = false
	d.err = msg.err
	d.models = msg.models

	// Position cursor on current model
	for i, m := range d.models {
		if m == d.current {
			d.cursor = i
			break
		}
	}
}

// Update handles input for the dialog
func (d *GeminiModelDialog) Update(msg tea.KeyMsg) (*GeminiModelDialog, tea.Cmd) {
	if !d.visible {
		return d, nil
	}

	key := msg.String()
	switch key {
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
		if len(d.models) > 0 && d.cursor >= 0 && d.cursor < len(d.models) {
			selected := d.models[d.cursor]
			instanceID := d.instanceID
			d.Hide()
			return d, func() tea.Msg {
				return modelSelectedMsg{model: selected, instanceID: instanceID}
			}
		}
	}

	return d, nil
}

// View renders the dialog
func (d *GeminiModelDialog) View() string {
	if !d.visible {
		return ""
	}

	// Styles
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorCyan)
	dimStyle := lipgloss.NewStyle().
		Foreground(ColorComment)
	selectedStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorAccent)
	currentStyle := lipgloss.NewStyle().
		Foreground(ColorGreen)
	errorStyle := lipgloss.NewStyle().
		Foreground(ColorRed)

	dialogWidth := 50
	if d.width > 0 && d.width < dialogWidth+10 {
		dialogWidth = d.width - 10
		if dialogWidth < 35 {
			dialogWidth = 35
		}
	}

	var content strings.Builder

	// Title
	content.WriteString(titleStyle.Render("Select Gemini Model"))
	content.WriteString(dimStyle.Render("            [Esc] Cancel"))
	content.WriteString("\n")
	content.WriteString(strings.Repeat("-", dialogWidth-4))
	content.WriteString("\n\n")

	if d.loading {
		content.WriteString(dimStyle.Render("  Loading models..."))
		content.WriteString("\n")
	} else if d.err != nil {
		content.WriteString(errorStyle.Render("  Error: " + d.err.Error()))
		content.WriteString("\n\n")
		// Still show models if we have fallback
		if len(d.models) > 0 {
			content.WriteString(dimStyle.Render("  Showing fallback models:"))
			content.WriteString("\n\n")
		}
	}

	// Model list
	maxVisible := 15
	if d.height > 0 {
		maxVisible = d.height/2 - 6
		if maxVisible < 5 {
			maxVisible = 5
		}
	}

	// Calculate scroll window
	start := 0
	if d.cursor >= maxVisible {
		start = d.cursor - maxVisible + 1
	}
	end := start + maxVisible
	if end > len(d.models) {
		end = len(d.models)
	}

	for i := start; i < end; i++ {
		model := d.models[i]
		prefix := "  "
		if i == d.cursor {
			prefix = "> "
		}

		line := prefix + model
		if model == d.current {
			line += " (current)"
		}

		if i == d.cursor {
			content.WriteString(selectedStyle.Render(line))
		} else if model == d.current {
			content.WriteString(currentStyle.Render(line))
		} else {
			content.WriteString(line)
		}
		content.WriteString("\n")
	}

	if len(d.models) > maxVisible {
		content.WriteString("\n")
		content.WriteString(dimStyle.Render("  " + strings.Repeat(".", 3) + " scroll for more"))
		content.WriteString("\n")
	}

	content.WriteString("\n")
	content.WriteString(dimStyle.Render("j/k Navigate  Enter Select  Esc Cancel"))

	// Wrap in dialog box
	dialogStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorCyan).
		Background(ColorBg).
		Padding(1, 2).
		Width(dialogWidth)

	dialog := dialogStyle.Render(content.String())

	// Center the dialog
	return lipgloss.Place(
		d.width,
		d.height,
		lipgloss.Center,
		lipgloss.Center,
		dialog,
	)
}
