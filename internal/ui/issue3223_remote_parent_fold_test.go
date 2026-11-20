package ui

// #3223: a remote parent row (a conductor on a remote, with its children
// nested under it since #2450) could not fold its child list. Tab, l and
// Right did nothing on it, and h/Left folded the whole remote group, taking
// the parent and its unrelated peers with it. A local parent folds just its
// children (#2631). These tests pin the same fold for remote parents, keyed
// per remote so a session ID two remotes share folds on one only.

import (
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// issue3223Sessions is one remote group holding a parent with two children
// and an unrelated peer.
func issue3223Sessions() []session.RemoteSessionInfo {
	return []session.RemoteSessionInfo{
		{ID: "p", Title: "conductor-demo", Group: "work", Status: "idle", Tool: "shell"},
		{ID: "c1", Title: "child-alpha", Group: "work", Status: "running", Tool: "shell", ParentSessionID: "p"},
		{ID: "c2", Title: "child-beta", Group: "work", Status: "idle", Tool: "shell", ParentSessionID: "p"},
		{ID: "q", Title: "independent-peer", Group: "work", Status: "idle", Tool: "shell"},
	}
}

// newIssue3223Home is a Home showing the given remotes. Storage is detached
// so no fold state saved by another test leaks in (see newCollapseTestHome).
func newIssue3223Home(t *testing.T, remotes map[string][]session.RemoteSessionInfo) *Home {
	t.Helper()
	h := NewHome()
	h.storage = nil
	h.remoteGroupsCollapsed = make(map[string]bool)
	h.remoteSessions = remotes
	h.rebuildFlatItems()
	return h
}

// issue3223RowIDs lists the remote session rows of one remote, in order.
func issue3223RowIDs(h *Home, remote string) string {
	var ids []string
	for _, it := range h.flatItems {
		if it.Type == session.ItemTypeRemoteSession && it.RemoteSession != nil && it.RemoteName == remote {
			ids = append(ids, it.RemoteSession.ID)
		}
	}
	return strings.Join(ids, ",")
}

func issue3223Select(t *testing.T, h *Home, remote, id string) {
	t.Helper()
	idx := h.remoteFlatItemIndex(remote, id)
	if idx < 0 {
		t.Fatalf("no row %s/%s; rows=%s", remote, id, issue3223RowIDs(h, remote))
	}
	h.cursor = idx
}

func issue3223AssertCursor(t *testing.T, h *Home, remote, id string) {
	t.Helper()
	if h.cursor < 0 || h.cursor >= len(h.flatItems) {
		t.Fatalf("cursor %d out of range", h.cursor)
	}
	it := h.flatItems[h.cursor]
	if it.Type != session.ItemTypeRemoteSession || it.RemoteSession == nil || it.RemoteName != remote || it.RemoteSession.ID != id {
		t.Fatalf("cursor should stay on %s/%s, sits on %+v", remote, id, it)
	}
}

var issue3223Keys = map[string]tea.KeyMsg{
	"tab":   {Type: tea.KeyTab},
	"l":     {Type: tea.KeyRunes, Runes: []rune{'l'}},
	"right": {Type: tea.KeyRight},
	"h":     {Type: tea.KeyRunes, Runes: []rune{'h'}},
	"left":  {Type: tea.KeyLeft},
}

// Every fold key folds only the selected remote parent's children, keeping
// the parent, its peer and the group header on screen, as a local parent
// does. Tab/l/Right toggle it open again; h/Left on an already-folded parent
// falls through to the group collapse, the local "collapse or go up" order.
func TestIssue3223_FoldKeysFoldOnlyRemoteParentChildren(t *testing.T) {
	for _, key := range []string{"tab", "l", "right", "h", "left"} {
		t.Run(key, func(t *testing.T) {
			h := newIssue3223Home(t, map[string][]session.RemoteSessionInfo{"lab": issue3223Sessions()})
			if got, want := issue3223RowIDs(h, "lab"), "p,c1,c2,q"; got != want {
				t.Fatalf("unfolded rows = %s, want %s", got, want)
			}
			issue3223Select(t, h, "lab", "p")

			h.handleMainKey(issue3223Keys[key])
			if got, want := issue3223RowIDs(h, "lab"), "p,q"; got != want {
				t.Fatalf("after %s on the parent rows = %s, want %s (children folded, parent and peer kept)", key, got, want)
			}
			if h.isRemoteGroupCollapsed("remotes/lab/work") || h.isRemoteGroupCollapsed("remotes/lab") {
				t.Fatalf("%s on a remote parent must not collapse its group: %v", key, h.remoteGroupsCollapsed)
			}
			issue3223AssertCursor(t, h, "lab", "p")

			// A remote refresh rebuilds the list; the fold and the cursor stay.
			h.rebuildFlatItemsPreservingSelection(h.captureSelectedItemIdentity())
			if got, want := issue3223RowIDs(h, "lab"), "p,q"; got != want {
				t.Fatalf("fold lost on rebuild: rows = %s, want %s", got, want)
			}
			issue3223AssertCursor(t, h, "lab", "p")

			h.handleMainKey(issue3223Keys[key])
			switch key {
			case "tab", "l", "right":
				if got, want := issue3223RowIDs(h, "lab"), "p,c1,c2,q"; got != want {
					t.Fatalf("second %s should unfold: rows = %s, want %s", key, got, want)
				}
				issue3223AssertCursor(t, h, "lab", "p")
			default:
				if !h.isRemoteGroupCollapsed("remotes/lab/work") {
					t.Fatalf("second %s on a folded parent should collapse its group", key)
				}
			}
		})
	}
}

// A child row keeps today's behaviour: it does not fold, and h/Left
// collapses its group as a local child row does.
func TestIssue3223_ChildRowDoesNotFold(t *testing.T) {
	h := newIssue3223Home(t, map[string][]session.RemoteSessionInfo{"lab": issue3223Sessions()})
	issue3223Select(t, h, "lab", "c1")
	h.handleMainKey(issue3223Keys["tab"])
	if got, want := issue3223RowIDs(h, "lab"), "p,c1,c2,q"; got != want {
		t.Fatalf("Tab on a child row changed the rows: %s, want %s", got, want)
	}
	h.handleMainKey(issue3223Keys["h"])
	if !h.isRemoteGroupCollapsed("remotes/lab/work") {
		t.Fatal("h on a child row should collapse its group")
	}
}

// Fold state is keyed per remote: the same parent ID on another remote keeps
// its children.
func TestIssue3223_FoldIsPerRemote(t *testing.T) {
	h := newIssue3223Home(t, map[string][]session.RemoteSessionInfo{
		"box": issue3223Sessions(),
		"lab": issue3223Sessions(),
	})
	issue3223Select(t, h, "lab", "p")
	h.handleMainKey(issue3223Keys["tab"])
	if got, want := issue3223RowIDs(h, "lab"), "p,q"; got != want {
		t.Fatalf("lab rows = %s, want %s", got, want)
	}
	if got, want := issue3223RowIDs(h, "box"), "p,c1,c2,q"; got != want {
		t.Fatalf("folding lab/p folded box/p too: box rows = %s, want %s", got, want)
	}
}

// The parent row carries the same ▾N/▸N child-fold badge as a local parent.
func TestIssue3223_FoldBadge(t *testing.T) {
	h := newIssue3223Home(t, map[string][]session.RemoteSessionInfo{"lab": issue3223Sessions()})
	h.width, h.height = 100, 30
	h.rebuildFlatItems()
	frame := stripAnsi(h.renderSessionList(100, 30))
	if !strings.Contains(frame, "conductor-demo") || !strings.Contains(frame, "▾2") {
		t.Fatalf("unfolded remote parent should carry ▾2:\n%s", frame)
	}
	if n := strings.Count(frame, "▾2") + strings.Count(frame, "▸"); n != 1 {
		t.Fatalf("only the parent row carries a fold badge, found %d:\n%s", n, frame)
	}

	issue3223Select(t, h, "lab", "p")
	h.handleMainKey(issue3223Keys["tab"])
	frame = stripAnsi(h.renderSessionList(100, 30))
	if !strings.Contains(frame, "▸2") {
		t.Fatalf("folded remote parent should carry ▸2:\n%s", frame)
	}
	if strings.Contains(frame, "child-alpha") || !strings.Contains(frame, "independent-peer") {
		t.Fatalf("folded frame should hide the children and keep the peer:\n%s", frame)
	}
}

// A fold hides children only while their parent row is drawn above them. A
// filter or the archive partition that leaves the parent out shows the
// children as top-level rows, so none is hidden with nothing to unfold.
func TestIssue3223_FoldOnlyWhileParentIsShown(t *testing.T) {
	t.Run("status filter hides the parent", func(t *testing.T) {
		h := newIssue3223Home(t, map[string][]session.RemoteSessionInfo{"lab": issue3223Sessions()})
		issue3223Select(t, h, "lab", "p")
		h.handleMainKey(issue3223Keys["tab"])
		h.statusFilter = session.StatusRunning
		h.rebuildFlatItems()
		if got, want := issue3223RowIDs(h, "lab"), "c1"; got != want {
			t.Fatalf("running filter rows = %s, want %s", got, want)
		}
	})
	t.Run("parent archived after the fold", func(t *testing.T) {
		sessions := issue3223Sessions()
		h := newIssue3223Home(t, map[string][]session.RemoteSessionInfo{"lab": sessions})
		issue3223Select(t, h, "lab", "p")
		h.handleMainKey(issue3223Keys["tab"])
		sessions[0].Archived = true
		h.rebuildFlatItems()
		if got, want := issue3223RowIDs(h, "lab"), "c1,c2,q"; got != want {
			t.Fatalf("active rows with the parent archived = %s, want %s", got, want)
		}
	})
}

// "Active on top" moves a parent and its children as one block; a folded
// parent with a live child sits on top with its children still folded.
func TestIssue3223_FoldInActiveOnTopView(t *testing.T) {
	h := newIssue3223Home(t, map[string][]session.RemoteSessionInfo{"lab": issue3223Sessions()})
	h.groupViewMode = session.GroupViewActiveTop
	h.rebuildFlatItems()
	issue3223Select(t, h, "lab", "p")
	h.handleMainKey(issue3223Keys["tab"])
	if got, want := issue3223RowIDs(h, "lab"), "p,q"; got != want {
		t.Fatalf("active-on-top rows = %s, want %s", got, want)
	}
	divider := -1
	for i, it := range h.flatItems {
		if it.Type == session.ItemTypeDivider {
			divider = i
		}
	}
	if divider < 0 || h.remoteFlatItemIndex("lab", "p") > divider || h.remoteFlatItemIndex("lab", "q") < divider {
		t.Fatalf("parent block should stay above the divider (%d) and the peer below: p=%d q=%d", divider, h.remoteFlatItemIndex("lab", "p"), h.remoteFlatItemIndex("lab", "q"))
	}
}

// A focus request for a child hidden by its parent's fold opens the fold,
// as a local reveal does.
func TestIssue3223_FocusRevealsFoldedChild(t *testing.T) {
	h := newIssue3223Home(t, map[string][]session.RemoteSessionInfo{"lab": issue3223Sessions()})
	issue3223Select(t, h, "lab", "p")
	h.handleMainKey(issue3223Keys["tab"])
	if !h.SelectRemoteSession("lab", "c2") {
		t.Fatal("SelectRemoteSession should reveal a child hidden by its parent's fold")
	}
	issue3223AssertCursor(t, h, "lab", "c2")
	if got, want := issue3223RowIDs(h, "lab"), "p,c1,c2,q"; got != want {
		t.Fatalf("rows after reveal = %s, want %s", got, want)
	}
}

// Flat sidebar mode lists every session by design, so a remote parent has no
// fold there, like a local one.
func TestIssue3223_NoFoldInFlatSidebar(t *testing.T) {
	h := newIssue3223Home(t, map[string][]session.RemoteSessionInfo{"lab": issue3223Sessions()})
	h.embeddedLayout = true
	h.sidebarMode = sidebarFlat
	h.rebuildFlatItems()
	issue3223Select(t, h, "lab", "p")
	h.handleMainKey(issue3223Keys["tab"])
	if got := issue3223RowIDs(h, "lab"); !strings.Contains(got, "c1") || !strings.Contains(got, "c2") {
		t.Fatalf("flat sidebar must not fold a remote parent: rows = %s", got)
	}
}

// The fold is a view preference of this machine: it survives a restart.
func TestIssue3223_FoldPersists(t *testing.T) {
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", t.TempDir())
	session.ClearUserConfigCache()
	t.Cleanup(func() { os.Setenv("HOME", origHome); session.ClearUserConfigCache() })

	storage, err := session.NewStorageWithProfile("_i3223fold")
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	t.Cleanup(func() { storage.Close() })
	remotes := func() map[string][]session.RemoteSessionInfo {
		return map[string][]session.RemoteSessionInfo{"box": issue3223Sessions(), "lab": issue3223Sessions()}
	}

	before := NewHome()
	before.storage = storage
	before.profile = "_i3223fold"
	before.storageWatcher = nil
	before.remoteSessions = remotes()
	before.rebuildFlatItems()
	issue3223Select(t, before, "lab", "p")
	before.handleMainKey(issue3223Keys["tab"])
	if err := before.saveUIStateErr(); err != nil {
		t.Fatalf("saveUIStateErr: %v", err)
	}

	after := NewHome()
	after.storage = storage
	after.profile = "_i3223fold"
	after.storageWatcher = nil
	after.loadUIState()
	after.remoteSessions = remotes()
	after.rebuildFlatItems()
	if got, want := issue3223RowIDs(after, "lab"), "p,q"; got != want {
		t.Fatalf("lab fold lost in roundtrip: rows = %s, want %s", got, want)
	}
	if got, want := issue3223RowIDs(after, "box"), "p,c1,c2,q"; got != want {
		t.Fatalf("box must stay unfolded after roundtrip: rows = %s, want %s", got, want)
	}
}
