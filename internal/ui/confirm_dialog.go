package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ConfirmType indicates what action is being confirmed
type ConfirmType int

const (
	ConfirmDeleteSession ConfirmType = iota
	ConfirmDeleteGroup
	ConfirmRestart
	ConfirmYoloRestart
)
// ConfirmDialog handles confirmation for destructive actions
type ConfirmDialog struct {
	visible     bool
	confirmType ConfirmType
	targetID    string // Session ID or group path
	targetName  string // Display name
	yoloEnabled bool   // For ConfirmYoloRestart
	width       int
	height      int
}
// NewConfirmDialog creates a new confirmation dialog
func NewConfirmDialog() *ConfirmDialog {
	return &ConfirmDialog{}
}

// ShowDeleteSession shows confirmation for session deletion
func (c *ConfirmDialog) ShowDeleteSession(sessionID, sessionName string) {
	c.visible = true
	c.confirmType = ConfirmDeleteSession
	c.targetID = sessionID
	c.targetName = sessionName
}

// ShowDeleteGroup shows confirmation for group deletion
func (c *ConfirmDialog) ShowDeleteGroup(groupPath, groupName string) {
	c.visible = true
	c.confirmType = ConfirmDeleteGroup
	c.targetID = groupPath
	c.targetName = groupName
}

// ShowRestart shows confirmation for session restart
func (c *ConfirmDialog) ShowRestart(sessionID, sessionName string) {
	c.visible = true
	c.confirmType = ConfirmRestart
	c.targetID = sessionID
	c.targetName = sessionName
}

// ShowYoloRestart shows confirmation for session restart with YOLO toggle
func (c *ConfirmDialog) ShowYoloRestart(sessionID, sessionName string, yoloEnabled bool) {
	c.visible = true
	c.confirmType = ConfirmYoloRestart
	c.targetID = sessionID
	c.targetName = sessionName
	c.yoloEnabled = yoloEnabled
}

// IsYoloEnabled returns whether YOLO was toggled (for ConfirmYoloRestart)
func (c *ConfirmDialog) IsYoloEnabled() bool {
	return c.yoloEnabled
}

// Hide hides the dialog
func (c *ConfirmDialog) Hide() {
	c.visible = false
	c.targetID = ""
	c.targetName = ""
}
// IsVisible returns whether the dialog is visible
func (c *ConfirmDialog) IsVisible() bool {
	return c.visible
}

// GetTargetID returns the session ID or group path being confirmed
func (c *ConfirmDialog) GetTargetID() string {
	return c.targetID
}

// GetConfirmType returns the type of confirmation
func (c *ConfirmDialog) GetConfirmType() ConfirmType {
	return c.confirmType
}

// SetSize updates dialog dimensions
func (c *ConfirmDialog) SetSize(width, height int) {
	c.width = width
	c.height = height
}

// Update handles key events
func (c *ConfirmDialog) Update(msg tea.KeyMsg) (*ConfirmDialog, tea.Cmd) {
	// Dialog handles y/n/enter/esc only
	return c, nil
}

// View renders the confirmation dialog
func (c *ConfirmDialog) View() string {
	if !c.visible {
		return ""
	}

	// Build warning message based on action type
	var title, warning, details string

		switch c.confirmType {
		case ConfirmDeleteSession:
			title = "⚠️  Delete Session?"
			warning = fmt.Sprintf("This will PERMANENTLY KILL the tmux session:\n\n  \"%s\"", c.targetName)
			details = "• The tmux session will be terminated\n• Any running processes will be killed\n• Terminal history will be lost\n• This cannot be undone"
	
		case ConfirmDeleteGroup:
			title = "⚠️  Delete Group?"
			warning = fmt.Sprintf("This will delete the group:\n\n  \"%s\"", c.targetName)
			details = "• All sessions will be MOVED to 'default' group\n• Sessions will NOT be killed\n• The group structure will be lost"
	
		case ConfirmRestart:
			title = "🔄  Restart Session?"
			warning = fmt.Sprintf("This will RESTART the session:\n\n  \"%s\"", c.targetName)
			details = "• The existing tmux session will be killed\n• A new session will be started immediately\n• Any unsaved work in the agent will be lost"
	
		case ConfirmYoloRestart:
			mode := "ON"
			if !c.yoloEnabled {
				mode = "OFF"
			}
			title = "⚡  Toggle YOLO Mode?"
			warning = fmt.Sprintf("Restart \"%s\" with YOLO mode %s?", c.targetName, mode)
			details = "• Session will restart to apply the new mode\n• YOLO mode skips all security confirmations\n• Use only in trusted environments"
		}
	
		// Styles
		accentColor := ColorRed
		if c.confirmType == ConfirmRestart || c.confirmType == ConfirmYoloRestart {
			accentColor = ColorCyan
		}
	
		titleStyle := lipgloss.NewStyle().
			Bold(true).
			Foreground(accentColor).
			MarginBottom(1)
	
		warningStyle := lipgloss.NewStyle().
			Foreground(ColorYellow).
			MarginBottom(1)
	
		detailsStyle := lipgloss.NewStyle().
			Foreground(ColorTextDim).
			MarginBottom(1)
	
		btnText := "y Delete"
		if c.confirmType == ConfirmRestart || c.confirmType == ConfirmYoloRestart {
			btnText = "y Restart"
		}
	
		buttonYes := lipgloss.NewStyle().
			Foreground(ColorBg).
			Background(accentColor).
			Padding(0, 2).
			Bold(true).
			Render(btnText)
	
		buttonNo := lipgloss.NewStyle().
			Foreground(ColorBg).
			Background(ColorAccent).
			Padding(0, 2).
			Bold(true).
			Render("n Cancel")
	
		escHint := lipgloss.NewStyle().
			Foreground(ColorTextDim).
			Render("(Esc to cancel)")
	
		// Build content
		content := lipgloss.JoinVertical(lipgloss.Left,
			titleStyle.Render(title),
			warningStyle.Render(warning),
			detailsStyle.Render(details),
			"",
			lipgloss.JoinHorizontal(lipgloss.Center, buttonYes, "  ", buttonNo, "  ", escHint),
		)
	
		// Dialog box
		dialogWidth := 50
		if c.width > 0 && c.width < dialogWidth+10 {
			dialogWidth = c.width - 10
		}
	
		dialogBox := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(accentColor).
			Padding(1, 2).
			Width(dialogWidth).
			Render(content)
		// Center in screen
	if c.width > 0 && c.height > 0 {
		// Create full-screen overlay with centered dialog
		dialogHeight := lipgloss.Height(dialogBox)
		dialogWidth := lipgloss.Width(dialogBox)

		padLeft := (c.width - dialogWidth) / 2
		if padLeft < 0 {
			padLeft = 0
		}
		padTop := (c.height - dialogHeight) / 2
		if padTop < 0 {
			padTop = 0
		}

		// Build centered dialog
		var b strings.Builder
		for i := 0; i < padTop; i++ {
			b.WriteString("\n")
		}
		for _, line := range strings.Split(dialogBox, "\n") {
			b.WriteString(strings.Repeat(" ", padLeft))
			b.WriteString(line)
			b.WriteString("\n")
		}

		return b.String()
	}

	return dialogBox
}
