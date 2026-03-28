package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// overlayDropdown paints `overlay` on top of `base` starting at the given
// row and column (0-indexed). Lines of the overlay replace the characters
// underneath while preserving the rest of each base line. This gives a
// "z-index" effect for floating dropdowns.
func overlayDropdown(base string, overlay string, row, col int) string {
	baseLines := strings.Split(base, "\n")
	overLines := strings.Split(overlay, "\n")

	for i, ol := range overLines {
		targetRow := row + i
		if targetRow < 0 || targetRow >= len(baseLines) {
			continue
		}
		bl := baseLines[targetRow]
		blWidth := lipgloss.Width(bl)

		var result strings.Builder

		if col > 0 {
			if col <= blWidth {
				result.WriteString(truncateVisible(bl, col))
			} else {
				result.WriteString(bl)
				result.WriteString(strings.Repeat(" ", col-blWidth))
			}
		}

		result.WriteString(ol)

		olWidth := lipgloss.Width(ol)
		afterCol := col + olWidth
		if afterCol < blWidth {
			result.WriteString(sliceVisibleFrom(bl, afterCol))
		}

		baseLines[targetRow] = result.String()
	}

	return strings.Join(baseLines, "\n")
}

// truncateVisible returns the prefix of s that spans exactly n visible columns.
func truncateVisible(s string, n int) string {
	if n <= 0 {
		return ""
	}
	visible := 0
	inEsc := false
	var buf strings.Builder
	for _, r := range s {
		if r == '\x1b' {
			inEsc = true
			buf.WriteRune(r)
			continue
		}
		if inEsc {
			buf.WriteRune(r)
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || r == '~' || r == '\\' {
				inEsc = false
			}
			continue
		}
		if visible >= n {
			break
		}
		buf.WriteRune(r)
		visible++
	}
	return buf.String()
}

// sliceVisibleFrom returns the suffix of s starting from visible column n.
func sliceVisibleFrom(s string, n int) string {
	if n <= 0 {
		return s
	}
	visible := 0
	inEsc := false
	for i, r := range s {
		if r == '\x1b' {
			inEsc = true
			continue
		}
		if inEsc {
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || r == '~' || r == '\\' {
				inEsc = false
			}
			continue
		}
		if visible >= n {
			return s[i:]
		}
		visible++
	}
	return ""
}

// dropdownMenuBg returns a slightly elevated background color for floating menus.
func dropdownMenuBg() lipgloss.Color {
	if currentTheme == ThemeLight {
		return lipgloss.Color("#dcdde2")
	}
	return lipgloss.Color("#292e42")
}

// EditPathsDialog allows editing the repo paths of an existing multi-repo session.
type EditPathsDialog struct {
	visible      bool
	width        int
	height       int
	sessionID    string
	sessionTitle string

	paths         []string // editable path list
	originalPaths []string // snapshot at Show() for change detection
	pathCursor    int
	editing       bool
	pathInput     textinput.Model
	pathCycler    session.CompletionCycler

	pathSuggestions       []string
	allPathSuggestions    []string
	pathSuggestionCursor  int
	suggestionNavigated   bool
	suggestionsLineOffset int

	validationErr string
}

func NewEditPathsDialog() *EditPathsDialog {
	pi := textinput.New()
	pi.Placeholder = "/path/to/repo"
	pi.CharLimit = 512
	return &EditPathsDialog{pathInput: pi}
}

func (d *EditPathsDialog) Show(inst *session.Instance, pathSuggestions []string) {
	d.visible = true
	d.sessionID = inst.ID
	d.sessionTitle = inst.Title
	d.validationErr = ""

	// Resolve symlinks back to original paths for display.
	// MultiRepoTempDir contains symlinks; resolve them for the user.
	var realPaths []string
	for _, p := range inst.AllProjectPaths() {
		if target, err := filepath.EvalSymlinks(p); err == nil {
			realPaths = append(realPaths, target)
		} else {
			realPaths = append(realPaths, p)
		}
	}
	// Collapse home dir to ~ for readability
	home, _ := os.UserHomeDir()
	for i, p := range realPaths {
		if home != "" && strings.HasPrefix(p, home) {
			realPaths[i] = "~" + p[len(home):]
		}
	}

	d.paths = realPaths
	d.originalPaths = append([]string{}, realPaths...)
	d.pathCursor = 0
	d.editing = false
	d.pathInput.Blur()
	d.allPathSuggestions = pathSuggestions
	d.pathSuggestions = nil
	d.pathSuggestionCursor = 0
	d.suggestionNavigated = false
}

func (d *EditPathsDialog) Hide() {
	d.visible = false
	d.editing = false
	d.pathInput.Blur()
	d.validationErr = ""
}

func (d *EditPathsDialog) IsVisible() bool  { return d.visible }
func (d *EditPathsDialog) IsEditing() bool   { return d.editing }
func (d *EditPathsDialog) GetSessionID() string { return d.sessionID }

func (d *EditPathsDialog) SetSize(w, h int) {
	d.width = w
	d.height = h
}

// HasChanged returns true if paths differ from the original set.
func (d *EditPathsDialog) HasChanged() bool {
	cleaned := d.cleanedPaths()
	if len(cleaned) != len(d.originalPaths) {
		return true
	}
	for i, p := range cleaned {
		if p != d.originalPaths[i] {
			return true
		}
	}
	return false
}

// GetPaths returns the validated, expanded, non-empty path list.
func (d *EditPathsDialog) GetPaths() []string {
	return d.cleanedPaths()
}

func (d *EditPathsDialog) cleanedPaths() []string {
	var out []string
	seen := make(map[string]bool)
	for _, p := range d.paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		expanded := session.ExpandPath(p)
		if seen[expanded] {
			continue
		}
		seen[expanded] = true
		out = append(out, expanded)
	}
	return out
}

// Validate checks the current paths. Returns error string or "".
func (d *EditPathsDialog) Validate() string {
	cleaned := d.cleanedPaths()
	if len(cleaned) < 2 {
		return "Multi-repo requires at least 2 paths"
	}
	for _, p := range cleaned {
		if info, err := os.Stat(p); err != nil || !info.IsDir() {
			short := p
			if home, hErr := os.UserHomeDir(); hErr == nil {
				short = strings.Replace(p, home, "~", 1)
			}
			return fmt.Sprintf("Path not found: %s", short)
		}
	}
	return ""
}

func (d *EditPathsDialog) filterPathSuggestions() {
	input := strings.TrimSpace(d.pathInput.Value())
	if input == "" {
		d.pathSuggestions = d.allPathSuggestions
		return
	}
	lower := strings.ToLower(input)
	var filtered []string
	for _, s := range d.allPathSuggestions {
		if strings.Contains(strings.ToLower(s), lower) {
			filtered = append(filtered, s)
		}
	}
	d.pathSuggestions = filtered
}

func (d *EditPathsDialog) Update(msg tea.Msg) (*EditPathsDialog, tea.Cmd) {
	if !d.visible {
		return d, nil
	}

	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return d, nil
	}

	key := keyMsg.String()

	if d.editing {
		switch key {
		case "esc":
			d.editing = false
			d.pathInput.Blur()
			return d, nil

		case "enter":
			d.paths[d.pathCursor] = strings.TrimSpace(d.pathInput.Value())
			d.editing = false
			d.pathInput.Blur()
			d.pathCycler.Reset()
			return d, nil

		case "tab":
			if d.suggestionNavigated && d.pathSuggestionCursor < len(d.pathSuggestions) {
				d.pathInput.SetValue(d.pathSuggestions[d.pathSuggestionCursor])
				d.pathInput.SetCursor(len(d.pathInput.Value()))
				d.suggestionNavigated = false
				d.filterPathSuggestions()
				return d, nil
			}
			// Tab completion via cycler (same logic as newdialog)
			if d.pathCycler.IsActive() {
				d.pathInput.SetValue(d.pathCycler.Next())
				d.pathInput.SetCursor(len(d.pathInput.Value()))
			} else {
				matches, err := session.GetDirectoryCompletions(d.pathInput.Value())
				if err == nil && len(matches) > 0 {
					d.pathCycler.SetMatches(matches)
					d.pathInput.SetValue(d.pathCycler.Next())
					d.pathInput.SetCursor(len(d.pathInput.Value()))
				}
			}
			d.filterPathSuggestions()
			return d, nil

		case "ctrl+n":
			if len(d.pathSuggestions) > 0 {
				d.pathSuggestionCursor = (d.pathSuggestionCursor + 1) % len(d.pathSuggestions)
				d.suggestionNavigated = true
			}
			return d, nil

		case "ctrl+p":
			if len(d.pathSuggestions) > 0 {
				d.pathSuggestionCursor = (d.pathSuggestionCursor - 1 + len(d.pathSuggestions)) % len(d.pathSuggestions)
				d.suggestionNavigated = true
			}
			return d, nil

		default:
			d.pathInput, _ = d.pathInput.Update(msg)
			d.pathCycler.Reset()
			d.suggestionNavigated = false
			d.pathSuggestionCursor = 0
			d.filterPathSuggestions()
			return d, nil
		}
	}

	// Not editing
	switch key {
	case "down", "j":
		if d.pathCursor < len(d.paths)-1 {
			d.pathCursor++
		}
	case "up", "k":
		if d.pathCursor > 0 {
			d.pathCursor--
		}

	case "enter":
		// Start editing selected path
		d.editing = true
		d.pathInput.SetValue(d.paths[d.pathCursor])
		d.pathInput.SetCursor(len(d.pathInput.Value()))
		d.pathInput.Focus()
		d.pathCycler.Reset()
		d.suggestionNavigated = false
		d.pathSuggestionCursor = 0
		d.filterPathSuggestions()

	case "a":
		// Add new path, pre-fill with parent directory of last path
		defaultPath := ""
		for i := len(d.paths) - 1; i >= 0; i-- {
			if p := strings.TrimSpace(d.paths[i]); p != "" {
				defaultPath = filepath.Dir(session.ExpandPath(p))
				if defaultPath != "" && defaultPath != "." {
					if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(defaultPath, home) {
						defaultPath = "~" + defaultPath[len(home):]
					}
					defaultPath += string(os.PathSeparator)
				} else {
					defaultPath = ""
				}
				break
			}
		}
		d.paths = append(d.paths, defaultPath)
		d.pathCursor = len(d.paths) - 1
		d.editing = true
		d.pathInput.SetValue(defaultPath)
		d.pathInput.SetCursor(len(defaultPath))
		d.pathInput.Focus()
		d.pathCycler.Reset()
		d.suggestionNavigated = false
		d.pathSuggestionCursor = 0
		d.filterPathSuggestions()

	case "d":
		// Remove path (minimum 2 for multi-repo)
		if len(d.paths) > 2 {
			d.paths = append(d.paths[:d.pathCursor], d.paths[d.pathCursor+1:]...)
			if d.pathCursor >= len(d.paths) {
				d.pathCursor = len(d.paths) - 1
			}
		}
	}

	return d, nil
}

func (d *EditPathsDialog) View() string {
	if !d.visible {
		return ""
	}

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorCyan).MarginBottom(1)
	labelStyle := lipgloss.NewStyle().Foreground(ColorText)
	activeLabelStyle := lipgloss.NewStyle().Foreground(ColorCyan).Bold(true)
	dimStyle := lipgloss.NewStyle().Foreground(ColorComment)

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

	var content strings.Builder

	content.WriteString(titleStyle.Render("Edit Multi-Repo Paths"))
	content.WriteString("\n")
	content.WriteString(dimStyle.Render(fmt.Sprintf("  session: %s", d.sessionTitle)))
	content.WriteString("\n\n")

	content.WriteString(activeLabelStyle.Render("▶ Paths:"))
	content.WriteString("\n")

	for i, p := range d.paths {
		isSelected := i == d.pathCursor
		prefix := "    "
		if isSelected {
			prefix = "  ▸ "
		}
		if isSelected && d.editing {
			content.WriteString(fmt.Sprintf("%s%d. ", prefix, i+1))
			content.WriteString(d.pathInput.View())
			content.WriteString("\n")
		} else {
			display := p
			if display == "" {
				display = "(empty)"
			}
			if isSelected {
				content.WriteString(lipgloss.NewStyle().Foreground(ColorCyan).Bold(true).Render(
					fmt.Sprintf("%s%d. %s", prefix, i+1, display)))
			} else {
				content.WriteString(dimStyle.Render(
					fmt.Sprintf("%s%d. %s", prefix, i+1, display)))
			}
			content.WriteString("\n")
		}
	}
	content.WriteString(dimStyle.Render("    [a: add, d: remove, enter: edit, ↑↓: navigate]"))
	content.WriteString("\n")

	// Record line offset for suggestions overlay
	d.suggestionsLineOffset = strings.Count(content.String(), "\n")

	if d.validationErr != "" {
		content.WriteString("\n")
		errStyle := lipgloss.NewStyle().Foreground(ColorRed).Bold(true)
		content.WriteString(errStyle.Render("  ⚠ " + d.validationErr))
	}

	content.WriteString("\n")

	_ = labelStyle // used above

	helpStyle := lipgloss.NewStyle().Foreground(ColorComment).MarginTop(1)
	content.WriteString(helpStyle.Render("Enter confirm │ Esc cancel"))

	dialog := dialogStyle.Render(content.String())

	placed := lipgloss.Place(d.width, d.height, lipgloss.Center, lipgloss.Center, dialog)

	// Overlay path suggestions
	if d.editing && len(d.pathSuggestions) > 0 {
		suggestionsOverlay := d.renderSuggestionsDropdown()
		if suggestionsOverlay != "" {
			dialogHeight := lipgloss.Height(dialog)
			dialogWidth := lipgloss.Width(dialog)
			topRow := (d.height - dialogHeight) / 2
			leftCol := (d.width - dialogWidth) / 2
			overlayRow := topRow + 1 + 2 + d.suggestionsLineOffset
			overlayCol := leftCol + 1 + 4

			placed = overlayDropdown(placed, suggestionsOverlay, overlayRow, overlayCol)
		}
	}

	return placed
}

func (d *EditPathsDialog) renderSuggestionsDropdown() string {
	if !d.editing || len(d.pathSuggestions) == 0 {
		return ""
	}

	menuBg := dropdownMenuBg()
	suggestionStyle := lipgloss.NewStyle().Foreground(ColorComment).Background(menuBg)
	selectedStyle := lipgloss.NewStyle().Foreground(ColorCyan).Bold(true).Background(menuBg)

	maxShow := 5
	total := len(d.pathSuggestions)
	startIdx := 0
	endIdx := total
	if total > maxShow {
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

	var b strings.Builder

	if startIdx > 0 {
		b.WriteString(suggestionStyle.Render(fmt.Sprintf("  ↑ %d more above", startIdx)))
		b.WriteString("\n")
	}

	for i := startIdx; i < endIdx; i++ {
		if i > startIdx {
			b.WriteString("\n")
		}
		style := suggestionStyle
		prefix := "  "
		if i == d.pathSuggestionCursor {
			style = selectedStyle
			prefix = "▶ "
		}
		b.WriteString(style.Render(prefix + d.pathSuggestions[i]))
	}

	if endIdx < total {
		b.WriteString("\n")
		b.WriteString(suggestionStyle.Render(fmt.Sprintf("  ↓ %d more below", total-endIdx)))
	}

	var footerText string
	if len(d.pathSuggestions) < len(d.allPathSuggestions) {
		footerText = fmt.Sprintf(" %d/%d matching │ ^N/^P cycle │ Tab accept ",
			len(d.pathSuggestions), len(d.allPathSuggestions))
	} else {
		footerText = " ^N/^P cycle │ Tab accept "
	}
	b.WriteString("\n")
	b.WriteString(lipgloss.NewStyle().Foreground(ColorBorder).Background(menuBg).Render(footerText))

	menuStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorBorder).
		Background(menuBg).
		Padding(0, 1)

	return menuStyle.Render(b.String())
}
