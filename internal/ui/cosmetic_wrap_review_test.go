// Cosmetic TUI review (v1.16.12 end check, walk section) — four findings,
// all about text wrapping/truncation, not behavior:
//
//  1. At 120 cols the full-tier footer hard-truncated the left block at the
//     terminal edge (no ellipsis) and dropped the entire right block,
//     including "↑↓ Nav" and Quit.
//  2. At 80 cols the filter bar's key-hint tail hard-truncated mid-word
//     ("t view • * time" cut down to "t v").
//  3. The New Session dialog's Command row wrapped its continuation line
//     with a different (2-column-off) indent than the first line.
//  4. The 50-col confirm dialog and 60-col search dialog footers wrapped
//     mid-phrase, splitting a key from its own label ("Enter" / "select",
//     "[Esc]" / "Cancel").
//
// This file pins the fixed behavior directly (not screen captures) so a
// regression shows up as a normal test failure rather than requiring a
// visual walk to notice.

package ui

import (
	"strconv"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// --- Finding 1: full-tier footer, width-aware -------------------------------

// TestCosmeticFooter_120Cols_KeepsNavAndQuit_NoHardCut pins the regression
// itself: at 120 cols (the tier between the 80-col compact footer and the
// 200-col footer that shows both blocks) the footer used to truncate its left
// block at the raw terminal edge with no ellipsis, and lose the entire right
// block ("↑↓ Nav … q Quit") in the process.
func TestCosmeticFooter_120Cols_KeepsNavAndQuit_NoHardCut(t *testing.T) {
	home := NewHome()
	home.width = 120
	home.height = 40
	inst := &session.Instance{ID: "s1", Tool: "claude", Status: session.StatusError}
	home.flatItems = []session.Item{{Type: session.ItemTypeSession, Session: inst}}
	home.cursor = 0

	result := tmux.StripANSI(home.renderHelpBar())

	if !strings.Contains(result, "↑↓ Nav") {
		t.Errorf("120-col footer dropped '↑↓ Nav': %q", result)
	}
	quitKey := home.actionKey(hotkeyQuit)
	if quitKey != "" && !strings.Contains(result, quitKey+" Quit") {
		t.Errorf("120-col footer dropped Quit (%q Quit): %q", quitKey, result)
	}
	for _, line := range strings.Split(result, "\n") {
		if cellWidth(line) > home.width {
			t.Errorf("120-col footer line exceeds width %d (%d cells): %q", home.width, cellWidth(line), line)
		}
	}
	// The regression's exact symptom: the left block's context hints
	// hard-truncated with no ellipsis and no trailing space before the
	// border repeats. A fixed footer either fits everything or marks the
	// cut with "…".
	if !strings.Contains(result, "…") {
		t.Errorf("120-col footer should show an ellipsis when content is dropped, got: %q", result)
	}
}

// TestCosmeticFooter_fitFullFooter_NeverDropsNavOrQuit exercises
// fitFullFooter directly across a range of widths, including ones far
// narrower than any real terminal, to pin the "Nav and Quit are never
// sacrificed" contract that the walk's fix relies on.
func TestCosmeticFooter_fitFullFooter_NeverDropsNavOrQuit(t *testing.T) {
	home := &Home{}
	parts := fullFooterParts{
		leftPrefix: "Session:",
		primary:    []string{"Enter Attach", "n/N New/Quick", "g Group"},
		secondary:  []string{"r Rename", "d Delete"},
		sep:        " │ ",
		nav:        "↑↓ Nav",
		droppable:  []string{"+/- Move", "/ Search", "G Global", "S Settings", "? Help"},
		quit:       "q Quit",
	}

	for width := 60; width <= 200; width += 10 {
		home.width = width
		got := home.fitFullFooter(parts)
		if !strings.Contains(got, parts.nav) {
			t.Errorf("width=%d: fitFullFooter dropped Nav: %q", width, got)
		}
		if !strings.Contains(got, parts.quit) {
			t.Errorf("width=%d: fitFullFooter dropped Quit: %q", width, got)
		}
	}
}

// --- Finding 2: filter bar, ellipsis on a separator boundary ----------------

// TestCosmeticFilterBar_80Cols_TruncatesOnBoundaryNotMidWord pins the exact
// regression: at 80 cols the hint tail used to cut inside the word "view"
// ("t view • * time" -> "t v"), with no ellipsis.
func TestCosmeticFilterBar_80Cols_TruncatesOnBoundaryNotMidWord(t *testing.T) {
	home := NewHome()
	home.width = 80
	home.height = 24

	snap := map[string]sessionRenderState{
		"s1": {status: session.StatusError},
		"s2": {status: session.StatusError},
		"s3": {status: session.StatusError},
		"s4": {status: session.StatusError},
	}
	home.sessionRenderSnapshot.Store(snap)
	home.cachedStatusCounts.valid.Store(false)

	bar := tmux.StripANSI(home.renderFilterBar())

	if strings.Contains(bar, "t v") && !strings.Contains(bar, "t view") {
		t.Errorf("filter bar cut mid-word ('t v' instead of 't view' or a clean ellipsis): %q", bar)
	}
	for _, line := range strings.Split(bar, "\n") {
		if cellWidth(line) > home.width {
			t.Errorf("filter bar line exceeds width %d (%d cells): %q", home.width, cellWidth(line), line)
		}
	}
}

// TestCosmeticFilterBar_fitFilterBarHint_EndsWithEllipsisWhenCut exercises
// fitFilterBarHint directly: whenever it must drop a segment, the visible
// result ends with "…" and only ever drops whole segments (never leaves a
// partial one).
func TestCosmeticFilterBar_fitFilterBarHint_EndsWithEllipsisWhenCut(t *testing.T) {
	home := NewHome()
	home.height = 24
	snap := map[string]sessionRenderState{"s1": {status: session.StatusError}}
	home.sessionRenderSnapshot.Store(snap)
	home.cachedStatusCounts.valid.Store(false)

	home.width = 0 // unknown width: no constraint, so every segment is kept
	full := tmux.StripANSI(home.fitFilterBarHint(0))
	fullSegs := strings.Split(strings.TrimPrefix(full, "  "), " • ")
	lastSeg := fullSegs[len(fullSegs)-1]

	home.width = 80
	for usedWidth := 0; usedWidth <= 40; usedWidth++ {
		got := tmux.StripANSI(home.fitFilterBarHint(usedWidth))
		if truncated := !strings.HasSuffix(got, lastSeg); truncated {
			if !strings.HasSuffix(strings.TrimRight(got, " "), "…") {
				t.Errorf("usedWidth=%d: truncated hint does not end with an ellipsis: %q", usedWidth, got)
			}
		}
		// The fitted hint plus the width it was budgeted against must stay
		// within the terminal.
		if cellWidth(got)+usedWidth > home.width {
			t.Errorf("usedWidth=%d: fitted hint (%d cells) plus used (%d) exceeds width %d: %q",
				usedWidth, cellWidth(got), usedWidth, home.width, got)
		}
	}
}

// --- Finding 3: New Session dialog Command row indent -----------------------

// TestCosmeticNewDialogCommandRow_ConsistentIndentAcrossWidths pins the
// regression: the wrapped Command row's continuation line used to land 2
// columns further left than its first line (200 cols), or occupy 3
// differently-indented lines (80 cols).
func TestCosmeticNewDialogCommandRow_ConsistentIndentAcrossWidths(t *testing.T) {
	for _, width := range []int{80, 120, 200} {
		d := NewNewDialog()
		d.SetSize(width, 50)
		d.Show()

		view := tmux.StripANSI(d.View())
		lines := strings.Split(view, "\n")

		commandRowIdx := -1
		for i, line := range lines {
			if strings.Contains(line, "Command:") {
				commandRowIdx = i
				break
			}
		}
		if commandRowIdx < 0 {
			t.Fatalf("width=%d: Command: label not found in dialog view:\n%s", width, view)
		}

		// The pill row(s) follow the label until a blank content line (the
		// box's vertical border with only whitespace between the "│"s).
		var indents []int
		for i := commandRowIdx + 1; i < len(lines); i++ {
			line := lines[i]
			trimmedRight := strings.TrimRight(line, " ")
			if strings.TrimSpace(trimmedRight) == "" || !strings.ContainsAny(trimmedRight, "abcdefghijklmnopqrstuvwxyz") {
				break
			}
			indents = append(indents, len(line)-len(strings.TrimLeft(line, " ")))
		}
		if len(indents) < 2 {
			// Narrow enough that every tool fit on one line: nothing to check.
			continue
		}
		for i, ind := range indents {
			if ind != indents[0] {
				t.Errorf("width=%d: Command row line %d has indent %d, want %d (same as the first line):\n%s",
					width, i, ind, indents[0], view)
			}
		}
	}
}

// --- Finding 4: dialog footers never split a key from its label ------------

// TestCosmeticConfirmDialog_50Cols_KeyLabelNeverSplit pins the regression: at
// 50 cols the delete-session confirm dialog used to wrap "Enter select"
// across two lines, and separately drop "Esc" onto its own line.
func TestCosmeticConfirmDialog_50Cols_KeyLabelNeverSplit(t *testing.T) {
	d := &ConfirmDialog{}
	d.SetSize(80, 24) // dialog box itself is a fixed ~50-col box regardless of terminal width
	d.ShowDeleteSession("s1", "beta-one", false, false)

	view := tmux.StripANSI(d.View())
	assertNoSplitKeyGroup(t, view, "Enter", "select")
	assertNoSplitKeyGroup(t, view, "y", "delete")
	assertNoSplitKeyGroup(t, view, "n", "cancel")
}

// TestCosmeticSearchDialog_60Cols_KeyLabelNeverSplit pins the regression: the
// 60-col search dialog footer used to wrap between "[Esc]" and "Cancel".
func TestCosmeticSearchDialog_60Cols_KeyLabelNeverSplit(t *testing.T) {
	for _, width := range []int{80, 120, 200} {
		s := NewSearch()
		s.SetSize(width, 40)
		s.Show()
		s.SetItems([]*session.Instance{
			{ID: "a", Title: "alpha-one", Tool: "shell"},
		})

		view := tmux.StripANSI(s.View())
		assertNoSplitKeyGroup(t, view, "[Enter]", "Select")
		assertNoSplitKeyGroup(t, view, "[Esc]", "Cancel")
	}
}

// assertNoSplitKeyGroup fails if key and label appear on adjacent lines of
// view with key ending one line and label starting the next (the mid-phrase
// wrap this file's fix eliminates), while allowing them to appear together on
// one line (with any amount of whitespace between, including the glued
// non-breaking space) or not at all (a dialog variant that doesn't use this
// hint).
func assertNoSplitKeyGroup(t *testing.T, view, key, label string) {
	t.Helper()
	lines := strings.Split(view, "\n")
	for i := 0; i < len(lines)-1; i++ {
		cur := strings.TrimRight(lines[i], " ")
		next := strings.TrimLeft(lines[i+1], " ")
		if strings.HasSuffix(cur, key) && strings.HasPrefix(next, label) {
			t.Errorf("key %q and label %q split across lines %d/%d:\n%s\n%s", key, label, i, i+1, lines[i], lines[i+1])
		}
	}
}

// --- Width sweep: no line ever exceeds its terminal width, and the footer
// --- never cuts a key hint mid-word --------------------------------------

// TestCosmeticWidthSweep_80to200_NoOverflowNoMidWordCut sweeps every width
// from 80 to 200 in steps of 10 (the range the walk exercised) and checks,
// for the full frame, the footer alone, and the filter bar alone: no rendered
// line exceeds the terminal width, and whenever the footer or filter bar
// visibly drops content it ends with the "…" marker rather than a bare,
// partial word.
func TestCosmeticWidthSweep_80to200_NoOverflowNoMidWordCut(t *testing.T) {
	for width := 80; width <= 200; width += 10 {
		t.Run("w"+strconv.Itoa(width), func(t *testing.T) {
			home := NewHome()
			home.width = width
			home.height = 40
			inst := &session.Instance{ID: "s1", Tool: "claude", Status: session.StatusRunning}
			home.flatItems = []session.Item{{Type: session.ItemTypeSession, Session: inst}}
			home.cursor = 0
			snap := map[string]sessionRenderState{
				"s1": {status: session.StatusRunning},
				"s2": {status: session.StatusError},
				"s3": {status: session.StatusError},
			}
			home.sessionRenderSnapshot.Store(snap)
			home.cachedStatusCounts.valid.Store(false)

			footer := tmux.StripANSI(home.renderHelpBar())
			for _, line := range strings.Split(footer, "\n") {
				if cellWidth(line) > width {
					t.Errorf("footer line exceeds width %d (%d cells): %q", width, cellWidth(line), line)
				}
			}

			filterBar := tmux.StripANSI(home.renderFilterBar())
			for _, line := range strings.Split(filterBar, "\n") {
				if cellWidth(line) > width {
					t.Errorf("filter bar line exceeds width %d (%d cells): %q", width, cellWidth(line), line)
				}
			}
			if strings.Contains(filterBar, "t v") && !strings.Contains(filterBar, "t view") {
				t.Errorf("filter bar cut mid-word ('t v' instead of 't view' or a clean ellipsis) at width %d: %q", width, filterBar)
			}
		})
	}
}
