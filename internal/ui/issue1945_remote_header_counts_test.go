package ui

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// #1945: archiving tears down the pane but does not reset Status, so an
// archived remote session keeps whatever it was doing when it was archived.
// remoteStatusCounts matched the raw wire string and had no case for the
// archived override, so the header counted a session as running while #1944
// gave that same row the stopped glyph — the number and the glyphs describing
// different sets of the same rows.
//
// Note what this test does NOT assert: that archived rows are excluded from the
// header's TOTAL. They are not, and must not be — unlike the local list, the
// remote list does not partition by archive state, so an archived remote row is
// rendered (with ■) in the normal view. Its total belongs in the count; only
// the running/waiting tallies must exclude it.
func TestRemoteStatusCounts_ArchivedIsNeitherRunningNorWaiting(t *testing.T) {
	sessions := []session.RemoteSessionInfo{
		{ID: "a", Group: "work", Status: "running"},
		{ID: "b", Group: "work", Status: "waiting"},
		{ID: "c", Group: "work", Status: "running", Archived: true},
		{ID: "d", Group: "work", Status: "waiting", Archived: true},
	}

	running, waiting := remoteStatusCounts(sessions, "work")
	if running != 1 {
		t.Errorf("running = %d, want 1 — an archived session keeps a live Status and must not be counted (#1945)", running)
	}
	if waiting != 1 {
		t.Errorf("waiting = %d, want 1 — same for waiting (#1945)", waiting)
	}

	// The subtree total is a different question and keeps every rendered row.
	if got := remoteSubGroupCount(sessions, "work"); got != 4 {
		t.Errorf("remoteSubGroupCount = %d, want 4 — archived remote rows ARE rendered, so the header total includes them", got)
	}
}

// The whole-remote header (empty groupPath) follows the same rule.
func TestRemoteStatusCounts_HostHeaderSkipsArchived(t *testing.T) {
	sessions := []session.RemoteSessionInfo{
		{ID: "a", Group: "one", Status: "running"},
		{ID: "b", Group: "two", Status: "running", Archived: true},
	}
	running, waiting := remoteStatusCounts(sessions, "")
	if running != 1 || waiting != 0 {
		t.Errorf("host header counts = (%d running, %d waiting), want (1, 0) (#1945)", running, waiting)
	}
}
