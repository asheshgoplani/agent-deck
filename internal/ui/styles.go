package ui

import (
	"fmt"
	"sync"

	"github.com/charmbracelet/lipgloss"
)

// Theme represents the current color scheme
type Theme string

const (
	ThemeDark  Theme = "dark"
	ThemeLight Theme = "light"
)

// currentTheme holds the active theme (set at init)
var currentTheme Theme = ThemeDark

// Dark Theme - Tokyo Night
var darkColors = struct {
	Bg, Surface, Border, Text, TextDim  lipgloss.Color
	Accent, Purple, Cyan, Green, Yellow lipgloss.Color
	Orange, Red, Comment                lipgloss.Color
}{
	Bg:      lipgloss.Color("#1a1b26"),
	Surface: lipgloss.Color("#24283b"),
	Border:  lipgloss.Color("#414868"),
	Text:    lipgloss.Color("#c0caf5"),
	TextDim: lipgloss.Color("#787fa0"),
	Accent:  lipgloss.Color("#7aa2f7"),
	Purple:  lipgloss.Color("#bb9af7"),
	Cyan:    lipgloss.Color("#7dcfff"),
	Green:   lipgloss.Color("#9ece6a"),
	Yellow:  lipgloss.Color("#e0af68"),
	Orange:  lipgloss.Color("#ff9e64"),
	Red:     lipgloss.Color("#f7768e"),
	Comment: lipgloss.Color("#787fa0"),
}

// Light Theme - Tokyo Night Light variant
var lightColors = struct {
	Bg, Surface, Border, Text, TextDim  lipgloss.Color
	Accent, Purple, Cyan, Green, Yellow lipgloss.Color
	Orange, Red, Comment                lipgloss.Color
}{
	Bg:      lipgloss.Color("#d5d6db"),
	Surface: lipgloss.Color("#e9e9ec"),
	Border:  lipgloss.Color("#9699a3"),
	Text:    lipgloss.Color("#343b58"),
	TextDim: lipgloss.Color("#6a6d7c"),
	Accent:  lipgloss.Color("#34548a"),
	Purple:  lipgloss.Color("#7847bd"),
	Cyan:    lipgloss.Color("#166775"),
	Green:   lipgloss.Color("#485e30"),
	Yellow:  lipgloss.Color("#8f5e15"),
	Orange:  lipgloss.Color("#965027"),
	Red:     lipgloss.Color("#8c4351"),
	Comment: lipgloss.Color("#6a6d7c"),
}

// Active color variables (set by InitTheme)
var (
	ColorBg      lipgloss.Color
	ColorSurface lipgloss.Color
	ColorBorder  lipgloss.Color
	ColorText    lipgloss.Color
	ColorTextDim lipgloss.Color
	ColorAccent  lipgloss.Color
	ColorPurple  lipgloss.Color
	ColorCyan    lipgloss.Color
	ColorGreen   lipgloss.Color
	ColorYellow  lipgloss.Color
	ColorOrange  lipgloss.Color
	ColorRed     lipgloss.Color
	ColorComment lipgloss.Color
)

// themeMu protects global color/style variables during live theme switches.
// Write lock held by InitTheme; read lock held by GetToolStyle (map access).
var themeMu sync.RWMutex

// InitTheme sets the active color palette based on theme name
// Must be called before any UI rendering
func InitTheme(theme string) {
	themeMu.Lock()
	defer themeMu.Unlock()
	if theme == "light" {
		currentTheme = ThemeLight
		ColorBg = lightColors.Bg
		ColorSurface = lightColors.Surface
		ColorBorder = lightColors.Border
		ColorText = lightColors.Text
		ColorTextDim = lightColors.TextDim
		ColorAccent = lightColors.Accent
		ColorPurple = lightColors.Purple
		ColorCyan = lightColors.Cyan
		ColorGreen = lightColors.Green
		ColorYellow = lightColors.Yellow
		ColorOrange = lightColors.Orange
		ColorRed = lightColors.Red
		ColorComment = lightColors.Comment
	} else {
		currentTheme = ThemeDark
		ColorBg = darkColors.Bg
		ColorSurface = darkColors.Surface
		ColorBorder = darkColors.Border
		ColorText = darkColors.Text
		ColorTextDim = darkColors.TextDim
		ColorAccent = darkColors.Accent
		ColorPurple = darkColors.Purple
		ColorCyan = darkColors.Cyan
		ColorGreen = darkColors.Green
		ColorYellow = darkColors.Yellow
		ColorOrange = darkColors.Orange
		ColorRed = darkColors.Red
		ColorComment = darkColors.Comment
	}
	// Reinitialize styles with new colors
	initStyles()
}

// GetCurrentTheme returns the active theme
func GetCurrentTheme() Theme {
	return currentTheme
}

func init() {
	// Default to dark theme at package init
	InitTheme("dark")
}

// Base Styles
var (
	BaseStyle      lipgloss.Style
	TitleStyle     lipgloss.Style
	PanelStyle     lipgloss.Style
	HighlightStyle lipgloss.Style
	DimStyle       lipgloss.Style
	ErrorStyle     lipgloss.Style
	SuccessStyle   lipgloss.Style
	WarningStyle   lipgloss.Style
	InfoStyle      lipgloss.Style
)

// Status Indicator Styles
var (
	RunningStyle        lipgloss.Style
	WaitingStyle        lipgloss.Style
	IdleStyle           lipgloss.Style
	ErrorIndicatorStyle lipgloss.Style
)

// Menu Bar Styles
var (
	MenuBarStyle       lipgloss.Style
	MenuKeyStyle       lipgloss.Style
	MenuDescStyle      lipgloss.Style
	MenuSeparatorStyle lipgloss.Style
)

// Search Styles
var (
	SearchBoxStyle    lipgloss.Style
	SearchPromptStyle lipgloss.Style
	SearchMatchStyle  lipgloss.Style
)

// Dialog Styles
var (
	DialogBoxStyle          lipgloss.Style
	DialogTitleStyle        lipgloss.Style
	DialogButtonStyle       lipgloss.Style
	DialogButtonActiveStyle lipgloss.Style
)

// Preview Pane Styles
var (
	PreviewPanelStyle   lipgloss.Style
	PreviewTitleStyle   lipgloss.Style
	PreviewHeaderStyle  lipgloss.Style
	PreviewContentStyle lipgloss.Style
	PreviewMetaStyle    lipgloss.Style
)

// Tool Icons
const (
	IconClaude   = "🤖"
	IconGemini   = "✨"
	IconOpenCode = "🌐"
	IconCodex    = "💻"
	IconShell    = "🐚"
)

// MaxNameLength is the maximum allowed length for session and group names.
// Used by dialog CharLimits and Validate() methods to ensure consistency.
const MaxNameLength = 50

// List Item Styles (used by legacy list.go component in tests)
var (
	ListItemStyle       lipgloss.Style
	ListItemActiveStyle lipgloss.Style
)

// Tag Styles
var (
	TagStyle       lipgloss.Style
	TagActiveStyle lipgloss.Style
	TagErrorStyle  lipgloss.Style
)

// Timestamp Style
var TimestampStyle lipgloss.Style

// Folder Styles
var (
	FolderStyle          lipgloss.Style
	FolderCollapsedStyle lipgloss.Style
)

// Session Item Styles
var (
	SessionItemStyle         lipgloss.Style
	SessionItemSelectedStyle lipgloss.Style
)

// Session List Rendering Styles (PERFORMANCE: cached at package level)
// These styles are used by renderSessionItem() and renderGroupItem() to avoid
// repeated allocations on every View() call
var (
	// Tree connector styles
	TreeConnectorStyle    lipgloss.Style
	TreeConnectorSelStyle lipgloss.Style

	// Session status indicator styles
	SessionStatusRunning  lipgloss.Style
	SessionStatusWaiting  lipgloss.Style
	SessionStatusIdle     lipgloss.Style
	SessionStatusError    lipgloss.Style
	SessionStatusSelStyle lipgloss.Style

	// Session title styles by state
	SessionTitleDefault  lipgloss.Style
	SessionTitleActive   lipgloss.Style
	SessionTitleError    lipgloss.Style
	SessionTitleSelStyle lipgloss.Style

	// Selection indicator
	SessionSelectionPrefix lipgloss.Style

	// Group item styles
	GroupExpandStyle   lipgloss.Style
	GroupNameStyle     lipgloss.Style
	GroupCountStyle    lipgloss.Style
	GroupHotkeyStyle   lipgloss.Style
	GroupStatusRunning lipgloss.Style
	GroupStatusWaiting lipgloss.Style

	// Group selected styles
	GroupNameSelStyle   lipgloss.Style
	GroupCountSelStyle  lipgloss.Style
	GroupExpandSelStyle lipgloss.Style
)

// ToolStyleCache provides pre-allocated styles for each tool type
// Avoids repeated lipgloss.NewStyle() calls in renderSessionItem()
var ToolStyleCache map[string]lipgloss.Style

// DefaultToolStyle is used when tool is not in cache
var DefaultToolStyle lipgloss.Style

// Menu Styles
var MenuStyle lipgloss.Style

// Additional Styles
var (
	SubtitleStyle lipgloss.Style
	ColorError    lipgloss.Color
	ColorSuccess  lipgloss.Color
	ColorWarning  lipgloss.Color
	ColorPrimary  lipgloss.Color
)

// LogoBorderStyle for the grid lines
var LogoBorderStyle lipgloss.Style

// LogoFrames kept for backward compatibility (empty state default)
var LogoFrames = [][]string{
	{"●", "◐", "○"},
}

// initStyles initializes all style variables with current theme colors
// Called by InitTheme after color variables are set
func initStyles() {
	// Base Styles
	BaseStyle = lipgloss.NewStyle().
		Foreground(ColorText).
		Background(ColorBg)

	TitleStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorAccent).
		Background(ColorSurface).
		Padding(0, 1)

	PanelStyle = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorBorder).
		Padding(0, 1)

	HighlightStyle = lipgloss.NewStyle().
		Foreground(ColorBg).
		Background(ColorAccent).
		Bold(true)

	DimStyle = lipgloss.NewStyle().
		Foreground(ColorComment)

	ErrorStyle = lipgloss.NewStyle().
		Foreground(ColorRed).
		Bold(true)

	SuccessStyle = lipgloss.NewStyle().
		Foreground(ColorGreen).
		Bold(true)

	WarningStyle = lipgloss.NewStyle().
		Foreground(ColorYellow).
		Bold(true)

	InfoStyle = lipgloss.NewStyle().
		Foreground(ColorCyan)

	// Status Indicator Styles
	RunningStyle = lipgloss.NewStyle().
		Foreground(ColorGreen).
		Bold(true)

	WaitingStyle = lipgloss.NewStyle().
		Foreground(ColorYellow).
		Bold(true)

	IdleStyle = lipgloss.NewStyle().
		Foreground(ColorComment)

	ErrorIndicatorStyle = lipgloss.NewStyle().
		Foreground(ColorRed).
		Bold(true)

	// Menu Bar Styles
	MenuBarStyle = lipgloss.NewStyle().
		Background(ColorSurface).
		Foreground(ColorText).
		Padding(0, 1)

	MenuKeyStyle = lipgloss.NewStyle().
		Foreground(ColorAccent).
		Bold(true)

	MenuDescStyle = lipgloss.NewStyle().
		Foreground(ColorText)

	MenuSeparatorStyle = lipgloss.NewStyle().
		Foreground(ColorBorder)

	// Search Styles
	SearchBoxStyle = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorAccent).
		Padding(0, 1).
		Foreground(ColorText)

	SearchPromptStyle = lipgloss.NewStyle().
		Foreground(ColorPurple).
		Bold(true)

	SearchMatchStyle = lipgloss.NewStyle().
		Background(ColorYellow).
		Foreground(ColorBg).
		Bold(true)

	// Dialog Styles
	DialogBoxStyle = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorPurple).
		Padding(1, 2).
		Background(ColorSurface)

	DialogTitleStyle = lipgloss.NewStyle().
		Foreground(ColorPurple).
		Bold(true).
		Align(lipgloss.Center)

	DialogButtonStyle = lipgloss.NewStyle().
		Foreground(ColorAccent).
		Background(ColorBorder).
		Padding(0, 2).
		MarginRight(1)

	DialogButtonActiveStyle = lipgloss.NewStyle().
		Foreground(ColorBg).
		Background(ColorAccent).
		Padding(0, 2).
		MarginRight(1).
		Bold(true)

	// Preview Pane Styles
	PreviewPanelStyle = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorBorder).
		Padding(1)

	PreviewTitleStyle = lipgloss.NewStyle().
		Foreground(ColorCyan).
		Bold(true).
		Underline(true)

	PreviewHeaderStyle = lipgloss.NewStyle().
		Foreground(ColorPurple).
		Bold(true)

	PreviewContentStyle = lipgloss.NewStyle().
		Foreground(ColorText)

	PreviewMetaStyle = lipgloss.NewStyle().
		Foreground(ColorComment).
		Italic(true)

	// List Item Styles
	ListItemStyle = lipgloss.NewStyle().
		Foreground(ColorText).
		PaddingLeft(2)

	ListItemActiveStyle = lipgloss.NewStyle().
		Foreground(ColorAccent).
		Bold(true).
		PaddingLeft(2)

	// Tag Styles
	TagStyle = lipgloss.NewStyle().
		Foreground(ColorBg).
		Background(ColorPurple).
		Padding(0, 1).
		MarginRight(1)

	TagActiveStyle = lipgloss.NewStyle().
		Foreground(ColorBg).
		Background(ColorGreen).
		Padding(0, 1).
		MarginRight(1)

	TagErrorStyle = lipgloss.NewStyle().
		Foreground(ColorBg).
		Background(ColorRed).
		Padding(0, 1).
		MarginRight(1)

	// Timestamp Style
	TimestampStyle = lipgloss.NewStyle().
		Foreground(ColorComment).
		Italic(true)

	// Folder Styles
	FolderStyle = lipgloss.NewStyle().
		Foreground(ColorAccent).
		Bold(true)

	FolderCollapsedStyle = lipgloss.NewStyle().
		Foreground(ColorComment)

	// Session Item Styles
	SessionItemStyle = lipgloss.NewStyle().
		Foreground(ColorText).
		PaddingLeft(2)

	SessionItemSelectedStyle = lipgloss.NewStyle().
		Foreground(ColorBg).
		Background(ColorAccent).
		Bold(true).
		PaddingLeft(0)

	// Tree connector styles
	TreeConnectorStyle = lipgloss.NewStyle().Foreground(ColorText)
	TreeConnectorSelStyle = lipgloss.NewStyle().Foreground(ColorBg).Background(ColorAccent)

	// Session status indicator styles
	SessionStatusRunning = lipgloss.NewStyle().Foreground(ColorGreen)
	SessionStatusWaiting = lipgloss.NewStyle().Foreground(ColorYellow)
	SessionStatusIdle = lipgloss.NewStyle().Foreground(ColorTextDim)
	SessionStatusError = lipgloss.NewStyle().Foreground(ColorRed)
	SessionStatusSelStyle = lipgloss.NewStyle().Foreground(ColorBg).Background(ColorAccent)

	// Session title styles by state
	SessionTitleDefault = lipgloss.NewStyle().Foreground(ColorText)
	SessionTitleActive = lipgloss.NewStyle().Foreground(ColorText).Bold(true)
	SessionTitleError = lipgloss.NewStyle().Foreground(ColorText).Underline(true)
	SessionTitleSelStyle = lipgloss.NewStyle().Bold(true).Foreground(ColorBg).Background(ColorAccent)

	// Selection indicator
	SessionSelectionPrefix = lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)

	// Group item styles
	GroupExpandStyle = lipgloss.NewStyle().Foreground(ColorText)
	GroupNameStyle = lipgloss.NewStyle().Bold(true).Foreground(ColorCyan)
	GroupCountStyle = lipgloss.NewStyle().Foreground(ColorText)
	GroupHotkeyStyle = lipgloss.NewStyle().Foreground(ColorComment)
	GroupStatusRunning = lipgloss.NewStyle().Foreground(ColorGreen)
	GroupStatusWaiting = lipgloss.NewStyle().Foreground(ColorYellow)

	// Group selected styles
	GroupNameSelStyle = lipgloss.NewStyle().Bold(true).Foreground(ColorBg).Background(ColorAccent)
	GroupCountSelStyle = lipgloss.NewStyle().Foreground(ColorBg).Background(ColorAccent)
	GroupExpandSelStyle = lipgloss.NewStyle().Foreground(ColorBg).Background(ColorAccent)

	// ToolStyleCache - reinitialize with current theme colors
	ToolStyleCache = map[string]lipgloss.Style{
		"claude":   lipgloss.NewStyle().Foreground(ColorOrange),
		"gemini":   lipgloss.NewStyle().Foreground(ColorPurple),
		"codex":    lipgloss.NewStyle().Foreground(ColorCyan),
		"aider":    lipgloss.NewStyle().Foreground(ColorRed),
		"cursor":   lipgloss.NewStyle().Foreground(ColorAccent),
		"shell":    lipgloss.NewStyle().Foreground(ColorText),
		"opencode": lipgloss.NewStyle().Foreground(ColorText),
	}

	// DefaultToolStyle
	DefaultToolStyle = lipgloss.NewStyle().Foreground(ColorText)

	// Menu Styles
	MenuStyle = lipgloss.NewStyle().
		Background(ColorSurface).
		Foreground(ColorText).
		Padding(0, 1)

	// Additional Styles
	SubtitleStyle = lipgloss.NewStyle().
		Foreground(ColorComment).
		Italic(true)

	ColorError = ColorRed
	ColorSuccess = ColorGreen
	ColorWarning = ColorYellow
	ColorPrimary = ColorAccent

	// LogoBorderStyle
	LogoBorderStyle = lipgloss.NewStyle().Foreground(ColorBorder)
}

// Helper Functions

// MenuKey creates a formatted menu item with key and description
func MenuKey(key, description string) string {
	return fmt.Sprintf("%s %s %s",
		MenuKeyStyle.Render(key),
		MenuSeparatorStyle.Render("•"),
		MenuDescStyle.Render(description),
	)
}

// StatusIndicator returns a styled status indicator.
// Read-locked to protect against concurrent style access during live theme switches.
// Standard symbols: ● running, ◐ waiting, ○ idle, ✕ error, ⟳ starting
func StatusIndicator(status string) string {
	themeMu.RLock()
	defer themeMu.RUnlock()
	switch status {
	case "running":
		return RunningStyle.Render("●")
	case "waiting":
		return WaitingStyle.Render("◐")
	case "idle":
		return IdleStyle.Render("○")
	case "error":
		return ErrorIndicatorStyle.Render("✕")
	case "starting":
		return WaitingStyle.Render("⟳") // Use yellow color, spinning arrow symbol
	default:
		return IdleStyle.Render("○")
	}
}

// ToolIcon returns the icon for a given tool
// Checks user config for custom tools first, then falls back to built-ins
func ToolIcon(tool string) string {
	// Use session.GetToolIcon which handles custom + built-in
	// Import would be circular, so we duplicate the logic here
	// Custom icons are handled by the session layer's GetToolDef
	switch tool {
	case "claude":
		return IconClaude
	case "gemini":
		return IconGemini
	case "opencode":
		return IconOpenCode
	case "codex":
		return IconCodex
	case "cursor":
		return "📝"
	case "shell":
		return IconShell
	default:
		return IconShell
	}
}

// ToolColor returns the brand color for a given tool
// Claude=orange (Anthropic), Gemini=purple (Google AI), Codex=cyan, Aider=red
func ToolColor(tool string) lipgloss.Color {
	switch tool {
	case "claude":
		return ColorOrange // Anthropic's orange
	case "gemini":
		return ColorPurple // Google AI purple
	case "codex":
		return ColorCyan // Light blue for OpenAI
	case "aider":
		return ColorRed // Red for Aider
	case "cursor":
		return ColorAccent // Blue for Cursor
	default:
		return ColorTextDim // Default gray
	}
}

// GetToolStyle returns cached style for tool or default.
// Read-locked to protect against concurrent map access during live theme switches.
func GetToolStyle(tool string) lipgloss.Style {
	themeMu.RLock()
	defer themeMu.RUnlock()
	if style, ok := ToolStyleCache[tool]; ok {
		return style
	}
	return DefaultToolStyle
}

// RenderLogoIndicator renders a single indicator with appropriate color
func RenderLogoIndicator(indicator string) string {
	var color lipgloss.Color
	switch indicator {
	case "●":
		color = ColorGreen // Running
	case "◐":
		color = ColorYellow // Waiting
	case "○":
		color = ColorTextDim // Idle
	default:
		color = ColorTextDim
	}
	return lipgloss.NewStyle().Foreground(color).Bold(true).Render(indicator)
}

// getLogoIndicators returns 3 indicators based on actual session status counts
// Priority: Running > Waiting > Idle
// Shows up to 3 indicators reflecting the real state
func getLogoIndicators(running, waiting, idle int) []string {
	indicators := make([]string, 0, 3)

	// Add running indicators (green ●)
	for i := 0; i < running && len(indicators) < 3; i++ {
		indicators = append(indicators, "●")
	}

	// Add waiting indicators (yellow ◐)
	for i := 0; i < waiting && len(indicators) < 3; i++ {
		indicators = append(indicators, "◐")
	}

	// Fill remaining with idle (gray ○)
	for len(indicators) < 3 {
		indicators = append(indicators, "○")
	}

	return indicators
}

// RenderLogoCompact renders the compact inline logo for the header
// Shows REAL status: running=●, waiting=◐, idle=○
// Format: ⟨ ● │ ◐ │ ○ ⟩  (using angle brackets for modern look)
func RenderLogoCompact(running, waiting, idle int) string {
	indicators := getLogoIndicators(running, waiting, idle)
	bracketStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	return bracketStyle.Render("⟨") +
		" " + RenderLogoIndicator(indicators[0]) +
		LogoBorderStyle.Render(" │ ") +
		RenderLogoIndicator(indicators[1]) +
		LogoBorderStyle.Render(" │ ") +
		RenderLogoIndicator(indicators[2]) + " " +
		bracketStyle.Render("⟩")
}

// RenderLogoLarge renders the large logo for empty state
// Shows REAL status: running=●, waiting=◐, idle=○
// Format:
//
//	┌──┬──┬──┐
//	│● │◐ │○ │
//	└──┴──┴──┘
func RenderLogoLarge(running, waiting, idle int) string {
	indicators := getLogoIndicators(running, waiting, idle)
	top := LogoBorderStyle.Render("┌──┬──┬──┐")
	mid := LogoBorderStyle.Render("│") + RenderLogoIndicator(indicators[0]) + LogoBorderStyle.Render(" │") +
		RenderLogoIndicator(indicators[1]) + LogoBorderStyle.Render(" │") +
		RenderLogoIndicator(indicators[2]) + LogoBorderStyle.Render(" │")
	bot := LogoBorderStyle.Render("└──┴──┴──┘")
	return top + "\n" + mid + "\n" + bot
}
