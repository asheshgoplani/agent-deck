// Issue #1582: a manual shift+up/down reorder must be persisted (and thus
// honored across reloads/restarts), even when it happens during a reload
// window or after an external DB write.
//
// Root cause: the shift+up/down handlers persisted via saveInstances()
// (force=false), whose isReloading no-op and external-change guard silently
// drop the write. The subsequent reload then rebuilds the tree from stale disk
// and the reorder snaps back on screen. Fixed by using forceSaveInstances(),
// matching the sibling move/promote/demote handlers.
//
// This test drives the real shift+down handler through handleMainKey while
// isReloading=true (the exact race the user hits when a child session is
// writing status), then reads the order back from real SQLite storage. It
// FAILS on the pre-fix (saveInstances) code because the reorder is never
// persisted, and PASSES once the handler uses forceSaveInstances().
package ui

import (
	"os"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestShiftReorder_PersistsDuringReload_Issue1582(t *testing.T) {
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", t.TempDir())
	session.ClearUserConfigCache()
	t.Cleanup(func() { os.Setenv("HOME", origHome); session.ClearUserConfigCache() })

	storage, err := session.NewStorageWithProfile("_i1582manual")
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	t.Cleanup(func() { storage.Close() })

	// Two top-level sessions in group "test", initial order first(0), second(1).
	first := session.NewInstanceWithGroup("first", "/tmp/proj-a", "test")
	first.Order = 0
	second := session.NewInstanceWithGroup("second", "/tmp/proj-b", "test")
	second.Order = 1

	home := NewHome()
	home.width, home.height = 100, 30
	home.storage = storage
	home.profile = "_i1582manual"
	home.storageWatcher = nil

	home.instancesMu.Lock()
	home.instances = []*session.Instance{first, second}
	home.instanceByID[first.ID] = first
	home.instanceByID[second.ID] = second
	home.instancesMu.Unlock()
	home.groupTree = session.NewGroupTree(home.instances)

	// Seed disk with the initial order via a normal (non-reload) save.
	home.forceSaveInstances()

	// Sanity: disk order is first, second.
	if got := loadOrderedIDs(t, storage, "test"); len(got) != 2 || got[0] != first.ID || got[1] != second.ID {
		t.Fatalf("precondition: disk order = %v, want [first second]", got)
	}

	// Enter the reload window — this is what silently dropped the reorder.
	home.reloadMu.Lock()
	home.isReloading = true
	home.reloadMu.Unlock()

	// Put the cursor on "first" and press shift+down (reachable via the "J"
	// alias in the same case) to move it below "second".
	home.rebuildFlatItems()
	home.moveCursorToSession(first.ID)
	home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("J")})

	// The persisted order must now be second, first.
	got := loadOrderedIDs(t, storage, "test")
	if len(got) != 2 || got[0] != second.ID || got[1] != first.ID {
		t.Fatalf("reorder was not persisted during reload: disk order = %v, want [second first]", got)
	}
}

// loadOrderedIDs reads instances back from storage, rebuilds the group tree
// (which sorts by the persisted Order), and returns the session IDs of the
// named group in visual order.
func loadOrderedIDs(t *testing.T, storage *session.Storage, groupPath string) []string {
	t.Helper()
	insts, groups, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("LoadWithGroups: %v", err)
	}
	tree := session.NewGroupTreeWithGroups(insts, groups)
	g, ok := tree.Groups[groupPath]
	if !ok {
		return nil
	}
	ids := make([]string, 0, len(g.Sessions))
	for _, s := range g.Sessions {
		ids = append(ids, s.ID)
	}
	return ids
}
