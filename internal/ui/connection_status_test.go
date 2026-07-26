package ui

import (
	"strings"
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

// A stopped session can also need auth. An agent that exits on a 401 may exit
// cleanly (exit 0), which the exit-code classifier reads as stopped (■) — the
// silent-decay shape of the 2026-07-26 fleet death, where sessions quietly went
// ■ and nothing said the shared token was broken. The auth-401 substate only
// reaches a stopped row while an auth hold is live (a deliberate stop releases
// it), so a plain user-stopped session keeps ■ and archived still overrides.
func TestRowStatusGlyph_StoppedAuthHold(t *testing.T) {
	tests := []struct {
		name     string
		status   session.Status
		substate session.Substate
		archived bool
		wantIcon string
	}{
		{"stopped + auth substate", session.StatusStopped, session.SubstateAuth401, false, "🔒"},
		{"stopped without auth substate", session.StatusStopped, "", false, "■"},
		{"stopped + unrelated substate", session.StatusStopped, session.SubstateIdleAtEmptyPrompt, false, "■"},
		{"stopped + model-unavailable is not an auth hold", session.StatusStopped, session.SubstateModelUnavailable, false, "■"},
		{"archived overrides the stopped auth glyph", session.StatusStopped, session.SubstateAuth401, true, "■"},
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

// The preview banner is the one place that spells out the remedy in full. Pin
// its content: the 2026-07-26 outage dragged on because no surface said what to
// do, and a silently-empty banner would reintroduce exactly that.
func TestAuthHoldBannerLines_NamesTheRemedy(t *testing.T) {
	out := authHoldBannerLines(120)
	for _, want := range []string{"auth failed", "/login", "press R"} {
		if !strings.Contains(out, want) {
			t.Errorf("auth hold banner must mention %q, got:\n%s", want, out)
		}
	}
	if lines := strings.Count(out, "\n"); lines != len(authHoldHintLines) {
		t.Errorf("banner rendered %d lines, want %d", lines, len(authHoldHintLines))
	}
	// Must not panic or produce a runaway line on a pathologically narrow pane.
	if got := authHoldBannerLines(0); got == "" {
		t.Error("banner must still render on a zero-width pane")
	}
}
