// Issue #2156: shift+up/down on a remote group header used to be refused with
// "remote group rows are ordered by name". The remote keeps a persisted group
// order of its own (`group reorder`, reflected by `group list --json`), so the
// TUI now renders remote group headers in that order and forwards the
// keystroke as `group reorder <path> --up|--down`, exactly what the remote's
// own TUI runs for the same key.
package ui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// visibleRemoteGroupPaths reads the on-screen order of one remote's group
// headers (host header excluded) straight out of the flat item list.
func visibleRemoteGroupPaths(items []session.Item, remoteName string) []string {
	var paths []string
	for _, it := range items {
		if it.Type != session.ItemTypeRemoteGroup || it.RemoteName != remoteName {
			continue
		}
		if p := remoteGroupPathFromItem(it); p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}

func putCursorOnRemoteGroup(t *testing.T, h *Home, remoteName, groupPath string) {
	t.Helper()
	for i, it := range h.flatItems {
		if it.Type == session.ItemTypeRemoteGroup && it.RemoteName == remoteName && remoteGroupPathFromItem(it) == groupPath {
			h.cursor = i
			return
		}
	}
	t.Fatalf("no remote group header %q on %s; headers=%v", groupPath, remoteName, visibleRemoteGroupPaths(h.flatItems, remoteName))
}

var groupOrderSessions = []session.RemoteSessionInfo{
	{ID: "s1", Title: "one", Group: "archive"},
	{ID: "s2", Title: "two", Group: "work/alpha"},
	{ID: "s3", Title: "three", Group: "work/zeta"},
	{ID: "s4", Title: "four", Group: "work"},
	{ID: "s5", Title: "five", Group: ""},
}

// Headers follow the remote's own order where it is known; a parent still
// precedes its descendants, and groups the remote did not list (here the
// default bucket of an ungrouped session) fall back to name order after the
// ranked ones.
func TestRemoteGroupHeadersFollowRemoteOrder(t *testing.T) {
	remoteList := []string{"work", "work/zeta", "work/alpha", "archive"}
	items := buildRemoteFlatItemsWithGroups("dev", groupOrderSessions, nil, nil, remoteList)
	want := "work,work/zeta,work/alpha,archive," + session.DefaultGroupPath
	if got := strings.Join(visibleRemoteGroupPaths(items, "dev"), ","); got != want {
		t.Fatalf("headers = %s, want %s", got, want)
	}

	// Sessions still sit directly under their own header.
	var seq []string
	for _, it := range items {
		switch it.Type {
		case session.ItemTypeRemoteGroup:
			seq = append(seq, "G:"+remoteGroupPathFromItem(it))
		case session.ItemTypeRemoteSession:
			seq = append(seq, it.RemoteSession.ID)
		}
	}
	wantSeq := "G:,G:work,s4,G:work/zeta,s3,G:work/alpha,s2,G:archive,s1,G:" + session.DefaultGroupPath + ",s5"
	if got := strings.Join(seq, ","); got != wantSeq {
		t.Fatalf("rows = %s, want %s", got, wantSeq)
	}
}

// Nothing changes for a remote that reports no group list (older builds):
// name order, exactly as before.
func TestRemoteGroupHeadersDefaultToNameOrder(t *testing.T) {
	want := "archive," + session.DefaultGroupPath + ",work,work/alpha,work/zeta"
	for _, list := range [][]string{nil, {}} {
		items := buildRemoteFlatItemsWithGroups("dev", groupOrderSessions, nil, nil, list)
		if got := strings.Join(visibleRemoteGroupPaths(items, "dev"), ","); got != want {
			t.Fatalf("headers with list %v = %s, want %s", list, got, want)
		}
	}
	// The pre-existing entry point is the nil-list case.
	items := buildRemoteFlatItemsOrdered("dev", groupOrderSessions, nil, nil)
	if got := strings.Join(visibleRemoteGroupPaths(items, "dev"), ","); got != want {
		t.Fatalf("buildRemoteFlatItemsOrdered headers = %s, want %s", got, want)
	}
}

// A collapsed header still hides its subtree under the remote's order.
func TestRemoteGroupOrderRespectsCollapse(t *testing.T) {
	collapsed := map[string]bool{"remotes/dev/work": true}
	items := buildRemoteFlatItemsWithGroups("dev", groupOrderSessions, collapsed, nil, []string{"work", "work/zeta", "work/alpha", "archive"})
	if got := strings.Join(visibleRemoteGroupPaths(items, "dev"), ","); got != "work,archive,"+session.DefaultGroupPath {
		t.Fatalf("headers = %s", got)
	}
}

func TestSwapRemoteGroupSibling(t *testing.T) {
	list := []string{"work", "work/zeta", "work/alpha", "archive", "misc"}
	if got := strings.Join(swapRemoteGroupSibling(list, "archive", -1), ","); got != "archive,work/zeta,work/alpha,work,misc" {
		t.Fatalf("archive up = %s", got)
	}
	if got := strings.Join(swapRemoteGroupSibling(list, "work/alpha", -1), ","); got != "work,work/alpha,work/zeta,archive,misc" {
		t.Fatalf("work/alpha up = %s", got)
	}
	if got := strings.Join(swapRemoteGroupSibling(list, "misc", 1), ","); got != strings.Join(list, ",") {
		t.Fatalf("misc down (last) = %s, want unchanged", got)
	}
	if got := strings.Join(swapRemoteGroupSibling(list, "nope", 1), ","); got != strings.Join(list, ",") {
		t.Fatalf("unknown path = %s, want unchanged", got)
	}
	if len(list) != 5 || list[0] != "work" || list[3] != "archive" {
		t.Fatalf("input mutated: %v", list)
	}
}

func armHomeForRemoteGroupOrder(t *testing.T) *Home {
	t.Helper()
	withTempAgentDeckHome(t, `
[remotes.dev]
host = "user@dev.example"
agent_deck_path = "/usr/local/bin/agent-deck"
`)
	h := NewHome()
	h.width, h.height = 160, 40
	h.initialLoading = false
	h.remoteSessions = map[string][]session.RemoteSessionInfo{"dev": groupOrderSessions}
	h.remoteGroups = map[string][]string{"dev": {"work", "work/zeta", "work/alpha", "archive"}}
	h.rebuildFlatItems()
	return h
}

// The key handler forwards a movable group over SSH (a command comes back and
// nothing is announced yet), refuses an edge move locally with a plain
// message, and never says "ordered by name" any more.
func TestShiftUpOnRemoteGroupForwardsOrRefusesHonestly(t *testing.T) {
	h := armHomeForRemoteGroupOrder(t)

	putCursorOnRemoteGroup(t, h, "dev", "archive")
	h.clearError()
	if cmd := h.moveRemoteItem(h.flatItems[h.cursor], -1); cmd == nil {
		t.Fatal("moving a group with a sibling above it returned no command")
	}
	if h.err != nil {
		t.Fatalf("forwarded move set an error before the remote answered: %v", h.err)
	}

	putCursorOnRemoteGroup(t, h, "dev", "work")
	h.clearError()
	if cmd := h.moveRemoteItem(h.flatItems[h.cursor], -1); cmd != nil {
		t.Fatal("the first group returned a command instead of refusing locally")
	}
	if h.err == nil || !strings.Contains(h.err.Error(), "already first") {
		t.Fatalf("first group up: err = %v, want 'already first'", h.err)
	}
	if strings.Contains(strings.ToLower(h.err.Error()), "ordered by name") {
		t.Fatalf("old refusal still present: %v", h.err)
	}

	putCursorOnRemoteGroup(t, h, "dev", "work/alpha")
	h.clearError()
	if cmd := h.moveRemoteItem(h.flatItems[h.cursor], 1); cmd != nil {
		t.Fatal("the last subgroup returned a command instead of refusing locally")
	}
	if h.err == nil || !strings.Contains(h.err.Error(), "already last") {
		t.Fatalf("last subgroup down: err = %v, want 'already last'", h.err)
	}

	// The host header is not a remote group.
	for i, it := range h.flatItems {
		if it.Type == session.ItemTypeRemoteGroup && it.RemoteName == "dev" && remoteGroupPathFromItem(it) == "" {
			h.cursor = i
		}
	}
	h.clearError()
	if cmd := h.moveRemoteItem(h.flatItems[h.cursor], 1); cmd != nil {
		t.Fatal("host header returned a command")
	}
	if h.err == nil || !strings.Contains(h.err.Error(), "config.toml") {
		t.Fatalf("host header: err = %v", h.err)
	}

	// Through the real key dispatch, the command is returned to Bubble Tea.
	putCursorOnRemoteGroup(t, h, "dev", "archive")
	h.clearError()
	_, cmd := h.handleMainKey(tea.KeyMsg{Type: tea.KeyShiftUp})
	if cmd == nil {
		t.Fatal("shift+up on a movable remote group header returned no command")
	}
}

// Once the remote confirms, the cached group list is patched the way the
// remote patched its own, the header moves and the cursor stays on it. A
// refused edge move and an SSH failure both surface in the footer instead.
func TestRemoteGroupReorderResultUpdatesHeaders(t *testing.T) {
	h := armHomeForRemoteGroupOrder(t)
	putCursorOnRemoteGroup(t, h, "dev", "archive")

	model, _ := h.Update(remoteGroupReorderResultMsg{remoteName: "dev", groupPath: "archive", delta: -1, moved: true})
	h = model.(*Home)
	if h.err != nil {
		t.Fatalf("confirmed move set an error: %v", h.err)
	}
	if got := strings.Join(visibleRemoteGroupPaths(h.flatItems, "dev"), ","); got != "archive,work,work/zeta,work/alpha,"+session.DefaultGroupPath {
		t.Fatalf("headers after confirmed move = %s", got)
	}
	if it := h.flatItems[h.cursor]; it.Type != session.ItemTypeRemoteGroup || remoteGroupPathFromItem(it) != "archive" {
		t.Fatalf("cursor left the moved header: %+v", it)
	}
	h.remoteSessionsMu.RLock()
	cached := strings.Join(h.remoteGroups["dev"], ",")
	h.remoteSessionsMu.RUnlock()
	if cached != "archive,work/zeta,work/alpha,work" {
		t.Fatalf("cached remote group list = %s", cached)
	}

	model, _ = h.Update(remoteGroupReorderResultMsg{remoteName: "dev", groupPath: "archive", delta: -1, moved: false})
	h = model.(*Home)
	if h.err == nil || !strings.Contains(h.err.Error(), "did not move") {
		t.Fatalf("refused edge move: err = %v", h.err)
	}

	model, _ = h.Update(remoteGroupReorderResultMsg{remoteName: "dev", groupPath: "archive", delta: 1, err: errors.New("ssh: boom")})
	h = model.(*Home)
	if h.err == nil || !strings.Contains(h.err.Error(), "boom") {
		t.Fatalf("ssh failure: err = %v", h.err)
	}
	if got := strings.Join(visibleRemoteGroupPaths(h.flatItems, "dev"), ","); !strings.HasPrefix(got, "archive,work") {
		t.Fatalf("a failed move changed the headers: %s", got)
	}
}

// A fresh fleet poll replaces the cached list with the remote's order, and
// the headers follow it; a remote that created a group appends it last.
func TestRemoteGroupOrderFromFleetPoll(t *testing.T) {
	h := armHomeForRemoteGroupOrder(t)
	model, _ := h.Update(remoteSessionsFetchedMsg{
		sessions: map[string][]session.RemoteSessionInfo{"dev": groupOrderSessions},
		groups:   map[string][]string{"dev": {"archive", "work", "work/alpha", "work/zeta"}},
	})
	h = model.(*Home)
	if got := strings.Join(visibleRemoteGroupPaths(h.flatItems, "dev"), ","); got != "archive,work,work/alpha,work/zeta,"+session.DefaultGroupPath {
		t.Fatalf("headers after poll = %s", got)
	}
	model, _ = h.Update(remoteGroupResultMsg{remoteName: "dev", groupPath: "aaa"})
	h = model.(*Home)
	h.remoteSessionsMu.RLock()
	cached := strings.Join(h.remoteGroups["dev"], ",")
	h.remoteSessionsMu.RUnlock()
	if cached != "archive,work,work/alpha,work/zeta,aaa" {
		t.Fatalf("cached list after create = %s, want the new group last", cached)
	}
}
