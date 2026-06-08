package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/git"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// forkFocusTarget identifies a focusable element in the fork dialog. The set of
// visible targets (and their order) is computed on demand by focusTargets()
// from the dialog's current state, so hidden elements never occupy a focus stop.
type forkFocusTarget int

const (
	forkFocusName forkFocusTarget = iota
	forkFocusGroup
	forkFocusConductor  // conditional — only when conductors exist.
	forkFocusBranch     // conditional — only when worktree enabled.
	forkFocusCarryState // conditional — only when worktree enabled.
	forkFocusGitignored // conditional — only when worktree && with-state enabled.
	forkFocusOptions
)

// ForkDialog handles the fork session dialog
type ForkDialog struct {
	visible       bool
	nameInput     textinput.Model
	groupInput    textinput.Model
	optionsPanel  *ClaudeOptionsPanel
	focusIndex    int // position into focusTargets() (0..len-1)
	width         int
	height        int
	projectPath   string
	validationErr string // Inline validation error displayed inside the dialog

	// Worktree support
	worktreeEnabled bool
	worktreeToggled bool // true once the user explicitly toggled the worktree checkbox (vs config default_enabled); see #1185.
	branchInput     textinput.Model
	branchPicker    *BranchPickerDialog
	worktreeCapable bool
	// Fork-with-state (PR-B): carry the parent's working-tree state into the
	// new worktree. Nested under worktree; gitignored nested under with-state.
	withStateEnabled       bool
	withStateAndGitignored bool
	// Docker sandbox support
	sandboxEnabled bool

	// Conductor parent selector
	conductorSessions []*session.Instance
	conductorCursor   int // 0 = None, 1..n = conductorSessions[0..n-1]
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
		branchPicker: NewBranchPickerDialog(),
		optionsPanel: NewClaudeOptionsPanelForFork(),
	}
}

// hasConductors returns true when conductor sessions are available.
func (d *ForkDialog) hasConductors() bool {
	return len(d.conductorSessions) > 0
}

// focusTargets returns the visible focus stops in tab order for the current
// dialog state. Hidden elements are omitted, so focusIndex is always a position
// into this slice.
func (d *ForkDialog) focusTargets() []forkFocusTarget {
	targets := []forkFocusTarget{forkFocusName, forkFocusGroup}
	if d.hasConductors() {
		targets = append(targets, forkFocusConductor)
	}
	if d.worktreeCapable && d.worktreeEnabled {
		targets = append(targets, forkFocusBranch, forkFocusCarryState)
		if d.withStateEnabled {
			targets = append(targets, forkFocusGitignored)
		}
	}
	targets = append(targets, forkFocusOptions)
	return targets
}

// clampFocus pins focusIndex into the valid range of the current focusTargets
// slice (it may shrink after a toggle).
func (d *ForkDialog) clampFocus() {
	n := len(d.focusTargets())
	if d.focusIndex >= n {
		d.focusIndex = n - 1
	}
	if d.focusIndex < 0 {
		d.focusIndex = 0
	}
}

// currentFocus returns the focus target at the current focusIndex.
func (d *ForkDialog) currentFocus() forkFocusTarget {
	targets := d.focusTargets()
	i := d.focusIndex
	if i < 0 {
		i = 0
	}
	if i >= len(targets) {
		i = len(targets) - 1
	}
	return targets[i]
}

// currentFocusName returns the lowercase name of the current focus target.
func (d *ForkDialog) currentFocusName() string {
	switch d.currentFocus() {
	case forkFocusName:
		return "name"
	case forkFocusGroup:
		return "group"
	case forkFocusConductor:
		return "conductor"
	case forkFocusBranch:
		return "branch"
	case forkFocusCarryState:
		return "carryState"
	case forkFocusGitignored:
		return "gitignored"
	case forkFocusOptions:
		return "options"
	}
	return ""
}

// setFocus moves focus to the given target if it is currently visible, then
// refreshes the focused input.
func (d *ForkDialog) setFocus(target forkFocusTarget) {
	for i, t := range d.focusTargets() {
		if t == target {
			d.focusIndex = i
			d.updateFocus()
			return
		}
	}
}

// GetParentSessionID returns the conductor ID selected in the dialog (empty = None).
func (d *ForkDialog) GetParentSessionID() string {
	if d.conductorCursor == 0 || d.conductorCursor > len(d.conductorSessions) {
		return ""
	}
	return d.conductorSessions[d.conductorCursor-1].ID
}

// GetParentProjectPath returns the project path of the selected conductor.
func (d *ForkDialog) GetParentProjectPath() string {
	if d.conductorCursor == 0 || d.conductorCursor > len(d.conductorSessions) {
		return ""
	}
	return d.conductorSessions[d.conductorCursor-1].ProjectPath
}

// Show displays the dialog with pre-filled values
func (d *ForkDialog) Show(originalName, projectPath, groupPath string, conductors []*session.Instance, suggestedParentID string) {
	d.ShowWithParentSandboxed(originalName, projectPath, groupPath, conductors, suggestedParentID, false)
}

func (d *ForkDialog) ShowWithParentSandboxed(originalName, projectPath, groupPath string, conductors []*session.Instance, suggestedParentID string, parentSandboxed bool) {
	d.visible = true
	d.validationErr = ""
	d.projectPath = projectPath
	d.nameInput.SetValue(originalName + " (fork)")
	d.groupInput.SetValue(groupPath)
	d.focusIndex = 0
	d.nameInput.Focus()
	d.groupInput.Blur()
	d.branchInput.Blur()
	if d.branchPicker != nil {
		d.branchPicker.Hide()
	}
	d.optionsPanel.Blur()
	config, _ := session.LoadUserConfig()
	forkSettings := session.ForkSettings{}
	if config != nil {
		forkSettings = config.Fork
	}

	// Reset worktree fields from global config defaults.
	d.worktreeEnabled = false
	d.worktreeToggled = false
	d.withStateEnabled = false
	d.withStateAndGitignored = false
	d.sandboxEnabled = false
	d.worktreeCapable = git.IsGitRepoOrBareProjectRoot(projectPath)

	// Conductor parent selector
	d.conductorSessions = conductors
	d.conductorCursor = 0
	for i, c := range conductors {
		if c.ID == suggestedParentID {
			d.conductorCursor = i + 1
			break
		}
	}

	// Auto-suggest branch name based on fork title. Use the git sanitizer (same
	// as quick fork's quickForkInputs) so titles with ':' '?' etc. don't produce
	// an invalid branch like "fork/fix:-bug".
	d.branchInput.SetValue(forkSettings.GetBranchPrefix() + git.SanitizeBranchName(strings.ToLower(originalName)))

	// Initialize options + structural toggles from [fork] defaults so the dialog
	// opens "comprehensive, tweak down" — matching quick fork (f).
	if config != nil {
		d.optionsPanel.SetDefaults(config)
		plan := config.Fork.Resolve(parentSandboxed)
		d.worktreeEnabled = d.worktreeCapable && plan.Worktree
		d.withStateEnabled = d.worktreeEnabled && plan.WithState
		d.withStateAndGitignored = d.withStateEnabled && plan.WithIgnored
		d.sandboxEnabled = plan.Sandbox
	}
}

// Hide hides the dialog
func (d *ForkDialog) Hide() {
	d.visible = false
	d.nameInput.Blur()
	d.groupInput.Blur()
	d.branchInput.Blur()
	if d.branchPicker != nil {
		d.branchPicker.Hide()
	}
	d.optionsPanel.Blur()
}

// IsVisible returns whether the dialog is visible
func (d *ForkDialog) IsVisible() bool {
	return d.visible
}

// IsBranchPickerOpen returns whether the inline branch result list is visible.
func (d *ForkDialog) IsBranchPickerOpen() bool {
	return d.branchPicker != nil && d.branchPicker.IsVisible()
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
	if d.branchPicker != nil {
		d.branchPicker.SetSize(width, height)
	}
}

// ToggleWorktree toggles the worktree checkbox
func (d *ForkDialog) ToggleWorktree() {
	d.worktreeEnabled = !d.worktreeEnabled
	d.worktreeToggled = true // user made an explicit choice; see #1185.
	if !d.worktreeEnabled {
		// Worktree off clears the nested with-state selections (with-state
		// only applies to a freshly created worktree).
		d.withStateEnabled = false
		d.withStateAndGitignored = false
	}
}

// IsWithStateEnabled reports whether the fork should carry the parent's
// working-tree state into the new worktree (the --with-state behavior).
func (d *ForkDialog) IsWithStateEnabled() bool {
	return d.withStateEnabled
}

// IsWithStateAndGitignoredEnabled reports whether gitignored files are also
// carried (the --with-state-and-gitignored behavior).
func (d *ForkDialog) IsWithStateAndGitignoredEnabled() bool {
	return d.withStateAndGitignored
}

// ToggleWithState flips the carry-parent-state selection. No-op unless worktree
// creation is enabled. Turning it off clears the nested gitignored selection.
func (d *ForkDialog) ToggleWithState() {
	if !d.worktreeEnabled {
		return
	}
	d.withStateEnabled = !d.withStateEnabled
	if !d.withStateEnabled {
		d.withStateAndGitignored = false
	}
}

// ToggleWithStateAndGitignored flips the include-gitignored selection. No-op
// unless carry-parent-state is enabled.
func (d *ForkDialog) ToggleWithStateAndGitignored() {
	if !d.withStateEnabled {
		return
	}
	d.withStateAndGitignored = !d.withStateAndGitignored
}

// IsWorktreeExplicit reports whether the worktree state reflects an explicit
// user choice (the checkbox was toggled) rather than the config default
// (`[worktree] default_enabled`). See #1185.
func (d *ForkDialog) IsWorktreeExplicit() bool {
	return d.worktreeToggled
}

// IsWorktreeEnabled returns whether worktree mode is enabled
func (d *ForkDialog) IsWorktreeEnabled() bool {
	return d.worktreeEnabled
}

// IsSandboxEnabled returns whether Docker sandbox mode is enabled.
func (d *ForkDialog) IsSandboxEnabled() bool {
	return d.sandboxEnabled
}

// ToggleSandbox toggles Docker sandbox mode.
func (d *ForkDialog) ToggleSandbox() {
	d.sandboxEnabled = !d.sandboxEnabled
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

	switch msg := msg.(type) {
	case tea.KeyMsg:
		if d.branchPicker != nil && d.branchPicker.IsVisible() {
			if selected, handled := d.branchPicker.Update(msg); handled {
				if d.branchPicker == nil || !d.branchPicker.IsVisible() {
					d.branchInput.Focus()
				}
				if selected != "" {
					d.branchInput.SetValue(selected)
					d.branchInput.SetCursor(len(selected))
					d.ClearError()
				}
				return d, nil
			}
		}

		switch msg.String() {
		case "tab", "down":
			cur := d.currentFocus()
			// "down" navigates within the conductor list before advancing focus.
			if msg.String() == "down" && cur == forkFocusConductor {
				if d.conductorCursor < len(d.conductorSessions) {
					d.conductorCursor++
					return d, nil
				}
				// At last item — advance past conductor to next field.
			} else if cur == forkFocusOptions {
				// Inside options panel — delegate.
				return d, d.optionsPanel.Update(msg)
			}
			if d.focusIndex < len(d.focusTargets())-1 {
				d.focusIndex++
			}
			d.updateFocus()
			return d, nil

		case "shift+tab", "up":
			cur := d.currentFocus()
			// "up" navigates within the conductor list before retreating focus.
			if msg.String() == "up" && cur == forkFocusConductor {
				if d.conductorCursor > 0 {
					d.conductorCursor--
					return d, nil
				}
				// At None — retreat to the previous field.
			} else if cur == forkFocusOptions {
				if !d.optionsPanel.AtTop() {
					// Inside options panel, not at top — delegate.
					return d, d.optionsPanel.Update(msg)
				}
				// At first option item — retreat out of the panel.
			}
			if d.focusIndex > 0 {
				d.focusIndex--
			}
			d.updateFocus()
			return d, nil

		case "esc":
			d.Hide()
			return d, nil

		case "enter":
			// Enter toggles a focused with-state checkbox (matches Space); on any
			// other focus it submits.
			switch d.currentFocus() {
			case forkFocusCarryState:
				d.ToggleWithState()
				d.clampFocus()
				d.updateFocus()
				return d, nil
			case forkFocusGitignored:
				d.ToggleWithStateAndGitignored()
				d.clampFocus()
				d.updateFocus()
				return d, nil
			}
			if d.nameInput.Value() != "" {
				return d, nil // Signal completion
			}

		case "w":
			// Toggle worktree when on group field (only if git repo).
			if d.currentFocus() == forkFocusGroup && d.worktreeCapable {
				d.ToggleWorktree()
				if d.worktreeEnabled {
					d.setFocus(forkFocusBranch)
				} else {
					// Branch/with-state targets vanished — keep focusIndex valid.
					d.clampFocus()
				}
				return d, nil
			}

		case "ctrl+f":
			if d.currentFocus() == forkFocusBranch {
				if d.branchPicker == nil {
					d.branchPicker = NewBranchPickerDialog()
				}
				d.branchPicker.SetSize(d.width, d.height)
				if err := d.branchPicker.Show(d.projectPath, d.branchInput.Value()); err != nil {
					d.SetError(err.Error())
				} else {
					d.ClearError()
					d.branchInput.Focus()
				}
				return d, nil
			}

		case "s":
			// Toggle sandbox when on group field.
			if d.currentFocus() == forkFocusGroup {
				d.ToggleSandbox()
				return d, nil
			}

		case "y":
			// Shortcut: toggle carry-parent-state. Intercepted only on the group
			// row (like w/s) or the checkbox itself, so it stays typeable in the
			// name/branch inputs.
			if f := d.currentFocus(); f == forkFocusGroup || f == forkFocusCarryState {
				d.ToggleWithState()
				d.clampFocus()
				d.updateFocus()
				return d, nil
			}

		case "i":
			// Shortcut: toggle include-gitignored.
			if f := d.currentFocus(); f == forkFocusGroup || f == forkFocusGitignored {
				d.ToggleWithStateAndGitignored()
				d.clampFocus()
				d.updateFocus()
				return d, nil
			}

		case " ", "left", "right":
			// Space toggles the focused with-state checkbox; space/arrows inside
			// the options panel are delegated.
			switch d.currentFocus() {
			case forkFocusCarryState:
				if msg.String() == " " {
					d.ToggleWithState()
					d.clampFocus()
					d.updateFocus()
				}
				return d, nil
			case forkFocusGitignored:
				if msg.String() == " " {
					d.ToggleWithStateAndGitignored()
					d.clampFocus()
					d.updateFocus()
				}
				return d, nil
			case forkFocusOptions:
				return d, d.optionsPanel.Update(msg)
			}
		}
	}

	// Update focused input
	var cmd tea.Cmd
	switch d.currentFocus() {
	case forkFocusName:
		d.nameInput, cmd = d.nameInput.Update(msg)
	case forkFocusGroup:
		d.groupInput, cmd = d.groupInput.Update(msg)
	case forkFocusBranch:
		oldBranch := d.branchInput.Value()
		d.branchInput, cmd = d.branchInput.Update(msg)
		if d.branchInput.Value() != oldBranch && d.branchPicker != nil && d.branchPicker.IsVisible() {
			d.branchPicker.SetQuery(d.branchInput.Value())
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

	switch d.currentFocus() {
	case forkFocusName:
		d.nameInput.Focus()
	case forkFocusGroup:
		d.groupInput.Focus()
	case forkFocusConductor, forkFocusCarryState, forkFocusGitignored:
		// Conductor picker and the with-state checkboxes activate no text input.
	case forkFocusBranch:
		d.branchInput.Focus()
	case forkFocusOptions:
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
	switch d.currentFocus() {
	case forkFocusName:
		nameLabel = activeLabelStyle.Render("▶ Name:")
		groupLabel = labelStyle.Render("  Group:")
	case forkFocusGroup:
		nameLabel = labelStyle.Render("  Name:")
		groupLabel = activeLabelStyle.Render("▶ Group:")
	default:
		nameLabel = labelStyle.Render("  Name:")
		groupLabel = labelStyle.Render("  Group:")
	}

	// Conductor parent section (only when conductors exist)
	conductorSection := ""
	if d.hasConductors() {
		cLabel := labelStyle.Render("  Conductor:")
		if d.currentFocus() == forkFocusConductor {
			cLabel = activeLabelStyle.Render("▶ Conductor:")
		}
		conductorSection += cLabel + "\n"

		home, _ := os.UserHomeDir()
		shortPath := func(p string) string {
			if strings.HasPrefix(p, home) {
				p = "~" + p[len(home):]
			}
			return filepath.Base(p)
		}

		selectedStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
		itemStyle := lipgloss.NewStyle().Foreground(ColorText)

		if d.conductorCursor == 0 {
			conductorSection += selectedStyle.Render("  ▶ None") + "\n"
		} else {
			conductorSection += itemStyle.Render("    None") + "\n"
		}
		for i, inst := range d.conductorSessions {
			name := strings.TrimPrefix(inst.Title, "conductor-")
			label := name + " (" + shortPath(inst.ProjectPath) + ")"
			if d.conductorCursor == i+1 {
				conductorSection += selectedStyle.Render("  ▶ "+label) + "\n"
			} else {
				conductorSection += itemStyle.Render("    "+label) + "\n"
			}
		}
		conductorSection += "\n"
	}

	// Worktree checkbox and branch input (only for git repos)
	worktreeSection := ""
	if d.worktreeCapable {
		checkboxStyle := lipgloss.NewStyle().Foreground(ColorText)
		checkboxActiveStyle := lipgloss.NewStyle().Foreground(ColorCyan).Bold(true)

		checkbox := "[ ]"
		if d.worktreeEnabled {
			checkbox = "[x]"
		}

		if d.currentFocus() == forkFocusGroup {
			worktreeSection += checkboxActiveStyle.Render(fmt.Sprintf("  %s Create in worktree (press w)", checkbox))
		} else {
			worktreeSection += checkboxStyle.Render(fmt.Sprintf("  %s Create in worktree", checkbox))
		}
		worktreeSection += "\n"

		// Branch input (only visible when worktree is enabled)
		if d.worktreeEnabled {
			worktreeSection += "\n"
			if d.currentFocus() == forkFocusBranch {
				worktreeSection += activeLabelStyle.Render("▶ Branch:")
			} else {
				worktreeSection += labelStyle.Render("  Branch:")
			}
			worktreeSection += "\n"
			worktreeSection += "  " + d.branchInput.View() + "\n"
			if d.branchPicker != nil && d.branchPicker.IsVisible() {
				worktreeSection += "  " + strings.ReplaceAll(d.branchPicker.View(), "\n", "\n  ") + "\n"
			}

			// Fork-with-state: carry parent state, with nested gitignored (PR-B B3).
			carryCb := "[ ]"
			if d.withStateEnabled {
				carryCb = "[x]"
			}
			if d.currentFocus() == forkFocusCarryState {
				worktreeSection += "\n" + checkboxActiveStyle.Render(fmt.Sprintf("  ▶ %s Carry parent state (y)", carryCb)) + "\n"
			} else {
				worktreeSection += "\n" + checkboxStyle.Render(fmt.Sprintf("    %s Carry parent state (y)", carryCb)) + "\n"
			}
			worktreeSection += checkboxStyle.Render("      ↳ creates a NEW branch at parent HEAD") + "\n"

			if d.withStateEnabled {
				gitignoredCb := "[ ]"
				if d.withStateAndGitignored {
					gitignoredCb = "[x]"
				}
				if d.currentFocus() == forkFocusGitignored {
					worktreeSection += checkboxActiveStyle.Render(fmt.Sprintf("    ▶ %s Include gitignored files (i)", gitignoredCb)) + "\n"
				} else {
					worktreeSection += checkboxStyle.Render(fmt.Sprintf("      %s Include gitignored files (i)", gitignoredCb)) + "\n"
				}
			}
		}
	}

	// Docker sandbox checkbox.
	sandboxSection := ""
	sandboxLabel := "Run in Docker sandbox"
	if d.currentFocus() == forkFocusGroup {
		sandboxLabel = "Run in Docker sandbox (press s)"
	}
	sandboxCb := "[ ]"
	if d.sandboxEnabled {
		sandboxCb = "[x]"
	}
	sandboxStyle := lipgloss.NewStyle().Foreground(ColorText)
	if d.currentFocus() == forkFocusGroup {
		sandboxStyle = lipgloss.NewStyle().Foreground(ColorCyan).Bold(true)
	}
	sandboxSection = sandboxStyle.Render(fmt.Sprintf("  %s %s", sandboxCb, sandboxLabel)) + "\n"

	errLine := ""
	if d.validationErr != "" {
		errStyle := lipgloss.NewStyle().Foreground(ColorRed).Bold(true)
		errLine = "\n" + errStyle.Render("  ⚠ "+d.validationErr) + "\n"
	}

	helpText := "Enter create │ Esc cancel │ Tab next │ s sandbox │ Space toggle"
	if d.currentFocus() == forkFocusBranch {
		if d.branchPicker != nil && d.branchPicker.IsVisible() {
			helpText = "Type filter │ ↑↓ navigate │ Enter select │ Esc close"
		} else {
			helpText = "^F branch search │ Enter create │ Esc cancel │ Tab next"
		}
	}

	content := titleStyle.Render("Fork Session") + "\n\n" +
		nameLabel + "\n" +
		"  " + d.nameInput.View() + "\n\n" +
		groupLabel + "\n" +
		"  " + d.groupInput.View() + "\n" +
		conductorSection +
		worktreeSection +
		sandboxSection + "\n" +
		d.optionsPanel.View() +
		errLine + "\n" +
		lipgloss.NewStyle().Foreground(ColorComment).
			Render(helpText)

	dialog := boxStyle.Render(content)

	// Center the dialog on screen
	return lipgloss.Place(d.width, d.height, lipgloss.Center, lipgloss.Center, dialog)
}
