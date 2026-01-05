package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

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

	return &NewDialog{
		nameInput:       nameInput,
		pathInput:       pathInput,
		commandInput:    commandInput,
		claudeOptions:   NewClaudeOptionsPanel(),
		focusIndex:      0,
		visible:         false,
		presetCommands:  []string{"", "claude", "gemini", "opencode", "codex"},
		commandCursor:   0,
		parentGroupPath: "default",
		parentGroupName: "default",
	}
}

// ShowInGroup shows the dialog with a pre-selected parent group
func (d *NewDialog) ShowInGroup(groupPath, groupName string) {
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
	d.suggestionNavigated = false  // reset on show
	d.pathSuggestionCursor = 0     // reset cursor too
	d.pathInput.Blur()
	d.claudeOptions.Blur()
	// Keep commandCursor at previously set default (don't reset to 0)

	// Initialize Claude options with defaults from config
	if config, err := session.LoadUserConfig(); err == nil {
		d.claudeOptions.SetDefaults(config)
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
			return
		}
	}

	// Tool not found in presets, default to shell
	d.commandCursor = 0
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
	d.ShowInGroup("default", "default")
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
	if len(name) > 50 {
		return "Session name too long (max 50 characters)"
	}

	// Check for empty path
	if path == "" {
		return "Project path cannot be empty"
	}

	return "" // Valid
}

// updateFocus updates which input has focus
func (d *NewDialog) updateFocus() {
	d.nameInput.Blur()
	d.pathInput.Blur()
	d.commandInput.Blur()
	d.claudeOptions.Blur()

	switch d.focusIndex {
	case 0:
		d.nameInput.Focus()
	case 1:
		d.pathInput.Focus()
	case 2:
		// Command selection (no text input focus needed for presets)
	default:
		// Claude options (focusIndex >= 3)
		if d.isClaudeSelected() {
			d.claudeOptions.Focus()
		}
	}
}

// getMaxFocusIndex returns the maximum focus index based on current state
func (d *NewDialog) getMaxFocusIndex() int {
	if d.isClaudeSelected() {
		return 3 // 0=name, 1=path, 2=command, 3=claude options
	}
	return 2 // 0=name, 1=path, 2=command
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
			// On path field: apply selected suggestion ONLY if user explicitly navigated to one
			if d.focusIndex == 1 && d.suggestionNavigated && len(d.pathSuggestions) > 0 {
				if d.pathSuggestionCursor < len(d.pathSuggestions) {
					d.pathInput.SetValue(d.pathSuggestions[d.pathSuggestionCursor])
				}
			}
			// Move to next field (with wrap)
			if d.focusIndex < maxIdx {
				d.focusIndex++
				d.updateFocus()
			} else if d.focusIndex >= 3 && d.isClaudeSelected() {
				// Inside claude options - delegate
				d.claudeOptions, cmd = d.claudeOptions.Update(msg)
				return d, cmd
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
			// Down navigates fields or delegates to options
			if d.focusIndex < maxIdx {
				d.focusIndex++
				d.updateFocus()
			} else if d.focusIndex >= 3 && d.isClaudeSelected() {
				// Inside claude options - delegate
				d.claudeOptions, cmd = d.claudeOptions.Update(msg)
				return d, cmd
			}
			return d, nil

		case "shift+tab", "up":
			if d.focusIndex >= 3 && d.isClaudeSelected() && d.claudeOptions.focusIndex > 0 {
				// Inside claude options, not at first item - delegate
				d.claudeOptions, cmd = d.claudeOptions.Update(msg)
				return d, cmd
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
			// Command selection
			if d.focusIndex == 2 {
				d.commandCursor--
				if d.commandCursor < 0 {
					d.commandCursor = len(d.presetCommands) - 1
				}
				return d, nil
			}
			// Delegate to claude options if focused there
			if d.focusIndex >= 3 && d.isClaudeSelected() {
				d.claudeOptions, cmd = d.claudeOptions.Update(msg)
				return d, cmd
			}

		case "right":
			// Command selection
			if d.focusIndex == 2 {
				d.commandCursor = (d.commandCursor + 1) % len(d.presetCommands)
				return d, nil
			}
			// Delegate to claude options if focused there
			if d.focusIndex >= 3 && d.isClaudeSelected() {
				d.claudeOptions, cmd = d.claudeOptions.Update(msg)
				return d, cmd
			}

		case " ":
			// Space toggles checkboxes in claude options
			if d.focusIndex >= 3 && d.isClaudeSelected() {
				d.claudeOptions, cmd = d.claudeOptions.Update(msg)
				return d, cmd
			}
		}
	}

	// Update focused input
	switch d.focusIndex {
	case 0:
		d.nameInput, cmd = d.nameInput.Update(msg)
	case 1:
		oldValue := d.pathInput.Value()
		d.pathInput, cmd = d.pathInput.Update(msg)
		// Reset navigation if user typed something new
		if d.pathInput.Value() != oldValue {
			d.suggestionNavigated = false
			d.pathSuggestionCursor = 0
		}
	default:
		if d.focusIndex >= 3 && d.isClaudeSelected() {
			d.claudeOptions, cmd = d.claudeOptions.Update(msg)
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
		content.WriteString(lipgloss.NewStyle().Foreground(ColorComment).Render("─ recent paths (Tab: accept, Ctrl+N/P: cycle) ─"))
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
		content.WriteString(labelStyle.Render("  Custom command:"))
		content.WriteString("\n  ")
		content.WriteString(d.commandInput.View())
		content.WriteString("\n\n")
	}

	// Claude options (only if Claude is selected)
	if d.isClaudeSelected() {
		content.WriteString("\n")
		content.WriteString(d.claudeOptions.View())
	}

	// Help text with better contrast
	helpStyle := lipgloss.NewStyle().
		Foreground(ColorComment). // Use consistent theme color
		MarginTop(1)
	if d.isClaudeSelected() {
		content.WriteString(helpStyle.Render("Tab next │ ↑↓ navigate │ Space toggle │ Enter create │ Esc cancel"))
	} else {
		content.WriteString(helpStyle.Render("Tab next/accept │ ↑↓ navigate │ Enter create │ Esc cancel"))
	}

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
