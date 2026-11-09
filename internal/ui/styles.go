package ui

import (
	"fmt"
	"sync"

	"github.com/asheshgoplani/agent-deck/internal/session"
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
	// Brand mark dots (running, waiting, idle) for a dark terminal: the
	// brand kit's values, with its fixed 256- and 16-colour fallbacks.
	BrandRunning, BrandWaiting, BrandIdle lipgloss.CompleteColor
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

	BrandRunning: lipgloss.CompleteColor{TrueColor: "#5CC98F", ANSI256: "78", ANSI: "2"},
	BrandWaiting: lipgloss.CompleteColor{TrueColor: "#E8B464", ANSI256: "179", ANSI: "3"},
	BrandIdle:    lipgloss.CompleteColor{TrueColor: "#959DBD", ANSI256: "103", ANSI: "8"},
}

// Light Theme - Tokyo Night Light variant
var lightColors = struct {
	Bg, Surface, Border, Text, TextDim  lipgloss.Color
	Accent, Purple, Cyan, Green, Yellow lipgloss.Color
	Orange, Red, Comment                lipgloss.Color
	// Brand mark dots for a light terminal (darker, readable on white).
	BrandRunning, BrandWaiting, BrandIdle lipgloss.CompleteColor
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

	BrandRunning: lipgloss.CompleteColor{TrueColor: "#485E30", ANSI256: "64", ANSI: "2"},
	BrandWaiting: lipgloss.CompleteColor{TrueColor: "#855710", ANSI256: "94", ANSI: "3"},
	BrandIdle:    lipgloss.CompleteColor{TrueColor: "#5E6170", ANSI256: "60", ANSI: "0"},
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

	// Brand mark dot colours (see RenderBrandMark).
	ColorBrandRunning lipgloss.CompleteColor
	ColorBrandWaiting lipgloss.CompleteColor
	ColorBrandIdle    lipgloss.CompleteColor
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
		ColorBrandRunning = lightColors.BrandRunning
		ColorBrandWaiting = lightColors.BrandWaiting
		ColorBrandIdle = lightColors.BrandIdle
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
		ColorBrandRunning = darkColors.BrandRunning
		ColorBrandWaiting = darkColors.BrandWaiting
		ColorBrandIdle = darkColors.BrandIdle
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
	IconPi       = "π"
	IconOMP      = "⌥"
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
	SessionStatusStopped  lipgloss.Style
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
	SessionStatusStopped = lipgloss.NewStyle().Foreground(ColorTextDim)
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
		"copilot":  lipgloss.NewStyle().Foreground(ColorAccent),
		"hermes":   lipgloss.NewStyle().Foreground(ColorYellow),
		"deepseek": lipgloss.NewStyle().Foreground(ColorCyan),
		"pi":       lipgloss.NewStyle().Foreground(ColorAccent),
		"omp":      lipgloss.NewStyle().Foreground(ColorAccent),
		"aider":    lipgloss.NewStyle().Foreground(ColorRed),
		"cursor":   lipgloss.NewStyle().Foreground(ColorAccent),
		"shell":    lipgloss.NewStyle().Foreground(ColorText),
		"opencode": lipgloss.NewStyle().Foreground(ColorText),
		"crush":    lipgloss.NewStyle().Foreground(ColorPurple),
		"muse":     lipgloss.NewStyle().Foreground(ColorGreen),
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

// ToolIcon returns the icon for a given tool.
// The tool registry (built-ins plus [tools.<name>] custom entries) is the
// source of truth; the switch below is the fallback for names the registry
// does not know (issue #2136).
func ToolIcon(tool string) string {
	tool = session.CanonicalToolName(tool)
	if icon := session.ToolIconFor(tool); icon != "" {
		return icon
	}
	switch tool {
	case "claude":
		return IconClaude
	case "gemini":
		return IconGemini
	case "opencode":
		return IconOpenCode
	case "codex":
		return IconCodex
	case "copilot":
		return "🐙"
	case "crush":
		return "💘"
	case "muse":
		return "🔮"
	case "cursor":
		return "📝"
	case "hermes":
		return "☤"
	case "deepseek":
		return "🐋"
	case "pi":
		return IconPi
	case "omp":
		return IconOMP
	case "shell":
		return IconShell
	default:
		return IconShell
	}
}

// ToolColor returns the brand color for a given tool
// Claude=orange (Anthropic), Gemini=purple (Google AI), Codex=cyan, Pi=accent, Aider=red
//
// The tool registry is the source of truth: built-ins carry a palette slot
// name that is resolved against the active theme here, and custom tools may
// set color = "<lipgloss value>" in config. The switch below is the fallback
// for names the registry does not know (issue #2136).
func ToolColor(tool string) lipgloss.Color {
	tool = session.CanonicalToolName(tool)
	if c, ok := paletteColor(session.ToolColorFor(tool)); ok {
		return c
	}
	switch tool {
	case "claude":
		return ColorOrange // Anthropic's orange
	case "gemini":
		return ColorPurple // Google AI purple
	case "codex":
		return ColorCyan // Light blue for OpenAI
	case "copilot":
		return ColorAccent // Blue for GitHub Copilot
	case "crush":
		return ColorPurple // Pink/magenta for Charm Crush
	case "muse":
		return ColorGreen // Green for Muse Code
	case "cursor":
		return ColorAccent // Blue for Cursor
	case "hermes":
		return ColorYellow // Gold for Hermes Agent
	case "deepseek":
		return ColorCyan // DeepSeek Harness
	case "pi":
		return ColorAccent
	case "omp":
		return ColorAccent
	case "aider":
		return ColorRed // Red for Aider
	default:
		return ColorTextDim // Default gray
	}
}

// paletteColor resolves a registry color value to a lipgloss color. Palette
// slot names map to the active theme's variables so live theme switches keep
// working; any other non-empty value is passed to lipgloss verbatim (custom
// tool hex or ANSI colors). "" reports false so callers use their fallback.
func paletteColor(value string) (lipgloss.Color, bool) {
	switch value {
	case "":
		return "", false
	case "orange":
		return ColorOrange, true
	case "purple":
		return ColorPurple, true
	case "cyan":
		return ColorCyan, true
	case "accent":
		return ColorAccent, true
	case "yellow":
		return ColorYellow, true
	case "red":
		return ColorRed, true
	default:
		return lipgloss.Color(value), true
	}
}

// GetToolStyle returns the style for tool: the registry's color (built-in
// palette slot or custom [tools.<name>].color) if one resolves, else the
// cached built-in style, else DefaultToolStyle. This is the function every
// row/preview render call site actually uses, so it must consult the
// registry the same way ToolColor() does rather than only the hardcoded
// ToolStyleCache (issue #2136 — ToolColor() alone was never wired in here).
// Read-locked to protect against concurrent map access during live theme switches.
func GetToolStyle(tool string) lipgloss.Style {
	themeMu.RLock()
	defer themeMu.RUnlock()
	if c, ok := paletteColor(session.ToolColorFor(tool)); ok {
		return lipgloss.NewStyle().Foreground(c)
	}
	if style, ok := ToolStyleCache[tool]; ok {
		return style
	}
	return DefaultToolStyle
}

// BrandMarkText is the agent-deck mark in its plain form: running and
// waiting as filled dots, idle as a hollow ring. Three cells wide.
const BrandMarkText = "●●○"

// brandMarkWidth is the cells the mark plus its one-space gap take in front
// of a name ("●●○ Agent Deck").
const brandMarkWidth = 4

// RenderBrandMark renders the agent-deck mark in the brand colours of the
// active theme (dark or light). The colour profile picks truecolor, the
// 256-colour or the 16-colour value; with no colour it is BrandMarkText.
// No background and no bold, so the terminal's own background shows.
func RenderBrandMark() string {
	return renderBrandMarkLit(true, true, true)
}

// renderBrandMarkLit renders the mark with only the lit dots in their brand
// colour; an unlit dot keeps its glyph in the comment colour (the splash
// screens light the dots in turn while they wait).
func renderBrandMarkLit(running, waiting, idle bool) string {
	dot := func(lit bool, c lipgloss.TerminalColor, glyph string) string {
		if !lit {
			c = ColorComment
		}
		return lipgloss.NewStyle().Foreground(c).Render(glyph)
	}
	return dot(running, ColorBrandRunning, "●") +
		dot(waiting, ColorBrandWaiting, "●") +
		dot(idle, ColorBrandIdle, "○")
}

// withBrandMark puts the mark in front of an already-rendered name when the
// result fits in avail cells; otherwise it returns the name unchanged, so a
// narrow box keeps its words and gives up the mark first.
func withBrandMark(name string, avail int) string {
	if lipgloss.Width(name)+brandMarkWidth > avail {
		return name
	}
	return RenderBrandMark() + " " + name
}
