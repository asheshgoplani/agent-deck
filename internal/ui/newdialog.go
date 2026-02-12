package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/git"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// NewDialog represents the new session creation dialog
type NewDialog struct {
	nameInput            textinput.Model
	pathInput            textinput.Model
	commandInput         textinput.Model
	claudeOptions        *ClaudeOptionsPanel // Claude-specific options (concrete for value extraction)
	geminiOptions        *YoloOptionsPanel   // Gemini YOLO panel (concrete for value extraction)
	codexOptions         *YoloOptionsPanel   // Codex YOLO panel (concrete for value extraction)
	toolOptions          OptionsPanel        // Currently active tool options panel (nil if none)
	focusIndex           int                 // 0=name, 1=path, 2=command, 3+=options
	width                int
	height               int
	visible              bool
	presetCommands       []string
	commandCursor        int
	parentGroupPath      string
	parentGroupName      string
	pathSuggestions      []string // stores all available path suggestions
	pathSuggestionCursor int      // tracks selected suggestion in dropdown
	suggestionNavigated  bool     // tracks if user explicitly navigated suggestions
	// Worktree support
	worktreeEnabled bool
	branchInput     textinput.Model
	branchAutoSet   bool // true if branch was auto-derived from session name
	// Inline validation error displayed inside the dialog
	validationErr string
	pathCycler    session.CompletionCycler // Path autocomplete state
}

// buildPresetCommands returns the list of commands for the picker,
// including any custom tools from config.toml.
func buildPresetCommands() []string {
	presets := []string{"", "claude", "gemini", "opencode", "codex"}
	if customTools := session.GetCustomToolNames(); len(customTools) > 0 {
		presets = append(presets, customTools...)
	}
	return presets
}

// NewNewDialog creates a new NewDialog instance
func NewNewDialog() *NewDialog {
	// Create name input
	nameInput := textinput.New()
	nameInput.Placeholder = "session-name"
	nameInput.Focus()
	nameInput.CharLimit = MaxNameLength
	nameInput.Width = 40

	// Create path input
	pathInput := textinput.New()
	pathInput.Placeholder = "~/project/path"
	pathInput.CharLimit = 256
	pathInput.Width = 40
	pathInput.ShowSuggestions = true // enable built-in suggestions

	// Get current working directory for default path
	cwd, err := os.Getwd()
	if err == nil {
		pathInput.SetValue(cwd)
	}

	// Create command input
	commandInput := textinput.New()
	commandInput.Placeholder = "custom command"
	commandInput.CharLimit = 100
	commandInput.Width = 40

	// Create branch input for worktree
	branchInput := textinput.New()
	branchInput.Placeholder = "feature/branch-name"
	branchInput.CharLimit = 100
	branchInput.Width = 40

	dlg := &NewDialog{
		nameInput:       nameInput,
		pathInput:       pathInput,
		commandInput:    commandInput,
		branchInput:     branchInput,
		claudeOptions:   NewClaudeOptionsPanel(),
		geminiOptions:   NewYoloOptionsPanel("Gemini", "YOLO mode - auto-approve all"),
		codexOptions:    NewYoloOptionsPanel("Codex", "YOLO mode - bypass approvals and sandbox"),
		focusIndex:      0,
		visible:         false,
		presetCommands:  buildPresetCommands(),
		commandCursor:   0,
		parentGroupPath: "default",
		parentGroupName: "default",
		worktreeEnabled: false,
	}
	dlg.updateToolOptions()
	return dlg
}

// ShowInGroup shows the dialog with a pre-selected parent group and optional default path
func (d *NewDialog) ShowInGroup(groupPath, groupName, defaultPath string) {
	if groupPath == "" {
		groupPath = "default"
		groupName = "default"
	}
	d.parentGroupPath = groupPath
	d.parentGroupName = groupName
	d.visible = true
	d.focusIndex = 0
	d.validationErr = ""
	d.nameInput.SetValue("")
	d.nameInput.Focus()
	d.suggestionNavigated = false // reset on show
	d.pathSuggestionCursor = 0    // reset cursor too
	d.pathCycler.Reset()          // clear stale autocomplete matches from previous show
	d.pathInput.Blur()
	d.claudeOptions.Blur()
	d.geminiOptions.Blur()
	d.codexOptions.Blur()
	// Keep commandCursor at previously set default (don't reset to 0)
	d.updateToolOptions()
	// Reset worktree fields
	d.worktreeEnabled = false
	d.branchInput.SetValue("")
	d.branchAutoSet = false
	// Set path input to group's default path if provided, otherwise use current working directory
	if defaultPath != "" {
		d.pathInput.SetValue(defaultPath)
	} else {
		cwd, err := os.Getwd()
		if err == nil {
			d.pathInput.SetValue(cwd)
		}
	}
	// Initialize tool options from global config
	d.geminiOptions.SetDefaults(false)
	d.codexOptions.SetDefaults(false)
	if userConfig, err := session.LoadUserConfig(); err == nil && userConfig != nil {
		d.geminiOptions.SetDefaults(userConfig.Gemini.YoloMode)
		d.codexOptions.SetDefaults(userConfig.Codex.YoloMode)
		d.claudeOptions.SetDefaults(userConfig)
	}
}

// SetDefaultTool sets the pre-selected command based on tool name
// Call this before Show/ShowInGroup to apply user's preferred default
func (d *NewDialog) SetDefaultTool(tool string) {
	if tool == "" {
		d.commandCursor = 0 // Default to shell
		return
	}

	// Find the tool in preset commands
	for i, cmd := range d.presetCommands {
		if cmd == tool {
			d.commandCursor = i
			d.updateToolOptions()
			return
		}
	}

	// Tool not found in presets, default to shell
	d.commandCursor = 0
	d.updateToolOptions()
}

// GetSelectedGroup returns the parent group path
func (d *NewDialog) GetSelectedGroup() string {
	return d.parentGroupPath
}

// SetSize sets the dialog dimensions
func (d *NewDialog) SetSize(width, height int) {
	d.width = width
	d.height = height
}

// SetPathSuggestions sets the available path suggestions for autocomplete
func (d *NewDialog) SetPathSuggestions(paths []string) {
	d.pathSuggestions = paths
	d.pathSuggestionCursor = 0
	d.pathInput.SetSuggestions(paths)
}

// Show makes the dialog visible (uses default group)
func (d *NewDialog) Show() {
	d.ShowInGroup("default", "default", "")
}

// Hide hides the dialog
func (d *NewDialog) Hide() {
	d.visible = false
}

// IsVisible returns whether the dialog is visible
func (d *NewDialog) IsVisible() bool {
	return d.visible
}

// GetValues returns the current dialog values with expanded paths
func (d *NewDialog) GetValues() (name, path, command string) {
	name = strings.TrimSpace(d.nameInput.Value())
	// Fix: sanitize input to remove surrounding quotes that cause path issues
	path = strings.Trim(strings.TrimSpace(d.pathInput.Value()), "'\"")

	// Fix malformed paths that have ~ in the middle (e.g., "/some/path~/actual/path")
	// This can happen when textinput suggestion appends instead of replaces
	if idx := strings.Index(path, "~/"); idx > 0 {
		// Extract the part after the malformed prefix (the actual tilde-prefixed path)
		path = path[idx:]
	}

	// Expand tilde in path (handles both "~/" prefix and just "~")
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			path = filepath.Join(home, path[2:])
		}
	} else if path == "~" {
		home, err := os.UserHomeDir()
		if err == nil {
			path = home
		}
	}

	// Get command - either from preset or custom input
	if d.commandCursor < len(d.presetCommands) {
		command = d.presetCommands[d.commandCursor]
	}
	if command == "" && d.commandInput.Value() != "" {
		command = strings.TrimSpace(d.commandInput.Value())
	}

	return name, path, command
}

// ToggleWorktree toggles the worktree checkbox.
// When enabling, auto-populates the branch name from the session name.
func (d *NewDialog) ToggleWorktree() {
	d.worktreeEnabled = !d.worktreeEnabled
	if d.worktreeEnabled {
		d.autoBranchFromName()
	}
}

// autoBranchFromName sets the branch input to "feature/<session-name>" if the
// name field is non-empty and the branch hasn't been manually edited.
func (d *NewDialog) autoBranchFromName() {
	name := strings.TrimSpace(d.nameInput.Value())
	if name == "" {
		return
	}
	branch := "feature/" + name
	d.branchInput.SetValue(branch)
	d.branchAutoSet = true
}

// IsWorktreeEnabled returns whether worktree mode is enabled
func (d *NewDialog) IsWorktreeEnabled() bool {
	return d.worktreeEnabled
}

// GetValuesWithWorktree returns all values including worktree settings
func (d *NewDialog) GetValuesWithWorktree() (name, path, command, branch string, worktreeEnabled bool) {
	name, path, command = d.GetValues()
	branch = strings.TrimSpace(d.branchInput.Value())
	worktreeEnabled = d.worktreeEnabled
	return
}

// IsGeminiYoloMode returns whether YOLO mode is enabled for Gemini
func (d *NewDialog) IsGeminiYoloMode() bool {
	return d.geminiOptions.GetYoloMode()
}

// GetCodexYoloMode returns the Codex YOLO mode state
func (d *NewDialog) GetCodexYoloMode() bool {
	return d.codexOptions.GetYoloMode()
}

// GetSelectedCommand returns the currently selected command/tool
func (d *NewDialog) GetSelectedCommand() string {
	if d.commandCursor >= 0 && d.commandCursor < len(d.presetCommands) {
		return d.presetCommands[d.commandCursor]
	}
	return ""
}

// GetClaudeOptions returns the Claude-specific options (only relevant if command is "claude")
func (d *NewDialog) GetClaudeOptions() *session.ClaudeOptions {
	if !d.isClaudeSelected() {
		return nil
	}
	return d.claudeOptions.GetOptions()
}

// isClaudeSelected returns true if "claude" is the selected command
func (d *NewDialog) isClaudeSelected() bool {
	return d.commandCursor < len(d.presetCommands) && d.presetCommands[d.commandCursor] == "claude"
}

// Validate checks if the dialog values are valid and returns an error message if not
func (d *NewDialog) Validate() string {
	name := strings.TrimSpace(d.nameInput.Value())
	// Fix: sanitize input to remove surrounding quotes that cause path issues
	path := strings.Trim(strings.TrimSpace(d.pathInput.Value()), "'\"")

	// Check for empty name
	if name == "" {
		return "Session name cannot be empty"
	}

	// Check name length
	if len(name) > MaxNameLength {
		return fmt.Sprintf("Session name too long (max %d characters)", MaxNameLength)
	}

	// Check for empty path
	if path == "" {
		return "Project path cannot be empty"
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

	return "" // Valid
}

// SetError sets an inline validation error displayed inside the dialog
func (d *NewDialog) SetError(msg string) {
	d.validationErr = msg
}

// ClearError clears the inline validation error
func (d *NewDialog) ClearError() {
	d.validationErr = ""
}

// optionsStartIndex returns the focus index where tool options begin.
func (d *NewDialog) optionsStartIndex() int {
	if d.worktreeEnabled {
		return 4 // 0=name, 1=path, 2=command, 3=branch, 4=options
	}
	return 3 // 0=name, 1=path, 2=command, 3=options
}

// updateToolOptions sets d.toolOptions to the panel matching the current tool selection.
func (d *NewDialog) updateToolOptions() {
	switch d.GetSelectedCommand() {
	case "claude":
		d.toolOptions = d.claudeOptions
	case "gemini":
		d.toolOptions = d.geminiOptions
	case "codex":
		d.toolOptions = d.codexOptions
	default:
		d.toolOptions = nil
	}
}

func (d *NewDialog) updateFocus() {
	d.nameInput.Blur()
	d.pathInput.Blur()
	d.commandInput.Blur()
	d.branchInput.Blur()
	d.claudeOptions.Blur()
	d.geminiOptions.Blur()
	d.codexOptions.Blur()

	switch d.focusIndex {
	case 0:
		d.nameInput.Focus()
	case 1:
		d.pathInput.Focus()
	case 2:
		if d.commandCursor == 0 { // shell
			d.commandInput.Focus()
		}
	case 3:
		if d.worktreeEnabled {
			d.branchInput.Focus()
		} else if d.toolOptions != nil {
			d.toolOptions.Focus()
		}
	default:
		if d.toolOptions != nil {
			d.toolOptions.Focus()
		}
	}
}

// getMaxFocusIndex returns the maximum focus index based on current state
func (d *NewDialog) getMaxFocusIndex() int {
	if d.worktreeEnabled && d.toolOptions != nil {
		return 4
	}
	if d.worktreeEnabled || d.toolOptions != nil {
		return 3
	}
	return 2
}

// Update handles key messages
func (d *NewDialog) Update(msg tea.Msg) (*NewDialog, tea.Cmd) {
	if !d.visible {
		return d, nil
	}

	var cmd tea.Cmd
	maxIdx := d.getMaxFocusIndex()

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "tab":
			// On path field: trigger autocomplete or cycle through matches
			if d.focusIndex == 1 {
				// Determine if we should trigger autocomplete
				path := d.pathInput.Value()
				info, err := os.Stat(path)
				isDir := err == nil && info.IsDir()
				isPartial := !isDir || strings.HasSuffix(path, string(os.PathSeparator))

				if d.pathCycler.IsActive() || isPartial {
					if d.pathCycler.IsActive() {
						// Cycle to next match
						d.pathInput.SetValue(d.pathCycler.Next())
						d.pathInput.SetCursor(len(d.pathInput.Value()))
						return d, nil
					}

					// First Tab press on partial path - look for completions
					matches, err := session.GetDirectoryCompletions(path)
					if err == nil && len(matches) > 0 {
						d.pathCycler.SetMatches(matches)
						d.pathInput.SetValue(d.pathCycler.Next())
						d.pathInput.SetCursor(len(d.pathInput.Value()))
						return d, nil
					}
				}
				// If path is complete or no matches found - fall through to normal navigation
			}

			// On path field: apply selected suggestion ONLY if user explicitly navigated to one (fallback for Ctrl+N/P)
			if d.focusIndex == 1 && d.suggestionNavigated && len(d.pathSuggestions) > 0 {
				if d.pathSuggestionCursor < len(d.pathSuggestions) {
					d.pathInput.SetValue(d.pathSuggestions[d.pathSuggestionCursor])
					d.pathInput.SetCursor(len(d.pathInput.Value()))
				}
			}
			// Move to next field
			if d.focusIndex < maxIdx {
				d.focusIndex++
				d.updateFocus()
			} else if d.toolOptions != nil && d.focusIndex >= d.optionsStartIndex() {
				return d, d.toolOptions.Update(msg)
			} else {
				d.focusIndex = 0
				d.updateFocus()
			}
			// Reset navigation flag when leaving path field
			if d.focusIndex != 1 {
				d.suggestionNavigated = false
			}
			return d, cmd

		case "ctrl+n":
			// Next suggestion (when on path field)
			if d.focusIndex == 1 && len(d.pathSuggestions) > 0 {
				d.pathSuggestionCursor = (d.pathSuggestionCursor + 1) % len(d.pathSuggestions)
				d.suggestionNavigated = true // user explicitly navigated
				return d, nil
			}

		case "ctrl+p":
			// Previous suggestion (when on path field)
			if d.focusIndex == 1 && len(d.pathSuggestions) > 0 {
				d.pathSuggestionCursor--
				if d.pathSuggestionCursor < 0 {
					d.pathSuggestionCursor = len(d.pathSuggestions) - 1
				}
				d.suggestionNavigated = true // user explicitly navigated
				return d, nil
			}

		case "down":
			if d.focusIndex < maxIdx {
				d.focusIndex++
				d.updateFocus()
			} else if d.toolOptions != nil && d.focusIndex >= d.optionsStartIndex() {
				return d, d.toolOptions.Update(msg)
			}
			return d, nil

		case "shift+tab", "up":
			if d.toolOptions != nil && d.focusIndex >= d.optionsStartIndex() && !d.toolOptions.AtTop() {
				return d, d.toolOptions.Update(msg)
			}
			d.focusIndex--
			if d.focusIndex < 0 {
				d.focusIndex = maxIdx
			}
			d.updateFocus()
			return d, nil

		case "esc":
			d.Hide()
			return d, nil

		case "enter":
			// Let parent handle enter (create session)
			return d, nil

		case "left":
			if d.focusIndex == 2 {
				d.commandCursor--
				if d.commandCursor < 0 {
					d.commandCursor = len(d.presetCommands) - 1
				}
				d.updateToolOptions()
				d.updateFocus()
				return d, nil
			}
			if d.toolOptions != nil && d.focusIndex >= d.optionsStartIndex() {
				return d, d.toolOptions.Update(msg)
			}

		case "right":
			if d.focusIndex == 2 {
				d.commandCursor = (d.commandCursor + 1) % len(d.presetCommands)
				d.updateToolOptions()
				d.updateFocus()
				return d, nil
			}
			if d.toolOptions != nil && d.focusIndex >= d.optionsStartIndex() {
				return d, d.toolOptions.Update(msg)
			}

		case "w":
			// Toggle worktree when on command field (focusIndex == 2)
			if d.focusIndex == 2 {
				d.ToggleWorktree()
				// If enabling worktree, move to branch field
				if d.worktreeEnabled {
					d.focusIndex = 3
					d.updateFocus()
				}
				return d, nil
			}

		case "y":
			// 'y' shortcut from command field (gemini/codex only)
			selectedCmd := d.GetSelectedCommand()
			if d.focusIndex == 2 && (selectedCmd == "gemini" || selectedCmd == "codex") && d.toolOptions != nil {
				d.toolOptions.Update(msg)
				return d, nil
			}
			// 'y' from within tool options panel
			if d.toolOptions != nil && d.focusIndex >= d.optionsStartIndex() {
				d.toolOptions.Update(msg)
				return d, nil
			}

		case " ":
			if d.toolOptions != nil && d.focusIndex >= d.optionsStartIndex() {
				return d, d.toolOptions.Update(msg)
			}
		}
	}

	// Update focused input
	switch d.focusIndex {
	case 0:
		oldName := d.nameInput.Value()
		d.nameInput, cmd = d.nameInput.Update(msg)
		// Auto-update branch when name changes and worktree is enabled
		if d.worktreeEnabled && d.branchAutoSet && d.nameInput.Value() != oldName {
			d.autoBranchFromName()
		}
	case 1:
		oldValue := d.pathInput.Value()
		d.pathInput, cmd = d.pathInput.Update(msg)
		// Reset navigation if user typed something new
		if d.pathInput.Value() != oldValue {
			d.suggestionNavigated = false
			d.pathSuggestionCursor = 0
			d.pathCycler.Reset()
		}
	case 2:
		// Update custom command input when shell is selected
		if d.commandCursor == 0 { // shell
			d.commandInput, cmd = d.commandInput.Update(msg)
		}
	case 3:
		if d.worktreeEnabled {
			oldBranch := d.branchInput.Value()
			d.branchInput, cmd = d.branchInput.Update(msg)
			// User manually edited branch: stop auto-deriving from name
			if d.branchInput.Value() != oldBranch {
				d.branchAutoSet = false
			}
		} else if d.toolOptions != nil {
			cmd = d.toolOptions.Update(msg)
		}
	default:
		if d.toolOptions != nil && d.focusIndex >= d.optionsStartIndex() {
			cmd = d.toolOptions.Update(msg)
		}
	}

	return d, cmd
}

// View renders the dialog
func (d *NewDialog) View() string {
	if !d.visible {
		return ""
	}

	// Styles
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorCyan).
		MarginBottom(1)

	labelStyle := lipgloss.NewStyle().
		Foreground(ColorText)

	// Responsive dialog width
	dialogWidth := 60
	if d.width > 0 && d.width < dialogWidth+10 {
		dialogWidth = d.width - 10
		if dialogWidth < 40 {
			dialogWidth = 40
		}
	}

	dialogStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorCyan).
		Background(ColorSurface).
		Padding(2, 4).
		Width(dialogWidth)

	// Active field indicator style
	activeLabelStyle := lipgloss.NewStyle().
		Foreground(ColorCyan).
		Bold(true)

	// Build content
	var content strings.Builder

	// Title with parent group info
	content.WriteString(titleStyle.Render("New Session"))
	content.WriteString("\n")
	groupInfoStyle := lipgloss.NewStyle().Foreground(ColorPurple) // Purple for group context
	content.WriteString(groupInfoStyle.Render("  in group: " + d.parentGroupName))
	content.WriteString("\n\n")

	// Name input
	if d.focusIndex == 0 {
		content.WriteString(activeLabelStyle.Render("▶ Name:"))
	} else {
		content.WriteString(labelStyle.Render("  Name:"))
	}
	content.WriteString("\n")
	content.WriteString("  ")
	content.WriteString(d.nameInput.View())
	content.WriteString("\n\n")

	// Path input
	if d.focusIndex == 1 {
		content.WriteString(activeLabelStyle.Render("▶ Path:"))
	} else {
		content.WriteString(labelStyle.Render("  Path:"))
	}
	content.WriteString("\n")
	content.WriteString("  ")
	content.WriteString(d.pathInput.View())
	content.WriteString("\n")

	// Show path suggestions dropdown when path field is focused
	if d.focusIndex == 1 && len(d.pathSuggestions) > 0 {
		suggestionStyle := lipgloss.NewStyle().
			Foreground(ColorComment)
		selectedStyle := lipgloss.NewStyle().
			Foreground(ColorCyan).
			Bold(true)

		// Show up to 5 suggestions in a scrolling window around the cursor
		maxShow := 5
		total := len(d.pathSuggestions)

		// Calculate visible window that follows the cursor
		startIdx := 0
		endIdx := total // Start with all suggestions
		if total > maxShow {
			// Need scrolling - center the cursor in the window
			startIdx = d.pathSuggestionCursor - maxShow/2
			if startIdx < 0 {
				startIdx = 0
			}
			endIdx = startIdx + maxShow
			if endIdx > total {
				endIdx = total
				startIdx = endIdx - maxShow
			}
		}

		content.WriteString("  ")
		content.WriteString(lipgloss.NewStyle().Foreground(ColorComment).Render("─ recent paths (Ctrl+N/P: cycle, Tab: accept) ─"))
		content.WriteString("\n")

		// Show "more above" indicator
		if startIdx > 0 {
			content.WriteString(suggestionStyle.Render(fmt.Sprintf("    ↑ %d more above", startIdx)))
			content.WriteString("\n")
		}

		for i := startIdx; i < endIdx; i++ {
			style := suggestionStyle
			prefix := "    "
			if i == d.pathSuggestionCursor {
				style = selectedStyle
				prefix = "  ▶ "
			}
			content.WriteString(style.Render(prefix + d.pathSuggestions[i]))
			content.WriteString("\n")
		}

		// Show "more below" indicator
		if endIdx < total {
			content.WriteString(suggestionStyle.Render(fmt.Sprintf("    ↓ %d more below", total-endIdx)))
			content.WriteString("\n")
		}
	}
	content.WriteString("\n")

	// Command selection
	if d.focusIndex == 2 {
		content.WriteString(activeLabelStyle.Render("▶ Command:"))
	} else {
		content.WriteString(labelStyle.Render("  Command:"))
	}
	content.WriteString("\n  ")

	// Render command options as consistent pill buttons
	var cmdButtons []string
	for i, cmd := range d.presetCommands {
		displayName := cmd
		if displayName == "" {
			displayName = "shell"
		}
		// Prepend icon for custom tools
		if icon := session.GetToolIcon(cmd); cmd != "" && icon != "" {
			// Only prepend for custom tools (not built-ins which are recognizable by name)
			if toolDef := session.GetToolDef(cmd); toolDef != nil && toolDef.Icon != "" {
				displayName = icon + " " + displayName
			}
		}

		var btnStyle lipgloss.Style
		if i == d.commandCursor {
			// Selected: bright background, bold (active pill)
			btnStyle = lipgloss.NewStyle().
				Foreground(ColorBg).
				Background(ColorAccent).
				Bold(true).
				Padding(0, 2)
		} else {
			// Unselected: subtle background pill (consistent style)
			btnStyle = lipgloss.NewStyle().
				Foreground(ColorTextDim).
				Background(ColorSurface).
				Padding(0, 2)
		}

		cmdButtons = append(cmdButtons, btnStyle.Render(displayName))
	}
	content.WriteString(lipgloss.JoinHorizontal(lipgloss.Left, cmdButtons...))
	content.WriteString("\n\n")

	// Custom command input (only if shell is selected)
	if d.commandCursor == 0 {
		// Show active indicator when command field is focused
		if d.focusIndex == 2 {
			content.WriteString(activeLabelStyle.Render("  ▸ Custom:"))
		} else {
			content.WriteString(labelStyle.Render("    Custom:"))
		}
		content.WriteString("\n    ")
		content.WriteString(d.commandInput.View())
		content.WriteString("\n\n")
	}

	// Worktree checkbox (show when on command field or below)
	worktreeLabel := "Create in worktree"
	if d.focusIndex == 2 {
		worktreeLabel = "Create in worktree (press w)"
	}
	content.WriteString(renderCheckboxLine(worktreeLabel, d.worktreeEnabled, d.focusIndex == 2))

	// Branch input (only visible when worktree is enabled)
	if d.worktreeEnabled {
		content.WriteString("\n")
		if d.focusIndex == 3 {
			content.WriteString(activeLabelStyle.Render("▶ Branch:"))
		} else {
			content.WriteString(labelStyle.Render("  Branch:"))
		}
		content.WriteString("\n")
		content.WriteString("  ")
		content.WriteString(d.branchInput.View())
		content.WriteString("\n")
	}

	// Tool options panel
	if d.toolOptions != nil {
		content.WriteString("\n")
		content.WriteString(d.toolOptions.View())
	}

	// Inline validation error
	if d.validationErr != "" {
		errStyle := lipgloss.NewStyle().Foreground(ColorRed).Bold(true)
		content.WriteString("\n")
		content.WriteString(errStyle.Render("  ⚠ " + d.validationErr))
	}

	content.WriteString("\n")

	// Help text with better contrast
	helpStyle := lipgloss.NewStyle().
		Foreground(ColorComment). // Use consistent theme color
		MarginTop(1)
	helpText := "Tab next/accept │ ↑↓ navigate │ Enter create │ Esc cancel"
	if d.focusIndex == 1 {
		helpText = "Tab autocomplete │ ^N/^P recent │ ↑↓ navigate │ Enter create │ Esc cancel"
	} else if d.focusIndex == 2 {
		selectedCmd := d.GetSelectedCommand()
		if selectedCmd == "gemini" || selectedCmd == "codex" {
			helpText = "←→ command │ w worktree │ y yolo │ Tab next │ Enter create │ Esc cancel"
		} else {
			helpText = "←→ command │ w worktree │ Tab next │ Enter create │ Esc cancel"
		}
	} else if d.toolOptions != nil && d.focusIndex >= d.optionsStartIndex() {
		helpText = "Space/y toggle │ ↑↓ navigate │ Enter create │ Esc cancel"
	}
	content.WriteString(helpStyle.Render(helpText))

	// Wrap in dialog box
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
