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
	// a session that is no longer in error (e.g. a stopped session).
	if status == session.StatusError {
		switch substate {
		case session.SubstateModelUnavailable:
			icon = "⚡"
		case session.SubstateAuth401:
			icon = "🔒"
		}
	}

	if archived {
		icon, style = "■", SessionStatusStopped
	}
	return icon, style
}
