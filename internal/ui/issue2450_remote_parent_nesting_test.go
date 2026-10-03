// Issue #2450: on the controller's TUI, a remote session whose
// parent_session_id points at a conductor on the same remote renders flat, as
// a sibling of that conductor, while the remote's own TUI nests it.
// buildRemoteFlatItemsWithEmptyGroups bucketed rows by group path only and
// never looked at the parent link, and RemoteSessionInfo dropped the
// parent_session_id key that `list --json` sends.
//
// These tests pin the local-tree behaviour for remote rows: a child whose
// parent is in the same fetched group bucket sits directly under it, one
// level deeper (single level, like local sub-sessions); a child whose parent
// is not there renders flat exactly as before.

package ui

import (
	"encoding/json"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// issue2450Row is the part of a remote session row these tests assert on.
type issue2450Row struct {
	id    string
	level int
	sub   bool
}

func issue2450Rows(items []session.Item) []issue2450Row {
	var rows []issue2450Row
	for _, it := range items {
		if it.Type != session.ItemTypeRemoteSession || it.RemoteSession == nil {
			continue
		}
		rows = append(rows, issue2450Row{id: it.RemoteSession.ID, level: it.Level, sub: it.IsSubSession})
	}
	return rows
}

func issue2450RowIDs(items []session.Item) string {
	var ids []string
	for _, r := range issue2450Rows(items) {
		ids = append(ids, r.id)
	}
	return strings.Join(ids, ",")
}

func issue2450RowByID(t *testing.T, items []session.Item, id string) session.Item {
	t.Helper()
	for _, it := range items {
		if it.Type == session.ItemTypeRemoteSession && it.RemoteSession != nil && it.RemoteSession.ID == id {
			return it
		}
	}
	t.Fatalf("no remote session row %q; rows=%s", id, issue2450RowIDs(items))
	return session.Item{}
}

// The issue's own shape: a conductor and its child in tb500-prod/P4508, plus
// an unrelated session listed between them.
func TestIssue2450_RemoteChildNestsUnderConductor(t *testing.T) {
	const group = "tb500-prod/P4508"
	sessions := []session.RemoteSessionInfo{
		{ID: "56ee783b", Title: "conductor-p4508", Group: group, Status: "running"},
		{ID: "other", Title: "unrelated", Group: group, Status: "idle"},
		{ID: "child", Title: "TM-4739:TS2 Build", Group: group, Status: "running", ParentSessionID: "56ee783b"},
	}

	items := buildRemoteFlatItemsWithEmptyGroups("box", sessions, nil, nil, nil, false)

	if got, want := issue2450RowIDs(items), "56ee783b,child,other"; got != want {
		t.Fatalf("row order = %s, want %s (child directly under its conductor)", got, want)
	}

	parent := issue2450RowByID(t, items, "56ee783b")
	child := issue2450RowByID(t, items, "child")
	other := issue2450RowByID(t, items, "other")

	// The group sits at Level 2 (tb500-prod is 1), so its sessions are Level 3
	// and the conductor's child one deeper.
	if parent.Level != 3 || other.Level != 3 {
		t.Fatalf("top-level rows: conductor level %d, other level %d, want 3", parent.Level, other.Level)
	}
	if child.Level != parent.Level+1 {
		t.Fatalf("child level = %d, want %d (one below its conductor)", child.Level, parent.Level+1)
	}
	if !child.IsSubSession || !child.IsLastSubSession {
		t.Fatalf("child must be flagged as the conductor's last sub-session: %+v", child)
	}
	if parent.IsSubSession || other.IsSubSession {
		t.Fatalf("top-level rows must not be flagged as sub-sessions")
	}
	// The child stays in its group: same Path as the conductor, so collapse,
	// cursor restore and move-to-group keep addressing the same bucket.
	if child.Path != parent.Path || child.Path != "remotes/box/"+group {
		t.Fatalf("child path = %q, parent path = %q, want both remotes/box/%s", child.Path, parent.Path, group)
	}
	// Exactly one row closes the group, and it is the last one emitted.
	if parent.IsLastInGroup || child.IsLastInGroup || !other.IsLastInGroup {
		t.Fatalf("IsLastInGroup: conductor=%v child=%v other=%v, want false,false,true",
			parent.IsLastInGroup, child.IsLastInGroup, other.IsLastInGroup)
	}
}

// A child listed before its conductor still lands under it, and several
// children keep their listed order.
func TestIssue2450_ChildrenFollowParentInListedOrder(t *testing.T) {
	sessions := []session.RemoteSessionInfo{
		{ID: "c2", Group: "work", ParentSessionID: "cond"},
		{ID: "cond", Group: "work"},
		{ID: "c1", Group: "work", ParentSessionID: "cond"},
	}
	items := buildRemoteFlatItemsWithEmptyGroups("box", sessions, nil, nil, nil, false)

	want := []issue2450Row{{"cond", 2, false}, {"c2", 3, true}, {"c1", 3, true}}
	got := issue2450Rows(items)
	if len(got) != len(want) {
		t.Fatalf("rows = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rows = %+v, want %+v", got, want)
		}
	}
	if issue2450RowByID(t, items, "c2").IsLastSubSession || !issue2450RowByID(t, items, "c1").IsLastSubSession {
		t.Fatalf("only the last child may be IsLastSubSession")
	}
	if !issue2450RowByID(t, items, "c1").IsLastInGroup {
		t.Fatalf("the last child of the last top-level row closes the group")
	}
}

// Fallback: when the parent is not in the fetched bucket (archived and
// filtered out, in another group, or an older remote that never sends the
// key) the row renders exactly as it does today.
func TestIssue2450_MissingParentRendersFlat(t *testing.T) {
	cases := []struct {
		name     string
		sessions []session.RemoteSessionInfo
	}{
		{
			name: "parent not fetched",
			sessions: []session.RemoteSessionInfo{
				{ID: "a", Group: "work"},
				{ID: "orphan", Group: "work", ParentSessionID: "gone"},
				{ID: "b", Group: "work"},
			},
		},
		{
			name: "parent in another group",
			sessions: []session.RemoteSessionInfo{
				{ID: "cond", Group: "conductors"},
				{ID: "a", Group: "work"},
				{ID: "orphan", Group: "work", ParentSessionID: "cond"},
				{ID: "b", Group: "work"},
			},
		},
		{
			name: "remote without the field",
			sessions: []session.RemoteSessionInfo{
				{ID: "a", Group: "work"},
				{ID: "orphan", Group: "work"},
				{ID: "b", Group: "work"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			items := buildRemoteFlatItemsWithEmptyGroups("box", tc.sessions, nil, nil, nil, false)
			var work []issue2450Row
			for _, r := range issue2450Rows(items) {
				if r.id != "cond" {
					work = append(work, r)
				}
			}
			want := []issue2450Row{{"a", 2, false}, {"orphan", 2, false}, {"b", 2, false}}
			if len(work) != len(want) {
				t.Fatalf("rows = %+v, want %+v", work, want)
			}
			for i := range want {
				if work[i] != want[i] {
					t.Fatalf("rows = %+v, want %+v (flat, listed order)", work, want)
				}
			}
		})
	}
}

// Nesting is a single level, like local sub-sessions: a grandchild does not
// nest under a child, and a parent cycle never nests at all. Every fetched
// session is still emitted exactly once.
func TestIssue2450_SingleLevelAndCyclesStayFlat(t *testing.T) {
	sessions := []session.RemoteSessionInfo{
		{ID: "cond", Group: "work"},
		{ID: "child", Group: "work", ParentSessionID: "cond"},
		{ID: "grand", Group: "work", ParentSessionID: "child"},
		{ID: "x", Group: "work", ParentSessionID: "y"},
		{ID: "y", Group: "work", ParentSessionID: "x"},
		{ID: "self", Group: "work", ParentSessionID: "self"},
	}
	items := buildRemoteFlatItemsWithEmptyGroups("box", sessions, nil, nil, nil, false)

	want := []issue2450Row{
		{"cond", 2, false}, {"child", 3, true},
		{"grand", 2, false}, {"x", 2, false}, {"y", 2, false}, {"self", 2, false},
	}
	got := issue2450Rows(items)
	if len(got) != len(want) {
		t.Fatalf("rows = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rows = %+v, want %+v", got, want)
		}
	}
}

// Duplicate IDs make a parent link ambiguous; the bucket stays flat rather
// than doubling or dropping a row.
func TestIssue2450_DuplicateIDsStayFlat(t *testing.T) {
	sessions := []session.RemoteSessionInfo{
		{ID: "cond", Title: "one", Group: "work"},
		{ID: "cond", Title: "two", Group: "work"},
		{ID: "child", Group: "work", ParentSessionID: "cond"},
	}
	items := buildRemoteFlatItemsWithEmptyGroups("box", sessions, nil, nil, nil, false)
	if got := issue2450RowIDs(items); got != "cond,cond,child" {
		t.Fatalf("rows = %s, want cond,cond,child", got)
	}
	if issue2450RowByID(t, items, "child").IsSubSession {
		t.Fatalf("child of an ambiguous parent must stay flat")
	}
}

// Collapsing the group (or the whole remote) still hides the conductor and
// its children together.
func TestIssue2450_CollapseHidesParentAndChildren(t *testing.T) {
	sessions := []session.RemoteSessionInfo{
		{ID: "cond", Group: "work"},
		{ID: "child", Group: "work", ParentSessionID: "cond"},
		{ID: "loose", Group: "other"},
	}
	items := buildRemoteFlatItemsWithEmptyGroups("box", sessions, map[string]bool{"remotes/box/work": true}, nil, nil, false)
	if got := issue2450RowIDs(items); got != "loose" {
		t.Fatalf("collapsed group: rows = %s, want only loose", got)
	}
	items = buildRemoteFlatItemsWithEmptyGroups("box", sessions, map[string]bool{"remotes/box": true}, nil, nil, false)
	if got := issue2450RowIDs(items); got != "" {
		t.Fatalf("collapsed remote: rows = %s, want none", got)
	}
}

// The #1875 order overlay still decides the order: of top-level rows, and of
// a conductor's children among themselves.
func TestIssue2450_OrderOverlayHonoured(t *testing.T) {
	sessions := []session.RemoteSessionInfo{
		{ID: "cond", Group: "work"},
		{ID: "c1", Group: "work", ParentSessionID: "cond"},
		{ID: "c2", Group: "work", ParentSessionID: "cond"},
		{ID: "other", Group: "work"},
	}
	order := map[string][]string{"work": {"other", "c2", "cond", "c1"}}
	items := buildRemoteFlatItemsWithEmptyGroups("box", sessions, nil, order, nil, false)
	want := []issue2450Row{{"other", 2, false}, {"cond", 2, false}, {"c2", 3, true}, {"c1", 3, true}}
	got := issue2450Rows(items)
	if len(got) != len(want) {
		t.Fatalf("rows = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rows = %+v, want %+v", got, want)
		}
	}
}

// parent_session_id crosses the wire: `list --json` on the remote sends it,
// and the controller must keep it.
func TestIssue2450_ParentSessionIDDecodes(t *testing.T) {
	var got []session.RemoteSessionInfo
	if err := json.Unmarshal([]byte(`[
	  {"id":"cond","group":"g"},
	  {"id":"child","group":"g","parent_session_id":"cond"}
	]`), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ParentSessionID != "" || got[1].ParentSessionID != "cond" {
		t.Fatalf("decoded = %+v, want parent_session_id cond on the child only", got)
	}
}

// Rendering: the child row is indented one level (2 cols) deeper than its
// conductor, and the connectors read as a tree.
func TestIssue2450_ChildRowRendersIndentedUnderParent(t *testing.T) {
	h := NewHome()
	h.width, h.height = 120, 40
	sessions := []session.RemoteSessionInfo{
		{ID: "cond", Title: "conductor", Group: "work", Status: "running"},
		{ID: "c1", Title: "child-one", Group: "work", Status: "idle", ParentSessionID: "cond"},
		{ID: "c2", Title: "child-two", Group: "work", Status: "idle", ParentSessionID: "cond"},
		{ID: "other", Title: "other", Group: "work", Status: "idle"},
	}
	h.remoteSessions = map[string][]session.RemoteSessionInfo{"dev": sessions}
	items := buildRemoteFlatItemsWithEmptyGroups("dev", sessions, nil, nil, nil, false)

	render := func(id string) string {
		var b strings.Builder
		h.renderRemoteSessionItem(&b, issue2450RowByID(t, items, id), false)
		return strings.TrimRight(b.String(), "\n")
	}
	condLine, c1Line, c2Line, otherLine := render("cond"), render("c1"), render("c2"), render("other")

	if w := leadDisplayWidth(condLine, "├"); w != leftGutterWidth+4 {
		t.Fatalf("conductor indent = %d, want %d\n  line: %q", w, leftGutterWidth+4, condLine)
	}
	if w := leadDisplayWidth(c1Line, "├"); w != leftGutterWidth+6 {
		t.Fatalf("first child indent = %d, want %d (one level under its conductor)\n  line: %q", w, leftGutterWidth+6, c1Line)
	}
	if w := leadDisplayWidth(c2Line, "└"); w != leftGutterWidth+6 {
		t.Fatalf("last child must close its parent's subtree with └ at %d cols\n  line: %q", leftGutterWidth+6, c2Line)
	}
	if w := leadDisplayWidth(otherLine, "└"); w != leftGutterWidth+4 {
		t.Fatalf("last top-level row must close the group with └ at %d cols\n  line: %q", leftGutterWidth+4, otherLine)
	}
}

// Moves stay among siblings. With a conductor's children nested, swapping a
// child with whatever the flat bucket held next to it would often leave the
// screen unchanged, the silent no-op #1875 fixed.
func TestIssue2450_MoveStaysAmongSiblings(t *testing.T) {
	withTempAgentDeckHome(t, `
[remotes.dev]
host = "user@dev.example"
agent_deck_path = "/usr/local/bin/agent-deck"
`)
	h := NewHome()
	h.width, h.height = 160, 40
	h.initialLoading = false
	h.remoteSessions = map[string][]session.RemoteSessionInfo{
		"dev": {
			{ID: "cond", Title: "cond", Status: "idle", Group: "work"},
			{ID: "c1", Title: "c1", Status: "idle", Group: "work", ParentSessionID: "cond"},
			{ID: "other", Title: "other", Status: "idle", Group: "work"},
			{ID: "c2", Title: "c2", Status: "idle", Group: "work", ParentSessionID: "cond"},
		},
	}
	h.rebuildFlatItems()

	if got := strings.Join(visibleRemoteSessionIDs(h, "dev"), ","); got != "cond,c1,c2,other" {
		t.Fatalf("precondition: rows = %s, want cond,c1,c2,other", got)
	}

	// shift+down on the first child moves it below its sibling.
	putCursorOnRemoteSession(t, h, "c1")
	h.handleMainKey(tea.KeyMsg{Type: tea.KeyShiftDown})
	if got := strings.Join(visibleRemoteSessionIDs(h, "dev"), ","); got != "cond,c2,c1,other" {
		t.Fatalf("child move: rows = %s, want cond,c2,c1,other (err=%v)", got, h.err)
	}

	// Pressing again: it is already the last child, and must say so.
	h.handleMainKey(tea.KeyMsg{Type: tea.KeyShiftDown})
	if h.err == nil || !strings.Contains(h.err.Error(), "already last") {
		t.Fatalf("move past the last child must report it, err = %v", h.err)
	}
	if got := strings.Join(visibleRemoteSessionIDs(h, "dev"), ","); got != "cond,c2,c1,other" {
		t.Fatalf("refused move changed rows: %s", got)
	}

	// shift+down on the conductor jumps the whole subtree past the next
	// top-level row.
	putCursorOnRemoteSession(t, h, "cond")
	h.handleMainKey(tea.KeyMsg{Type: tea.KeyShiftDown})
	if got := strings.Join(visibleRemoteSessionIDs(h, "dev"), ","); got != "other,cond,c2,c1" {
		t.Fatalf("parent move: rows = %s, want other,cond,c2,c1 (err=%v)", got, h.err)
	}
}

// A child whose conductor is archived renders flat in the active view, and
// moves as the flat top-level row it is shown as.
func TestIssue2450_MoveChildOfHiddenParentAsTopLevel(t *testing.T) {
	withTempAgentDeckHome(t, `
[remotes.dev]
host = "user@dev.example"
agent_deck_path = "/usr/local/bin/agent-deck"
`)
	h := NewHome()
	h.width, h.height = 160, 40
	h.initialLoading = false
	h.remoteSessions = map[string][]session.RemoteSessionInfo{
		"dev": {
			{ID: "cond", Title: "cond", Status: "idle", Group: "work", Archived: true},
			{ID: "child", Title: "child", Status: "idle", Group: "work", ParentSessionID: "cond"},
			{ID: "other", Title: "other", Status: "idle", Group: "work"},
		},
	}
	h.rebuildFlatItems()

	if got := strings.Join(visibleRemoteSessionIDs(h, "dev"), ","); got != "child,other" {
		t.Fatalf("precondition: rows = %s, want child,other", got)
	}
	putCursorOnRemoteSession(t, h, "child")
	if h.flatItems[h.cursor].IsSubSession {
		t.Fatalf("child of an archived (hidden) conductor must render flat")
	}
	h.handleMainKey(tea.KeyMsg{Type: tea.KeyShiftDown})
	if got := strings.Join(visibleRemoteSessionIDs(h, "dev"), ","); got != "other,child" {
		t.Fatalf("move: rows = %s, want other,child (err=%v)", got, h.err)
	}
}
