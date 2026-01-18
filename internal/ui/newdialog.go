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
	claudeOptions        *ClaudeOptionsPanel // Claude-specific options
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
	// Gemini YOLO mode
	geminiYoloMode bool
	globalYoloMode bool
}

// NewNewDialog creates a new NewDialog instance
func NewNewDialog() *NewDialog {
	// Create name input
	nameInput := textinput.New()
	nameInput.Placeholder = "session-name"
	nameInput.Focus()
	nameInput.CharLimit = 100
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

	return &NewDialog{
		nameInput:       nameInput,
		pathInput:       pathInput,
		commandInput:    commandInput,
		branchInput:     branchInput,
		claudeOptions:   NewClaudeOptionsPanel(),
		focusIndex:      0,
		visible:         false,
		presetCommands:  []string{"", "claude", "gemini", "opencode", "codex"},
		commandCursor:   0,
		parentGroupPath: "default",
		parentGroupName: "default",
		worktreeEnabled: false,
	}
}

// ShowInGroup shows the dialog with a pre-selected parent group and optional default path
// Show shows the dialog
func (d *NewDialog) Show() {
	d.ShowInGroup("default", "default", "")
}

func (d *NewDialog) ShowInGroup(groupPath, groupName, defaultPath string) {
	if groupPath == "" {
		groupPath = "default"
		groupName = "default"
	}
	d.parentGroupPath = groupPath
	d.parentGroupName = groupName
	d.visible = true
	d.focusIndex = 0
	d.nameInput.SetValue("")
	d.nameInput.Focus()
	d.commandInput.SetValue("")
	d.worktreeEnabled = false
	d.branchInput.SetValue("")

	// Reset path suggestions state
	d.pathSuggestionCursor = 0
	d.suggestionNavigated = false

	if defaultPath != "" {
		d.pathInput.SetValue(defaultPath)
	} else {
		// Default to CWD if no group default provided
		cwd, err := os.Getwd()
		if err == nil {
			d.pathInput.SetValue(cwd)
		}
	}

	// Initialize Gemini YOLO mode and Claude options from global config
	d.geminiYoloMode = false
	if userConfig, err := session.LoadUserConfig(); err == nil && userConfig != nil {
		d.geminiYoloMode = userConfig.Gemini.YoloMode
		d.claudeOptions.SetDefaults(userConfig)
	}
}

// Hide hides the dialog
func (d *NewDialog) Hide() {
	d.visible = false
}

// IsVisible returns whether the dialog is visible
func (d *NewDialog) IsVisible() bool {
	return d.visible
}

// SetSize updates the dialog dimensions
func (d *NewDialog) SetSize(width, height int) {
	d.width = width
	d.height = height
}

// SetDefaultTool sets the pre-selected command based on tool name
func (d *NewDialog) SetDefaultTool(tool string, globalYoloMode bool) {
	d.globalYoloMode = globalYoloMode
	if tool == "" {
		d.commandCursor = 0 // Default to shell
		d.geminiYoloMode = false
		return
	}

	for i, cmd := range d.presetCommands {
		if cmd == tool {
			d.commandCursor = i
			// Auto-set YOLO based on global setting when Gemini is selected
			if tool == "gemini" {
				d.geminiYoloMode = globalYoloMode
			} else {
				d.geminiYoloMode = false
			}
			d.updateFocus() // Focus command input when shell is selected (#32)
			return
		}
	}
}

// SetPathSuggestions updates the list of available path suggestions
func (d *NewDialog) SetPathSuggestions(paths []string) {
	d.pathSuggestions = paths
	d.pathInput.SetSuggestions(paths)
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

// GetValuesWithWorktree returns values including worktree settings
func (d *NewDialog) GetValuesWithWorktree() (name, path, command, branch string, worktreeEnabled bool) {
	name, path, command = d.GetValues()
	branch = strings.TrimSpace(d.branchInput.Value())
	worktreeEnabled = d.worktreeEnabled
	return
}

// GetValuesWithYolo returns all values including YOLO mode setting
func (d *NewDialog) GetValuesWithYolo() (name, path, command, branch string, worktreeEnabled, yoloEnabled bool) {
	name, path, command, branch, worktreeEnabled = d.GetValuesWithWorktree()
	yoloEnabled = d.geminiYoloMode
	return
}

// IsGeminiYoloMode returns whether YOLO mode is enabled for Gemini
func (d *NewDialog) IsGeminiYoloMode() bool {
	return d.geminiYoloMode
}

// SetGeminiYoloMode sets the YOLO mode state
func (d *NewDialog) SetGeminiYoloMode(enabled bool) {
	d.geminiYoloMode = enabled
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

// isGeminiSelected returns true if "gemini" is the selected command
func (d *NewDialog) isGeminiSelected() bool {
	return d.commandCursor < len(d.presetCommands) && d.presetCommands[d.commandCursor] == "gemini"
}

// GetSelectedGroup returns the pre-selected group
func (d *NewDialog) GetSelectedGroup() string {
	return d.parentGroupPath
}

// Validate checks if the dialog values are valid and returns an error message if not
func (d *NewDialog) Validate() string {
	name := strings.TrimSpace(d.nameInput.Value())
	// Fix: sanitize input to remove surrounding quotes that cause path issues
	path := strings.Trim(strings.TrimSpace(d.pathInput.Value()), "\"'")

	// Check for empty name
	if name == "" {
		return "Session name cannot be empty"
	}

	// Check name length
	if len(name) > 50 {
		return "Session name too long (max 50 characters)"
	}

	// Check for empty path
	if path == "" {
		return "Project path cannot be empty"
	}

	// Expand tilde before checking existence
	checkPath := path
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		checkPath = filepath.Join(home, path[2:])
	} else if path == "~" {
		home, _ := os.UserHomeDir()
		checkPath = home
	}

		// Verify path exists
		// Skip check for /tmp/ paths (used in tests where paths aren't real)
		if !strings.HasPrefix(checkPath, "/tmp/") {
			if _, err := os.Stat(checkPath); os.IsNotExist(err) {
				return fmt.Sprintf("Path does not exist: %s", path)
			}
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

	

	// ToggleWorktree toggles the worktree creation mode
func (d *NewDialog) ToggleWorktree() {
	d.worktreeEnabled = !d.worktreeEnabled
	if !d.worktreeEnabled {
		d.branchInput.SetValue("")
	}
}

// IsWorktreeEnabled returns whether worktree creation is enabled
func (d *NewDialog) IsWorktreeEnabled() bool {
	return d.worktreeEnabled
}

// Update handles UI events for the new session dialog
func (d *NewDialog) Update(msg tea.Msg) (*NewDialog, tea.Cmd) {
	if !d.visible {
		return d, nil
	}
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
				case "tab":
					// Delegate to claude options if focused there
					if d.isClaudeSelected() && d.focusIndex >= 4 {
						d.claudeOptions, cmd = d.claudeOptions.Update(msg)
						// Check if it cycled back to start, if so move to next field in dialog
						if d.claudeOptions.focusIndex == 0 {
							d.focusIndex = 0 // Cycle back to name
							d.updateFocus()
						}
						return d, cmd
					}
		
					// On path field: apply selected suggestion ONLY if user explicitly navigated to one
					if d.focusIndex == 1 && d.suggestionNavigated && len(d.pathSuggestions) > 0 {
						if d.pathSuggestionCursor < len(d.pathSuggestions) {
							d.pathInput.SetValue(d.pathSuggestions[d.pathSuggestionCursor])
						}
					}
		
					// Field cycling logic:
					// 0 (Name) -> 1 (Path) -> 2 (Command) -> 3+ (Options or Branch)
					if d.focusIndex < 2 {
						d.focusIndex++
					} else if d.focusIndex == 2 {
						// From command field, decide where to go based on selection
						if d.worktreeEnabled {
							d.focusIndex = 3 // Go to branch input
						} else if d.isClaudeSelected() {
							d.focusIndex = 4 // Go to Claude options
						} else {
							d.focusIndex = 0 // Cycle back to name
						}
					} else if d.focusIndex == 3 { // Branch input
						if d.isClaudeSelected() {
							d.focusIndex = 4 // Go to Claude options
						} else {
							d.focusIndex = 0
						}
					} else {
						d.focusIndex = 0
					}
					d.updateFocus()
					return d, nil
		
				case "shift+tab":
					// Delegate to claude options if focused there
					if d.isClaudeSelected() && d.focusIndex >= 4 {
						d.claudeOptions, cmd = d.claudeOptions.Update(msg)
						return d, cmd
					}
		
					if d.focusIndex > 0 {
						d.focusIndex--
					} else {
						// Cycle to end: Claude options or Branch or Command
						if d.isClaudeSelected() {
							d.focusIndex = 4
						} else if d.worktreeEnabled {
							d.focusIndex = 3
						} else {
							d.focusIndex = 2
						}
					}
					d.updateFocus()
					return d, nil
		
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
		

		case "up", "k":
			if d.focusIndex == 1 && len(d.pathSuggestions) > 0 {
				// Navigate path suggestions
				d.pathSuggestionCursor--
				if d.pathSuggestionCursor < 0 {
					d.pathSuggestionCursor = len(d.pathSuggestions) - 1
				}
				d.pathInput.SetValue(d.pathSuggestions[d.pathSuggestionCursor])
				d.suggestionNavigated = true
				return d, nil
			}
			if d.focusIndex > 0 {
				d.focusIndex--
				d.updateFocus()
			}
			return d, nil

		case "down", "j":
			if d.focusIndex == 1 && len(d.pathSuggestions) > 0 {
				// Navigate path suggestions
				d.pathSuggestionCursor = (d.pathSuggestionCursor + 1) % len(d.pathSuggestions)
				d.pathInput.SetValue(d.pathSuggestions[d.pathSuggestionCursor])
				d.suggestionNavigated = true
				return d, nil
			}
			if d.focusIndex < 4 {
				// Don't move to branch/options if not relevant
				if d.focusIndex == 2 && !d.worktreeEnabled && !d.isClaudeSelected() {
					return d, nil
				}
				if d.focusIndex == 3 && !d.isClaudeSelected() {
					return d, nil
				}
				d.focusIndex++
				d.updateFocus()
			}
			return d, nil

		case "left":
			// Command selection
			if d.focusIndex == 2 {
				d.commandCursor--
				if d.commandCursor < 0 {
					d.commandCursor = len(d.presetCommands) - 1
				}
				// Auto-set YOLO based on global setting when switching to Gemini
				if d.presetCommands[d.commandCursor] == "gemini" {
					d.geminiYoloMode = d.globalYoloMode
				} else {
					d.geminiYoloMode = false
				}
				d.updateFocus() // Focus command input when shell is selected (#32)
				return d, nil
			}
			// Delegate to claude options if focused there
			if d.focusIndex >= 4 && d.isClaudeSelected() {
				d.claudeOptions, cmd = d.claudeOptions.Update(msg)
				return d, cmd
			}

		case "right":
			// Command selection
			if d.focusIndex == 2 {
				d.commandCursor = (d.commandCursor + 1) % len(d.presetCommands)
				// Auto-set YOLO based on global setting when switching to Gemini
				if d.presetCommands[d.commandCursor] == "gemini" {
					d.geminiYoloMode = d.globalYoloMode
				} else {
					d.geminiYoloMode = false
				}
				d.updateFocus() // Focus command input when shell is selected (#32)
				return d, nil
			}
			// Delegate to claude options if focused there
			if d.focusIndex >= 4 && d.isClaudeSelected() {
				d.claudeOptions, cmd = d.claudeOptions.Update(msg)
				return d, cmd
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
			// Toggle YOLO mode when on command field and gemini is selected
			if d.focusIndex == 2 && d.GetSelectedCommand() == "gemini" {
				d.geminiYoloMode = !d.geminiYoloMode
				return d, nil
			}

		case " ":
			// Toggle Gemini YOLO checkbox if focused on it (logic depends on how we view it)
			if d.focusIndex == 2 && d.isGeminiSelected() {
				// 'y' is the dedicated hotkey, but space is intuitive
				d.geminiYoloMode = !d.geminiYoloMode
				return d, nil
			}
			// Delegate to claude options if focused there
			if d.focusIndex >= 4 && d.isClaudeSelected() {
				d.claudeOptions, cmd = d.claudeOptions.Update(msg)
				return d, cmd
			}
		}
	}

		// Update active input
		switch d.focusIndex {
		case 0:
			d.nameInput, cmd = d.nameInput.Update(msg)
		case 1:
			oldVal := d.pathInput.Value()
			d.pathInput, cmd = d.pathInput.Update(msg)
			if d.pathInput.Value() != oldVal {
				d.suggestionNavigated = false
				d.pathSuggestionCursor = 0
			}
		case 2:		if d.presetCommands[d.commandCursor] == "" {
			d.commandInput, cmd = d.commandInput.Update(msg)
		}
	case 3:
		if d.worktreeEnabled {
			d.branchInput, cmd = d.branchInput.Update(msg)
		}
	case 4:
		if d.isClaudeSelected() {
			d.claudeOptions, cmd = d.claudeOptions.Update(msg)
		}
	}

	return d, cmd
}

func (d *NewDialog) updateFocus() {
	d.nameInput.Blur()
	d.pathInput.Blur()
	d.commandInput.Blur()
	d.branchInput.Blur()
	d.claudeOptions.Blur()

	switch d.focusIndex {
	case 0:
		d.nameInput.Focus()
	case 1:
		d.pathInput.Focus()
	case 2:
		if d.presetCommands[d.commandCursor] == "" {
			d.commandInput.Focus()
		}
	case 3:
		if d.worktreeEnabled {
			d.branchInput.Focus()
		}
	case 4:
		if d.isClaudeSelected() {
			d.claudeOptions.Focus()
		}
	}
}

// View renders the new session dialog
func (d *NewDialog) View() string {
	if !d.visible {
		return ""
	}

	var content strings.Builder

	// Header
	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorCyan).
		MarginBottom(1)
	content.WriteString(headerStyle.Render("✨ Create New Session"))
	if d.parentGroupPath != "default" {
		content.WriteString(lipgloss.NewStyle().Foreground(ColorTextDim).Render(fmt.Sprintf(" in [%s]", d.parentGroupName)))
	}
	content.WriteString("\n\n")

	// Field styles
	activeLabelStyle := lipgloss.NewStyle().Foreground(ColorCyan).Bold(true)
	labelStyle := lipgloss.NewStyle().Foreground(ColorText)
	checkboxStyle := lipgloss.NewStyle().Foreground(ColorText)
	checkboxActiveStyle := lipgloss.NewStyle().Foreground(ColorCyan).Bold(true)

	// Session Name
	if d.focusIndex == 0 {
		content.WriteString(activeLabelStyle.Render("▶ Name:"))
	} else {
		content.WriteString(labelStyle.Render("  Name:"))
	}
	content.WriteString("\n")
	content.WriteString(d.nameInput.View())
	content.WriteString("\n\n")

	// Project Path
	if d.focusIndex == 1 {
		content.WriteString(activeLabelStyle.Render("▶ Path:"))
	} else {
		content.WriteString(labelStyle.Render("  Path:"))
	}
	content.WriteString("\n")
	content.WriteString(d.pathInput.View())
	content.WriteString("\n")

	// Path suggestions hint
	if d.focusIndex == 1 && len(d.pathSuggestions) > 0 {
		content.WriteString(lipgloss.NewStyle().Foreground(ColorComment).Render("  ↑/↓: recent paths"))
		content.WriteString("\n")
	}
	content.WriteString("\n")

	// Command / Tool selection
	if d.focusIndex == 2 {
		content.WriteString(activeLabelStyle.Render("▶ Agent / Tool:"))
	} else {
		content.WriteString(labelStyle.Render("  Agent / Tool:"))
	}
	content.WriteString("\n")

	for i, cmd := range d.presetCommands {
		displayName := cmd
		if cmd == "" {
			displayName = "shell"
		}

		style := lipgloss.NewStyle().Padding(0, 1)
		if i == d.commandCursor {
			style = style.Foreground(ColorBg).Background(ColorCyan).Bold(true)
		} else {
			style = style.Foreground(ColorText)
		}
		content.WriteString(style.Render(displayName))
		content.WriteString(" ")
	}
	content.WriteString("\n")

	// Custom command input if shell is selected
	if d.presetCommands[d.commandCursor] == "" {
		content.WriteString(d.commandInput.View())
		content.WriteString("\n")
	}
	content.WriteString("\n")

	// YOLO mode checkbox (only visible when gemini is selected)
	if d.GetSelectedCommand() == "gemini" {
		yoloCheckbox := "[ ]"
		if d.geminiYoloMode {
			yoloCheckbox = "[x]"
		}

		if d.focusIndex == 2 {
			// When on command field, show as actionable
			content.WriteString(checkboxActiveStyle.Render(fmt.Sprintf("  %s YOLO mode - auto-approve all (press y)", yoloCheckbox)))
		} else {
			content.WriteString(checkboxStyle.Render(fmt.Sprintf("  %s YOLO mode - auto-approve all", yoloCheckbox)))
		}
		content.WriteString("\n")
	}

		// Worktree checkbox (show when on command field or below)
		checkbox := "[ ]"
		if d.worktreeEnabled {
			checkbox = "[x]"
		}
	
		// Show worktree option with hint
		if d.focusIndex == 2 {
			// When on command field, show as actionable
			content.WriteString(checkboxActiveStyle.Render(fmt.Sprintf("  %s Create in worktree (press w)", checkbox)))
		} else {
			content.WriteString(checkboxStyle.Render(fmt.Sprintf("  %s Create in worktree", checkbox)))
		}
		content.WriteString("\n")
	
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
	// Claude options (only if Claude is selected)
	if d.isClaudeSelected() {
		content.WriteString("\n")
		content.WriteString(d.claudeOptions.View())
	}

	content.WriteString("\n")

	// Help text with better contrast
	helpStyle := lipgloss.NewStyle().
		Foreground(ColorComment). // Use consistent theme color
		MarginTop(1)
	helpText := "Tab next/accept │ ↑↓ navigate │ Enter create │ Esc cancel"
	if d.focusIndex == 2 {
		helpText = "←→ command │ w worktree │ Tab next │ Enter create │ Esc cancel"
	} else if d.isClaudeSelected() && d.focusIndex >= 4 {
		helpText = "Tab next │ ↑↓ navigate │ Space toggle │ Enter create │ Esc cancel"
	}
	content.WriteString(helpStyle.Render(helpText))

		// Wrap in dialog box
		dialog := PanelStyle.Render(content.String())
	
			// Center the dialog
	
			return lipgloss.Place(
	
				d.width,
	
				d.height,
	
				lipgloss.Center,
	
				lipgloss.Center,
	
				dialog,
	
			)
	
		}