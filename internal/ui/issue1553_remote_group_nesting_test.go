// Issue #1553 — Remote sessions ignore their Group and render as a flat
// Level-1 dump under the remote host header. RemoteSessionInfo.Group crosses
// the wire (internal/session/ssh.go) but the remote-append loop in
// rebuildFlatItems discarded it. These tests pin the nested-tree behavior
// added by buildRemoteFlatItems (internal/ui/remote_tree.go).

package ui

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// findRemoteItems returns only the remote portion of a flat item list.
func remoteItemsOf(items []session.Item) []session.Item {
	out := make([]session.Item, 0, len(items))
	for _, it := range items {
		if it.Type == session.ItemTypeRemoteGroup || it.Type == session.ItemTypeRemoteSession {
			out = append(out, it)
		}
	}
	return out
}

func TestIssue1553_RemoteSessionsNestUnderGroups(t *testing.T) {
	sessions := []session.RemoteSessionInfo{
		{ID: "a", Title: "api-1", Group: "work", Status: "running"},
		{ID: "b", Title: "api-2", Group: "work/api", Status: "idle"},
		{ID: "c", Title: "loose", Group: "", Status: "waiting"}, // -> my-sessions
	}

	items := buildRemoteFlatItems("dev", sessions)

	// Level-0 remote header comes first, path "remotes/dev".
	if len(items) == 0 || items[0].Type != session.ItemTypeRemoteGroup || items[0].Level != 0 || items[0].Path != "remotes/dev" {
		t.Fatalf("first item must be the Level-0 remote header, got %+v", items[0])
	}

	// Every session must appear under a group header whose Path encodes its
	// group — the pre-fix bug put them all at Level 1 with Path "remotes/dev".
	byID := map[string]session.Item{}
	headerPaths := map[string]bool{}
	for _, it := range items {
		switch it.Type {
		case session.ItemTypeRemoteGroup:
			headerPaths[it.Path] = true
		case session.ItemTypeRemoteSession:
			byID[it.RemoteSession.ID] = it
		}
	}

	// The ungrouped session must nest under the default group header.
	if !headerPaths["remotes/dev/my-sessions"] {
		t.Errorf("missing default-group header for ungrouped session; headers=%v", headerPaths)
	}
	if !headerPaths["remotes/dev/work"] || !headerPaths["remotes/dev/work/api"] {
		t.Errorf("missing group headers for work / work/api; headers=%v", headerPaths)
	}

	// Session levels must sit one below their owning group header.
	if got := byID["a"]; got.Level != 2 || got.Path != "remotes/dev/work" {
		t.Errorf("session a (group work) = level %d path %q, want level 2 path remotes/dev/work", got.Level, got.Path)
	}
	if got := byID["b"]; got.Level != 3 || got.Path != "remotes/dev/work/api" {
		t.Errorf("session b (group work/api) = level %d path %q, want level 3 path remotes/dev/work/api", got.Level, got.Path)
	}
	if got := byID["c"]; got.Level != 2 || got.Path != "remotes/dev/my-sessions" {
		t.Errorf("session c (ungrouped) = level %d path %q, want level 2 path remotes/dev/my-sessions", got.Level, got.Path)
	}
}

// TestIssue1553_RegressionGuard_NoFlatLevelOneDump pins the exact pre-fix
// failure: no remote session may be emitted flat at Level 1 with the bare
// "remotes/<name>" path.
func TestIssue1553_RegressionGuard_NoFlatLevelOneDump(t *testing.T) {
	sessions := []session.RemoteSessionInfo{
		{ID: "a", Title: "grouped", Group: "work"},
		{ID: "b", Title: "loose", Group: ""},
	}
	items := buildRemoteFlatItems("dev", sessions)
	for _, it := range items {
		if it.Type != session.ItemTypeRemoteSession {
			continue
		}
		if it.Level == 1 {
			t.Errorf("remote session %q emitted flat at Level 1 — the #1553 bug", it.RemoteSession.ID)
		}
		if it.Path == "remotes/dev" {
			t.Errorf("remote session %q kept the bare remote path (group discarded) — the #1553 bug", it.RemoteSession.ID)
		}
	}
}

// TestIssue1553_DeepPathEmitsIntermediateHeaders ensures a/b/c yields headers
// for a, a/b, a/b/c in that order at increasing levels.
func TestIssue1553_DeepPathEmitsIntermediateHeaders(t *testing.T) {
	sessions := []session.RemoteSessionInfo{
		{ID: "deep", Title: "deep", Group: "a/b/c"},
	}
	items := buildRemoteFlatItems("dev", sessions)

	want := []struct {
		path  string
		level int
	}{
		{"remotes/dev", 0},
		{"remotes/dev/a", 1},
		{"remotes/dev/a/b", 2},
		{"remotes/dev/a/b/c", 3},
	}
	headers := []session.Item{}
	for _, it := range items {
		if it.Type == session.ItemTypeRemoteGroup {
			headers = append(headers, it)
		}
	}
	if len(headers) != len(want) {
		t.Fatalf("got %d headers, want %d: %+v", len(headers), len(want), headers)
	}
	for i, w := range want {
		if headers[i].Path != w.path || headers[i].Level != w.level {
			t.Errorf("header[%d] = (%q, L%d), want (%q, L%d)", i, headers[i].Path, headers[i].Level, w.path, w.level)
		}
	}
	// The session sits one below a/b/c.
	for _, it := range items {
		if it.Type == session.ItemTypeRemoteSession && it.RemoteSession.ID == "deep" {
			if it.Level != 4 || it.Path != "remotes/dev/a/b/c" {
				t.Errorf("deep session = level %d path %q, want level 4 path remotes/dev/a/b/c", it.Level, it.Path)
			}
		}
	}
}

// TestIssue1553_IntegrationThroughRebuild drives the real rebuildFlatItems and
// confirms the nested headers reach h.flatItems, and the header renderer shows
// the sub-group segment name + subtree count.
func TestIssue1553_IntegrationThroughRebuild(t *testing.T) {
	home := NewHome()
	home.width = 100
	home.height = 40
	home.refreshSessionRenderSnapshot(nil)

	home.remoteSessionsMu.Lock()
	home.remoteSessions = map[string][]session.RemoteSessionInfo{
		"dev": {
			{ID: "a", Title: "api-1", Group: "work", Tool: "claude", Status: "running", RemoteName: "dev"},
			{ID: "b", Title: "api-2", Group: "work/api", Tool: "claude", Status: "idle", RemoteName: "dev"},
			{ID: "c", Title: "loose", Group: "", Tool: "claude", Status: "waiting", RemoteName: "dev"},
		},
	}
	home.remoteSessionsMu.Unlock()

	home.rebuildFlatItems()

	rem := remoteItemsOf(home.flatItems)
	if len(rem) == 0 {
		t.Fatal("no remote items in flatItems after rebuild")
	}
	// At least the remote header + 3 sub-group headers (my-sessions, work,
	// work/api) + 3 sessions.
	headers, remoteSessions := 0, 0
	var workHeader session.Item
	foundWork := false
	for _, it := range rem {
		if it.Type == session.ItemTypeRemoteGroup {
			headers++
			if it.Path == "remotes/dev/work" {
				workHeader = it
				foundWork = true
			}
		} else {
			remoteSessions++
		}
	}
	if headers < 4 {
		t.Errorf("headers = %d, want >= 4 (remote + my-sessions + work + work/api)", headers)
	}
	if remoteSessions != 3 {
		t.Errorf("remote sessions = %d, want 3", remoteSessions)
	}
	if !foundWork {
		t.Fatal("no remotes/dev/work sub-group header emitted")
	}

	// Render the sub-group header: it must show the segment name "work" and a
	// subtree count of 2 (work + work/api), indented, no host-latency marker.
	var b strings.Builder
	home.renderRemoteGroupItem(&b, workHeader, false)
	out := b.String()
	if !strings.Contains(out, "work") {
		t.Errorf("sub-group header render missing segment name: %q", out)
	}
	if !strings.Contains(out, "(2)") {
		t.Errorf("sub-group header render missing subtree count (2): %q", out)
	}
}
