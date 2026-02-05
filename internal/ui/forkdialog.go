package ui

import (
	"fmt"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/git"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ForkDialog handles the fork session dialog
type ForkDialog struct {
	visible       bool
	nameInput     textinput.Model
	groupInput    textinput.Model
	optionsPanel  *ClaudeOptionsPanel
	focusIndex    int // 0=name, 1=group, 2=branch(if worktree), 2/3+=options
	width         int
	height        int
	projectPath   string
	validationErr string // Inline validation error displayed inside the dialog

	// Worktree support
	worktreeEnabled bool
	branchInput     textinput.Model
	isGitRepo       bool
}

// NewForkDialog creates a new fork dialog
func NewForkDialog() *ForkDialog {
	nameInput := textinput.New()
	nameInput.Placeholder = "Session name"
	nameInput.CharLimit = MaxNameLength
	nameInput.Width = 40

	groupInput := textinput.New()
	groupInput.Placeholder = "Group path (optional)"
	groupInput.CharLimit = 64
	groupInput.Width = 40

	branchInput := textinput.New()
	branchInput.Placeholder = "fork/branch-name"
	branchInput.CharLimit = 100
	branchInput.Width = 40

	return &ForkDialog{
		nameInput:    nameInput,
		groupInput:   groupInput,
		branchInput:  branchInput,
		optionsPanel: NewClaudeOptionsPanelForFork(),
	}
}

// Show displays the dialog with pre-filled values
func (d *ForkDialog) Show(originalName, projectPath, groupPath string) {
	d.visible = true
	d.validationErr = ""
	d.projectPath = projectPath
	d.nameInput.SetValue(originalName + " (fork)")
	d.groupInput.SetValue(groupPath)
	d.focusIndex = 0
	d.nameInput.Focus()
	d.groupInput.Blur()
	d.branchInput.Blur()
	d.optionsPanel.Blur()

	// Reset worktree fields
	d.worktreeEnabled = false
	d.isGitRepo = git.IsGitRepo(projectPath)

	// Auto-suggest branch name based on fork title
	sanitized := strings.ToLower(originalName)
	sanitized = strings.ReplaceAll(sanitized, " ", "-")
	d.branchInput.SetValue("fork/" + sanitized)

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
	d.branchInput.Blur()
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

// GetValuesWithWorktree returns all values including worktree settings
func (d *ForkDialog) GetValuesWithWorktree() (name, group, branch string, worktreeEnabled bool) {
	name = d.nameInput.Value()
	group = d.groupInput.Value()
	branch = strings.TrimSpace(d.branchInput.Value())
	worktreeEnabled = d.worktreeEnabled
	return
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

// ToggleWorktree toggles the worktree checkbox
func (d *ForkDialog) ToggleWorktree() {
	d.worktreeEnabled = !d.worktreeEnabled
}

// IsWorktreeEnabled returns whether worktree mode is enabled
func (d *ForkDialog) IsWorktreeEnabled() bool {
	return d.worktreeEnabled
}

// optionsStartIndex returns the focus index where the options panel begins
func (d *ForkDialog) optionsStartIndex() int {
	if d.worktreeEnabled {
		return 3 // 0=name, 1=group, 2=branch, 3+=options
	}
	return 2 // 0=name, 1=group, 2+=options
}

// Validate checks if the dialog values are valid and returns an error message if not
func (d *ForkDialog) Validate() string {
	name := strings.TrimSpace(d.nameInput.Value())
	if name == "" {
		return "Session name cannot be empty"
	}
	if len(name) > MaxNameLength {
		return fmt.Sprintf("Session name too long (max %d characters)", MaxNameLength)
	}
	// Validate worktree branch if enabled
	if d.worktreeEnabled {
		branch := strings.TrimSpace(d.branchInput.Value())
		if branch == "" {
			return "Branch name required for worktree"
		}
		if err := git.ValidateBranchName(branch); err != nil {
			return err.Error()
		}
	}
	return ""
}

// SetError sets an inline validation error displayed inside the dialog
func (d *ForkDialog) SetError(msg string) {
	d.validationErr = msg
}

// ClearError clears the inline validation error
func (d *ForkDialog) ClearError() {
	d.validationErr = ""
}

// Update handles input events
func (d *ForkDialog) Update(msg tea.Msg) (*ForkDialog, tea.Cmd) {
	if !d.visible {
		return d, nil
	}

	optStart := d.optionsStartIndex()

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "tab", "down":
			if d.focusIndex < optStart {
				// Move from name/group/branch to next field
				d.focusIndex++
				// Skip branch field if worktree not enabled
				if d.focusIndex == 2 && !d.worktreeEnabled {
					d.focusIndex = optStart
				}
				d.updateFocus()
			} else {
				// Inside options panel - delegate
				return d, d.optionsPanel.Update(msg)
			}
			return d, nil

		case "shift+tab", "up":
			if d.focusIndex == optStart && d.optionsPanel.AtTop() {
				// At first option item, move back
				if d.worktreeEnabled {
					d.focusIndex = 2 // branch
				} else {
					d.focusIndex = 1 // group
				}
				d.updateFocus()
			} else if d.focusIndex < optStart {
				d.focusIndex--
				// Skip branch field if worktree not enabled
				if d.focusIndex == 2 && !d.worktreeEnabled {
					d.focusIndex = 1
				}
				if d.focusIndex < 0 {
					d.focusIndex = 0
				}
				d.updateFocus()
			} else {
				// Inside options panel - delegate
				return d, d.optionsPanel.Update(msg)
			}
			return d, nil

		case "esc":
			d.Hide()
			return d, nil

		case "enter":
			if d.nameInput.Value() != "" {
				return d, nil // Signal completion
			}

		case "w":
			// Toggle worktree when on group field (only if git repo)
			if d.focusIndex == 1 && d.isGitRepo {
				d.ToggleWorktree()
				if d.worktreeEnabled {
					d.focusIndex = 2
					d.updateFocus()
				}
				return d, nil
			}

		case " ", "left", "right":
			// Delegate space/arrow keys to options panel if focused there
			if d.focusIndex >= optStart {
				return d, d.optionsPanel.Update(msg)
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
	case 2:
		if d.worktreeEnabled {
			d.branchInput, cmd = d.branchInput.Update(msg)
		} else {
			cmd = d.optionsPanel.Update(msg)
		}
	default:
		// Options panel handles its own inputs
		cmd = d.optionsPanel.Update(msg)
	}

	return d, cmd
}

func (d *ForkDialog) updateFocus() {
	d.nameInput.Blur()
	d.groupInput.Blur()
	d.branchInput.Blur()
	d.optionsPanel.Blur()

	switch d.focusIndex {
	case 0:
		d.nameInput.Focus()
	case 1:
		d.groupInput.Focus()
	case 2:
		if d.worktreeEnabled {
			d.branchInput.Focus()
		} else {
			d.optionsPanel.Focus()
		}
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

	// Worktree checkbox and branch input (only for git repos)
	worktreeSection := ""
	if d.isGitRepo {
		checkboxStyle := lipgloss.NewStyle().Foreground(ColorText)
		checkboxActiveStyle := lipgloss.NewStyle().Foreground(ColorCyan).Bold(true)

		checkbox := "[ ]"
		if d.worktreeEnabled {
			checkbox = "[x]"
		}

		if d.focusIndex == 1 {
			worktreeSection += checkboxActiveStyle.Render(fmt.Sprintf("  %s Create in worktree (press w)", checkbox))
		} else {
			worktreeSection += checkboxStyle.Render(fmt.Sprintf("  %s Create in worktree", checkbox))
		}
		worktreeSection += "\n"

		// Branch input (only visible when worktree is enabled)
		if d.worktreeEnabled {
			worktreeSection += "\n"
			if d.focusIndex == 2 {
				worktreeSection += activeLabelStyle.Render("▶ Branch:")
			} else {
				worktreeSection += labelStyle.Render("  Branch:")
			}
			worktreeSection += "\n"
			worktreeSection += "  " + d.branchInput.View() + "\n"
		}
	}

	errLine := ""
	if d.validationErr != "" {
		errStyle := lipgloss.NewStyle().Foreground(ColorRed).Bold(true)
		errLine = "\n" + errStyle.Render("  ⚠ "+d.validationErr) + "\n"
	}

	content := titleStyle.Render("Fork Session") + "\n\n" +
		nameLabel + "\n" +
		"  " + d.nameInput.View() + "\n\n" +
		groupLabel + "\n" +
		"  " + d.groupInput.View() + "\n" +
		worktreeSection + "\n" +
		d.optionsPanel.View() +
		errLine + "\n" +
		lipgloss.NewStyle().Foreground(ColorComment).
			Render("Enter create │ Esc cancel │ Tab next │ Space toggle")

	dialog := boxStyle.Render(content)

	// Center the dialog on screen
	return lipgloss.Place(d.width, d.height, lipgloss.Center, lipgloss.Center, dialog)
}
