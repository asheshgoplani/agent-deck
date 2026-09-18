package ui

import (
	"fmt"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
	"github.com/charmbracelet/lipgloss"
)

// Viewers indicator: who else has a session open.
//
// tmux knows every client attached to a session (its interactive terminals;
// agent-deck's own control pipes are filtered out in internal/tmux), so the
// deck can say "yasir has this open at 120x40" next to the row and in the
// preview. Unknown is a first-class state: the cache is cold, tmux could not
// be asked, or a remote is too old to report viewers.

// viewersEye is the badge glyph. The VS16 form (U+1F441 U+FE0F) is measured
// at 2 cells by the width library and rendered at 2 by the terminals the
// deck runs in; the bare U+1F441 is measured at 1 and rendered at 2 by some,
// which would push the row's trailing badges off the pane.
const viewersEye = "👁️"

// viewersBadge is the row badge: " 👁️ 2" for two attached terminals, nothing
// when nobody is attached or the count is unknown (a row has no room to say
// why).
func viewersBadge(viewers []tmux.Viewer, known bool) string {
	if !known || len(viewers) == 0 {
		return ""
	}
	return fmt.Sprintf(" %s %d", viewersEye, len(viewers))
}

// renderViewersBadge styles viewersBadge for a session row: cyan, or the
// selection-bar style on the selected row so it stays legible inside the
// highlight.
func renderViewersBadge(viewers []tmux.Viewer, known bool, selected bool) string {
	badge, _ := fitViewersBadge(viewers, known, selected, -1)
	return badge
}

// fitViewersBadge renders the row badge within budget cells (a negative
// budget means unbounded) and returns it with its width. Like the account
// badge, it takes part in the row's width budget (#2201): when the full
// " 👁️ N" does not fit it shortens to the eye alone, then drops, so the
// title never pays for it.
func fitViewersBadge(viewers []tmux.Viewer, known bool, selected bool, budget int) (string, int) {
	badge := viewersBadge(viewers, known)
	if badge == "" {
		return "", 0
	}
	if budget >= 0 && cellWidth(badge) > budget {
		badge = " " + viewersEye
		if cellWidth(badge) > budget {
			return "", 0
		}
	}
	style := lipgloss.NewStyle().Foreground(ColorCyan)
	if selected {
		style = SessionStatusSelStyle
	}
	return style.Render(badge), cellWidth(badge)
}

// viewersText is the value form of a viewer list: the labels joined with
// " · ", "none" for nobody, "unknown" (with the reason when given) when the
// list could not be read.
func viewersText(viewers []tmux.Viewer, known bool, unknownReason string, now time.Time) string {
	switch {
	case !known:
		if unknownReason != "" {
			return "unknown (" + unknownReason + ")"
		}
		return "unknown"
	case len(viewers) == 0:
		return "none"
	}
	return tmux.FormatViewersAt(viewers, now)
}

// viewersLine is the preview panel line under the activity time:
//
//	👁️ viewers: ashesh (200x60, active 5s ago) · yasir (120x40, 3m ago)
//	👁️ no viewers
//	👁️ viewers unknown
func viewersLine(viewers []tmux.Viewer, known bool, now time.Time) string {
	switch {
	case !known:
		return viewersEye + " viewers unknown"
	case len(viewers) == 0:
		return viewersEye + " no viewers"
	}
	return viewersEye + " viewers: " + tmux.FormatViewersAt(viewers, now)
}

// remoteViewersUnknownReason explains a remote row's unknown viewers: the
// field arrived with agent-deck 1.16.11, so an older remote cannot send it;
// a current remote that still omits it could not ask its tmux.
func remoteViewersUnknownReason(state session.RemoteVersionState, controller string) string {
	if state.Compare(controller) == session.RemoteVersionOlder {
		return "remote older than 1.16.11"
	}
	return "remote did not report viewers"
}
