package ui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

const (
	maxZoxideResultsShown = 10
	zoxideQueryTimeout    = 500 * time.Millisecond
)

// zoxideQueryFunc looks up matching paths from the zoxide database for the
// given query. Injected via the picker for deterministic tests.
type zoxideQueryFunc func(query string) ([]string, error)

// ZoxidePicker is a minimal overlay that fuzzy-matches directories via zoxide
// and returns the selected path for quick session creation.
type ZoxidePicker struct {
	visible    bool
	queryInput textinput.Model
	results    []string
	cursor     int
	width      int
	height     int
	errMsg     string
	unavail    bool // zoxide not installed; results disabled
	queryFn    zoxideQueryFunc
	checkAvail bool

	// suggestFn, when set, supplies the unified frecency-ranked candidate list
	// (recents + group defaults + zoxide, via session.PathSuggest). It makes
	// the picker "the interaction": Show() pre-populates it with no query, and
	// it takes precedence over the raw zoxide query (and its availability gate).
	suggestFn  suggestQueryFunc
	candidates []session.PathCandidate
}

// suggestQueryFunc returns the ranked candidate list for the given query.
type suggestQueryFunc func(query string) []session.PathCandidate

// NewZoxidePicker constructs a picker wired to the real zoxide binary.
func NewZoxidePicker() *ZoxidePicker {
	z := newZoxidePickerWithQueryFn(defaultZoxideQuery)
	z.checkAvail = true
	return z
}

func newZoxidePickerWithQueryFn(fn zoxideQueryFunc) *ZoxidePicker {
	ti := textinput.New()
	ti.Placeholder = "fragment of a path you've visited"
	ti.CharLimit = 256
	ti.Width = 40
	return &ZoxidePicker{
		queryInput: ti,
		queryFn:    fn,
	}
}

func defaultZoxideQuery(query string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), zoxideQueryTimeout)
	defer cancel()
	return session.ZoxideQuery(ctx, query)
}

// SetSuggestProvider wires the unified PathSuggest candidate source. Once set,
// the picker ranks candidates from it (recents + group defaults + zoxide) and
// ignores the standalone zoxide availability gate.
func (z *ZoxidePicker) SetSuggestProvider(fn suggestQueryFunc) {
	z.suggestFn = fn
}

// Show opens the picker. If zoxide is missing the picker still renders but
// displays an install hint and disables selection.
func (z *ZoxidePicker) Show() {
	z.visible = true
	z.errMsg = ""
	z.cursor = 0
	z.queryInput.SetValue("")
	z.queryInput.CursorEnd()
	z.queryInput.Focus()

	// A wired suggest provider is the source of truth and never "unavailable":
	// even without zoxide there are recents and group defaults to show.
	if z.suggestFn == nil && z.checkAvail && !session.ZoxideAvailable() {
		z.unavail = true
		z.results = nil
		return
	}
	z.unavail = false
	z.refreshResults()
}

// Hide closes the picker.
func (z *ZoxidePicker) Hide() {
	z.visible = false
	z.queryInput.Blur()
}

// IsVisible reports whether the picker is currently shown.
func (z *ZoxidePicker) IsVisible() bool { return z.visible }

// Selected returns the highlighted path, or empty if nothing is selectable.
func (z *ZoxidePicker) Selected() string {
	if z.cursor < 0 || z.cursor >= len(z.results) {
		return ""
	}
	return z.results[z.cursor]
}

// SetSize updates the dialog viewport for centering.
func (z *ZoxidePicker) SetSize(width, height int) {
	z.width = width
	z.height = height
}

// Update processes a key event and refreshes results when the query changes.
func (z *ZoxidePicker) Update(msg tea.KeyMsg) (*ZoxidePicker, tea.Cmd) {
	switch msg.String() {
	case "up", "ctrl+p":
		if z.cursor > 0 {
			z.cursor--
		}
		return z, nil
	case "down", "ctrl+n":
		if z.cursor < len(z.results)-1 {
			z.cursor++
		}
		return z, nil
	}

	prev := z.queryInput.Value()
	var cmd tea.Cmd
	z.queryInput, cmd = z.queryInput.Update(msg)
	if z.queryInput.Value() != prev && !z.unavail {
		z.refreshResults()
	}
	return z, cmd
}

func (z *ZoxidePicker) refreshResults() {
	if z.suggestFn != nil {
		z.candidates = z.suggestFn(z.queryInput.Value())
		z.errMsg = ""
		z.results = z.results[:0]
		for _, c := range z.candidates {
			z.results = append(z.results, c.Path)
		}
		if z.cursor >= len(z.results) {
			z.cursor = 0
		}
		return
	}

	results, err := z.queryFn(z.queryInput.Value())
	if err != nil {
		z.errMsg = err.Error()
		z.results = nil
		z.cursor = 0
		return
	}
	z.errMsg = ""
	z.results = results
	if z.cursor >= len(z.results) {
		z.cursor = 0
	}
}

// View renders the overlay, centered in the viewport.
func (z *ZoxidePicker) View() string {
	if !z.visible {
		return ""
	}

	title := DialogTitleStyle.Render("Quick Open (zoxide)")
	body := z.queryInput.View()

	var listBlock string
	switch {
	case z.unavail:
		listBlock = lipgloss.NewStyle().
			Foreground(ColorRed).
			Render("zoxide not found on PATH\ninstall: brew install zoxide")
	case z.errMsg != "":
		listBlock = lipgloss.NewStyle().
			Foreground(ColorRed).
			Render("⚠ " + z.errMsg)
	case len(z.results) == 0:
		listBlock = lipgloss.NewStyle().
			Foreground(ColorTextDim).
			Render("(no matches)")
	default:
		listBlock = z.renderResults()
	}

	hintStyle := lipgloss.NewStyle().Foreground(ColorComment)
	hint := hintStyle.Render("↑/↓ navigate │ Enter open │ Esc cancel")

	content := lipgloss.JoinVertical(
		lipgloss.Left,
		title,
		"",
		body,
		"",
		listBlock,
		"",
		hint,
	)

	dialog := DialogBoxStyle.
		Width(z.dialogWidth()).
		Render(content)

	return lipgloss.Place(
		z.width,
		z.height,
		lipgloss.Center,
		lipgloss.Center,
		dialog,
	)
}

func (z *ZoxidePicker) renderResults() string {
	shown := z.results
	if len(shown) > maxZoxideResultsShown {
		shown = shown[:maxZoxideResultsShown]
	}
	rowStyle := lipgloss.NewStyle().Foreground(ColorText).Padding(0, 1)
	selStyle := lipgloss.NewStyle().
		Foreground(ColorBg).
		Background(ColorAccent).
		Bold(true).
		Padding(0, 1)

	hintStyle := lipgloss.NewStyle().Foreground(ColorComment)
	home, _ := os.UserHomeDir()
	rows := make([]string, 0, len(shown)+1)
	for i, p := range shown {
		display := p
		if home != "" && strings.HasPrefix(p, home) {
			display = "~" + strings.TrimPrefix(p, home)
		}
		// Annotate with the candidate source (recent/group/zoxide) when the
		// unified provider is driving the list, so the origin is legible.
		if i < len(z.candidates) && z.candidates[i].Source != "" {
			display += "  " + hintStyle.Render(string(z.candidates[i].Source))
		}
		if i == z.cursor {
			rows = append(rows, selStyle.Render(display))
		} else {
			rows = append(rows, rowStyle.Render(display))
		}
	}
	if extra := len(z.results) - len(shown); extra > 0 {
		rows = append(rows, lipgloss.NewStyle().
			Foreground(ColorTextDim).
			Render(fmt.Sprintf("  (+%d more — refine query)", extra)))
	}
	return strings.Join(rows, "\n")
}

func (z *ZoxidePicker) dialogWidth() int {
	return fitDialogWidth(70, 40, z.width)
}
