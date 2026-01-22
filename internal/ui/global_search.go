package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	globalSearchBoxStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(ColorCyan).
				Padding(0, 1)

	globalResultStyle = lipgloss.NewStyle().
				Padding(0, 2)

	globalSelectedStyle = lipgloss.NewStyle().
				Padding(0, 2).
				Background(ColorCyan).
				Foreground(ColorBg)

	globalSearchHeaderStyle = lipgloss.NewStyle().
				Foreground(ColorCyan).
				Bold(true)

	highlightStyle = lipgloss.NewStyle().
			Background(ColorYellow).
			Foreground(ColorBg).
			Bold(true)
)

// GlobalSearchResult wraps a search result for UI display
type GlobalSearchResult struct {
	SessionID   string
	Summary     string
	Snippet     string
	Content     string // Full conversation content for preview
	CWD         string
	ModTime     time.Time // Last modified time
	Score       int       // Fuzzy match score (higher = better match)
	MatchCount  int       // Number of query matches in content
	InAgentDeck bool      // True if this session is already in Agent Deck
	InstanceID  string    // Agent Deck instance ID if exists
}

// GlobalSearch represents the global session search overlay
type GlobalSearch struct {
	input         textinput.Model
	results       []*GlobalSearchResult
	cursor        int
	width         int
	height        int
	visible       bool
	loading       bool
	tierName      string
	entryCount    int
	switchToLocal bool   // Flag to signal switch to local search
	previewScroll int    // Scroll offset for preview pane
	query         string // Current search query for highlighting

	// Index reference (set by Home)
	index *session.GlobalSearchIndex
}

// NewGlobalSearch creates a new global search overlay
func NewGlobalSearch() *GlobalSearch {
	ti := textinput.New()
	ti.Placeholder = "Search all Claude conversations..."
	ti.Focus()
	ti.CharLimit = 100
	ti.Width = 60

	return &GlobalSearch{
		input:   ti,
		results: []*GlobalSearchResult{},
		cursor:  0,
		visible: false,
	}
}

// SetIndex sets the search index reference
func (gs *GlobalSearch) SetIndex(index *session.GlobalSearchIndex) {
	gs.index = index
	if index != nil {
		gs.tierName = session.TierName(index.GetTier())
		gs.entryCount = index.EntryCount()
	}
}

// RefreshStats updates the stats from the index
func (gs *GlobalSearch) RefreshStats() {
	if gs.index != nil {
		gs.entryCount = gs.index.EntryCount()
		gs.loading = gs.index.IsLoading()
	}
}

// SetSize sets the dimensions of the overlay
func (gs *GlobalSearch) SetSize(width, height int) {
	gs.width = width
	gs.height = height
}

// Show makes the overlay visible
func (gs *GlobalSearch) Show() {
	gs.visible = true
	gs.input.Focus()
	gs.input.SetValue("")
	gs.results = nil
	gs.cursor = 0
	gs.switchToLocal = false
	gs.previewScroll = 0
	gs.RefreshStats()
}

// WantsSwitchToLocal returns true if user pressed Tab to switch to local search
func (gs *GlobalSearch) WantsSwitchToLocal() bool {
	if gs.switchToLocal {
		gs.switchToLocal = false
		return true
	}
	return false
}

// Hide hides the overlay
func (gs *GlobalSearch) Hide() {
	gs.visible = false
	gs.input.Blur()
}

// IsVisible returns whether the overlay is visible
func (gs *GlobalSearch) IsVisible() bool {
	return gs.visible
}

// Selected returns the currently selected result
func (gs *GlobalSearch) Selected() *GlobalSearchResult {
	if len(gs.results) == 0 {
		return nil
	}
	if gs.cursor >= len(gs.results) {
		gs.cursor = len(gs.results) - 1
	}
	return gs.results[gs.cursor]
}

// Update handles messages for the overlay
func (gs *GlobalSearch) Update(msg tea.Msg) (*GlobalSearch, tea.Cmd) {
	if !gs.visible {
		return gs, nil
	}

	// Refresh stats on every update cycle (catches loading completion)
	gs.RefreshStats()

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc":
			gs.Hide()
			return gs, nil

		case "enter":
			if len(gs.results) > 0 {
				gs.Hide()
				// Parent handles the selection
			}
			return gs, nil

		case "up":
			if gs.cursor > 0 {
				gs.cursor--
				gs.previewScroll = 0 // Reset preview scroll on cursor change
			}
			return gs, nil

		case "down":
			if gs.cursor < len(gs.results)-1 {
				gs.cursor++
				gs.previewScroll = 0 // Reset preview scroll on cursor change
			}
			return gs, nil

		case "[", "pgup":
			// Scroll preview up
			if gs.previewScroll > 0 {
				gs.previewScroll -= 5
				if gs.previewScroll < 0 {
					gs.previewScroll = 0
				}
			}
			return gs, nil

		case "]", "pgdown":
			// Scroll preview down (with bounds check)
			if len(gs.results) > 0 && gs.cursor < len(gs.results) {
				result := gs.results[gs.cursor]
				contentLines := gs.formatPreviewContent(result.Content, 80)
				maxScroll := len(contentLines) - 10 // Leave some visible
				if maxScroll < 0 {
					maxScroll = 0
				}
				if gs.previewScroll < maxScroll {
					gs.previewScroll += 5
				}
			}
			return gs, nil

		case "tab":
			// Signal to switch to local search
			gs.switchToLocal = true
			gs.Hide()
			return gs, nil

		default:
			var cmd tea.Cmd
			gs.input, cmd = gs.input.Update(msg)
			gs.updateResults()
			return gs, cmd
		}
	}

	return gs, nil
}

// updateResults performs search and updates results
func (gs *GlobalSearch) updateResults() {
	gs.query = gs.input.Value() // Store for highlighting
	query := gs.query
	if gs.index == nil || query == "" {
		gs.results = nil
		return
	}

	// Perform full-content search first (more comprehensive)
	searchResults := gs.index.Search(query)

	// If no substring matches, fall back to fuzzy search (typo tolerance)
	if len(searchResults) == 0 {
		searchResults = gs.index.FuzzySearch(query)
	}

	// Convert to UI results (limit to 15 for split view)
	gs.results = make([]*GlobalSearchResult, 0, min(len(searchResults), 15))
	queryLower := strings.ToLower(query)
	for i, sr := range searchResults {
		if i >= 15 {
			break
		}
		// Count occurrences of query in content (case-insensitive)
		matchCount := strings.Count(strings.ToLower(sr.Entry.Content), queryLower)
		gs.results = append(gs.results, &GlobalSearchResult{
			SessionID:  sr.Entry.SessionID,
			Summary:    sr.Entry.Summary,
			Snippet:    sr.Snippet,
			Content:    sr.Entry.Content, // Full content for preview
			CWD:        sr.Entry.CWD,
			ModTime:    sr.Entry.ModTime,
			Score:      sr.Score,
			MatchCount: matchCount,
		})
	}

	// Sort by combined score: fuzzy match score + recency bonus
	now := time.Now()
	sort.Slice(gs.results, func(i, j int) bool {
		// Calculate recency bonus (more recent = higher bonus)
		recencyI := 1.0 / (1.0 + now.Sub(gs.results[i].ModTime).Hours()/24)
		recencyJ := 1.0 / (1.0 + now.Sub(gs.results[j].ModTime).Hours()/24)

		// Combined score: fuzzy score * recency (both higher is better)
		// Note: fuzzy.Score is higher for better matches
		scoreI := float64(gs.results[i].Score) * (1.0 + recencyI)
		scoreJ := float64(gs.results[j].Score) * (1.0 + recencyJ)

		return scoreI > scoreJ
	})

	gs.cursor = 0
	gs.previewScroll = 0
}

// View renders the overlay with split-pane layout
func (gs *GlobalSearch) View() string {
	if !gs.visible {
		return ""
	}

	// Calculate dimensions - use most of the screen
	totalWidth := gs.width - 4
	if totalWidth > 160 {
		totalWidth = 160
	}
	if totalWidth < 100 {
		totalWidth = 100
	}
	leftWidth := totalWidth * 35 / 100       // 35% for results
	rightWidth := totalWidth - leftWidth - 3 // Rest for preview (minus border)

	previewHeight := gs.height - 12 // Leave room for header, input, hints
	if previewHeight < 10 {
		previewHeight = 10
	}

	// === LEFT PANE: Search + Results ===
	var leftPane strings.Builder

	// Header with loading indicator
	var headerText string
	if gs.loading {
		headerText = "🔍 Global Search (Loading...)"
	} else {
		headerText = fmt.Sprintf("🔍 Global Search (%d sessions)", gs.entryCount)
	}
	header := globalSearchHeaderStyle.Render(headerText)
	leftPane.WriteString(header + "\n\n")

	// Search input
	searchBox := globalSearchBoxStyle.Width(leftWidth - 4).Render(gs.input.View())
	leftPane.WriteString(searchBox + "\n\n")

	// Results list
	if len(gs.results) == 0 && gs.input.Value() != "" {
		leftPane.WriteString(lipgloss.NewStyle().
			Foreground(ColorComment).
			Render("  No results"))
	} else if len(gs.results) == 0 {
		leftPane.WriteString(lipgloss.NewStyle().
			Foreground(ColorComment).
			Italic(true).
			Render("  Type to search..."))
	} else {
		for i, result := range gs.results {
			title := result.Summary
			if title == "" {
				title = result.SessionID[:8] + "..."
			}
			// Truncate to fit left pane
			maxTitleLen := leftWidth - 12
			if maxTitleLen < 20 {
				maxTitleLen = 20
			}
			if len(title) > maxTitleLen {
				title = title[:maxTitleLen] + "..."
			}

			// Format date
			dateStr := gs.formatRelativeTime(result.ModTime)

			// Build line
			prefix := "  "
			if result.InAgentDeck {
				prefix = "• "
			}

			if i == gs.cursor {
				// Selected item - highlight
				line := globalSelectedStyle.Render(fmt.Sprintf("› %s", title))
				leftPane.WriteString(line + "\n")
				// Show date and match count below selected
				matchText := "match"
				if result.MatchCount != 1 {
					matchText = "matches"
				}
				leftPane.WriteString(lipgloss.NewStyle().
					Foreground(ColorPurple).
					Render(fmt.Sprintf("    %s • %d %s", dateStr, result.MatchCount, matchText)) + "\n")
			} else {
				line := globalResultStyle.Render(fmt.Sprintf("%s%s", prefix, title))
				leftPane.WriteString(line + "\n")
			}
		}
	}

	// Left pane hints
	leftPane.WriteString("\n")
	leftPane.WriteString(lipgloss.NewStyle().
		Foreground(ColorComment).
		Render("[↑↓] Select  [Enter] Open\n[PgUp] or '[' scroll up\n[PgDn] or ']' scroll down\n[Tab] Local  [Esc] Cancel"))

	// === RIGHT PANE: Preview ===
	var rightPane strings.Builder

	if len(gs.results) > 0 && gs.cursor < len(gs.results) {
		result := gs.results[gs.cursor]

		// Preview header
		previewHeader := lipgloss.NewStyle().
			Foreground(ColorCyan).
			Bold(true).
			Render("📄 Preview")
		rightPane.WriteString(previewHeader + "\n")

		// Show CWD
		if result.CWD != "" {
			cwdDisplay := result.CWD
			if len(cwdDisplay) > rightWidth-5 {
				cwdDisplay = "..." + cwdDisplay[len(cwdDisplay)-(rightWidth-8):]
			}
			rightPane.WriteString(lipgloss.NewStyle().
				Foreground(ColorComment).
				Render("📁 "+cwdDisplay) + "\n")
		}
		rightPane.WriteString("\n")

		// Format and display content
		content := result.Content
		if content == "" {
			content = "(No content available)"
		}

		// Split content into lines and wrap
		contentLines := gs.formatPreviewContent(content, rightWidth-2)

		// Auto-scroll to first match if scroll is at 0 (initial view)
		if gs.previewScroll == 0 && gs.query != "" {
			queryLower := strings.ToLower(gs.query)
			for i, line := range contentLines {
				if strings.Contains(strings.ToLower(line), queryLower) {
					// Scroll to a few lines before the match for context
					gs.previewScroll = i - 3
					if gs.previewScroll < 0 {
						gs.previewScroll = 0
					}
					break
				}
			}
		}

		// Apply scroll offset
		startLine := gs.previewScroll
		if startLine >= len(contentLines) {
			startLine = len(contentLines) - 1
			if startLine < 0 {
				startLine = 0
			}
			gs.previewScroll = startLine
		}

		// Show visible lines
		visibleLines := previewHeight - 4 // Account for header
		endLine := startLine + visibleLines
		if endLine > len(contentLines) {
			endLine = len(contentLines)
		}

		for i := startLine; i < endLine; i++ {
			rightPane.WriteString(contentLines[i] + "\n")
		}

		// Scroll indicator
		if len(contentLines) > visibleLines {
			scrollInfo := fmt.Sprintf("─── %d/%d lines ───", startLine+1, len(contentLines))
			rightPane.WriteString("\n" + lipgloss.NewStyle().
				Foreground(ColorComment).
				Render(scrollInfo))
		}
	} else {
		rightPane.WriteString(lipgloss.NewStyle().
			Foreground(ColorComment).
			Italic(true).
			Render("Select a result to preview"))
	}

	// Style the panes
	leftStyle := lipgloss.NewStyle().
		Width(leftWidth).
		Height(previewHeight+6).
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(ColorAccent).
		Padding(0, 1)

	rightStyle := lipgloss.NewStyle().
		Width(rightWidth).
		Height(previewHeight+6).
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(ColorCyan).
		Padding(0, 1)

	// Combine panes side by side
	combined := lipgloss.JoinHorizontal(
		lipgloss.Top,
		leftStyle.Render(leftPane.String()),
		rightStyle.Render(rightPane.String()),
	)

	return centerInScreen(combined, gs.width, gs.height)
}

// formatPreviewContent formats the conversation content for preview display
func (gs *GlobalSearch) formatPreviewContent(content string, maxWidth int) []string {
	var lines []string
	query := gs.query // Get current search query for highlighting

	// Split by newlines first
	rawLines := strings.Split(content, "\n")

	for _, rawLine := range rawLines {
		rawLine = strings.TrimSpace(rawLine)
		if rawLine == "" {
			lines = append(lines, "")
			continue
		}

		// Determine base style and prefix for user vs assistant messages
		var prefix string
		var baseColor lipgloss.Color
		if strings.HasPrefix(rawLine, "User:") || strings.HasPrefix(rawLine, "[User]") {
			prefix = "👤 "
			rawLine = strings.TrimPrefix(strings.TrimPrefix(rawLine, "User:"), "[User]")
			baseColor = ColorGreen
		} else if strings.HasPrefix(rawLine, "Assistant:") || strings.HasPrefix(rawLine, "[Assistant]") {
			prefix = "🤖 "
			rawLine = strings.TrimPrefix(strings.TrimPrefix(rawLine, "Assistant:"), "[Assistant]")
			baseColor = ColorCyan
		} else {
			prefix = ""
			baseColor = ColorText
		}

		// Word wrap long lines (wrap before highlighting for accurate width calculation)
		wrapped := gs.wrapText(rawLine, maxWidth-len(prefix))
		for i, w := range wrapped {
			// Apply highlighting after wrap
			highlighted := gs.highlightMatches(w, query)
			if i == 0 {
				lines = append(lines, lipgloss.NewStyle().Foreground(baseColor).Render(prefix)+highlighted)
			} else {
				padding := strings.Repeat(" ", len(prefix))
				lines = append(lines, padding+highlighted)
			}
		}
	}

	return lines
}

// formatRelativeTime formats time as relative (e.g., "2h ago", "3d ago")
func (gs *GlobalSearch) formatRelativeTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	diff := time.Since(t)
	switch {
	case diff < time.Minute:
		return "just now"
	case diff < time.Hour:
		return fmt.Sprintf("%dm ago", int(diff.Minutes()))
	case diff < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(diff.Hours()))
	case diff < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(diff.Hours()/24))
	case diff < 30*24*time.Hour:
		return fmt.Sprintf("%dw ago", int(diff.Hours()/(24*7)))
	default:
		return t.Format("Jan 2")
	}
}

// wrapText wraps text at word boundaries to fit within maxWidth
func (gs *GlobalSearch) wrapText(text string, maxWidth int) []string {
	if len(text) <= maxWidth {
		return []string{text}
	}

	var lines []string
	words := strings.Fields(text)
	var currentLine strings.Builder

	for _, word := range words {
		if currentLine.Len() == 0 {
			currentLine.WriteString(word)
		} else if currentLine.Len()+1+len(word) <= maxWidth {
			currentLine.WriteString(" ")
			currentLine.WriteString(word)
		} else {
			lines = append(lines, currentLine.String())
			currentLine.Reset()
			currentLine.WriteString(word)
		}
	}

	if currentLine.Len() > 0 {
		lines = append(lines, currentLine.String())
	}

	return lines
}

// highlightMatches highlights occurrences of query in text
func (gs *GlobalSearch) highlightMatches(text, query string) string {
	if query == "" || text == "" {
		return text
	}

	queryLower := strings.ToLower(query)
	textLower := strings.ToLower(text)

	var result strings.Builder
	lastEnd := 0

	for {
		idx := strings.Index(textLower[lastEnd:], queryLower)
		if idx == -1 {
			result.WriteString(text[lastEnd:])
			break
		}

		absIdx := lastEnd + idx
		// Write text before match
		result.WriteString(text[lastEnd:absIdx])
		// Write highlighted match (preserve original case)
		result.WriteString(highlightStyle.Render(text[absIdx : absIdx+len(query)]))
		lastEnd = absIdx + len(query)
	}

	return result.String()
}

// MarkInAgentDeck marks which results are already in Agent Deck
func (gs *GlobalSearch) MarkInAgentDeck(instances []*session.Instance) {
	idMap := make(map[string]string) // sessionID -> instanceID
	for _, inst := range instances {
		if inst.ClaudeSessionID != "" {
			idMap[inst.ClaudeSessionID] = inst.ID
		}
	}

	for _, result := range gs.results {
		if instID, ok := idMap[result.SessionID]; ok {
			result.InAgentDeck = true
			result.InstanceID = instID
		}
	}
}
