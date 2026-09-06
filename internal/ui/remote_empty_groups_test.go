// Empty remote groups: a group created on a remote (or emptied by moves)
// exists only in the remote's `group list --json`, never on a session. The
// TUI used to build remote rows from sessions alone, so such a group vanished
// the moment it was created and the user could not see, collapse, or target
// it. These tests pin the header-only rows that
// buildRemoteFlatItemsWithEmptyGroups emits for them.

package ui

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func remoteHeaderPaths(items []session.Item) map[string]int {
	out := map[string]int{}
	for _, it := range items {
		if it.Type == session.ItemTypeRemoteGroup {
			out[it.Path] = it.Level
		}
	}
	return out
}

func TestRemoteEmptyGroups_ListedGroupWithoutSessionsGetsHeader(t *testing.T) {
	sessions := []session.RemoteSessionInfo{
		{ID: "a", Title: "api", Group: "agent-deck", Status: "running"},
	}
	groups := []string{"agent-deck", "semantic", "buddi", "buddi/test"}

	items := buildRemoteFlatItemsWithEmptyGroups("box", sessions, nil, nil, groups, true)
	headers := remoteHeaderPaths(items)

	for path, level := range map[string]int{
		"remotes/box/semantic":   1,
		"remotes/box/buddi":      1,
		"remotes/box/buddi/test": 2,
	} {
		got, ok := headers[path]
		if !ok {
			t.Errorf("empty group %q must get a header row; headers=%v", path, headers)
			continue
		}
		if got != level {
			t.Errorf("header %q level = %d, want %d", path, got, level)
		}
	}

	// Header-only groups contribute no session rows and do not disturb the
	// sessions of populated groups.
	sessionRows := 0
	for _, it := range items {
		if it.Type == session.ItemTypeRemoteSession {
			sessionRows++
			if it.Path != "remotes/box/agent-deck" {
				t.Errorf("session row landed under %q, want remotes/box/agent-deck", it.Path)
			}
		}
	}
	if sessionRows != 1 {
		t.Errorf("session rows = %d, want 1", sessionRows)
	}
}

func TestRemoteEmptyGroups_HiddenUnlessRequested(t *testing.T) {
	sessions := []session.RemoteSessionInfo{
		{ID: "a", Title: "api", Group: "agent-deck", Status: "running"},
	}
	groups := []string{"agent-deck", "semantic"}

	// Filtered views pass includeEmpty=false and must render exactly as before.
	items := buildRemoteFlatItemsWithEmptyGroups("box", sessions, nil, nil, groups, false)
	if _, ok := remoteHeaderPaths(items)["remotes/box/semantic"]; ok {
		t.Fatalf("empty group must stay hidden when includeEmpty is false; items=%+v", items)
	}
	// The plain WithGroups form is the filtered form.
	items = buildRemoteFlatItemsWithGroups("box", sessions, nil, nil, groups)
	if _, ok := remoteHeaderPaths(items)["remotes/box/semantic"]; ok {
		t.Fatalf("buildRemoteFlatItemsWithGroups must not emit empty groups; items=%+v", items)
	}
}

func TestRemoteEmptyGroups_CollapseAndOrderStillApply(t *testing.T) {
	sessions := []session.RemoteSessionInfo{
		{ID: "a", Title: "one", Group: "buddi/ashesh", Status: "idle"},
	}
	// Remote order: semantic first, then buddi. Both must appear in that order
	// even though semantic is empty, and a collapsed empty parent hides its
	// empty child.
	groups := []string{"semantic", "semantic/inner", "buddi", "buddi/ashesh"}
	collapsed := map[string]bool{"remotes/box/semantic": true}

	items := buildRemoteFlatItemsWithEmptyGroups("box", sessions, collapsed, nil, groups, true)
	headers := remoteHeaderPaths(items)

	if _, ok := headers["remotes/box/semantic/inner"]; ok {
		t.Errorf("child of a collapsed empty group must be hidden; headers=%v", headers)
	}
	if _, ok := headers["remotes/box/semantic"]; !ok {
		t.Errorf("collapsed empty group must keep its own header; headers=%v", headers)
	}

	var order []string
	for _, it := range items {
		if it.Type == session.ItemTypeRemoteGroup && it.Level == 1 {
			order = append(order, it.Path)
		}
	}
	want := []string{"remotes/box/semantic", "remotes/box/buddi"}
	if len(order) != len(want) {
		t.Fatalf("level-1 headers = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("level-1 header order = %v, want %v (remote order must win over name order)", order, want)
		}
	}
}

func TestRemoteEmptyGroups_NilListIsUnchanged(t *testing.T) {
	sessions := []session.RemoteSessionInfo{
		{ID: "a", Title: "api", Group: "work", Status: "running"},
	}
	with := buildRemoteFlatItemsWithEmptyGroups("box", sessions, nil, nil, nil, true)
	without := buildRemoteFlatItemsOrdered("box", sessions, nil, nil)
	if len(with) != len(without) {
		t.Fatalf("a remote without a group list must render as before: %d rows vs %d", len(with), len(without))
	}
	for i := range with {
		if with[i].Path != without[i].Path || with[i].Type != without[i].Type || with[i].Level != without[i].Level {
			t.Fatalf("row %d differs: %+v vs %+v", i, with[i], without[i])
		}
	}
}
