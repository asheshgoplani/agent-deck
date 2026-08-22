package ui

import (
	"fmt"

	"github.com/asheshgoplani/agent-deck/internal/update"
	tea "github.com/charmbracelet/bubbletea"
)

// shouldRenderUpdateNudge reports whether the >5-releases-behind nudge
// banner should be drawn on this frame. The nudge is suppressed when:
//
//  1. No update info yet (async check still pending or returned clean).
//  2. Fewer than NudgeThreshold+1 releases behind — the legacy banner
//     handles gentle cases, the nudge only fires for severely behind.
//  3. The user dismissed it via Esc earlier in this session.
//  4. AGENTDECK_SKIP_UPDATE_CHECK is set (ShouldNudge checks this).
func (h *Home) shouldRenderUpdateNudge() bool {
	if h.updateNudgeDismissed {
		return false
	}
	return update.ShouldNudge(h.updateInfo)
}

// handleUpdateNudgeDismiss is the key handler for Esc. It marks the
// nudge dismissed for the rest of the session. The caller is expected to
// only route Esc here when shouldRenderUpdateNudge() was true, but
// the handler is idempotent either way.
func (h *Home) handleUpdateNudgeDismiss(_ tea.KeyMsg) {
	h.updateNudgeDismissed = true
}

// renderUpdateNudgeText builds the user-visible banner string. Kept as a
// separate method so the unit test can assert on its content without
// reaching through lipgloss styling. The rendered banner in View() wraps
// this text in the styled bar.
func (h *Home) renderUpdateNudgeText() string {
	if h.updateInfo == nil {
		return ""
	}
	return fmt.Sprintf(" ⬆ Update available: v%s → v%s (%d releases behind — run: agent-deck update · press Esc to dismiss) ",
		h.updateInfo.CurrentVersion,
		h.updateInfo.LatestVersion,
		h.updateInfo.ReleasesBehind,
	)
}
