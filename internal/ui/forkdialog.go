package ui

import (
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ForkDialog handles the fork session dialog
type ForkDialog struct {
	visible     bool
	nameInput   textinput.Model
	groupInput  textinput.Model
	focusIndex  int
	width       int
	height      int
	projectPath string
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
		nameInput:  nameInput,
		groupInput: groupInput,
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
}

// Hide hides the dialog
func (d *ForkDialog) Hide() {
	d.visible = false
	d.nameInput.Blur()
	d.groupInput.Blur()
}

// IsVisible returns whether the dialog is visible
func (d *ForkDialog) IsVisible() bool {
	return d.visible
}

// GetValues returns the current input values
func (d *ForkDialog) GetValues() (name, group string) {
	return d.nameInput.Value(), d.groupInput.Value()
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
			d.focusIndex = (d.focusIndex + 1) % 2
			d.updateFocus()
		case "shift+tab", "up":
			d.focusIndex = (d.focusIndex - 1)
			if d.focusIndex < 0 {
				d.focusIndex = 1
			}
			d.updateFocus()
		case "esc":
			d.Hide()
			return d, nil
		case "enter":
			if d.nameInput.Value() != "" {
				return d, nil // Signal completion
			}
		}
	}

	var cmd tea.Cmd
	if d.focusIndex == 0 {
		d.nameInput, cmd = d.nameInput.Update(msg)
	} else {
		d.groupInput, cmd = d.groupInput.Update(msg)
	}

	return d, cmd
}

func (d *ForkDialog) updateFocus() {
	if d.focusIndex == 0 {
		d.nameInput.Focus()
		d.groupInput.Blur()
	} else {
		d.nameInput.Blur()
		d.groupInput.Focus()
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

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorAccent).
		Padding(1, 2).
		Width(50)

	// Build content
	var nameLabel, groupLabel string
	if d.focusIndex == 0 {
		nameLabel = activeLabelStyle.Render("▶ Name:")
		groupLabel = labelStyle.Render("  Group:")
	} else {
		nameLabel = labelStyle.Render("  Name:")
		groupLabel = activeLabelStyle.Render("▶ Group:")
	}

	content := titleStyle.Render("Fork Session") + "\n\n" +
		nameLabel + "\n" +
		d.nameInput.View() + "\n\n" +
		groupLabel + "\n" +
		d.groupInput.View() + "\n\n" +
		lipgloss.NewStyle().Foreground(ColorComment).
			Render("Enter create │ Esc cancel │ Tab next")

	dialog := boxStyle.Render(content)

	// Center the dialog on screen
	return lipgloss.Place(d.width, d.height, lipgloss.Center, lipgloss.Center, dialog)
}
