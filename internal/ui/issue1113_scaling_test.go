// Issue #1113 — Screen scaling bugged at narrow widths and skewed splits.
//
// Reporter: @ddorman-dn against v1.9.24. Regression from #1099 (configurable
// Sessions/Preview split). At low preview_pct or narrow widths, the preview
// pane shrank below its title and rendered clipped. The integer width math
// in sessionsPaneWidth() didn't account for the 3-column separator chrome
// or enforce a minimum per-pane width.
//
// The fix introduces splitPaneWidths() which guarantees:
//   - left + separator + right == h.width (no overflow or under-fill)
//   - right >= minPreviewPaneWidth so PREVIEW title is never clipped
//   - left  >= minSessionsPaneWidth so SESSIONS title is never clipped
//
// These tests are the regression gate.

package ui

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestIssue1113_PreviewPaneFitsAtCommonWidths asserts the preview pane is
// wide enough to render its title without clipping across the terminal
// widths users actually run (laptop 80, 120; desktop 160, 200) and across
// the full configurable split range.
func TestIssue1113_PreviewPaneFitsAtCommonWidths(t *testing.T) {
	cases := []struct {
		width      int
		previewPct int
	}{
		{80, session.DefaultPreviewPct},
		{80, session.MinPreviewPct},
		{80, session.MaxPreviewPct},
		{120, session.DefaultPreviewPct},
		{120, session.MinPreviewPct},
		{120, session.MaxPreviewPct},
		{160, session.DefaultPreviewPct},
		{160, session.MinPreviewPct},
		{160, session.MaxPreviewPct},
		{200, session.DefaultPreviewPct},
		{200, session.MinPreviewPct},
		{200, session.MaxPreviewPct},
	}

	for _, tc := range cases {
		t.Run("", func(t *testing.T) {
			h := &Home{width: tc.width, previewPct: tc.previewPct}
			left, right := h.splitPaneWidths()

			if right < minPreviewPaneWidth {
				t.Fatalf("width=%d preview_pct=%d: right=%d < min=%d (preview title would clip)",
					tc.width, tc.previewPct, right, minPreviewPaneWidth)
			}
			if left < minSessionsPaneWidth {
				t.Fatalf("width=%d preview_pct=%d: left=%d < min=%d (sessions title would clip)",
					tc.width, tc.previewPct, left, minSessionsPaneWidth)
			}
			if got := left + paneSeparatorWidth + right; got != tc.width {
				t.Fatalf("width=%d preview_pct=%d: left(%d) + sep(%d) + right(%d) = %d, want %d",
					tc.width, tc.previewPct, left, paneSeparatorWidth, right, got, tc.width)
			}
		})
	}
}

// TestIssue1113_SessionsPaneWidthUsesClampedSplit verifies the existing
// sessionsPaneWidth() helper now honors the minimum-pane-width clamp so
// callers (renderDualColumnLayout, jump etc.) all see the same widths.
func TestIssue1113_SessionsPaneWidthUsesClampedSplit(t *testing.T) {
	h := &Home{width: 80, previewPct: session.MinPreviewPct}
	left := h.sessionsPaneWidth()
	_, right := h.splitPaneWidths()
	if right < minPreviewPaneWidth {
		t.Fatalf("at width=80 preview_pct=min, preview pane width %d below min %d",
			right, minPreviewPaneWidth)
	}
	if left+paneSeparatorWidth+right != 80 {
		t.Fatalf("layout overflow: %d + %d + %d != 80", left, paneSeparatorWidth, right)
	}
}

// TestIssue1113_GracefulFallbackBelowChromeBudget covers the boundary where
// the terminal is too narrow to fit both minimums plus the separator. The
// split must not panic and must not produce negative widths.
func TestIssue1113_GracefulFallbackBelowChromeBudget(t *testing.T) {
	cases := []int{0, 1, 5, 10, 15}
	for _, w := range cases {
		t.Run("", func(t *testing.T) {
			h := &Home{width: w, previewPct: session.DefaultPreviewPct}
			left, right := h.splitPaneWidths()
			if left < 0 || right < 0 {
				t.Fatalf("width=%d: negative widths left=%d right=%d", w, left, right)
			}
			// Overflow check only meaningful when there is room for the
			// separator itself. Below that the function may return (0,0)
			// without subtracting separator chrome (no panel can be drawn).
			if w > paneSeparatorWidth && left+paneSeparatorWidth+right > w {
				t.Fatalf("width=%d: overflow left=%d right=%d (left+sep+right=%d > w)",
					w, left, right, left+paneSeparatorWidth+right)
			}
		})
	}
}

// TestIssue1113_BoundaryPreviewPctRespectsClamp checks that even at the
// extreme min/max preview_pct values the clamp keeps both pane titles
// visible (the truncation in the bug screenshot).
func TestIssue1113_BoundaryPreviewPctRespectsClamp(t *testing.T) {
	cases := []struct {
		name       string
		width      int
		previewPct int
	}{
		{"min preview at narrow", 80, session.MinPreviewPct},
		{"max preview at narrow", 80, session.MaxPreviewPct},
		{"min preview at wide", 200, session.MinPreviewPct},
		{"max preview at wide", 200, session.MaxPreviewPct},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &Home{width: tc.width, previewPct: tc.previewPct}
			left, right := h.splitPaneWidths()
			if left < minSessionsPaneWidth {
				t.Errorf("sessions width %d < min %d (title clips)", left, minSessionsPaneWidth)
			}
			if right < minPreviewPaneWidth {
				t.Errorf("preview width %d < min %d (title clips)", right, minPreviewPaneWidth)
			}
			if left+paneSeparatorWidth+right != tc.width {
				t.Errorf("layout mismatch: %d+%d+%d != %d",
					left, paneSeparatorWidth, right, tc.width)
			}
		})
	}
}
