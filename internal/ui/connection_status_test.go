package ui

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// A recorded session/conversation id is kept even after a session is archived
// or stopped (so it can be resumed), so its presence must NOT be reported as
// "Connected" — the tmux pane is gone. These tests pin the honest mapping.
func TestConnectionStatusLine(t *testing.T) {
	tests := []struct {
		name     string
		archived bool
		status   session.Status
		wantText string
	}{
		{"live running session is connected", false, session.StatusRunning, "● Connected"},
		{"live waiting session is connected", false, session.StatusWaiting, "● Connected"},
		{"live idle session is connected", false, session.StatusIdle, "● Connected"},
		{"stopped session is not connected", false, session.StatusStopped, "■ Stopped"},
		{"archived session is not connected", true, session.StatusRunning, "■ Archived"},
		{"archived wins over stopped", true, session.StatusStopped, "■ Archived"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, _ := connectionStatusLine(tt.archived, tt.status)
			if text != tt.wantText {
				t.Errorf("connectionStatusLine(%v, %q) text = %q, want %q",
					tt.archived, tt.status, text, tt.wantText)
			}
		})
	}
}

// The list row status dot reads the session's last-known coarse status from a
// render snapshot. Archiving tears down tmux but does NOT reset Status, so an
// archived row would otherwise keep a live glyph (e.g. ● running). Pin the
// existing glyph mapping plus the archived override.
func TestRowStatusGlyph(t *testing.T) {
	tests := []struct {
		name     string
		status   session.Status
		substate session.Substate
		archived bool
		wantIcon string
	}{
		{"running", session.StatusRunning, "", false, "●"},
		{"waiting", session.StatusWaiting, "", false, "◐"},
		{"idle", session.StatusIdle, "", false, "○"},
		{"error", session.StatusError, "", false, "✕"},
		{"stopped", session.StatusStopped, "", false, "■"},
		{"unknown status falls back to idle glyph", session.Status("weird"), "", false, "○"},
		{"error + model-unavailable substate", session.StatusError, session.SubstateModelUnavailable, false, "⚡"},
		{"error + auth substate", session.StatusError, session.SubstateAuth401, false, "🔒"},
		{"substate glyph only applies in error status", session.StatusRunning, session.SubstateAuth401, false, "●"},
		{"archived overrides a live status", session.StatusRunning, "", true, "■"},
		{"archived overrides an error substate glyph", session.StatusError, session.SubstateAuth401, true, "■"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			icon, _ := rowStatusGlyph(tt.status, tt.substate, tt.archived)
			if icon != tt.wantIcon {
				t.Errorf("rowStatusGlyph(%q, %q, %v) icon = %q, want %q",
					tt.status, tt.substate, tt.archived, icon, tt.wantIcon)
			}
		})
	}
}
