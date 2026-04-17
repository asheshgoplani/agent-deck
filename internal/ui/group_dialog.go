package ui

import (
	"fmt"
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
	visible       bool
	mode          GroupDialogMode
	nameInput     textinput.Model
	width         int
	height        int
	groupPath     string   // Current group being edited (for rename) or parent path (for create subgroup)
	parentName    string   // Display name of parent group (for subgroup creation)
	groupPaths    []string // Available target group paths (for move)
	selected      int      // Selected group index (for move)
	sessionID     string   // Session ID being renamed (for rename session)
	validationErr string   // Inline validation error displayed inside the dialog

	// Tab toggle between Root and Subgroup modes (Issue #111)
	contextParentPath string // Original cursor context parent path (for toggling back)
	contextParentName string // Original cursor context parent name (for toggling back)
}

// NewGroupDialog creates a new group dialog
func NewGroupDialog() *GroupDialog {
	ti := textinput.New()
	ti.Placeholder = "Group name"
	ti.CharLimit = 50
	ti.Width = 30

	return &GroupDialog{
		nameInput:  ti,
		groupPaths: []string{},
	}
}

// Show shows the dialog in create mode (root level group)
func (g *GroupDialog) Show() {
	g.visible = true
	g.mode = GroupDialogCreate
	g.groupPath = "" // No parent = root level
	g.parentName = ""
	g.validationErr = ""
	g.nameInput.SetValue("")
	g.nameInput.CursorEnd() // Issue #604: reset cursor — SetValue only clamps, it does not reset.
	g.nameInput.Focus()
}

// ShowCreateSubgroup shows the dialog for creating a subgroup under a parent
func (g *GroupDialog) ShowCreateSubgroup(parentPath, parentName string) {
	g.visible = true
	g.mode = GroupDialogCreate
	g.groupPath = parentPath // Parent path for the new subgroup
	g.parentName = parentName
	g.validationErr = ""
	g.nameInput.SetValue("")
	g.nameInput.CursorEnd() // Issue #604
	g.nameInput.Focus()
}

// ShowCreateWithContext opens the create dialog with cursor context for Tab toggling.
// If parentPath is non-empty, defaults to subgroup mode with Tab toggle available.
// If parentPath is empty, opens as root-level group with no toggle.
func (g *GroupDialog) ShowCreateWithContext(parentPath, parentName string) {
	g.visible = true
	g.mode = GroupDialogCreate
	g.contextParentPath = parentPath
	g.contextParentName = parentName
	g.validationErr = ""
	g.nameInput.SetValue("")
	g.nameInput.CursorEnd() // Issue #604
	g.nameInput.Focus()

	if parentPath != "" {
		// Default to subgroup mode
		g.groupPath = parentPath
		g.parentName = parentName
	} else {
		// Root mode, no toggle
		g.groupPath = ""
		g.parentName = ""
	}
}

// ShowCreateWithContextDefaultRoot opens the create dialog defaulting to root mode,
// but stores the cursor context so Tab toggle can switch to subgroup mode.
// Used when the cursor is on a session inside a group (not on the group header itself).
func (g *GroupDialog) ShowCreateWithContextDefaultRoot(parentPath, parentName string) {
	g.visible = true
	g.mode = GroupDialogCreate
	g.contextParentPath = parentPath
	g.contextParentName = parentName
	g.validationErr = ""
	g.nameInput.SetValue("")
	g.nameInput.CursorEnd() // Issue #604
	g.nameInput.Focus()

	// Default to root mode, Tab toggles to subgroup
	g.groupPath = ""
	g.parentName = ""
}

// CanToggle returns true when the Tab toggle between Root and Subgroup is available.
// Only applies in Create mode when the cursor was on a group context.
func (g *GroupDialog) CanToggle() bool {
	return g.mode == GroupDialogCreate && g.contextParentPath != ""
}

// ToggleRootSubgroup swaps between root-level and subgroup creation modes.
func (g *GroupDialog) ToggleRootSubgroup() {
	if !g.CanToggle() {
		return
	}
	if g.groupPath == "" {
		// Currently root → switch to subgroup
		g.groupPath = g.contextParentPath
		g.parentName = g.contextParentName
	} else {
		// Currently subgroup → switch to root
		g.groupPath = ""
		g.parentName = ""
	}
	g.validationErr = ""
}

// ShowRename shows the dialog in rename mode
func (g *GroupDialog) ShowRename(currentPath, currentName string) {
	g.visible = true
	g.mode = GroupDialogRename
	g.groupPath = currentPath
	g.validationErr = ""
	g.nameInput.SetValue(currentName)
	g.nameInput.CursorEnd() // Issue #604: place cursor at end of pre-filled name.
	g.nameInput.Focus()
}

// ShowMove shows the dialog for moving a session to a group path.
func (g *GroupDialog) ShowMove(groupPaths []string) {
	g.visible = true
	g.mode = GroupDialogMove
	g.validationErr = ""
	g.groupPaths = groupPaths
	g.selected = 0
}

// ShowRenameSession shows the dialog for renaming a session
func (g *GroupDialog) ShowRenameSession(sessionID, currentName string) {
	g.visible = true
	g.mode = GroupDialogRenameSession
	g.sessionID = sessionID
	g.validationErr = ""
	g.nameInput.SetValue(currentName)
	g.nameInput.CursorEnd() // Issue #604: place cursor at end of pre-filled name.
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
	if len(name) > MaxNameLength {
		return fmt.Sprintf("Name too long (max %d characters)", MaxNameLength)
	}

	// Check for "/" in group names (would break path hierarchy)
	if g.mode == GroupDialogCreate || g.mode == GroupDialogRename {
		if strings.Contains(name, "/") {
			return "Group name cannot contain '/' character"
		}
	}

	return "" // Valid
}

// SetError sets an inline validation error displayed inside the dialog
func (g *GroupDialog) SetError(msg string) {
	g.validationErr = msg
}

// ClearError clears the inline validation error
func (g *GroupDialog) ClearError() {
	g.validationErr = ""
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
	if g.selected >= 0 && g.selected < len(g.groupPaths) {
		return g.groupPaths[g.selected]
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
			if g.selected < len(g.groupPaths)-1 {
				g.selected++
			}
		}
		return g, nil
	}

	// Tab toggles between Root and Subgroup modes
	if msg.String() == "tab" && g.CanToggle() {
		g.ToggleRootSubgroup()
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

		// Add Root/Subgroup toggle indicator when Tab toggle is available
		if g.CanToggle() {
			activeStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)
			dimStyle := lipgloss.NewStyle().Foreground(ColorTextDim)

			rootTab := "Root"
			subTab := "Subgroup"
			var tabs string
			if g.groupPath == "" {
				// Root mode active
				tabs = activeStyle.Render("["+rootTab+"]") + " ─── " + dimStyle.Render(subTab)
			} else {
				// Subgroup mode active
				tabs = dimStyle.Render(rootTab) + " ─── " + activeStyle.Render("["+subTab+"]")
			}
			content = tabs + "\n\n" + content
		}
	case GroupDialogRename:
		title = "Rename Group"
		content = g.nameInput.View()
	case GroupDialogMove:
		title = "Move to Group"
		var items []string
		for i, groupPath := range g.groupPaths {
			if i == g.selected {
				items = append(items, lipgloss.NewStyle().
					Foreground(ColorBg).
					Background(ColorAccent).
					Bold(true).
					Padding(0, 1).
					Render(groupPath))
			} else {
				items = append(items, lipgloss.NewStyle().
					Foreground(ColorText).
					Padding(0, 1).
					Render(groupPath))
			}
		}
		content = strings.Join(items, "\n")
	case GroupDialogRenameSession:
		title = "Rename Session"
		content = g.nameInput.View()
	}

	// Responsive dialog width
	dialogWidth := 44
	if g.width > 0 && g.width < dialogWidth+10 {
		dialogWidth = g.width - 10
		if dialogWidth < 30 {
			dialogWidth = 30
		}
	}
	titleWidth := dialogWidth - 4

	titleStyle := DialogTitleStyle.Width(titleWidth)
	hintStyle := lipgloss.NewStyle().Foreground(ColorComment)
	var hint string
	if g.CanToggle() {
		hint = hintStyle.Render("Tab toggle │ Enter confirm │ Esc cancel")
	} else {
		hint = hintStyle.Render("Enter confirm │ Esc cancel")
	}

	errContent := ""
	if g.validationErr != "" {
		errStyle := lipgloss.NewStyle().Foreground(ColorRed).Bold(true)
		errContent = errStyle.Render("⚠ " + g.validationErr)
	}

	dialogContent := lipgloss.JoinVertical(
		lipgloss.Center,
		titleStyle.Render(title),
		"",
		content,
		errContent,
		"",
		hint,
	)

	dialog := DialogBoxStyle.
		Width(dialogWidth).
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
