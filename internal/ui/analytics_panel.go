package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/charmbracelet/lipgloss"
)

// AnalyticsPanel displays session analytics in a formatted panel
type AnalyticsPanel struct {
	analytics       *session.SessionAnalytics
	geminiAnalytics *session.GeminiSessionAnalytics
	width           int
	height          int
	displaySettings session.AnalyticsDisplaySettings
}

// NewAnalyticsPanel creates a new analytics panel
func NewAnalyticsPanel() *AnalyticsPanel {
	return &AnalyticsPanel{}
}

// SetDisplaySettings configures which sections to show
func (p *AnalyticsPanel) SetDisplaySettings(settings session.AnalyticsDisplaySettings) {
	p.displaySettings = settings
}

// SetAnalytics sets the Claude analytics data to display
func (p *AnalyticsPanel) SetAnalytics(a *session.SessionAnalytics) {
	p.analytics = a
	p.geminiAnalytics = nil // Clear Gemini analytics when setting Claude
}

// SetGeminiAnalytics sets the Gemini analytics data to display
func (p *AnalyticsPanel) SetGeminiAnalytics(a *session.GeminiSessionAnalytics) {
	p.geminiAnalytics = a
	p.analytics = nil // Clear Claude analytics when setting Gemini
}

// SetSize sets the panel dimensions
func (p *AnalyticsPanel) SetSize(width, height int) {
	p.width = width
	p.height = height
}

// View renders the analytics panel
func (p *AnalyticsPanel) View() string {
	if p.analytics == nil && p.geminiAnalytics == nil {
		return p.renderEmpty()
	}

	// Render Gemini analytics if available
	if p.geminiAnalytics != nil {
		return p.renderGeminiView()
	}

	var b strings.Builder
	sectionsRendered := 0

	// Header
	b.WriteString(p.renderHeader())
	b.WriteString("\n")

	// Context bar (default: ON)
	if p.displaySettings.GetShowContextBar() {
		b.WriteString(p.renderContextBar())
		b.WriteString("\n\n")
		sectionsRendered++
	}

	// Token breakdown (default: OFF)
	if p.displaySettings.GetShowTokens() {
		b.WriteString(p.renderTokens())
		b.WriteString("\n")
		sectionsRendered++
	}

	// Session info (default: OFF)
	if p.displaySettings.GetShowSessionInfo() {
		b.WriteString(p.renderSessionInfo())
		b.WriteString("\n")
		sectionsRendered++
	}

	// Tool calls (default: ON)
	if p.displaySettings.GetShowTools() && len(p.analytics.ToolCalls) > 0 {
		b.WriteString(p.renderToolCalls())
		b.WriteString("\n")
		sectionsRendered++
	}

	// Cost estimate (default: OFF)
	if p.displaySettings.GetShowCost() && (p.analytics.EstimatedCost > 0 || p.analytics.TotalTokens() > 0) {
		b.WriteString(p.renderCost())
		sectionsRendered++
	}

	// If no sections were rendered, show a minimal message
	if sectionsRendered == 0 {
		dimStyle := lipgloss.NewStyle().Foreground(ColorTextDim).Italic(true)
		b.WriteString(dimStyle.Render("Analytics available"))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("(enable sections in config.toml)"))
	}

	return b.String()
}

// renderGeminiView renders Gemini-specific analytics
func (p *AnalyticsPanel) renderGeminiView() string {
	var b strings.Builder
	sectionsRendered := 0

	// Header
	b.WriteString(p.renderHeader())
	b.WriteString("\n")

	// Context bar (default: ON)
	if p.displaySettings.GetShowContextBar() {
		b.WriteString(p.renderGeminiContextBar())
		b.WriteString("\n\n")
		sectionsRendered++
	}

	// Token breakdown (default: OFF)
	if p.displaySettings.GetShowTokens() {
		b.WriteString(p.renderGeminiTokens())
		b.WriteString("\n")
		sectionsRendered++
	}

	// Session info (default: OFF)
	if p.displaySettings.GetShowSessionInfo() {
		b.WriteString(p.renderGeminiSessionInfo())
		b.WriteString("\n")
		sectionsRendered++
	}

	// Cost estimate (default: OFF)
	if p.displaySettings.GetShowCost() && p.geminiAnalytics.TotalTokens() > 0 {
		b.WriteString(p.renderGeminiCost())
		sectionsRendered++
	}

	// If no sections were rendered, show a minimal message
	if sectionsRendered == 0 {
		dimStyle := lipgloss.NewStyle().Foreground(ColorTextDim).Italic(true)
		b.WriteString(dimStyle.Render("Analytics available"))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("(enable sections in config.toml)"))
	}

	return b.String()
}

// renderGeminiContextBar renders a visual bar for Gemini context usage
func (p *AnalyticsPanel) renderGeminiContextBar() string {
	labelStyle := lipgloss.NewStyle().Foreground(ColorText).Bold(true)
	dimStyle := lipgloss.NewStyle().Foreground(ColorTextDim)

	// Gemini 2.0 Flash has 1M context window
	contextLimit := 1000000
	percent := float64(p.geminiAnalytics.CurrentContextTokens) / float64(contextLimit) * 100
	if percent > 100 {
		percent = 100
	}

	// Choose color based on usage
	var barColor lipgloss.Color
	switch {
	case percent < 60:
		barColor = ColorGreen
	case percent < 80:
		barColor = ColorYellow
	default:
		barColor = ColorRed
	}

	barStyle := lipgloss.NewStyle().Foreground(barColor)

	// Calculate bar width
	maxBarWidth := 30
	if p.width > 0 && p.width < 50 {
		maxBarWidth = p.width - 20
		if maxBarWidth < 10 {
			maxBarWidth = 10
		}
	}

	filledWidth := int(percent / 100 * float64(maxBarWidth))
	if filledWidth > maxBarWidth {
		filledWidth = maxBarWidth
	}
	emptyWidth := maxBarWidth - filledWidth

	bar := barStyle.Render(strings.Repeat("█", filledWidth)) +
		dimStyle.Render(strings.Repeat("░", emptyWidth))

	percentStr := fmt.Sprintf("%.1f%%", percent)
	percentStyle := lipgloss.NewStyle().Foreground(barColor).Bold(true)

	return fmt.Sprintf("%s [%s] %s",
		labelStyle.Render("Context"),
		bar,
		percentStyle.Render(percentStr),
	)
}

// renderGeminiTokens renders the token breakdown for Gemini
func (p *AnalyticsPanel) renderGeminiTokens() string {
	labelStyle := lipgloss.NewStyle().Foreground(ColorText).Bold(true)
	valueStyle := lipgloss.NewStyle().Foreground(ColorAccent)
	dimStyle := lipgloss.NewStyle().Foreground(ColorTextDim)

	var b strings.Builder
	b.WriteString(labelStyle.Render("Tokens"))
	b.WriteString("\n")

	inputStr := formatNumber(p.geminiAnalytics.InputTokens)
	outputStr := formatNumber(p.geminiAnalytics.OutputTokens)
	totalStr := formatNumber(p.geminiAnalytics.TotalTokens())

	// Input/Output row
	b.WriteString(fmt.Sprintf("  %s %s  %s %s\n",
		dimStyle.Render("In:"),
		valueStyle.Render(inputStr),
		dimStyle.Render("Out:"),
		valueStyle.Render(outputStr),
	))

	// Total row
	totalStyle := lipgloss.NewStyle().Foreground(ColorCyan).Bold(true)
	b.WriteString(fmt.Sprintf("  %s %s\n",
		dimStyle.Render("Total:"),
		totalStyle.Render(totalStr),
	))

	return b.String()
}

// renderGeminiSessionInfo renders Gemini session info
func (p *AnalyticsPanel) renderGeminiSessionInfo() string {
	labelStyle := lipgloss.NewStyle().Foreground(ColorText).Bold(true)
	valueStyle := lipgloss.NewStyle().Foreground(ColorAccent)
	dimStyle := lipgloss.NewStyle().Foreground(ColorTextDim)

	var b strings.Builder
	b.WriteString(labelStyle.Render("Session"))
	b.WriteString("\n")

	// Duration
	durationStr := formatDuration(p.geminiAnalytics.Duration)
	b.WriteString(fmt.Sprintf("  %s %s",
		dimStyle.Render("Duration:"),
		valueStyle.Render(durationStr),
	))

	// Turns
	b.WriteString(fmt.Sprintf("  %s %s\n",
		dimStyle.Render("Turns:"),
		valueStyle.Render(fmt.Sprintf("%d", p.geminiAnalytics.TotalTurns)),
	))

	// Start time if available
	if !p.geminiAnalytics.StartTime.IsZero() {
		timeStr := p.geminiAnalytics.StartTime.Format("Jan 2 15:04")
		b.WriteString(fmt.Sprintf("  %s %s\n",
			dimStyle.Render("Started:"),
			valueStyle.Render(timeStr),
		))
	}

	return b.String()
}

// renderGeminiCost renders the estimated cost for Gemini
func (p *AnalyticsPanel) renderGeminiCost() string {
	labelStyle := lipgloss.NewStyle().Foreground(ColorText).Bold(true)
	valueStyle := lipgloss.NewStyle().Foreground(ColorGreen).Bold(true)
	dimStyle := lipgloss.NewStyle().Foreground(ColorTextDim)

	var b strings.Builder
	b.WriteString(labelStyle.Render("Cost"))
	b.WriteString("\n")

	// Use detected model from session, fall back to default pricing
	model := p.geminiAnalytics.Model
	if model == "" {
		model = "default"
	}
	cost := p.geminiAnalytics.CalculateCost(model)

	if cost > 0 {
		costStr := fmt.Sprintf("$%.4f", cost)
		b.WriteString(fmt.Sprintf("  %s %s\n",
			dimStyle.Render("Estimated:"),
			valueStyle.Render(costStr),
		))
	} else {
		b.WriteString(dimStyle.Render("  (calculating...)\n"))
	}

	return b.String()
}

// renderEmpty renders the panel when no analytics are available
func (p *AnalyticsPanel) renderEmpty() string {
	dimStyle := lipgloss.NewStyle().Foreground(ColorTextDim).Italic(true)

	var b strings.Builder
	b.WriteString(p.renderHeader())
	b.WriteString("\n\n")
	b.WriteString(dimStyle.Render("No analytics available"))
	b.WriteString("\n")
	b.WriteString(dimStyle.Render("(Claude/Gemini sessions only)"))

	return b.String()
}

// renderHeader renders the panel header
func (p *AnalyticsPanel) renderHeader() string {
	headerStyle := lipgloss.NewStyle().Foreground(ColorCyan).Bold(true)

	var b strings.Builder
	b.WriteString(headerStyle.Render("📊 Session Analytics"))
	b.WriteString("\n")
	lineLen := min(p.width-4, 40)
	if lineLen < 10 {
		lineLen = 40
	}
	b.WriteString(strings.Repeat("─", lineLen))

	return b.String()
}

// renderContextBar renders a visual bar showing context window usage
func (p *AnalyticsPanel) renderContextBar() string {
	labelStyle := lipgloss.NewStyle().Foreground(ColorText).Bold(true)
	dimStyle := lipgloss.NewStyle().Foreground(ColorTextDim)

	percent := p.analytics.ContextPercent(0) // Use default 200k limit
	if percent > 100 {
		percent = 100
	}

	// Choose color based on usage
	var barColor lipgloss.Color
	switch {
	case percent < 60:
		barColor = ColorGreen
	case percent < 80:
		barColor = ColorYellow
	default:
		barColor = ColorRed
	}

	barStyle := lipgloss.NewStyle().Foreground(barColor)

	// Calculate bar width (max 30 chars for the bar itself)
	maxBarWidth := 30
	if p.width > 0 && p.width < 50 {
		maxBarWidth = p.width - 20
		if maxBarWidth < 10 {
			maxBarWidth = 10
		}
	}

	filledWidth := int(percent / 100 * float64(maxBarWidth))
	if filledWidth > maxBarWidth {
		filledWidth = maxBarWidth
	}
	emptyWidth := maxBarWidth - filledWidth

	bar := barStyle.Render(strings.Repeat("█", filledWidth)) +
		dimStyle.Render(strings.Repeat("░", emptyWidth))

	percentStr := fmt.Sprintf("%.1f%%", percent)
	percentStyle := lipgloss.NewStyle().Foreground(barColor).Bold(true)

	return fmt.Sprintf("%s [%s] %s",
		labelStyle.Render("Context"),
		bar,
		percentStyle.Render(percentStr),
	)
}

// renderTokens renders the token breakdown section
func (p *AnalyticsPanel) renderTokens() string {
	labelStyle := lipgloss.NewStyle().Foreground(ColorText).Bold(true)
	valueStyle := lipgloss.NewStyle().Foreground(ColorAccent)
	dimStyle := lipgloss.NewStyle().Foreground(ColorTextDim)

	var b strings.Builder
	b.WriteString(labelStyle.Render("Tokens"))
	b.WriteString("\n")

	// Format token counts with commas
	inputStr := formatNumber(p.analytics.InputTokens)
	outputStr := formatNumber(p.analytics.OutputTokens)
	cacheReadStr := formatNumber(p.analytics.CacheReadTokens)
	cacheWriteStr := formatNumber(p.analytics.CacheWriteTokens)
	totalStr := formatNumber(p.analytics.TotalTokens())

	// Input/Output row
	b.WriteString(fmt.Sprintf("  %s %s  %s %s\n",
		dimStyle.Render("In:"),
		valueStyle.Render(inputStr),
		dimStyle.Render("Out:"),
		valueStyle.Render(outputStr),
	))

	// Cache row (if any cache activity)
	if p.analytics.CacheReadTokens > 0 || p.analytics.CacheWriteTokens > 0 {
		b.WriteString(fmt.Sprintf("  %s %s  %s %s\n",
			dimStyle.Render("Cache Read:"),
			valueStyle.Render(cacheReadStr),
			dimStyle.Render("Write:"),
			valueStyle.Render(cacheWriteStr),
		))
	}

	// Total row
	totalStyle := lipgloss.NewStyle().Foreground(ColorCyan).Bold(true)
	b.WriteString(fmt.Sprintf("  %s %s\n",
		dimStyle.Render("Total:"),
		totalStyle.Render(totalStr),
	))

	return b.String()
}

// renderSessionInfo renders session duration and turn count
func (p *AnalyticsPanel) renderSessionInfo() string {
	labelStyle := lipgloss.NewStyle().Foreground(ColorText).Bold(true)
	valueStyle := lipgloss.NewStyle().Foreground(ColorAccent)
	dimStyle := lipgloss.NewStyle().Foreground(ColorTextDim)

	var b strings.Builder
	b.WriteString(labelStyle.Render("Session"))
	b.WriteString("\n")

	// Duration
	durationStr := formatDuration(p.analytics.Duration)
	b.WriteString(fmt.Sprintf("  %s %s",
		dimStyle.Render("Duration:"),
		valueStyle.Render(durationStr),
	))

	// Turns
	b.WriteString(fmt.Sprintf("  %s %s\n",
		dimStyle.Render("Turns:"),
		valueStyle.Render(fmt.Sprintf("%d", p.analytics.TotalTurns)),
	))

	// Start time if available
	if !p.analytics.StartTime.IsZero() {
		timeStr := p.analytics.StartTime.Format("Jan 2 15:04")
		b.WriteString(fmt.Sprintf("  %s %s\n",
			dimStyle.Render("Started:"),
			valueStyle.Render(timeStr),
		))
	}

	return b.String()
}

// renderToolCalls renders the top 5 tools by usage count
func (p *AnalyticsPanel) renderToolCalls() string {
	labelStyle := lipgloss.NewStyle().Foreground(ColorText).Bold(true)
	toolStyle := lipgloss.NewStyle().Foreground(ColorPurple)
	countStyle := lipgloss.NewStyle().Foreground(ColorTextDim)

	var b strings.Builder
	b.WriteString(labelStyle.Render("Tools"))
	b.WriteString("\n")

	// Sort tools by count (descending)
	tools := make([]session.ToolCall, len(p.analytics.ToolCalls))
	copy(tools, p.analytics.ToolCalls)
	sort.Slice(tools, func(i, j int) bool {
		return tools[i].Count > tools[j].Count
	})

	// Show top 5
	maxTools := 5
	if len(tools) < maxTools {
		maxTools = len(tools)
	}

	for i := 0; i < maxTools; i++ {
		tool := tools[i]
		b.WriteString(fmt.Sprintf("  %s %s\n",
			toolStyle.Render(tool.Name),
			countStyle.Render(fmt.Sprintf("(%d)", tool.Count)),
		))
	}

	// Show "and N more" if there are more tools
	if len(tools) > 5 {
		b.WriteString(countStyle.Render(fmt.Sprintf("  ...and %d more\n", len(tools)-5)))
	}

	return b.String()
}

// renderCost renders the estimated cost
func (p *AnalyticsPanel) renderCost() string {
	labelStyle := lipgloss.NewStyle().Foreground(ColorText).Bold(true)
	valueStyle := lipgloss.NewStyle().Foreground(ColorGreen).Bold(true)
	dimStyle := lipgloss.NewStyle().Foreground(ColorTextDim)

	var b strings.Builder
	b.WriteString(labelStyle.Render("Cost"))
	b.WriteString("\n")

	// Calculate cost if not already set
	cost := p.analytics.EstimatedCost
	if cost == 0 && p.analytics.TotalTokens() > 0 {
		// Use default Sonnet pricing
		cost = p.analytics.CalculateCost("default")
	}

	if cost > 0 {
		costStr := fmt.Sprintf("$%.4f", cost)
		b.WriteString(fmt.Sprintf("  %s %s\n",
			dimStyle.Render("Estimated:"),
			valueStyle.Render(costStr),
		))
	} else {
		b.WriteString(dimStyle.Render("  (calculating...)\n"))
	}

	return b.String()
}

// formatNumber formats an integer with comma separators
func formatNumber(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}

	str := fmt.Sprintf("%d", n)
	result := make([]byte, 0, len(str)+(len(str)-1)/3)

	for i, c := range str {
		if i > 0 && (len(str)-i)%3 == 0 {
			result = append(result, ',')
		}
		result = append(result, byte(c))
	}

	return string(result)
}

// formatDuration formats a duration in a human-readable way
func formatDuration(d time.Duration) string {
	if d == 0 {
		return "0s"
	}

	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60
	seconds := int(d.Seconds()) % 60

	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
	if minutes > 0 {
		return fmt.Sprintf("%dm %ds", minutes, seconds)
	}
	return fmt.Sprintf("%ds", seconds)
}
