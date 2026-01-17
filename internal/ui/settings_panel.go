package ui

import (
	"strconv"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// SettingType identifies which setting is being edited
type SettingType int

const (
	SettingTheme SettingType = iota // Theme must be first (index 0)
	SettingDefaultTool
	SettingDangerousMode
	SettingClaudeConfigDir
	SettingCheckForUpdates
	SettingAutoUpdate
	SettingLogMaxSize
	SettingLogMaxLines
	SettingRemoveOrphans
	SettingGlobalSearchEnabled
	SettingSearchTier
	SettingRecentDays
	SettingShowOutput
	SettingShowAnalytics
)

// Total number of navigable settings
const settingsCount = 14

// SettingsPanel displays and edits user configuration
type SettingsPanel struct {
	visible bool
	width   int
	height  int
	cursor  int // Current setting index

	// Setting values
	selectedTheme       int // 0=dark, 1=light
	selectedTool        int // 0=claude, 1=gemini, 2=opencode, 3=codex, 4=none
	dangerousMode       bool
	claudeConfigDir     string
	checkForUpdates     bool
	autoUpdate          bool
	logMaxSizeMB        int
	logMaxLines         int
	removeOrphans       bool
	globalSearchEnabled bool
	searchTier          int // 0=auto, 1=instant, 2=balanced
	recentDays          int
	showOutput          bool
	showAnalytics       bool

	// Text input state
	editingText bool
	textBuffer  string

	// Track if global search settings changed (requires restart)
	needsRestart bool

	// Original config for detecting changes
	originalConfig *session.UserConfig
}

// Tool names for radio selection
var toolNames = []string{"Claude", "Gemini", "OpenCode", "Codex", "None"}
var toolValues = []string{"claude", "gemini", "opencode", "codex", ""}

// Search tier names for radio selection
var tierNames = []string{"Auto", "Instant", "Balanced"}
var tierValues = []string{"auto", "instant", "balanced"}

// Theme names for radio selection
var themeNames = []string{"Dark", "Light"}
var themeValues = []string{"dark", "light"}

// NewSettingsPanel creates a new settings panel
func NewSettingsPanel() *SettingsPanel {
	return &SettingsPanel{
		logMaxSizeMB:        10,
		logMaxLines:         10000,
		removeOrphans:       true,
		checkForUpdates:     true,
		globalSearchEnabled: true,
		recentDays:          90,
		showOutput:          true, // Default: output ON (shows launch animation)
		showAnalytics:       true, // Default: analytics ON
	}
}

// Show displays the settings panel and loads current config
func (s *SettingsPanel) Show() {
	s.visible = true
	s.cursor = 0
	s.editingText = false
	s.needsRestart = false

	// Load current config
	config, _ := session.LoadUserConfig()
	if config != nil {
		s.LoadConfig(config)
		s.originalConfig = config
	}
}

// Hide hides the settings panel
func (s *SettingsPanel) Hide() {
	s.visible = false
	s.editingText = false
}

// IsVisible returns whether the panel is visible
func (s *SettingsPanel) IsVisible() bool {
	return s.visible
}

// NeedsRestart returns true if changes require a restart
func (s *SettingsPanel) NeedsRestart() bool {
	return s.needsRestart
}

// SetSize sets the panel dimensions
func (s *SettingsPanel) SetSize(width, height int) {
	s.width = width
	s.height = height
}

// LoadConfig populates panel values from a UserConfig
func (s *SettingsPanel) LoadConfig(config *session.UserConfig) {
	// Load theme
	switch config.Theme {
	case "light":
		s.selectedTheme = 1
	default:
		s.selectedTheme = 0
	}

	// Default tool
	s.selectedTool = 4 // None by default
	for i, val := range toolValues {
		if val == config.DefaultTool {
			s.selectedTool = i
			break
		}
	}

	// Claude settings
	s.dangerousMode = config.Claude.DangerousMode
	s.claudeConfigDir = config.Claude.ConfigDir

	// Update settings
	s.checkForUpdates = config.Updates.CheckEnabled
	s.autoUpdate = config.Updates.AutoUpdate

	// Log settings
	s.logMaxSizeMB = config.Logs.MaxSizeMB
	if s.logMaxSizeMB <= 0 {
		s.logMaxSizeMB = 10
	}
	s.logMaxLines = config.Logs.MaxLines
	if s.logMaxLines <= 0 {
		s.logMaxLines = 10000
	}
	s.removeOrphans = config.Logs.RemoveOrphans

	// Global search settings
	s.globalSearchEnabled = config.GlobalSearch.Enabled
	s.searchTier = 0 // auto by default
	for i, val := range tierValues {
		if val == config.GlobalSearch.Tier {
			s.searchTier = i
			break
		}
	}
	s.recentDays = config.GlobalSearch.RecentDays
	if s.recentDays < 0 {
		s.recentDays = 90
	}

	// Preview settings
	s.showOutput = config.GetShowOutput()
	s.showAnalytics = config.GetShowAnalytics()
}

// GetConfig returns a UserConfig with current panel values
func (s *SettingsPanel) GetConfig() *session.UserConfig {
	config := &session.UserConfig{
		DefaultTool: "",
		Tools:       make(map[string]session.ToolDef),
		MCPs:        make(map[string]session.MCPDef),
	}

	// Theme
	if s.selectedTheme < len(themeValues) {
		config.Theme = themeValues[s.selectedTheme]
	}

	// Default tool
	if s.selectedTool >= 0 && s.selectedTool < len(toolValues) {
		config.DefaultTool = toolValues[s.selectedTool]
	}

	// Claude settings
	config.Claude.DangerousMode = s.dangerousMode
	config.Claude.ConfigDir = s.claudeConfigDir

	// Update settings
	config.Updates.CheckEnabled = s.checkForUpdates
	config.Updates.AutoUpdate = s.autoUpdate

	// Log settings
	config.Logs.MaxSizeMB = s.logMaxSizeMB
	config.Logs.MaxLines = s.logMaxLines
	config.Logs.RemoveOrphans = s.removeOrphans

	// Global search settings
	config.GlobalSearch.Enabled = s.globalSearchEnabled
	if s.searchTier >= 0 && s.searchTier < len(tierValues) {
		config.GlobalSearch.Tier = tierValues[s.searchTier]
	}
	config.GlobalSearch.RecentDays = s.recentDays

	// Preview settings
	showOutput := s.showOutput
	config.Preview.ShowOutput = &showOutput
	showAnalytics := s.showAnalytics
	config.Preview.ShowAnalytics = &showAnalytics

	// Preserve original MCPs and Tools if we have them
	if s.originalConfig != nil {
		config.MCPs = s.originalConfig.MCPs
		config.Tools = s.originalConfig.Tools
		config.MCPPool = s.originalConfig.MCPPool
	}

	return config
}

// Update handles input and returns (panel, cmd, valueChanged)
func (s *SettingsPanel) Update(msg tea.KeyMsg) (*SettingsPanel, tea.Cmd, bool) {
	if !s.visible {
		return s, nil, false
	}

	// Handle text editing mode
	if s.editingText {
		return s.handleTextEdit(msg)
	}

	valueChanged := false
	key := msg.String()

	switch key {
	case "esc", "S":
		s.Hide()
		return s, nil, false

	case "up", "k":
		if s.cursor > 0 {
			s.cursor--
		}

	case "down", "j":
		if s.cursor < settingsCount-1 {
			s.cursor++
		}

	case "left", "h":
		valueChanged = s.adjustValue(-1)

	case "right", "l":
		valueChanged = s.adjustValue(1)

	case " ":
		valueChanged = s.toggleValue()

	case "enter":
		if s.isTextSetting() {
			s.startTextEdit()
		}
	}

	return s, nil, valueChanged
}

// adjustValue changes a radio or number value by delta
func (s *SettingsPanel) adjustValue(delta int) bool {
	setting := SettingType(s.cursor)
	changed := false

	switch setting {
	case SettingTheme:
		newVal := s.selectedTheme + delta
		if newVal >= 0 && newVal < len(themeNames) {
			s.selectedTheme = newVal
			changed = true
			s.needsRestart = true
		}

	case SettingDefaultTool:
		newVal := s.selectedTool + delta
		if newVal >= 0 && newVal < len(toolNames) {
			s.selectedTool = newVal
			changed = true
		}

	case SettingSearchTier:
		newVal := s.searchTier + delta
		if newVal >= 0 && newVal < len(tierNames) {
			oldTier := s.searchTier
			s.searchTier = newVal
			changed = true
			if oldTier != newVal {
				s.needsRestart = true
			}
		}

	case SettingLogMaxSize:
		newVal := s.logMaxSizeMB + delta
		if newVal >= 1 {
			s.logMaxSizeMB = newVal
			changed = true
		}

	case SettingLogMaxLines:
		// Adjust by 1000 for lines
		newVal := s.logMaxLines + (delta * 1000)
		if newVal >= 1000 {
			s.logMaxLines = newVal
			changed = true
		}

	case SettingRecentDays:
		newVal := s.recentDays + (delta * 10)
		if newVal >= 0 {
			s.recentDays = newVal
			changed = true
			s.needsRestart = true
		}
	}

	return changed
}

// toggleValue toggles a checkbox value
func (s *SettingsPanel) toggleValue() bool {
	setting := SettingType(s.cursor)

	switch setting {
	case SettingDangerousMode:
		s.dangerousMode = !s.dangerousMode
		return true

	case SettingCheckForUpdates:
		s.checkForUpdates = !s.checkForUpdates
		return true

	case SettingAutoUpdate:
		s.autoUpdate = !s.autoUpdate
		return true

	case SettingRemoveOrphans:
		s.removeOrphans = !s.removeOrphans
		return true

	case SettingGlobalSearchEnabled:
		s.globalSearchEnabled = !s.globalSearchEnabled
		s.needsRestart = true
		return true

	case SettingShowOutput:
		s.showOutput = !s.showOutput
		return true

	case SettingShowAnalytics:
		s.showAnalytics = !s.showAnalytics
		return true
	}

	return false
}

// isTextSetting returns true if current setting uses text input
func (s *SettingsPanel) isTextSetting() bool {
	return SettingType(s.cursor) == SettingClaudeConfigDir
}

// startTextEdit begins text editing for current setting
func (s *SettingsPanel) startTextEdit() {
	setting := SettingType(s.cursor)
	if setting == SettingClaudeConfigDir {
		s.textBuffer = s.claudeConfigDir
		s.editingText = true
	}
}

// handleTextEdit processes keys during text editing
func (s *SettingsPanel) handleTextEdit(msg tea.KeyMsg) (*SettingsPanel, tea.Cmd, bool) {
	key := msg.String()

	switch key {
	case "enter":
		// Save the text
		if SettingType(s.cursor) == SettingClaudeConfigDir {
			s.claudeConfigDir = s.textBuffer
		}
		s.editingText = false
		return s, nil, true

	case "esc":
		// Cancel editing
		s.editingText = false
		return s, nil, false

	case "backspace":
		if len(s.textBuffer) > 0 {
			s.textBuffer = s.textBuffer[:len(s.textBuffer)-1]
		}

	default:
		// Add character
		if len(key) == 1 {
			s.textBuffer += key
		}
	}

	return s, nil, false
}

// View renders the settings panel
func (s *SettingsPanel) View() string {
	if !s.visible {
		return ""
	}

	// Styles
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorCyan)

	sectionStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorAccent)

	labelStyle := lipgloss.NewStyle().
		Foreground(ColorText)

	dimStyle := lipgloss.NewStyle().
		Foreground(ColorComment)

	highlightStyle := lipgloss.NewStyle().
		Background(ColorSurface)

	warningStyle := lipgloss.NewStyle().
		Foreground(ColorYellow)

	// Dialog dimensions
	dialogWidth := 64
	if s.width > 0 && s.width < dialogWidth+10 {
		dialogWidth = s.width - 10
		if dialogWidth < 50 {
			dialogWidth = 50
		}
	}

	var content strings.Builder

	// Title
	content.WriteString(titleStyle.Render("Settings"))
	content.WriteString(dimStyle.Render("                                    [Esc] Close"))
	content.WriteString("\n")
	content.WriteString(strings.Repeat("-", dialogWidth-4))
	content.WriteString("\n\n")

	// Theme section
	content.WriteString(sectionStyle.Render("THEME"))
	if s.needsRestart {
		content.WriteString(warningStyle.Render(" (restart required)"))
	}
	content.WriteString("\n")
	themeRow := s.renderRadioGroup(themeNames, s.selectedTheme, s.cursor == int(SettingTheme))
	if s.cursor == int(SettingTheme) {
		themeRow = highlightStyle.Render(themeRow)
	}
	content.WriteString("  " + themeRow + "\n\n")

	// DEFAULT TOOL
	content.WriteString(sectionStyle.Render("DEFAULT TOOL"))
	content.WriteString("\n")
	line := s.renderRadioGroup(toolNames, s.selectedTool, s.cursor == int(SettingDefaultTool))
	if s.cursor == int(SettingDefaultTool) {
		line = highlightStyle.Render(line)
	}
	content.WriteString("  " + line + "\n")
	content.WriteString(dimStyle.Render("  Pre-selected when creating new sessions"))
	content.WriteString("\n\n")

	// CLAUDE
	content.WriteString(sectionStyle.Render("CLAUDE"))
	content.WriteString("\n")

	// Dangerous mode checkbox
	line = s.renderCheckbox("Dangerous mode", s.dangerousMode) + " - Skip permission prompts"
	if s.cursor == int(SettingDangerousMode) {
		line = highlightStyle.Render(line)
	}
	content.WriteString("  " + labelStyle.Render(line) + "\n")

	// Config directory
	line = "Config directory: "
	if s.editingText && s.cursor == int(SettingClaudeConfigDir) {
		line += "[" + s.textBuffer + "|]"
	} else if s.claudeConfigDir == "" {
		line += dimStyle.Render("~/.claude (default)")
	} else {
		line += s.claudeConfigDir
	}
	if s.cursor == int(SettingClaudeConfigDir) {
		line = highlightStyle.Render(line)
	}
	content.WriteString("  " + labelStyle.Render(line) + "\n\n")

	// UPDATES
	content.WriteString(sectionStyle.Render("UPDATES"))
	content.WriteString("\n")

	line = s.renderCheckbox("Check for updates on startup", s.checkForUpdates)
	if s.cursor == int(SettingCheckForUpdates) {
		line = highlightStyle.Render(line)
	}
	content.WriteString("  " + labelStyle.Render(line) + "\n")

	line = s.renderCheckbox("Auto-install updates", s.autoUpdate)
	if s.cursor == int(SettingAutoUpdate) {
		line = highlightStyle.Render(line)
	}
	content.WriteString("  " + labelStyle.Render(line) + "\n\n")

	// LOGS
	content.WriteString(sectionStyle.Render("LOGS"))
	content.WriteString("\n")

	line = s.renderNumber("Max file size:", s.logMaxSizeMB, "MB")
	if s.cursor == int(SettingLogMaxSize) {
		line = highlightStyle.Render(line)
	}
	content.WriteString("  " + labelStyle.Render(line))

	line = s.renderNumber("    Lines to keep:", s.logMaxLines, "")
	if s.cursor == int(SettingLogMaxLines) {
		line = highlightStyle.Render(line)
	}
	content.WriteString(labelStyle.Render(line) + "\n")

	line = s.renderCheckbox("Remove orphan logs", s.removeOrphans)
	if s.cursor == int(SettingRemoveOrphans) {
		line = highlightStyle.Render(line)
	}
	content.WriteString("  " + labelStyle.Render(line) + "\n\n")

	// GLOBAL SEARCH
	content.WriteString(sectionStyle.Render("GLOBAL SEARCH"))
	if s.needsRestart {
		content.WriteString(warningStyle.Render("  (changes require restart)"))
	}
	content.WriteString("\n")

	line = s.renderCheckbox("Enabled", s.globalSearchEnabled)
	if s.cursor == int(SettingGlobalSearchEnabled) {
		line = highlightStyle.Render(line)
	}
	content.WriteString("  " + labelStyle.Render(line) + "\n")

	line = "Search tier: " + s.renderRadioGroup(tierNames, s.searchTier, s.cursor == int(SettingSearchTier))
	if s.cursor == int(SettingSearchTier) {
		line = highlightStyle.Render(line)
	}
	content.WriteString("  " + labelStyle.Render(line) + "\n")

	line = s.renderNumber("Recent days:", s.recentDays, "(0 = all)")
	if s.cursor == int(SettingRecentDays) {
		line = highlightStyle.Render(line)
	}
	content.WriteString("  " + labelStyle.Render(line) + "\n\n")

	// PREVIEW
	content.WriteString(sectionStyle.Render("PREVIEW"))
	content.WriteString("\n")

	line = s.renderCheckbox("Show Output", s.showOutput) + " - Terminal output in preview"
	if s.cursor == int(SettingShowOutput) {
		line = highlightStyle.Render(line)
	}
	content.WriteString("  " + labelStyle.Render(line) + "\n")

	line = s.renderCheckbox("Show Analytics", s.showAnalytics) + " - Claude analytics panel"
	if s.cursor == int(SettingShowAnalytics) {
		line = highlightStyle.Render(line)
	}
	content.WriteString("  " + labelStyle.Render(line) + "\n\n")

	// MCP & TOOLS
	content.WriteString(sectionStyle.Render("MCP SERVERS & CUSTOM TOOLS"))
	content.WriteString("\n")
	content.WriteString(dimStyle.Render("  Edit ~/.agent-deck/config.toml to configure MCPs and tools."))
	content.WriteString("\n")
	content.WriteString(dimStyle.Render("  Press M on any Claude/Gemini session to attach MCPs."))
	content.WriteString("\n\n")

	// Help bar
	content.WriteString(dimStyle.Render("j/k Navigate  Space Toggle  h/l Adjust  Enter Edit  Esc Close"))

	// Wrap in dialog box
	dialogStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorCyan).
		Background(ColorBg).
		Padding(1, 2).
		Width(dialogWidth)

	dialog := dialogStyle.Render(content.String())

	// Center the dialog
	return lipgloss.Place(
		s.width,
		s.height,
		lipgloss.Center,
		lipgloss.Center,
		dialog,
	)
}

// renderCheckbox renders a checkbox with label
func (s *SettingsPanel) renderCheckbox(label string, checked bool) string {
	box := "[ ]"
	if checked {
		box = "[x]"
	}
	return box + " " + label
}

// renderRadioGroup renders a group of radio options
func (s *SettingsPanel) renderRadioGroup(options []string, selected int, focused bool) string {
	var parts []string
	for i, opt := range options {
		if i == selected {
			style := lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)
			parts = append(parts, style.Render(">"+opt))
		} else {
			style := lipgloss.NewStyle().Foreground(ColorTextDim)
			parts = append(parts, style.Render(" "+opt))
		}
	}
	return strings.Join(parts, "  ")
}

// renderNumber renders a number input with label and suffix
func (s *SettingsPanel) renderNumber(label string, value int, suffix string) string {
	numStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	valueStr := strconv.Itoa(value)
	result := label + " [" + numStyle.Render(valueStr) + "]"
	if suffix != "" {
		result += " " + suffix
	}
	return result
}
