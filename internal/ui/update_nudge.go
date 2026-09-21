package ui

import (
	"fmt"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/update"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
	return h.binaryOrphanReason != "" || h.installedUpdateVersion() != "" || h.shouldRenderUpdateNudge()
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
	if h.binaryOrphanReason != "" {
		// Action first: the path at the end may be cut off by the width.
		text := " ⚠ Quit and start agent-deck again"
		if h.updateInfo != nil && h.updateInfo.Available {
			text += fmt.Sprintf(" to get v%s", h.updateInfo.LatestVersion)
		}
		return text + ": this one cannot update or restart itself, " + h.binaryOrphanReason + " "
	}
	if v := h.installedUpdateVersion(); v != "" {
		if h.autoInstallInFlight != "" {
			return h.updateRunBannerText(v)
		}
		if !h.autoRestartEnabled() {
			return fmt.Sprintf(" ⬆ v%s installed, press %s to restart agent-deck ", v, h.restartDeckKeyLabel())
		}
		if h.restartOverdueReason != "" {
			hours := int(time.Since(h.installedUpdateSince()).Round(time.Hour).Hours())
			return fmt.Sprintf(" ⚠ v%s installed %dh ago, restart overdue: %s (%s to restart now) ",
				v, hours, h.restartOverdueReason, h.restartDeckKeyLabel())
		}
		return fmt.Sprintf(" ⬆ v%s installed, restarting when idle (%s now) ", v, h.restartDeckKeyLabel())
	}
	return h.renderUpdateNudgeText()
}

// updateRunBannerText is the banner while the newer build is on disk but
// the unattended updater this TUI spawned is still running (remote phase or
// launch agent re-registration). The restart key is not "now" in this
// state: it queues the restart, so the text says what runs and when the
// restart happens, with the remote progress when known, and picks the
// longest variant that fits the terminal width.
func (h *Home) updateRunBannerText(v string) string {
	key := h.restartDeckKeyLabel()
	what, short := h.updateRunPhase()
	var variants []string
	switch {
	case h.restartQueued:
		variants = []string{
			fmt.Sprintf(" ⬆ v%s installed, restart queued: %s, then restarting ", v, what),
			fmt.Sprintf(" ⬆ v%s installed, restart queued after %s ", v, short),
			fmt.Sprintf(" ⬆ v%s, restart queued ", v),
		}
	case !h.autoRestartEnabled():
		variants = []string{
			fmt.Sprintf(" ⬆ v%s installed, %s, then press %s to restart ", v, what, key),
			fmt.Sprintf(" ⬆ v%s installed, %s, then %s ", v, short, key),
			fmt.Sprintf(" ⬆ v%s installed, then %s ", v, key),
		}
	default:
		variants = []string{
			fmt.Sprintf(" ⬆ v%s installed, %s, then restarting (%s queues it) ", v, what, key),
			fmt.Sprintf(" ⬆ v%s installed, %s, then restarting ", v, what),
			fmt.Sprintf(" ⬆ v%s installed, %s, then restart ", v, short),
			fmt.Sprintf(" ⬆ v%s installed, restarting soon ", v),
		}
	}
	for _, t := range variants {
		if h.width <= 0 || lipgloss.Width(t) <= h.width {
			return t
		}
	}
	return variants[len(variants)-1]
}

// updateRunPhase names what the updater child is doing, in a long and a
// short form, for the banner and the footer: "nudging 4 remotes" by
// default, "finishing the remote sweep" only in the opt-in push model.
func (h *Home) updateRunPhase() (long, short string) {
	if h.autoInstallInFlight == pendingDrainKey {
		return "re-registering launchd agents", "launchd agents"
	}
	mode, total := h.autoInstallProgress.Phase()
	remotes := "remotes"
	if total == 1 {
		remotes = "remote"
	}
	switch mode {
	case update.PhaseNudge:
		return fmt.Sprintf("nudging %d %s to update", total, remotes), fmt.Sprintf("nudging %d %s", total, remotes)
	case update.PhaseSweep:
		return fmt.Sprintf("finishing the remote sweep (%d %s)", total, remotes), fmt.Sprintf("remote sweep %d", total)
	}
	return "finishing the update", "the update"
}

// handleUpdateNudgeDismiss is the key handler for Esc. It marks the
// nudge dismissed for the rest of the session. The caller is expected to
// only route Esc here when shouldRenderUpdateNudge() was true, but
// the handler is idempotent either way.
func (h *Home) handleUpdateNudgeDismiss(_ tea.KeyMsg) {
	h.updateNudgeDismissed = true
	// Dismissing the banner gives its row back to the embedded pane.
	h.syncEmbeddedTerminalGeometry()
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
