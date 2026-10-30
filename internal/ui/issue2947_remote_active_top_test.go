package ui

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// #2947: "Active on top" (t) partitioned only the local rows. A remote's
// sessions stayed one alphabetical list per group, so the few live sessions
// of a large remote were lost among the stopped ones. These tests pin the
// same split inside each remote's own section.

func newActiveTopRemoteHome(mode session.GroupViewMode, opts session.ViewModeOptions, sessions []session.RemoteSessionInfo, collapsed map[string]bool) *Home {
	h := NewHome()
	h.storage = nil
	h.groupViewMode = mode
	h.viewModeOpts = opts
	h.remoteGroupsCollapsed = collapsed
	if h.remoteGroupsCollapsed == nil {
		h.remoteGroupsCollapsed = make(map[string]bool)
	}
	h.remoteSessions = map[string][]session.RemoteSessionInfo{"box": sessions}
	h.rebuildFlatItems()
	return h
}

// remoteRows renders the remote section as compact labels: H:<path> for a
// header, S:<id> for a session, "--" for a divider.
func remoteRows(items []session.Item) []string {
	var out []string
	inRemote := false
	for _, it := range items {
		switch it.Type {
		case session.ItemTypeRemoteGroup:
			inRemote = true
			out = append(out, "H:"+it.Path)
		case session.ItemTypeRemoteSession:
			out = append(out, "S:"+it.RemoteSession.ID)
		case session.ItemTypeDivider:
			if inRemote {
				out = append(out, "--")
			}
		}
	}
	return out
}

func activeTopFixture() []session.RemoteSessionInfo {
	return []session.RemoteSessionInfo{
		{ID: "a", Title: "a", Group: "work", Status: "stopped"},
		{ID: "b", Title: "b", Group: "work", Status: "running"},
		{ID: "c", Title: "c", Group: "work", Status: "idle"},
		{ID: "d", Title: "d", Group: "work", Status: "waiting"},
		{ID: "e", Title: "e", Group: "old", Status: "stopped"},
	}
}

func TestIssue2947_RemoteActiveOnTopSplitsGroups(t *testing.T) {
	h := newActiveTopRemoteHome(session.GroupViewActiveTop, session.ViewModeOptions{}, activeTopFixture(), nil)
	got := strings.Join(remoteRows(h.flatItems), " ")
	want := "H:remotes/box H:remotes/box/work S:b S:d -- H:remotes/box/old S:e H:remotes/box/work S:a S:c"
	if got != want {
		t.Fatalf("remote rows under Active on top:\n got  %s\n want %s", got, want)
	}
	// The connector flag follows the visible rows of each section.
	for _, it := range h.flatItems {
		if it.Type == session.ItemTypeRemoteSession {
			wantLast := it.RemoteSession.ID == "d" || it.RemoteSession.ID == "c" || it.RemoteSession.ID == "e"
			if it.IsLastInGroup != wantLast {
				t.Errorf("row %s IsLastInGroup=%v, want %v", it.RemoteSession.ID, it.IsLastInGroup, wantLast)
			}
		}
	}
}

func TestIssue2947_RemoteActiveOnTopHonoursActiveIncludesIdle(t *testing.T) {
	h := newActiveTopRemoteHome(session.GroupViewActiveTop, session.ViewModeOptions{ActiveIncludesIdle: true}, activeTopFixture(), nil)
	got := strings.Join(remoteRows(h.flatItems), " ")
	want := "H:remotes/box H:remotes/box/work S:b S:c S:d -- H:remotes/box/old S:e H:remotes/box/work S:a"
	if got != want {
		t.Fatalf("remote rows with active_includes_idle:\n got  %s\n want %s", got, want)
	}
}

func TestIssue2947_RemoteNormalModeUnchanged(t *testing.T) {
	h := newActiveTopRemoteHome(session.GroupViewNormal, session.ViewModeOptions{}, activeTopFixture(), nil)
	got := strings.Join(remoteRows(h.flatItems), " ")
	want := "H:remotes/box H:remotes/box/old S:e H:remotes/box/work S:a S:b S:c S:d"
	if got != want {
		t.Fatalf("Normal mode must not reorder remote rows:\n got  %s\n want %s", got, want)
	}
}

// A folded group shows once: on top when anything under it is live, below the
// divider otherwise. Its fold state is kept.
func TestIssue2947_RemoteActiveOnTopKeepsFoldState(t *testing.T) {
	collapsed := map[string]bool{"remotes/box/work": true, "remotes/box/old": true}
	h := newActiveTopRemoteHome(session.GroupViewActiveTop, session.ViewModeOptions{}, activeTopFixture(), collapsed)
	got := strings.Join(remoteRows(h.flatItems), " ")
	want := "H:remotes/box H:remotes/box/work -- H:remotes/box/old"
	if got != want {
		t.Fatalf("folded remote groups under Active on top:\n got  %s\n want %s", got, want)
	}
	if !h.remoteGroupsCollapsed["remotes/box/work"] || !h.remoteGroupsCollapsed["remotes/box/old"] {
		t.Fatal("fold state must be left as it was")
	}
}

// A conductor and its children move as one block, so a live child never
// renders orphaned away from its parent row.
func TestIssue2947_RemoteActiveOnTopKeepsChildWithParent(t *testing.T) {
	sessions := []session.RemoteSessionInfo{
		{ID: "p", Title: "p", Group: "work", Status: "stopped"},
		{ID: "k", Title: "k", Group: "work", Status: "running", ParentSessionID: "p"},
		{ID: "z", Title: "z", Group: "work", Status: "stopped"},
	}
	h := newActiveTopRemoteHome(session.GroupViewActiveTop, session.ViewModeOptions{}, sessions, nil)
	got := strings.Join(remoteRows(h.flatItems), " ")
	want := "H:remotes/box H:remotes/box/work S:p S:k -- H:remotes/box/work S:z"
	if got != want {
		t.Fatalf("parent/child block under Active on top:\n got  %s\n want %s", got, want)
	}
}

// All live or all stopped: nothing to split, so no divider (same rule as local).
func TestIssue2947_RemoteActiveOnTopOneSidedNoDivider(t *testing.T) {
	sessions := []session.RemoteSessionInfo{
		{ID: "a", Title: "a", Group: "work", Status: "stopped"},
		{ID: "b", Title: "b", Group: "work", Status: "error"},
	}
	h := newActiveTopRemoteHome(session.GroupViewActiveTop, session.ViewModeOptions{}, sessions, nil)
	got := strings.Join(remoteRows(h.flatItems), " ")
	want := "H:remotes/box H:remotes/box/work S:a S:b"
	if got != want {
		t.Fatalf("one-sided remote must stay unsplit:\n got  %s\n want %s", got, want)
	}
}

// The repeated header below the divider carries the same "(idle)" suffix a
// repeated local header gets.
func TestIssue2947_RemoteRepeatedHeaderSuffix(t *testing.T) {
	h := newActiveTopRemoteHome(session.GroupViewActiveTop, session.ViewModeOptions{}, activeTopFixture(), nil)
	h.width, h.height = 120, 40
	var headers []string
	for i, it := range h.flatItems {
		if it.Type == session.ItemTypeRemoteGroup && it.Path == "remotes/box/work" {
			var b strings.Builder
			h.renderItem(&b, it, false, i, nil, nil, 120)
			headers = append(headers, b.String())
		}
	}
	if len(headers) != 2 {
		t.Fatalf("want the work header twice, got %d", len(headers))
	}
	if strings.Contains(headers[0], "(idle)") {
		t.Errorf("top header must not carry the suffix: %q", headers[0])
	}
	if !strings.Contains(headers[1], "work (idle)") {
		t.Errorf("repeated header must read 'work (idle)': %q", headers[1])
	}
}
