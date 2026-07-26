package ui

import (
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/charmbracelet/lipgloss"
)

// connectionStatusLine decides the preview-pane "Status:" line for a tool
// section whose conversation/session id is on record.
//
// A tool's session id is retained even after a session is archived or stopped
// (so the conversation can be resumed), so its mere presence does NOT mean the
// agent is live. Archived and stopped sessions have had their tmux pane torn
// down and must not claim "Connected".
func connectionStatusLine(archived bool, status session.Status) (text string, style lipgloss.Style) {
	switch {
	case archived:
		return "■ Archived", SessionStatusStopped
	case status == session.StatusStopped:
		return "■ Stopped", SessionStatusStopped
	default:
		return "● Connected", lipgloss.NewStyle().Foreground(ColorGreen).Bold(true)
	}
}

// rowStatusGlyph decides the session-list row status indicator (glyph + style).
//
// The coarse status comes from a render snapshot of the session's last-known
// state. Archiving tears down the tmux pane but does NOT reset Status, so an
// archived row would otherwise keep a live glyph (e.g. ● running); the archived
// override forces the stopped glyph regardless of the stale status/substate.
func rowStatusGlyph(status session.Status, substate session.Substate, archived bool) (icon string, style lipgloss.Style) {
	switch status {
	case session.StatusRunning:
		icon, style = "●", SessionStatusRunning
	case session.StatusWaiting:
		icon, style = "◐", SessionStatusWaiting
	case session.StatusIdle:
		icon, style = "○", SessionStatusIdle
	case session.StatusError:
		icon, style = "✕", SessionStatusError
	case session.StatusStopped:
		icon, style = "■", SessionStatusStopped
	default:
		icon, style = "○", SessionStatusIdle
	}

	// Honest Status v2: a distinct glyph for the two error substates a
	// supervisor must act on differently — a dead-model no-op loop and an
	// auth/login failure both render as "error", but a generic "✕" hides which.
	// "⚡" = model unavailable (the Fable-down no-op), "🔒" = auth/login needed.
	// Gated on StatusError so a stale cached substate cannot leak the glyph onto
	// a session that is no longer in error.
	if status == session.StatusError {
		switch substate {
		case session.SubstateModelUnavailable:
			icon = "⚡"
		case session.SubstateAuth401:
			icon = "🔒"
		}
	}

	// A stopped session can ALSO need auth: an agent that exits on a 401 may exit
	// cleanly (exit 0), which the exit-code classifier reads as stopped (■). That
	// is the silent-decay shape of the 2026-07-26 fleet death — sessions quietly
	// going ■ with nothing saying "your token is broken". The auth-401 substate
	// only reaches a stopped row when an auth hold is live (a deliberate stop
	// releases the hold), so this cannot resurrect a stale glyph.
	if status == session.StatusStopped && substate == session.SubstateAuth401 {
		icon = "🔒"
	}

	if archived {
		icon, style = "■", SessionStatusStopped
	}
	return icon, style
}

// authHoldHintLines is the preview-pane hint for a session held out of automatic
// restarts because its agent cannot authenticate.
//
// Deliberately spells out BOTH halves the 2026-07-26 outage lacked: why nothing
// is retrying (so the silence does not read as agent-deck being broken), and the
// exact two-step remedy. "Press R" is the escape hatch — the TUI restart is
// never gated, because a human pressing it IS the "credentials are fixed" signal.
var authHoldHintLines = []string{
	"🔒 auth failed — automatic restarts are held",
	"   a restart can't fix a credential, and each one races the shared token",
	"   fix: run /login (or repair credentials), then press R here",
}

// authHoldBannerLines renders the hint, each line truncated to width.
func authHoldBannerLines(width int) string {
	headStyle := lipgloss.NewStyle().Foreground(ColorRed).Bold(true)
	bodyStyle := lipgloss.NewStyle().Foreground(ColorYellow)
	out := ""
	for i, line := range authHoldHintLines {
		style := bodyStyle
		if i == 0 {
			style = headStyle
		}
		out += style.MaxWidth(max(width, 1)).Render(line) + "\n"
	}
	return out
}
