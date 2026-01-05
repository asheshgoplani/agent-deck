package ui

import (
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ForkDialog handles the fork session dialog
type ForkDialog struct {
	visible      bool
	nameInput    textinput.Model
	groupInput   textinput.Model
	optionsPanel *ClaudeOptionsPanel
	focusIndex   int // 0=name, 1=group, 2+=options
	width        int
	height       int
	projectPath  string
}

// NewForkDialog creates a new fork dialog
func NewForkDialog() *ForkDialog {
	nameInput := textinput.New()
	nameInput.Placeholder = "Session name"
	nameInput.CharLimit = 64
	nameInput.Width = 40

	groupInput := textinput.New()
	groupInput.Placeholder = "Group path (optional)"
	groupInput.CharLimit = 64
	groupInput.Width = 40

	return &ForkDialog{
		nameInput:    nameInput,
		groupInput:   groupInput,
		optionsPanel: NewClaudeOptionsPanelForFork(),
	}
}

// Show displays the dialog with pre-filled values
func (d *ForkDialog) Show(originalName, projectPath, groupPath string) {
	d.visible = true
	d.projectPath = projectPath
	d.nameInput.SetValue(originalName + " (fork)")
	d.groupInput.SetValue(groupPath)
	d.focusIndex = 0
	d.nameInput.Focus()
	d.groupInput.Blur()
	d.optionsPanel.Blur()

	// Initialize options with defaults from config
	if config, err := session.LoadUserConfig(); err == nil {
		d.optionsPanel.SetDefaults(config)
	}
}

// Hide hides the dialog
func (d *ForkDialog) Hide() {
	d.visible = false
	d.nameInput.Blur()
	d.groupInput.Blur()
	d.optionsPanel.Blur()
}

// IsVisible returns whether the dialog is visible
func (d *ForkDialog) IsVisible() bool {
	return d.visible
}

// GetValues returns the current input values
func (d *ForkDialog) GetValues() (name, group string) {
	return d.nameInput.Value(), d.groupInput.Value()
}

// GetOptions returns the current Claude options
func (d *ForkDialog) GetOptions() *session.ClaudeOptions {
	return d.optionsPanel.GetOptions()
}

// SetSize sets the dialog dimensions
func (d *ForkDialog) SetSize(width, height int) {
	d.width = width
	d.height = height
}

// Update handles input events
func (d *ForkDialog) Update(msg tea.Msg) (*ForkDialog, tea.Cmd) {
	if !d.visible {
		return d, nil
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "tab", "down":
			if d.focusIndex < 2 {
				// Move from name/group to next field or options
				d.focusIndex++
				d.updateFocus()
			} else {
				// Inside options panel - delegate
				var cmd tea.Cmd
				d.optionsPanel, cmd = d.optionsPanel.Update(msg)
				return d, cmd
			}
			return d, nil

		case "shift+tab", "up":
			if d.focusIndex == 2 && d.optionsPanel.focusIndex == 0 {
				// At first option item, move back to group
				d.focusIndex = 1
				d.updateFocus()
			} else if d.focusIndex < 2 {
				d.focusIndex--
				if d.focusIndex < 0 {
					d.focusIndex = 0
				}
				d.updateFocus()
			} else {
				// Inside options panel - delegate
				var cmd tea.Cmd
				d.optionsPanel, cmd = d.optionsPanel.Update(msg)
				return d, cmd
			}
			return d, nil

		case "esc":
			d.Hide()
			return d, nil

		case "enter":
			if d.nameInput.Value() != "" {
				return d, nil // Signal completion
			}

		case " ", "left", "right":
			// Delegate space/arrow keys to options panel if focused there
			if d.focusIndex >= 2 {
				var cmd tea.Cmd
				d.optionsPanel, cmd = d.optionsPanel.Update(msg)
				return d, cmd
			}
		}
	}

	// Update focused input
	var cmd tea.Cmd
	switch d.focusIndex {
	case 0:
		d.nameInput, cmd = d.nameInput.Update(msg)
	case 1:
		d.groupInput, cmd = d.groupInput.Update(msg)
	default:
		// Options panel handles its own inputs
		d.optionsPanel, cmd = d.optionsPanel.Update(msg)
	}

	return d, cmd
}

func (d *ForkDialog) updateFocus() {
	d.nameInput.Blur()
	d.groupInput.Blur()
	d.optionsPanel.Blur()

	switch d.focusIndex {
	case 0:
		d.nameInput.Focus()
	case 1:
		d.groupInput.Focus()
	default:
		d.optionsPanel.Focus()
	}
}

// View renders the dialog
func (d *ForkDialog) View() string {
	if !d.visible {
		return ""
	}

	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorCyan)

	labelStyle := lipgloss.NewStyle().
		Foreground(ColorText)

	activeLabelStyle := lipgloss.NewStyle().
		Foreground(ColorAccent).
		Bold(true)

	// Responsive dialog width
	dialogWidth := 50
	if d.width > 0 && d.width < dialogWidth+10 {
		dialogWidth = d.width - 10
		if dialogWidth < 35 {
			dialogWidth = 35
		}
	}

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorAccent).
		Padding(1, 2).
		Width(dialogWidth)

	// Build content
	var nameLabel, groupLabel string
	if d.focusIndex == 0 {
		nameLabel = activeLabelStyle.Render("▶ Name:")
		groupLabel = labelStyle.Render("  Group:")
	} else if d.focusIndex == 1 {
		nameLabel = labelStyle.Render("  Name:")
		groupLabel = activeLabelStyle.Render("▶ Group:")
	} else {
		nameLabel = labelStyle.Render("  Name:")
		groupLabel = labelStyle.Render("  Group:")
	}

	content := titleStyle.Render("Fork Session") + "\n\n" +
		nameLabel + "\n" +
		"  " + d.nameInput.View() + "\n\n" +
		groupLabel + "\n" +
		"  " + d.groupInput.View() + "\n\n" +
		d.optionsPanel.View() + "\n" +
		lipgloss.NewStyle().Foreground(ColorComment).
			Render("Enter create │ Esc cancel │ Tab next │ Space toggle")

	dialog := boxStyle.Render(content)

	// Center the dialog on screen
	return lipgloss.Place(d.width, d.height, lipgloss.Center, lipgloss.Center, dialog)
}
