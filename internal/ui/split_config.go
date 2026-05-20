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

// previewPctOverlayDuration is how long the "Sessions / Preview ratio"
// overlay stays visible after an adjustment.
const previewPctOverlayDuration = 1500 * time.Millisecond

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
	previewPct := h.getPreviewPct()
	sessionsPct := 100 - previewPct
	return int(float64(h.width) * float64(sessionsPct) / 100.0)
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
