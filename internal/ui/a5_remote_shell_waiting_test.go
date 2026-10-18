package ui

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// A5: a finished Claude session with a leftover background shell reports
// waiting / idle-at-empty-prompt from the remote's own `list --json`; the
// remote header counts and the row glyph must count it as waiting, never
// running.
func TestA5_RemoteLeftoverShellRowCountsAsWaiting(t *testing.T) {
	rows := []session.RemoteSessionInfo{
		{ID: "a5", Group: "work", Tool: "claude", Status: "waiting", Substate: "idle-at-empty-prompt", RemoteName: "dev"},
	}
	if running, waiting := remoteStatusCounts(rows, "work"); running != 0 || waiting != 1 {
		t.Fatalf("sub-group header = (%d running, %d waiting), want (0, 1)", running, waiting)
	}
	if c := remoteHeaderCounts("dev", rows)["remotes/dev"]; c.running != 0 || c.waiting != 1 {
		t.Fatalf("host header = %+v, want 0 running, 1 waiting", c)
	}
	waitingIcon, _ := remoteRowStatusGlyph("waiting", "", false)
	if icon, _ := remoteRowStatusGlyph("waiting", "idle-at-empty-prompt", false); icon != waitingIcon {
		t.Fatalf("row glyph = %q, want the waiting glyph %q", icon, waitingIcon)
	}
}
