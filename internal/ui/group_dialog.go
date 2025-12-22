package ui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// GroupDialogMode represents the dialog mode
type GroupDialogMode int

const (
	GroupDialogCreate GroupDialogMode = iota
	GroupDialogRename
	GroupDialogMove
	GroupDialogRenameSession
)

// GroupDialog handles group creation, renaming, and moving sessions
type GroupDialog struct {
	visible    bool
	mode       GroupDialogMode
	nameInput  textinput.Model
	width      int
	height     int
	groupPath  string   // Current group being edited (for rename) or parent path (for create subgroup)
	parentName string   // Display name of parent group (for subgroup creation)
	groupNames []string // Available groups (for move)
	selected   int      // Selected group index (for move)
	sessionID  string   // Session ID being renamed (for rename session)
}

// NewGroupDialog creates a new group dialog
func NewGroupDialog() *GroupDialog {
	ti := textinput.New()
	ti.Placeholder = "Group name"
	ti.CharLimit = 50
	ti.Width = 30

	return &GroupDialog{
		nameInput:  ti,
		groupNames: []string{},
	}
}

// Show shows the dialog in create mode (root level group)
func (g *GroupDialog) Show() {
	g.visible = true
	g.mode = GroupDialogCreate
	g.groupPath = "" // No parent = root level
	g.parentName = ""
	g.nameInput.SetValue("")
	g.nameInput.Focus()
}

// ShowCreateSubgroup shows the dialog for creating a subgroup under a parent
func (g *GroupDialog) ShowCreateSubgroup(parentPath, parentName string) {
	g.visible = true
	g.mode = GroupDialogCreate
	g.groupPath = parentPath // Parent path for the new subgroup
	g.parentName = parentName
	g.nameInput.SetValue("")
	g.nameInput.Focus()
}

// ShowRename shows the dialog in rename mode
func (g *GroupDialog) ShowRename(currentPath, currentName string) {
	g.visible = true
	g.mode = GroupDialogRename
	g.groupPath = currentPath
	g.nameInput.SetValue(currentName)
	g.nameInput.Focus()
}

// ShowMove shows the dialog for moving a session to a group
func (g *GroupDialog) ShowMove(groups []string) {
	g.visible = true
	g.mode = GroupDialogMove
	g.groupNames = groups
	g.selected = 0
}

// ShowRenameSession shows the dialog for renaming a session
func (g *GroupDialog) ShowRenameSession(sessionID, currentName string) {
	g.visible = true
	g.mode = GroupDialogRenameSession
	g.sessionID = sessionID
	g.nameInput.SetValue(currentName)
	g.nameInput.Focus()
}

// GetSessionID returns the session ID being renamed
func (g *GroupDialog) GetSessionID() string {
	return g.sessionID
}

// Hide hides the dialog
func (g *GroupDialog) Hide() {
	g.visible = false
	g.nameInput.Blur()
}

// IsVisible returns whether the dialog is visible
func (g *GroupDialog) IsVisible() bool {
	return g.visible
}

// Mode returns the current dialog mode
func (g *GroupDialog) Mode() GroupDialogMode {
	return g.mode
}

// GetValue returns the input value
func (g *GroupDialog) GetValue() string {
	return strings.TrimSpace(g.nameInput.Value())
}

// Validate checks if the dialog values are valid and returns an error message if not
func (g *GroupDialog) Validate() string {
	if g.mode == GroupDialogMove {
		return "" // Move mode doesn't need validation
	}

	name := strings.TrimSpace(g.nameInput.Value())

	// Check for empty name
	if name == "" {
		if g.mode == GroupDialogRenameSession {
			return "Session name cannot be empty"
		}
		return "Group name cannot be empty"
	}

	// Check name length
	if len(name) > 50 {
		return "Name too long (max 50 characters)"
	}

	// Check for "/" in group names (would break path hierarchy)
	if g.mode == GroupDialogCreate || g.mode == GroupDialogRename {
		if strings.Contains(name, "/") {
			return "Group name cannot contain '/' character"
		}
	}

	return "" // Valid
}

// GetGroupPath returns the group path being edited (or parent path for subgroup creation)
func (g *GroupDialog) GetGroupPath() string {
	return g.groupPath
}

// GetParentPath returns the parent path for subgroup creation
func (g *GroupDialog) GetParentPath() string {
	return g.groupPath
}

// HasParent returns true if creating a subgroup under a parent
func (g *GroupDialog) HasParent() bool {
	return g.groupPath != "" && g.mode == GroupDialogCreate
}

// GetSelectedGroup returns the selected group for move mode
func (g *GroupDialog) GetSelectedGroup() string {
	if g.selected >= 0 && g.selected < len(g.groupNames) {
		return g.groupNames[g.selected]
	}
	return ""
}

// SetSize sets the dialog size
func (g *GroupDialog) SetSize(width, height int) {
	g.width = width
	g.height = height
}

// Update handles input
func (g *GroupDialog) Update(msg tea.KeyMsg) (*GroupDialog, tea.Cmd) {
	if g.mode == GroupDialogMove {
		switch msg.String() {
		case "up", "k":
			if g.selected > 0 {
				g.selected--
			}
		case "down", "j":
			if g.selected < len(g.groupNames)-1 {
				g.selected++
			}
		}
		return g, nil
	}

	var cmd tea.Cmd
	g.nameInput, cmd = g.nameInput.Update(msg)
	return g, cmd
}

// View renders the dialog
func (g *GroupDialog) View() string {
	if !g.visible {
		return ""
	}

	var title string
	var content string

	switch g.mode {
	case GroupDialogCreate:
		if g.parentName != "" {
			title = "Create Subgroup"
			parentInfo := lipgloss.NewStyle().
				Foreground(ColorCyan).
				Render("Parent: " + g.parentName)
			content = parentInfo + "\n\n" + g.nameInput.View()
		} else {
			title = "Create New Group"
			content = g.nameInput.View()
		}
	case GroupDialogRename:
		title = "Rename Group"
		content = g.nameInput.View()
	case GroupDialogMove:
		title = "Move to Group"
		var items []string
		for i, name := range g.groupNames {
			if i == g.selected {
				items = append(items, lipgloss.NewStyle().
					Foreground(ColorBg).
					Background(ColorAccent).
					Bold(true).
					Padding(0, 1).
					Render(name))
			} else {
				items = append(items, lipgloss.NewStyle().
					Foreground(ColorText).
					Padding(0, 1).
					Render(name))
			}
		}
		content = strings.Join(items, "\n")
	case GroupDialogRenameSession:
		title = "Rename Session"
		content = g.nameInput.View()
	}

	titleStyle := DialogTitleStyle.Width(40)
	hintStyle := lipgloss.NewStyle().Foreground(ColorComment)
	hint := hintStyle.Render("Enter confirm │ Esc cancel")

	dialogContent := lipgloss.JoinVertical(
		lipgloss.Center,
		titleStyle.Render(title),
		"",
		content,
		"",
		hint,
	)

	dialog := DialogBoxStyle.
		Width(44).
		Render(dialogContent)

	// Center the dialog
	return lipgloss.Place(
		g.width,
		g.height,
		lipgloss.Center,
		lipgloss.Center,
		dialog,
	)
}
