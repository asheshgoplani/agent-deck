// Sessions/Preview split configuration (issue #1092).
//
// Resolves a configurable horizontal split between the SESSIONS list and
// the PREVIEW pane. Defaults preserve the historical 35/65 layout.
//
// Two surfaces:
//   - Config file: ~/.agent-deck/config.toml -> [ui] preview_pct (10-90)
//   - Runtime keybinding: < shrinks preview by 5%, > grows it by 5%
//
// The runtime adjustment persists back to config.toml so it survives
// restart. The brief overlay showing the new ratio is drawn by the
// home renderer when previewPctOverlayAt is in the future.

package ui

import (
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// previewPctStep is the percentage delta per < / > keystroke.
const previewPctStep = 5

// Preview-orientation values, re-exported from the session package so the
// ui layer can compare h.previewOrientation without importing the constant
// at every call site.
const (
	PreviewOrientationRight = session.PreviewOrientationRight
	PreviewOrientationBelow = session.PreviewOrientationBelow
)

// stackedListHeight resolves the SESSIONS-list height (in rows) for the
// stacked layout, given the total content height. The preview pane gets
// previewPct of the height and the list gets the remainder — mirroring the
// dual layout's width convention so the < / > keybindings adjust the split
// in either orientation. A single source of truth for the three call sites
// (renderStackedLayout and the two maxVisible calcs) that must stay in
// lockstep. Guarantees list >= 5 and preview >= 3 rows when height allows.
func (h *Home) stackedListHeight(totalHeight int) int {
	if totalHeight <= 0 {
		return 0
	}
	previewPct := h.getPreviewPct()
	sessionsPct := 100 - previewPct
	listHeight := (totalHeight * sessionsPct) / 100

	// Reserve a floor for each pane (preview loses 1 row to the separator).
	if listHeight < 5 {
		listHeight = 5
	}
	if totalHeight-listHeight-1 < 3 {
		listHeight = totalHeight - 4 // leave 3 for preview + 1 for separator
	}
	if listHeight < 0 {
		listHeight = 0
	}
	return listHeight
}

// previewPctOverlayDuration is how long the "Sessions / Preview ratio"
// overlay stays visible after an adjustment.
const previewPctOverlayDuration = 1500 * time.Millisecond

// Pane chrome / minimum widths for the dual layout (issue #1113).
//
// The dual layout draws " │ " (3 cols) between sessions and preview. At
// extreme preview_pct values or narrow widths the integer percentage math
// alone would shrink one pane below its title. These minimums guarantee
// both panel titles always render without truncation; splitPaneWidths
// clamps to them and gives the leftover columns to the other pane.
const (
	paneSeparatorWidth   = 3 // " │ "
	minSessionsPaneWidth = 8 // fits "SESSIONS"
	minPreviewPaneWidth  = 8 // fits "PREVIEW " (with overlay suffix budget)
)

// getPreviewPct returns the current preview percentage with bounds
// applied. Falls back to the package default when the field is zero
// (which is the case for Home instances built before this feature
// landed and for tests that bypass NewHome).
func (h *Home) getPreviewPct() int {
	if h.previewPct <= 0 {
		return session.DefaultPreviewPct
	}
	if h.previewPct < session.MinPreviewPct {
		return session.MinPreviewPct
	}
	if h.previewPct > session.MaxPreviewPct {
		return session.MaxPreviewPct
	}
	return h.previewPct
}

// sessionsPaneWidth returns the column width allocated to the sessions
// list panel in the dual layout. Replaces the historical
// `int(float64(h.width) * 0.35)` literal.
func (h *Home) sessionsPaneWidth() int {
	left, _ := h.splitPaneWidths()
	return left
}

// splitPaneWidths resolves the (sessions, preview) column widths for the
// dual layout, accounting for the 3-column separator chrome and clamping
// each pane to its title-fit minimum (issue #1113). Returned widths
// always satisfy left + paneSeparatorWidth + right == h.width when
// h.width is wide enough to fit both minimums plus the separator. For
// degenerate widths (below the chrome budget), the function falls back
// gracefully: it gives whatever it can to each pane without producing
// negative widths.
func (h *Home) splitPaneWidths() (int, int) {
	if h.width <= 0 {
		return 0, 0
	}
	previewPct := h.getPreviewPct()
	sessionsPct := 100 - previewPct
	left := int(float64(h.width) * float64(sessionsPct) / 100.0)
	right := h.width - left - paneSeparatorWidth

	chromeBudget := minSessionsPaneWidth + minPreviewPaneWidth + paneSeparatorWidth
	if h.width < chromeBudget {
		// Not enough room for both minimums. Keep widths non-negative;
		// renderDualColumnLayout is only routed to above
		// layoutBreakpointStacked (80), so this branch is safety for
		// edge calls (tests, dynamic resize churn).
		return max(left, 0), max(right, 0)
	}

	// Below the preview minimum: borrow from sessions.
	if right < minPreviewPaneWidth {
		right = minPreviewPaneWidth
		left = h.width - right - paneSeparatorWidth
	}
	// Below the sessions minimum: borrow from preview.
	if left < minSessionsPaneWidth {
		left = minSessionsPaneWidth
		right = h.width - left - paneSeparatorWidth
	}
	return left, right
}

// isOnDivider reports whether column x falls on the " │ " separator drawn
// between the sessions and preview panes in the dual layout. The separator
// occupies the paneSeparatorWidth columns immediately to the right of the
// sessions pane. Used as the grab target for mouse-drag resizing.
func (h *Home) isOnDivider(x int) bool {
	left := h.sessionsPaneWidth()
	return x >= left && x < left+paneSeparatorWidth
}

// setPreviewPctFromMouseX resizes the split so the divider follows the mouse
// to column x: the sessions pane spans columns [0, x), the preview pane takes
// the rest. The result is clamped to [MinPreviewPct, MaxPreviewPct] and the
// ratio overlay is armed for visual feedback. This updates the in-memory value
// only — persistence happens once on drag release, not on every motion event.
func (h *Home) setPreviewPctFromMouseX(x int) {
	if h.width <= 0 {
		return
	}
	if x < 0 {
		x = 0
	}
	if x > h.width {
		x = h.width
	}
	// Floor x/width to a percent, matching splitPaneWidths' percent->column
	// truncation. Using the same rounding rule in both directions keeps the
	// rendered divider tracking the cursor without a systematic 1-column drift.
	sessionsPct := x * 100 / h.width
	previewPct := 100 - sessionsPct
	if previewPct < session.MinPreviewPct {
		previewPct = session.MinPreviewPct
	}
	if previewPct > session.MaxPreviewPct {
		previewPct = session.MaxPreviewPct
	}
	h.previewPct = previewPct
	h.previewPctOverlayAt = time.Now().Add(previewPctOverlayDuration)
}

// adjustPreviewPct shifts the preview percentage by delta (in percent
// points), clamps to [MinPreviewPct, MaxPreviewPct], persists the new
// value to config.toml, and arms the on-screen overlay.
//
// Returns true if the value actually changed so callers can decide
// whether to trigger a repaint.
func (h *Home) adjustPreviewPct(delta int) bool {
	current := h.getPreviewPct()
	next := current + delta
	if next < session.MinPreviewPct {
		next = session.MinPreviewPct
	}
	if next > session.MaxPreviewPct {
		next = session.MaxPreviewPct
	}
	if next == current {
		// Already at a bound; still arm the overlay so the user gets
		// visual feedback that the keystroke was received.
		h.previewPctOverlayAt = time.Now().Add(previewPctOverlayDuration)
		return false
	}
	h.previewPct = next
	h.previewPctOverlayAt = time.Now().Add(previewPctOverlayDuration)
	persistPreviewPct(next)
	return true
}

// persistPreviewPct writes the new preview percentage to config.toml.
// Errors are swallowed: a failed save shouldn't crash the TUI, and the
// in-memory value still takes effect for the current session.
func persistPreviewPct(pct int) {
	cfg, err := session.LoadUserConfig()
	if err != nil || cfg == nil {
		return
	}
	if cfg.UI.PreviewPct == pct {
		return
	}
	cfg.UI.PreviewPct = pct
	_ = session.SaveUserConfig(cfg)
}

// getPreviewOrientation returns the current orientation with the package
// default applied when the field is empty (Home instances built before this
// feature landed, or tests that bypass NewHome).
func (h *Home) getPreviewOrientation() string {
	switch h.previewOrientation {
	case PreviewOrientationBelow:
		return PreviewOrientationBelow
	case PreviewOrientationRight:
		return PreviewOrientationRight
	}
	return session.DefaultPreviewOrientation
}

// togglePreviewOrientation flips the preview-pane orientation between
// "right" (side-by-side) and "below" (stacked), persists it to config.toml,
// and arms the on-screen overlay for visual feedback.
func (h *Home) togglePreviewOrientation() {
	if h.getPreviewOrientation() == PreviewOrientationBelow {
		h.previewOrientation = PreviewOrientationRight
	} else {
		h.previewOrientation = PreviewOrientationBelow
	}
	h.previewPctOverlayAt = time.Now().Add(previewPctOverlayDuration)
	persistPreviewOrientation(h.previewOrientation)
}

// persistPreviewOrientation writes the new orientation to config.toml.
// Errors are swallowed for the same reason as persistPreviewPct.
func persistPreviewOrientation(orientation string) {
	cfg, err := session.LoadUserConfig()
	if err != nil || cfg == nil {
		return
	}
	if cfg.UI.PreviewOrientation == orientation {
		return
	}
	cfg.UI.PreviewOrientation = orientation
	_ = session.SaveUserConfig(cfg)
}
