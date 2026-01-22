package ui

import (
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// SetupWizard represents the first-time setup wizard dialog
// It guides users through initial configuration when config.toml doesn't exist
type SetupWizard struct {
	visible     bool
	complete    bool
	currentStep int
	width       int
	height      int

	// Step 1: Tool selection
	toolOptions  []string
	selectedTool int // 0=Claude, 1=Gemini, 2=OpenCode, 3=Codex, 4=Shell

	// Step 2: Claude settings (only if Claude selected)
	dangerousMode        bool
	useDefaultConfigDir  bool
	customConfigDir      string
	configDirInput       textinput.Model
	claudeSettingsCursor int // 0=dangerous mode, 1=config dir

	// Theme setting
	selectedTheme int // 0=dark, 1=light
}

// Wizard steps
const (
	stepWelcome        = 0
	stepToolSelection  = 1
	stepClaudeSettings = 2
	stepReady          = 3
)

// NewSetupWizard creates a new setup wizard
func NewSetupWizard() *SetupWizard {
	// Create config dir input
	configInput := textinput.New()
	configInput.Placeholder = "~/.claude"
	configInput.CharLimit = 256
	configInput.Width = 40

	return &SetupWizard{
		visible:             false,
		complete:            false,
		currentStep:         0,
		toolOptions:         []string{"claude", "gemini", "opencode", "codex", "shell"},
		selectedTool:        0, // Default to Claude
		dangerousMode:       false,
		useDefaultConfigDir: true,
		configDirInput:      configInput,
		selectedTheme:       0, // Default to dark
	}
}

// Show makes the wizard visible
func (w *SetupWizard) Show() {
	w.visible = true
	w.complete = false
	w.currentStep = 0
}

// Hide hides the wizard
func (w *SetupWizard) Hide() {
	w.visible = false
}

// IsVisible returns whether the wizard is visible
func (w *SetupWizard) IsVisible() bool {
	return w.visible
}

// IsComplete returns whether the wizard has been completed
func (w *SetupWizard) IsComplete() bool {
	return w.complete
}

// SetSize updates the wizard dimensions
func (w *SetupWizard) SetSize(width, height int) {
	w.width = width
	w.height = height
}

// nextStep advances to the next step
func (w *SetupWizard) nextStep() {
	switch w.currentStep {
	case stepWelcome:
		w.currentStep = stepToolSelection
	case stepToolSelection:
		// Skip Claude settings if non-Claude tool selected
		if w.toolOptions[w.selectedTool] == "claude" {
			w.currentStep = stepClaudeSettings
		} else {
			w.currentStep = stepReady
		}
	case stepClaudeSettings:
		w.currentStep = stepReady
	case stepReady:
		// Don't go beyond Ready step
	}
}

// prevStep goes back to the previous step
func (w *SetupWizard) prevStep() {
	switch w.currentStep {
	case stepWelcome:
		// Can't go before Welcome
	case stepToolSelection:
		w.currentStep = stepWelcome
	case stepClaudeSettings:
		w.currentStep = stepToolSelection
	case stepReady:
		// Skip Claude settings if non-Claude tool selected
		if w.toolOptions[w.selectedTool] == "claude" {
			w.currentStep = stepClaudeSettings
		} else {
			w.currentStep = stepToolSelection
		}
	}
}

// GetConfig returns the user configuration based on wizard selections
func (w *SetupWizard) GetConfig() *session.UserConfig {
	config := &session.UserConfig{
		Tools: make(map[string]session.ToolDef),
		MCPs:  make(map[string]session.MCPDef),
	}

	// Set default tool based on selection
	if w.selectedTool < len(w.toolOptions) {
		tool := w.toolOptions[w.selectedTool]
		if tool != "shell" {
			config.DefaultTool = tool
		}
	}

	// Set Claude settings
	config.Claude.DangerousMode = w.dangerousMode
	if !w.useDefaultConfigDir && w.customConfigDir != "" {
		config.Claude.ConfigDir = w.customConfigDir
	}

	// Set reasonable defaults for other settings
	config.GlobalSearch = session.GlobalSearchSettings{
		Enabled:    true,
		Tier:       "auto",
		RecentDays: 90,
	}

	config.Logs = session.LogSettings{
		MaxSizeMB:     10,
		MaxLines:      10000,
		RemoveOrphans: true,
	}

	config.Updates = session.UpdateSettings{
		CheckEnabled:       true,
		CheckIntervalHours: 24,
		NotifyInCLI:        true,
	}

	// Set MCP pool settings based on platform
	// Only enable on platforms that support Unix sockets
	config.MCPPool = session.MCPPoolSettings{
		Enabled:       false, // Disabled by default, user can enable if desired
		FallbackStdio: true,  // Always fall back to stdio if sockets fail
	}
	// Note: Even on supported platforms, we don't enable by default
	// Users can enable it manually if they want the optimization

	// Theme (defaults to dark)
	if w.selectedTheme == 1 {
		config.Theme = "light"
	} else {
		config.Theme = "dark"
	}

	return config
}

// Update handles key events for the wizard
func (w *SetupWizard) Update(msg tea.Msg) (*SetupWizard, tea.Cmd) {
	if !w.visible {
		return w, nil
	}

	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "enter":
			if w.currentStep == stepReady {
				w.complete = true
				return w, nil
			}
			w.nextStep()
			return w, nil

		case "esc", "backspace":
			w.prevStep()
			return w, nil

		case "up", "k":
			switch w.currentStep {
			case stepToolSelection:
				w.selectedTool--
				if w.selectedTool < 0 {
					w.selectedTool = len(w.toolOptions) - 1
				}
			case stepClaudeSettings:
				w.claudeSettingsCursor--
				if w.claudeSettingsCursor < 0 {
					w.claudeSettingsCursor = 1
				}
			}
			return w, nil

		case "down", "j":
			switch w.currentStep {
			case stepToolSelection:
				w.selectedTool = (w.selectedTool + 1) % len(w.toolOptions)
			case stepClaudeSettings:
				w.claudeSettingsCursor = (w.claudeSettingsCursor + 1) % 2
			}
			return w, nil

		case " ": // Space to toggle
			if w.currentStep == stepClaudeSettings {
				switch w.claudeSettingsCursor {
				case 0:
					w.dangerousMode = !w.dangerousMode
				case 1:
					w.useDefaultConfigDir = !w.useDefaultConfigDir
					if !w.useDefaultConfigDir {
						w.configDirInput.Focus()
					} else {
						w.configDirInput.Blur()
					}
				}
				return w, nil
			}
		}

		// Handle text input for custom config dir
		if w.currentStep == stepClaudeSettings && !w.useDefaultConfigDir {
			w.configDirInput, cmd = w.configDirInput.Update(msg)
			w.customConfigDir = w.configDirInput.Value()
			return w, cmd
		}
	}

	return w, nil
}

// View renders the wizard dialog
func (w *SetupWizard) View() string {
	if !w.visible {
		return ""
	}

	// Styles
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorCyan).
		MarginBottom(1)

	subtitleStyle := lipgloss.NewStyle().
		Foreground(ColorTextDim).
		MarginBottom(1)

	labelStyle := lipgloss.NewStyle().
		Foreground(ColorText)

	selectedStyle := lipgloss.NewStyle().
		Foreground(ColorBg).
		Background(ColorAccent).
		Bold(true).
		Padding(0, 1)

	unselectedStyle := lipgloss.NewStyle().
		Foreground(ColorText).
		Padding(0, 1)

	checkboxOn := lipgloss.NewStyle().
		Foreground(ColorGreen).
		Bold(true).
		Render("[x]")

	checkboxOff := lipgloss.NewStyle().
		Foreground(ColorTextDim).
		Render("[ ]")

	radioOn := lipgloss.NewStyle().
		Foreground(ColorGreen).
		Bold(true).
		Render("(o)")

	radioOff := lipgloss.NewStyle().
		Foreground(ColorTextDim).
		Render("( )")

	helpStyle := lipgloss.NewStyle().
		Foreground(ColorComment).
		MarginTop(1)

	stepIndicatorStyle := lipgloss.NewStyle().
		Foreground(ColorAccent).
		Bold(true)

	// Dialog dimensions
	dialogWidth := 60
	if w.width > 0 && w.width < dialogWidth+10 {
		dialogWidth = w.width - 10
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

	// Build content based on current step
	var content strings.Builder

	// Step indicator
	stepNames := []string{"Welcome", "Tool", "Claude", "Ready"}
	var stepIndicators []string
	for i, name := range stepNames {
		if i == w.currentStep {
			stepIndicators = append(stepIndicators, stepIndicatorStyle.Render("["+name+"]"))
		} else if i == stepClaudeSettings && w.toolOptions[w.selectedTool] != "claude" {
			// Skip Claude step indicator for non-Claude tools
			stepIndicators = append(stepIndicators, lipgloss.NewStyle().Foreground(ColorBorder).Render("-"))
		} else if i < w.currentStep {
			stepIndicators = append(stepIndicators, lipgloss.NewStyle().Foreground(ColorGreen).Render(name))
		} else {
			stepIndicators = append(stepIndicators, lipgloss.NewStyle().Foreground(ColorTextDim).Render(name))
		}
	}
	content.WriteString(strings.Join(stepIndicators, " > "))
	content.WriteString("\n\n")

	switch w.currentStep {
	case stepWelcome:
		content.WriteString(titleStyle.Render("Welcome to Agent Deck!"))
		content.WriteString("\n\n")
		content.WriteString(labelStyle.Render("Agent Deck is a terminal session manager for AI coding agents."))
		content.WriteString("\n\n")
		content.WriteString(labelStyle.Render("This wizard will help you configure:"))
		content.WriteString("\n")
		content.WriteString(lipgloss.NewStyle().Foreground(ColorCyan).Render("  - Default AI tool"))
		content.WriteString("\n")
		content.WriteString(lipgloss.NewStyle().Foreground(ColorCyan).Render("  - Claude Code settings"))
		content.WriteString("\n\n")
		content.WriteString(subtitleStyle.Render("Press Enter to continue or Esc to use defaults."))
		content.WriteString("\n\n")
		content.WriteString(helpStyle.Render("Enter: continue"))

	case stepToolSelection:
		content.WriteString(titleStyle.Render("Select Default AI Tool"))
		content.WriteString("\n\n")
		content.WriteString(subtitleStyle.Render("This tool will be pre-selected when creating new sessions:"))
		content.WriteString("\n\n")

		toolDescriptions := map[string]string{
			"claude":   "Claude Code - Anthropic's AI coding assistant",
			"gemini":   "Gemini CLI - Google's AI assistant",
			"opencode": "OpenCode - Open source AI coding tool",
			"codex":    "Codex CLI - OpenAI's coding assistant",
			"shell":    "Shell - No AI tool (plain terminal)",
		}

		for i, tool := range w.toolOptions {
			icon := ToolIcon(tool)
			desc := toolDescriptions[tool]

			var line string
			if i == w.selectedTool {
				line = selectedStyle.Render(icon + " " + tool)
			} else {
				line = unselectedStyle.Render(icon + " " + tool)
			}
			content.WriteString("  " + line)
			content.WriteString(lipgloss.NewStyle().Foreground(ColorTextDim).Render("  " + desc))
			content.WriteString("\n")
		}

		content.WriteString("\n")
		content.WriteString(helpStyle.Render("Up/Down: select | Enter: continue | Esc: back"))

	case stepClaudeSettings:
		content.WriteString(titleStyle.Render("Claude Code Settings"))
		content.WriteString("\n\n")

		// Dangerous mode checkbox
		checkbox := checkboxOff
		if w.dangerousMode {
			checkbox = checkboxOn
		}
		cursor := "  "
		style := labelStyle
		if w.claudeSettingsCursor == 0 {
			cursor = "> "
			style = lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
		}
		content.WriteString(cursor + checkbox + " " + style.Render("Enable dangerous mode"))
		content.WriteString("\n")
		content.WriteString(lipgloss.NewStyle().Foreground(ColorTextDim).Render("    Skip permission prompts (--dangerously-skip-permissions)"))
		content.WriteString("\n\n")

		// Config directory radio buttons
		cursor = "  "
		style = labelStyle
		if w.claudeSettingsCursor == 1 {
			cursor = "> "
			style = lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
		}
		content.WriteString(cursor + style.Render("Claude config directory:"))
		content.WriteString("\n")

		// Default option
		radio := radioOff
		if w.useDefaultConfigDir {
			radio = radioOn
		}
		content.WriteString("    " + radio + " Use default (~/.claude)")
		content.WriteString("\n")

		// Custom option
		radio = radioOff
		if !w.useDefaultConfigDir {
			radio = radioOn
		}
		content.WriteString("    " + radio + " Use custom: ")
		if !w.useDefaultConfigDir {
			content.WriteString(w.configDirInput.View())
		} else {
			content.WriteString(lipgloss.NewStyle().Foreground(ColorTextDim).Render("(press Space to select)"))
		}
		content.WriteString("\n\n")

		content.WriteString(helpStyle.Render("Up/Down: navigate | Space: toggle | Enter: continue | Esc: back"))

	case stepReady:
		content.WriteString(titleStyle.Render("Ready to Go!"))
		content.WriteString("\n\n")
		content.WriteString(labelStyle.Render("Your configuration:"))
		content.WriteString("\n\n")

		// Show selected tool
		selectedTool := w.toolOptions[w.selectedTool]
		icon := ToolIcon(selectedTool)
		content.WriteString(lipgloss.NewStyle().Foreground(ColorCyan).Render("  Default tool: "))
		content.WriteString(icon + " " + selectedTool)
		content.WriteString("\n")

		// Show Claude settings if applicable
		if selectedTool == "claude" {
			content.WriteString(lipgloss.NewStyle().Foreground(ColorCyan).Render("  Dangerous mode: "))
			if w.dangerousMode {
				content.WriteString(lipgloss.NewStyle().Foreground(ColorGreen).Render("enabled"))
			} else {
				content.WriteString(lipgloss.NewStyle().Foreground(ColorTextDim).Render("disabled"))
			}
			content.WriteString("\n")

			content.WriteString(lipgloss.NewStyle().Foreground(ColorCyan).Render("  Config dir: "))
			if w.useDefaultConfigDir {
				content.WriteString("~/.claude (default)")
			} else {
				content.WriteString(w.customConfigDir)
			}
			content.WriteString("\n")
		}

		content.WriteString("\n")
		content.WriteString(subtitleStyle.Render("Press Enter to save and start using Agent Deck!"))
		content.WriteString("\n\n")
		content.WriteString(helpStyle.Render("Enter: save & finish | Esc: back"))
	}

	// Wrap in dialog box
	dialog := dialogStyle.Render(content.String())

	// Center the dialog using lipgloss.Place
	return lipgloss.Place(
		w.width,
		w.height,
		lipgloss.Center,
		lipgloss.Center,
		dialog,
	)
}
