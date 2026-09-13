// A TAB in rendered content used to garble the whole deck.
//
// ansi.StringWidth measures a TAB as ZERO cells. The terminal expands it to the
// next multiple-of-8 column. So a preview row captured from a pane running
// `git status` or `ls` (both emit real tabs, and tmux's capture-pane preserves
// them) measured exactly h.width at every width gate — clampViewToViewport
// included — and then rendered 8 to 90 cells wider. The row wrapped, the
// alternate screen scrolled, and the header, filter bar and SESSIONS title were
// pushed off the top. Bubble Tea repaints only rows whose content changed, so
// the live header came back on its next tick while the static rows stayed blank
// until an attach/detach forced a full repaint.
//
// Observed on a 131-column window: rows the deck measured at exactly 131 cells
// rendered at 139, 163, 210 and 219.
//
// These tests gate both halves of the fix: expansion at the preview ingest
// chokepoint, and the safety net in the final frame clamp.

package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

// terminalRowWidth measures a row the way a terminal renders it: escape
// sequences occupy nothing, a TAB advances to the next multiple-of-8 column,
// everything else costs its grapheme-cluster width.
//
// Deliberately independent of cellWidth/expandTabs — measuring the fix with the
// function under test would pass no matter what it did. The TAB arm here is the
// entire contract: it is what a terminal does and what ansi.StringWidth does
// not.
func terminalRowWidth(row string) int {
	plain := ansi.Strip(row)
	col := 0
	state := -1
	for len(plain) > 0 {
		cluster, rest, width, newState := uniseg.FirstGraphemeClusterInString(plain, state)
		state = newState
		plain = rest
		if cluster == "\t" {
			col += 8 - col%8
			continue
		}
		col += width
	}
	return col
}

func TestTerminalRowWidth_CountsTabsToTheNextStop(t *testing.T) {
	// Guard the guard: if this measurement ever stops disagreeing with
	// ansi.StringWidth about tabs, the regression tests below become vacuous.
	const row = "a\tb"
	if got := ansi.StringWidth(row); got != 2 {
		t.Fatalf("ansi.StringWidth(%q) = %d, want 2 (a TAB it does not count)", row, got)
	}
	if got := terminalRowWidth(row); got != 9 {
		t.Fatalf("terminalRowWidth(%q) = %d, want 9 (tab stop at column 8)", row, got)
	}
}

// TestClampViewToViewport_TabRowRendersExactlyViewportWidth is the regression.
// Every row of a clamped frame must render at exactly the viewport width — the
// contract the whole fixed-cell frame depends on — including rows carrying
// tabs.
func TestClampViewToViewport_TabRowRendersExactlyViewportWidth(t *testing.T) {
	const width, height = 131, 6

	// Shapes taken from the real capture that reproduced the bug: `ls` column
	// output and a git-status body, in the right-hand pane of the dual-column
	// layout, with and without SGR colour.
	frame := strings.Join([]string{
		strings.Repeat(" ", 46) + "│ capacity-assessment.md\t\t\t\tcutover-runbook.md\t\t\t\tgitops-layout.md",
		strings.Repeat(" ", 46) + "│ \tdocs/capacity-assessment.md",
		strings.Repeat(" ", 46) + "│ \x1b[31mdeleted:\x1b[0m\tapps/focus/acc/prometheus.yaml",
		strings.Repeat(" ", 46) + "│ 📁 wide\tglyph before the stop",
		strings.Repeat(" ", 46) + "│ no tabs on this row at all",
		"",
	}, "\n")

	clamped := clampViewToViewport(frame, width, height)

	rows := strings.Split(clamped, "\n")
	if len(rows) != height {
		t.Fatalf("clamped frame has %d rows, want %d", len(rows), height)
	}
	for i, row := range rows {
		if strings.ContainsRune(row, '\t') {
			t.Errorf("row %d still contains a TAB: %q", i, ansi.Strip(row))
		}
		if got := terminalRowWidth(row); got != width {
			t.Errorf("row %d renders %d cells, want exactly %d: %q", i, got, width, ansi.Strip(row))
		}
	}
}

func TestExpandTabs(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"no tabs is returned unchanged", "plain row", "plain row"},
		{"tab at column 0 fills the first stop", "\tx", "        x"},
		{"tab advances to the next stop, not by a fixed width", "a\tb", "a       b"},
		{"a full stop still advances a whole stop", "12345678\tx", "12345678        x"},
		{"consecutive tabs each land on their own stop", "a\t\tb", "a               b"},
		{"escape sequences occupy no columns", "\x1b[31ma\x1b[0m\tb", "\x1b[31ma\x1b[0m       b"},
		{"OSC 8 hyperlinks occupy no columns", "\x1b]8;;http://x\x1b\\a\x1b]8;;\x1b\\\tb", "\x1b]8;;http://x\x1b\\a\x1b]8;;\x1b\\       b"},
		{"a wide glyph moves the stop by the cells it occupies", "📁\tx", "📁      x"},
		{"the column resets on each line", "a\tb\nc\td", "a       b\nc       d"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := expandTabs(tc.in); got != tc.want {
				t.Fatalf("expandTabs(%q)\n got: %q\nwant: %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestExpandTabs_PreservesRenderedWidth(t *testing.T) {
	// The point of the expansion: after it, the width the deck measures and the
	// width the terminal renders agree.
	const row = "deleted:\tapps/focus/acc/prometheus.yaml\tHEAD"
	expanded := expandTabs(row)
	if cellWidth(expanded) != terminalRowWidth(row) {
		t.Fatalf("cellWidth(expanded) = %d, terminal renders %d cells",
			cellWidth(expanded), terminalRowWidth(row))
	}
}

// TestPreviewFetchedMsg_ExpandsTabs pins the ingest chokepoint: content cached
// for a preview is already what the terminal will render, for local and remote
// fetches alike (both land in this one handler).
func TestPreviewFetchedMsg_ExpandsTabs(t *testing.T) {
	home := NewHome()
	const key = "session-with-tabs"

	model, _ := home.Update(previewFetchedMsg{
		previewKey: key,
		content:    "modified:\tinternal/ui/home.go\nnew file:\tinternal/ui/cellwidth.go\n",
	})
	updated := model.(*Home)

	cached := updated.previewCache[key]
	if cached == "" {
		t.Fatal("preview content was not cached")
	}
	if strings.ContainsRune(cached, '\t') {
		t.Fatalf("cached preview still contains a TAB: %q", cached)
	}
	if want := "modified:       internal/ui/home.go\nnew file:       internal/ui/cellwidth.go\n"; cached != want {
		t.Fatalf("cached preview\n got: %q\nwant: %q", cached, want)
	}
}
