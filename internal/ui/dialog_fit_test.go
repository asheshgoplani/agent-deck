package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestRenderFittedDialogIdentityWhenItFits(t *testing.T) {
	style := DialogBoxStyle.Width(30)
	s := dialogSections{head: []string{"Title", ""}, body: []string{"a", "b\nc"}, focus: 1, foot: []string{"", "Esc cancel"}}
	want := style.Render("Title\n\na\nb\nc\n\nEsc cancel")
	if got := renderFittedDialog(style, 40, s); got != want {
		t.Fatalf("a dialog that fits must render unchanged:\n%s\nwant:\n%s", got, want)
	}
	if got := renderFittedDialog(style, 0, s); got != want {
		t.Fatal("unknown height must not fit")
	}
}

func TestRenderFittedDialogShrinksPaddingThenScrolls(t *testing.T) {
	style := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(2, 1).Width(20)
	var body []string
	for i := 0; i < 20; i++ {
		body = append(body, fmt.Sprintf("row-%02d", i))
	}
	s := dialogSections{head: []string{"TITLE"}, body: body, focus: 15, foot: []string{"FOOT"}}
	for _, h := range []int{8, 12, 16} {
		got := stripAnsi(renderFittedDialog(style, h, s))
		if n := lipgloss.Height(got); n > h {
			t.Fatalf("h=%d: box is %d rows:\n%s", h, n, got)
		}
		for _, want := range []string{"TITLE", "row-15", "FOOT", "▲ more above", "▼ more below"} {
			if !strings.Contains(got, want) {
				t.Errorf("h=%d: missing %q:\n%s", h, want, got)
			}
		}
	}
	// Padding alone is enough: no scroll indicators.
	s.body = body[:6]
	s.focus = 0
	got := stripAnsi(renderFittedDialog(style, 11, s))
	if lipgloss.Height(got) != 10 || strings.Contains(got, "more") {
		t.Fatalf("shrinking padding to 0 should fit 8 rows without scrolling:\n%s", got)
	}
}

func TestDialogWindowKeepsFocusInside(t *testing.T) {
	for total := 1; total < 30; total++ {
		for budget := 3; budget < total; budget++ {
			for first := 0; first < total; first++ {
				last := min(first+1, total-1)
				win := dialogWindow(total, budget, first, last)
				if win.end-win.start != budget || win.start < 0 || win.end > total {
					t.Fatalf("total=%d budget=%d first=%d: bad window %+v", total, budget, first, win)
				}
				lo, hi := win.start, win.end-1
				if win.start > 0 {
					lo++
				}
				if win.end < total {
					hi--
				}
				if first < lo || (last > hi && last-first < budget-2) {
					t.Fatalf("total=%d budget=%d focus=[%d,%d]: window %+v hides the focus (visible %d..%d)",
						total, budget, first, last, win, lo, hi)
				}
			}
		}
	}
}

func TestRenderDialogFooter(t *testing.T) {
	items := []string{"Tab scope", "←→ column", "Type jump", "Space move", "Enter apply", "Esc cancel"}
	full := strings.Join(items, " │ ")
	if got := renderDialogFooter(200, " │ ", items, 2, 1, 0); got != full {
		t.Fatalf("a footer that fits must be the plain join: %q", got)
	}
	if got := renderDialogFooter(0, " │ ", items, 2); got != full {
		t.Fatalf("width 0 disables fitting: %q", got)
	}
	got := renderDialogFooter(64, " │ ", items, 2, 1, 0)
	if want := "Tab scope │ ←→ column │ Space move │ Enter apply │ Esc cancel"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	// Undroppable items that still do not fit take extra rows, broken
	// between items; they are never cut.
	got = renderDialogFooter(30, " │ ", items, 2, 1)
	if want := "Tab scope │ Space move\nEnter apply │ Esc cancel"; got != want {
		t.Fatalf("undroppable items spill onto a second row: got %q, want %q", got, want)
	}
	// Only a single item wider than the row is clipped.
	if got := renderDialogFooter(8, " │ ", []string{"Esc cancel"}); got != "Esc can…" {
		t.Fatalf("an item wider than the row is clipped: %q", got)
	}
}

func TestRenderDialogFooterRows(t *testing.T) {
	items := []string{"Tab scope", "←→ column", "Type jump", "Space move", "Enter apply", "Esc cancel"}
	if got := renderDialogFooterRows(80, 2, " │ ", items, 2, 1); got != strings.Join(items, " │ ") {
		t.Fatalf("a footer that fits one row is the plain join: %q", got)
	}
	// Two balanced rows before anything drops.
	got := renderDialogFooterRows(60, 2, " │ ", items, 2, 1)
	if want := "Tab scope │ ←→ column │ Type jump\nSpace move │ Enter apply │ Esc cancel"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	// Two rows too narrow: the drop list goes first, Tab scope stays.
	got = renderDialogFooterRows(30, 2, " │ ", items, 2, 1)
	if want := "Tab scope │ Space move\nEnter apply │ Esc cancel"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	// Continuation rows keep the first item's indent.
	got = renderDialogFooterRows(50, 2, "  ", []string{"  [Enter] Select", "[↑↓] Navigate", "[Tab] Global", "[Esc] Cancel"}, 1)
	if want := "  [Enter] Select  [↑↓] Navigate\n  [Tab] Global  [Esc] Cancel"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// On a terminal so short that the head and foot leave fewer than three body
// rows, the head gives rows back first: the key hints and the focused row
// stay on screen.
func TestFitDialogRowsTinyKeepsHintsAndFocus(t *testing.T) {
	var body []string
	for i := 0; i < 10; i++ {
		body = append(body, fmt.Sprintf("row-%02d", i))
	}
	s := dialogSections{
		head:  []string{"TITLE", "", "notice one", "notice two", ""},
		body:  body,
		focus: 9,
		foot:  []string{"", "count", "Esc cancel"},
	}
	for maxRows := 3; maxRows <= 10; maxRows++ {
		got := fitDialogRows(0, maxRows, s)
		rows := strings.Split(got, "\n")
		if len(rows) > maxRows {
			t.Fatalf("maxRows=%d: %d rows:\n%s", maxRows, len(rows), got)
		}
		for _, want := range []string{"Esc cancel", "row-09"} {
			if !strings.Contains(got, want) {
				t.Errorf("maxRows=%d: missing %q:\n%s", maxRows, want, got)
			}
		}
		if maxRows >= 4 && rows[0] != "TITLE" {
			t.Errorf("maxRows=%d: the title goes last:\n%s", maxRows, got)
		}
	}
	// Blank rows go before text: at 8 rows the notices stay.
	got := stripAnsi(fitDialogRows(0, 8, s))
	if want := "TITLE\nnotice one\nnotice two\n▲ more above\nrow-08\nrow-09\ncount\nEsc cancel"; got != want {
		t.Errorf("blank rows must go first:\n%s\nwant:\n%s", got, want)
	}
}
