// Group create/rename/move must survive the save-abort → reload race.
//
// Group mutations persist via the non-force saveInstances(), whose
// external-change guard aborts (and triggers a reload) when the on-disk state
// mtime is newer than this instance's last load. The reload rebuilds the
// groupTree from disk, discarding the just-applied mutation — so the user's new
// group silently vanishes and they have to redo it (observed 2026-07-11 in the
// debug log: `save_abort_external_change`). loadSessionsMsg re-applies pending
// group ops after the reload, mirroring pendingTitleChanges for session renames.
//
// RemoteSession note: group create/rename/move act purely on local
// session.Instance.GroupPath and GroupTree. RemoteSessions (tracked in
// h.remoteSessions with no local Instance/GroupPath) cannot participate in
// these operations, so RemoteSession coverage is not applicable here (no
// t.Skip needed).

package ui

import (
	"os"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestGroupCreate_SurvivesReloadRace(t *testing.T) {
	home := NewHome()
	home.width, home.height = 100, 30
	home.storage = nil // reapply force-saves; nil storage makes that a no-op

	home.instancesMu.Lock()
	home.instances = []*session.Instance{}
	home.instancesMu.Unlock()
	home.groupTree = session.NewGroupTree(home.instances)

	// User created group "home"; the create handler mutates the tree and records
	// a pending op. The save then aborts (external change) and fires a reload.
	home.groupTree.CreateGroup("home")
	home.pendingGroupOps = append(home.pendingGroupOps, pendingGroupOp{kind: groupOpCreate, name: "home"})
	home.rebuildFlatItems()

	// The aborted save's reload arrives with disk groups that DON'T include
	// "home" — this is what wipes the in-memory group.
	model, _ := home.Update(loadSessionsMsg{instances: []*session.Instance{}, groups: nil})
	h := model.(*Home)

	if _, ok := h.groupTree.Groups["home"]; !ok {
		t.Fatalf("group 'home' was lost after the reload race; reapply did not restore it")
	}
	if len(h.pendingGroupOps) != 0 {
		t.Errorf("pendingGroupOps should be cleared after reapply, got %d", len(h.pendingGroupOps))
	}
}

func TestGroupRename_SurvivesReloadRace(t *testing.T) {
	home := NewHome()
	home.width, home.height = 100, 30
	home.storage = nil

	home.instancesMu.Lock()
	home.instances = []*session.Instance{}
	home.instancesMu.Unlock()
	home.groupTree = session.NewGroupTree(home.instances)
	home.groupTree.CreateGroup("work")

	// User renamed "work" -> "life"; handler mutates the tree and records the op.
	home.groupTree.RenameGroup("work", "life")
	home.pendingGroupOps = append(home.pendingGroupOps, pendingGroupOp{kind: groupOpRename, oldPath: "work", name: "life"})
	home.rebuildFlatItems()

	// The reload brings back the pre-rename disk state (still "work", because the
	// rename's save was aborted).
	model, _ := home.Update(loadSessionsMsg{
		instances: []*session.Instance{},
		groups:    []*session.GroupData{{Name: "work", Path: "work", Expanded: true, Order: 0}},
	})
	h := model.(*Home)

	if _, ok := h.groupTree.Groups["life"]; !ok {
		t.Fatalf("rename to 'life' was lost after the reload race")
	}
	if _, ok := h.groupTree.Groups["work"]; ok {
		t.Errorf("stale 'work' group still present after reapplied rename")
	}
	if len(h.pendingGroupOps) != 0 {
		t.Errorf("pendingGroupOps should be cleared after reapply, got %d", len(h.pendingGroupOps))
	}
}

// A pending op must NOT outlive a successful save. Otherwise it lingers and a
// later, unrelated reload blindly re-applies it against a diverged tree —
// resurrecting a group that was legitimately deleted elsewhere (and, for a
// create-then-rename sequence, a RenameGroup collision that silently drops
// sessions). Mirrors how pendingTitleChanges is cleared on successful save.
func TestPendingGroupOps_ClearedOnSuccessfulSave_NoResurrection(t *testing.T) {
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", t.TempDir())
	session.ClearUserConfigCache()
	t.Cleanup(func() { os.Setenv("HOME", origHome); session.ClearUserConfigCache() })

	storage, err := session.NewStorageWithProfile("_greloadrace")
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	t.Cleanup(func() { storage.Close() })

	home := NewHome()
	home.width, home.height = 100, 30
	home.storage = storage
	home.profile = "_greloadrace"
	home.storageWatcher = nil

	// One real session so the "refuse to save empty over non-empty file" guard
	// doesn't short-circuit the save.
	inst := session.NewInstance("s1", "/tmp/proj")
	home.instancesMu.Lock()
	home.instances = []*session.Instance{inst}
	home.instanceByID[inst.ID] = inst
	home.instancesMu.Unlock()
	home.groupTree = session.NewGroupTree(home.instances)

	// Create "home"; the save succeeds (fresh storage, no external change).
	home.groupTree.CreateGroup("home")
	home.pendingGroupOps = append(home.pendingGroupOps, pendingGroupOp{kind: groupOpCreate, name: "home"})
	home.saveInstances()

	if len(home.pendingGroupOps) != 0 {
		t.Fatalf("pendingGroupOps not cleared after a successful save (got %d) — a stale op would be reapplied later", len(home.pendingGroupOps))
	}

	// A later, unrelated reload where "home" was legitimately deleted elsewhere
	// must NOT resurrect it.
	model, _ := home.Update(loadSessionsMsg{instances: []*session.Instance{inst}, groups: nil})
	h := model.(*Home)
	if _, ok := h.groupTree.Groups["home"]; ok {
		t.Errorf("group 'home' was resurrected by a stale pending op after external deletion")
	}
}

// A group re-created through the reload race must keep the configured
// [group_defaults].max_concurrent, not silently fall back to serial (1) —
// the reloaded tree loses DefaultMaxConcurrent, so the op carries it.
func TestGroupCreate_ReapplyHonorsMaxConcurrent(t *testing.T) {
	home := NewHome()
	home.width, home.height = 100, 30
	home.storage = nil

	home.instancesMu.Lock()
	home.instances = []*session.Instance{}
	home.instancesMu.Unlock()
	home.groupTree = session.NewGroupTree(home.instances)

	five := 5
	home.groupTree.CreateGroup("home")
	home.pendingGroupOps = append(home.pendingGroupOps, pendingGroupOp{kind: groupOpCreate, name: "home", maxConcurrent: &five})

	model, _ := home.Update(loadSessionsMsg{instances: []*session.Instance{}, groups: nil})
	h := model.(*Home)

	g, ok := h.groupTree.Groups["home"]
	if !ok {
		t.Fatalf("group 'home' not restored after reload race")
	}
	if g.MaxConcurrent != 5 {
		t.Errorf("reapplied group MaxConcurrent = %d, want 5 (configured default must survive the reload race)", g.MaxConcurrent)
	}
}

// If the abort-triggering external change ALSO created a different group at the
// rename's target path, reapply must NOT overwrite it — doing so would orphan
// that group's sessions (they'd vanish from GetAllInstances and be force-saved
// away). The rename is dropped in favour of the external group.
func TestGroupRename_ReloadRace_CollisionDoesNotDropSessions(t *testing.T) {
	home := NewHome()
	home.width, home.height = 100, 30
	home.storage = nil

	s1 := session.NewInstance("s1", "/tmp/w")
	s1.GroupPath = "work"
	s2 := session.NewInstance("s2", "/tmp/l") // lives in the externally-created "life"
	s2.GroupPath = "life"
	home.instancesMu.Lock()
	home.instances = []*session.Instance{s1, s2}
	home.instanceByID[s1.ID] = s1
	home.instanceByID[s2.ID] = s2
	home.instancesMu.Unlock()

	// Pre-reload: the user renamed "work" -> "life"; record the pending op.
	home.groupTree = session.NewGroupTree([]*session.Instance{})
	home.groupTree.CreateGroup("life")
	home.pendingGroupOps = append(home.pendingGroupOps, pendingGroupOp{kind: groupOpRename, oldPath: "work", name: "life"})

	// The reload's external state contains BOTH the un-renamed "work" AND a
	// different, legitimately-created "life" (holding session s2).
	model, _ := home.Update(loadSessionsMsg{
		instances: []*session.Instance{s1, s2},
		groups: []*session.GroupData{
			{Name: "work", Path: "work", Expanded: true, Order: 0},
			{Name: "life", Path: "life", Expanded: true, Order: 1},
		},
	})
	h := model.(*Home)

	// s2 must survive — the external "life" was not clobbered.
	found := false
	for _, inst := range h.groupTree.GetAllInstances() {
		if inst.ID == s2.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("session s2 was lost — a rename collision clobbered the external 'life' group")
	}
	if _, ok := h.groupTree.Groups["life"]; !ok {
		t.Errorf("'life' group missing after collision-guarded reapply")
	}
	if len(h.pendingGroupOps) != 0 {
		t.Errorf("pendingGroupOps should be cleared after reapply, got %d", len(h.pendingGroupOps))
	}
}

func TestPendingGroupCreate_IdempotentWhenSaveSucceeded(t *testing.T) {
	home := NewHome()
	home.width, home.height = 100, 30
	home.storage = nil

	home.instancesMu.Lock()
	home.instances = []*session.Instance{}
	home.instancesMu.Unlock()
	home.groupTree = session.NewGroupTree(home.instances)
	home.groupTree.CreateGroup("home")
	// The op lingers as pending even though the save succeeded (cleared only on
	// the next reload) — like pendingTitleChanges.
	home.pendingGroupOps = append(home.pendingGroupOps, pendingGroupOp{kind: groupOpCreate, name: "home"})
	home.rebuildFlatItems()

	// A later reload arrives with disk groups that ALREADY include "home".
	model, _ := home.Update(loadSessionsMsg{
		instances: []*session.Instance{},
		groups:    []*session.GroupData{{Name: "home", Path: "home", Expanded: true, Order: 0}},
	})
	h := model.(*Home)

	// Exactly one "home" — reapply must not duplicate.
	count := 0
	for _, g := range h.groupTree.Groups {
		if g.Path == "home" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly one 'home' group after idempotent reapply, got %d", count)
	}
	if len(h.pendingGroupOps) != 0 {
		t.Errorf("pendingGroupOps should be cleared after reapply, got %d", len(h.pendingGroupOps))
	}
}
