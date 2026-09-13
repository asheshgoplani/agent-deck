package ui

import (
	"strings"
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
// header's TOTAL by these helpers. They count whatever slice they are given;
// the archive partition (active view vs ^ view) is applied by the caller via
// Home.remoteSessionsInView, pinned in TestRemoteHeaderCountsFollowArchiveView.
// Only the running/waiting tallies must exclude archived rows here.
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

// The rendered header and preview counts must follow the archive partition the
// list itself uses: with one live and two archived sessions, the active view
// shows (1) and the ^ view shows (2); neither advertises the grand total of 3.
func TestRemoteHeaderCountsFollowArchiveView(t *testing.T) {
	home := NewHome()
	home.width = 120
	home.height = 40
	home.remoteSessions = map[string][]session.RemoteSessionInfo{
		"dev": {
			{ID: "a", Group: "work", Status: "running"},
			{ID: "b", Group: "work", Status: "running", Archived: true},
			{ID: "c", Group: "work/api", Status: "idle", Archived: true},
		},
	}
	host := session.Item{Type: session.ItemTypeRemoteGroup, RemoteName: "dev", Path: "remotes/dev", Level: 0}
	work := session.Item{Type: session.ItemTypeRemoteGroup, RemoteName: "dev", Path: "remotes/dev/work", Level: 1}

	render := func(item session.Item) string {
		var b strings.Builder
		home.renderRemoteGroupItem(&b, item, false)
		return b.String()
	}

	home.statusFilter = ""
	if out := render(host); !strings.Contains(out, "(1)") {
		t.Errorf("active view host header = %q, want count (1)", out)
	}
	if out := render(work); !strings.Contains(out, "(1)") {
		t.Errorf("active view sub-group header = %q, want count (1)", out)
	}
	if out := home.renderRemotePreview(host, 80, 20); !strings.Contains(out, "1 sessions") {
		t.Errorf("active view preview = %q, want \"1 sessions\"", out)
	}

	home.statusFilter = FilterModeArchived
	if out := render(host); !strings.Contains(out, "(2)") {
		t.Errorf("archived view host header = %q, want count (2)", out)
	}
	if out := render(work); !strings.Contains(out, "(2)") {
		t.Errorf("archived view sub-group header = %q, want count (2)", out)
	}
	if out := home.renderRemotePreview(host, 80, 20); !strings.Contains(out, "2 sessions") {
		t.Errorf("archived view preview = %q, want \"2 sessions\"", out)
	}
}
