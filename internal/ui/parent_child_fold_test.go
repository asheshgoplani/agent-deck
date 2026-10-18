package ui

// Tests for the parent-child fold (#2631): FlattenWithParentFolds covers the
// session-package side; these exercise the home-side wiring the maintainer
// review flagged: fold-state persistence in uiState, badge/fold rendering in
// the list, and the flat-sidebar opt-out.

import (
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func makeFoldTestHome() (*Home, *session.Instance, *session.Instance) {
	h := seamBNewHome()
	h.parentChildrenCollapsed = make(map[string]bool)
	parent := session.NewInstanceWithTool("parent", "/tmp/parent", "claude")
	parent.ID = "parent"
	parent.GroupPath = "alpha"
	parent.Status = session.StatusIdle
	child := session.NewInstanceWithTool("child", "/tmp/child", "claude")
	child.ID = "child"
	child.GroupPath = "alpha"
	child.Status = session.StatusIdle
	child.SetParent(parent.ID)
	h.groupTree = session.NewGroupTree([]*session.Instance{parent, child})
	return h, parent, child
}

func flatSessionTitles(h *Home) []string {
	titles := make([]string, 0, len(h.flatItems))
	for _, item := range h.flatItems {
		if item.Type == session.ItemTypeSession && item.Session != nil {
			titles = append(titles, item.Session.Title)
		}
	}
	return titles
}

func TestParentFoldHidesChildrenInList(t *testing.T) {
	h, _, _ := makeFoldTestHome()
	h.rebuildFlatItems()
	if got := strings.Join(flatSessionTitles(h), ","); got != "parent,child" {
		t.Fatalf("unfolded list = %s, want parent,child", got)
	}

	h.parentChildrenCollapsed["parent"] = true
	h.rebuildFlatItems()
	if got := strings.Join(flatSessionTitles(h), ","); got != "parent" {
		t.Fatalf("folded list = %s, want parent", got)
	}
}

func TestParentFoldBadgeRendersState(t *testing.T) {
	h, _, _ := makeFoldTestHome()
	h.width, h.height = 80, 24
	h.rebuildFlatItems()

	unfolded := stripAnsi(h.renderSessionList(80, 24))
	if !strings.Contains(unfolded, "▾1") {
		t.Fatalf("unfolded frame should carry the ▾1 badge, got:\n%s", unfolded)
	}

	h.parentChildrenCollapsed["parent"] = true
	h.rebuildFlatItems()
	folded := stripAnsi(h.renderSessionList(80, 24))
	if !strings.Contains(folded, "▸1") {
		t.Fatalf("folded frame should carry the ▸1 badge, got:\n%s", folded)
	}
	if strings.Contains(folded, "child") {
		t.Fatalf("folded frame must not render the child row, got:\n%s", folded)
	}
}

func TestParentFoldStateRoundTripsThroughUIState(t *testing.T) {
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", t.TempDir())
	session.ClearUserConfigCache()
	t.Cleanup(func() { os.Setenv("HOME", origHome); session.ClearUserConfigCache() })

	storage, err := session.NewStorageWithProfile("_i2631fold")
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	t.Cleanup(func() { storage.Close() })

	before := NewHome()
	before.storage = storage
	before.profile = "_i2631fold"
	before.storageWatcher = nil
	before.parentChildrenCollapsed["parent"] = true
	before.parentChildrenCollapsed["ghost"] = true
	before.parentChildrenCollapsed["reopened"] = false
	if err := before.saveUIStateErr(); err != nil {
		t.Fatalf("saveUIStateErr: %v", err)
	}

	// A fresh process: new Home, same on-disk state.
	after := NewHome()
	after.storage = storage
	after.profile = "_i2631fold"
	after.storageWatcher = nil
	after.loadUIState()
	if !after.parentChildrenCollapsed["parent"] || !after.parentChildrenCollapsed["ghost"] {
		t.Fatalf("fold state lost in roundtrip: %v", after.parentChildrenCollapsed)
	}
	if after.parentChildrenCollapsed["reopened"] {
		t.Fatalf("an unfolded parent must not come back folded: %v", after.parentChildrenCollapsed)
	}
}

// A fresh install has no ui_state row, so loadUIState returns before it
// allocates the fold map; the first Tab on a parent row must not write to a
// nil map.
func TestParentFoldToggleOnFreshHomeDoesNotPanic(t *testing.T) {
	h := NewHome()
	h.storage = nil
	parent := session.NewInstanceWithTool("parent", "/tmp/parent", "claude")
	parent.ID = "parent"
	parent.GroupPath = "alpha"
	child := session.NewInstanceWithTool("child", "/tmp/child", "claude")
	child.ID = "child"
	child.GroupPath = "alpha"
	child.SetParent(parent.ID)
	h.groupTree = session.NewGroupTree([]*session.Instance{parent, child})
	h.rebuildFlatItems()
	h.moveCursorToSession("parent")

	h.handleMainKey(tea.KeyMsg{Type: tea.KeyTab})
	if got := strings.Join(flatSessionTitles(h), ","); got != "parent" {
		t.Fatalf("after Tab = %s, want parent", got)
	}
	h.handleMainKey(tea.KeyMsg{Type: tea.KeyTab})
	if got := strings.Join(flatSessionTitles(h), ","); got != "parent,child" {
		t.Fatalf("after second Tab = %s, want parent,child", got)
	}
}

// Unfolding a parent reopens its window rows: with children present Tab
// never reaches the window toggle, so windows folded by h/left would
// otherwise stay shut for the rest of the run.
func TestParentUnfoldReopensWindows(t *testing.T) {
	h, _, _ := makeFoldTestHome()
	h.parentChildrenCollapsed["parent"] = true
	h.windowsCollapsed["parent"] = true
	h.rebuildFlatItems()
	h.moveCursorToSession("parent")

	h.handleMainKey(tea.KeyMsg{Type: tea.KeyTab})
	if h.parentChildrenCollapsed["parent"] {
		t.Fatal("Tab on a folded parent should unfold it")
	}
	if h.windowsCollapsed["parent"] {
		t.Fatal("unfolding a parent should reopen its window rows")
	}
}

// Reveal paths must open a folded parent the way they expand a collapsed
// group, or the cursor never reaches the hidden child.
func TestSelectSessionByIDUnfoldsParent(t *testing.T) {
	h, parent, child := makeFoldTestHome()
	h.instances = []*session.Instance{parent, child}
	h.parentChildrenCollapsed["parent"] = true
	h.rebuildFlatItems()

	if !h.SelectSessionByID("child") {
		t.Fatal("SelectSessionByID should reveal a child hidden by a parent fold")
	}
	if h.parentChildrenCollapsed["parent"] {
		t.Fatal("parent fold should be opened by the reveal")
	}
	if it := h.flatItems[h.cursor]; it.Session == nil || it.Session.ID != "child" {
		t.Fatalf("cursor not on child: %+v", it)
	}
}

func TestParentFoldOffInFlatSidebarMode(t *testing.T) {
	h, _, _ := makeFoldTestHome()
	h.embeddedLayout = true
	h.sidebarMode = sidebarFlat
	if h.sessionHasChildren(session.Item{Type: session.ItemTypeSession, Session: h.groupTree.GroupList[0].Sessions[0], Path: "alpha"}) {
		t.Fatal("flat sidebar mode must disable parent-child folds")
	}
}

// The `/`-search Enter path must compose the parent-fold reveal (#2631) with
// the filter-clearing jump from #2655: a child that is both folded under its
// parent and hidden by the active status filter still gets the cursor.
func TestSearchJumpUnfoldsParentAndClearsFilter(t *testing.T) {
	h, parent, child := makeFoldTestHome()
	h.activeFilterExcludes = (session.DisplaySettings{}).GetActiveFilterExcludes()
	parent.Status = session.StatusRunning
	child.Status = session.StatusStopped
	h.instances = []*session.Instance{parent, child}
	h.parentChildrenCollapsed["parent"] = true
	h.statusFilter = FilterModeActive
	h.rebuildFlatItems()
	for _, title := range flatSessionTitles(h) {
		if title == "child" {
			t.Fatal("test setup: child should be hidden by fold and filter")
		}
	}

	h.search.SetItems([]*session.Instance{child})
	h.search.Show()
	h.handleSearchKey(tea.KeyMsg{Type: tea.KeyEnter})

	if h.parentChildrenCollapsed["parent"] {
		t.Fatal("search jump should open the parent fold")
	}
	if h.statusFilter != "" {
		t.Fatalf("search jump should clear the hiding filter, got %q", h.statusFilter)
	}
	if got := cursorSessionID(h); got != "child" {
		t.Fatalf("cursor landed on %q, want child", got)
	}
}

// The deck renders one archive partition at a time (#1987), so the fold
// badge and the fold keys must count only children in the partition on
// screen. A parent whose only child is archived shows no badge in the
// active view, and Tab falls past the fold branch (toward the window
// toggle) instead of flipping a fold that hides nothing.
func TestParentFoldIgnoresChildrenInOtherArchivePartition(t *testing.T) {
	h, parent, child := makeFoldTestHome()
	child.ArchivedAt = time.Now()
	h.instances = []*session.Instance{parent, child}
	h.width, h.height = 80, 24
	h.rebuildFlatItems()

	if got := strings.Join(flatSessionTitles(h), ","); got != "parent" {
		t.Fatalf("active view list = %s, want parent", got)
	}
	parentItem := h.flatItems[0]
	for _, it := range h.flatItems {
		if it.Type == session.ItemTypeSession && it.Session != nil && it.Session.ID == "parent" {
			parentItem = it
		}
	}
	if h.sessionHasChildren(parentItem) {
		t.Fatal("a parent whose only child is archived must not count as foldable in the active view")
	}
	frame := stripAnsi(h.renderSessionList(80, 24))
	if strings.Contains(frame, "▾1") || strings.Contains(frame, "▸1") {
		t.Fatalf("active view must not show a fold badge for an archived-only child, got:\n%s", frame)
	}

	h.moveCursorToSession("parent")
	h.handleMainKey(tea.KeyMsg{Type: tea.KeyTab})
	if h.parentChildrenCollapsed["parent"] {
		t.Fatal("Tab must not toggle a fold over children that are not on screen")
	}

	// In the ^ archived view the archived child is on screen, so an
	// archived parent counts it there.
	parent.ArchivedAt = time.Now()
	h.statusFilter = FilterModeArchived
	h.rebuildFlatItems()
	for _, it := range h.flatItems {
		if it.Type == session.ItemTypeSession && it.Session != nil && it.Session.ID == "parent" {
			if got := h.sessionChildCount(it); got != 1 {
				t.Fatalf("archived view child count = %d, want 1", got)
			}
		}
	}
}

// A fold only applies in the archive partition where its parent row is
// drawn. Flatten runs before the active/archived partition, so a fold on a
// parent that was archived afterwards must not hide its still-active
// children from the active view, where no parent row or badge exists to
// unfold them.
func TestParentFoldOnArchivedParentKeepsActiveChildrenVisible(t *testing.T) {
	h, parent, child := makeFoldTestHome()
	h.instances = []*session.Instance{parent, child}
	h.parentChildrenCollapsed["parent"] = true
	parent.ArchivedAt = time.Now()
	h.rebuildFlatItems()

	if got := strings.Join(flatSessionTitles(h), ","); got != "child" {
		t.Fatalf("active view list = %s, want child (fold belongs to the archived parent)", got)
	}

	h.statusFilter = FilterModeArchived
	h.rebuildFlatItems()
	if got := strings.Join(flatSessionTitles(h), ","); got != "parent" {
		t.Fatalf("archived view list = %s, want parent", got)
	}
}

// The mirror case: a fold on an active parent must not drop that parent's
// archived children from the ^ archived view, where the active parent row
// is not drawn.
func TestParentFoldOnActiveParentKeepsArchivedChildrenVisible(t *testing.T) {
	h, parent, child := makeFoldTestHome()
	h.instances = []*session.Instance{parent, child}
	h.parentChildrenCollapsed["parent"] = true
	child.ArchivedAt = time.Now()

	h.statusFilter = FilterModeArchived
	h.rebuildFlatItems()
	if got := strings.Join(flatSessionTitles(h), ","); got != "child" {
		t.Fatalf("archived view list = %s, want child (fold belongs to the active parent)", got)
	}

	h.statusFilter = ""
	h.rebuildFlatItems()
	if got := strings.Join(flatSessionTitles(h), ","); got != "parent" {
		t.Fatalf("active view list = %s, want parent", got)
	}
}

// A fold only hides children while its parent row is drawn. The status
// filter runs after Flatten, so a folded idle parent with a running child
// must not take the child with it when the "running" filter drops the
// parent row: the group header would stay with nothing under it and no
// row to unfold.
func TestParentFoldIgnoredWhenStatusFilterHidesParent(t *testing.T) {
	h, parent, child := makeFoldTestHome()
	parent.Status = session.StatusIdle
	child.Status = session.StatusRunning
	h.instances = []*session.Instance{parent, child}
	h.parentChildrenCollapsed["parent"] = true

	h.statusFilter = session.StatusRunning
	h.rebuildFlatItems()
	if got := strings.Join(flatSessionTitles(h), ","); got != "child" {
		t.Fatalf("running filter list = %s, want child (parent row is filtered out)", got)
	}

	// When the parent itself passes the filter, its fold applies again.
	parent.Status = session.StatusRunning
	h.rebuildFlatItems()
	if got := strings.Join(flatSessionTitles(h), ","); got != "parent" {
		t.Fatalf("running filter list with running parent = %s, want parent", got)
	}
}

// The same for the time filter: an old folded parent with a recent child.
func TestParentFoldIgnoredWhenTimeFilterHidesParent(t *testing.T) {
	h, parent, child := makeFoldTestHome()
	now := time.Now()
	parent.CreatedAt = now.AddDate(0, 0, -30)
	parent.LastAccessedAt = now.AddDate(0, 0, -30)
	child.LastAccessedAt = now
	h.instances = []*session.Instance{parent, child}
	h.parentChildrenCollapsed["parent"] = true

	h.timeFilter = session.TimeFilterToday
	h.rebuildFlatItemsAt(now)
	if got := strings.Join(flatSessionTitles(h), ","); got != "child" {
		t.Fatalf("today filter list = %s, want child (parent row is filtered out)", got)
	}
}

// If no row matches a status filter, the filter clears and the full list
// shows. A fold that the filter had suspended must apply again then, since
// the parent row is back on screen.
func TestParentFoldReappliedWhenStatusFilterFallsBack(t *testing.T) {
	h, parent, child := makeFoldTestHome()
	parent.Status = session.StatusIdle
	child.Status = session.StatusIdle
	h.instances = []*session.Instance{parent, child}
	h.parentChildrenCollapsed["parent"] = true

	h.statusFilter = session.StatusRunning
	h.rebuildFlatItems()
	if h.statusFilter != "" {
		t.Fatalf("test setup: filter should fall back when nothing matches, got %q", h.statusFilter)
	}
	if got := strings.Join(flatSessionTitles(h), ","); got != "parent" {
		t.Fatalf("fallback list = %s, want parent (fold applies once the parent is drawn)", got)
	}
}
