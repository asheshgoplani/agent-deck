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

// shouldRenderUpdateBanner reports whether the one-line update banner is
// drawn on this frame. It is true when a newer release has been installed
// on disk while this process runs (restart hint) or when the >5-releases
// nudge applies. Every layout height computation must use this, not
// shouldRenderUpdateNudge, so the list does not overlap the banner.
func (h *Home) shouldRenderUpdateBanner() bool {
	return h.installedUpdateVersion() != "" || h.shouldRenderUpdateNudge()
}

// restartDeckKeyLabel is the key shown in the banner and status messages.
// Falls back to the default binding for a Home built without hotkeys
// (unit tests).
func (h *Home) restartDeckKeyLabel() string {
	if key := h.actionKey(hotkeyRestartDeck); key != "" {
		return key
	}
	return defaultHotkeyBindings[hotkeyRestartDeck]
}

// renderUpdateBannerText picks the banner wording: an installed update wins
// over the nudge because the user can act on it right now.
func (h *Home) renderUpdateBannerText() string {
	if v := h.installedUpdateVersion(); v != "" {
		if h.autoRestartEnabled() {
			return fmt.Sprintf(" ⬆ v%s installed, restarting when idle (%s now) ", v, h.restartDeckKeyLabel())
		}
		return fmt.Sprintf(" ⬆ v%s installed, press %s to restart agent-deck ", v, h.restartDeckKeyLabel())
	}
	return h.renderUpdateNudgeText()
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
	return fmt.Sprintf(" ⬆ Update available: v%s → v%s (%d releases behind — press %s to install (agent-deck update) · Esc to dismiss) ",
		h.updateInfo.CurrentVersion,
		h.updateInfo.LatestVersion,
		h.updateInfo.ReleasesBehind,
		h.installUpdateKeyLabel(),
	)
}
