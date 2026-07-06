package ui

import (
	"os"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// These tests are the regression guard for the "new main group only works
// every second try" / "rename group only works every second try" bug.
//
// Root cause: GroupDialogCreate/Rename persisted via saveInstances(), which is
// SKIPPED while isReloading (and aborts on the mtime external-change check). A
// group mutation that landed during a storage-watcher reload window was written
// to memory but never to disk, so when the reload rebuilt groupTree from disk
// the mutation vanished — hence the alternating "every second try". The fix
// switches these paths to forceSaveInstances(), which persists regardless of
// the reload flag (mirroring session creation's "was losing groups!" forceSave).

// testHomeWithStorage builds a minimally-wired Home backed by real SQLite
// storage under an isolated temp HOME, with one seed instance so the
// empty-overwrite guard in saveInstancesWithForce does not refuse the write.
func testHomeWithStorage(t *testing.T, profile string) (*Home, *session.Storage) {
	t.Helper()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", t.TempDir())
	session.ClearUserConfigCache()
	t.Cleanup(func() {
		os.Setenv("HOME", origHome)
		session.ClearUserConfigCache()
	})

	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	t.Cleanup(func() { storage.Close() })

	seed := session.NewInstance("seed", "/tmp/seed")
	h := &Home{
		storage:     storage,
		profile:     profile,
		instances:   []*session.Instance{seed},
		groupTree:   session.NewGroupTree([]*session.Instance{seed}),
		groupDialog: NewGroupDialog(),
	}
	return h, storage
}

func groupPersisted(t *testing.T, storage *session.Storage, name string) bool {
	t.Helper()
	_, groups, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("LoadWithGroups: %v", err)
	}
	for _, g := range groups {
		if g.Name == name {
			return true
		}
	}
	return false
}

// sessionGroupPath reads the persisted GroupPath of a session straight from
// storage, so a move that only mutated in-memory state (but was skipped by the
// old saveInstances during a reload) is detectable.
func sessionGroupPath(t *testing.T, storage *session.Storage, sessionID string) string {
	t.Helper()
	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("LoadWithGroups: %v", err)
	}
	for _, inst := range instances {
		if inst.ID == sessionID {
			return inst.GroupPath
		}
	}
	t.Fatalf("session %q not found in storage", sessionID)
	return ""
}

func TestGroupCreatePersistsDuringReload(t *testing.T) {
	h, storage := testHomeWithStorage(t, "_group_persist_create")

	// Simulate a storage-watcher reload in flight — the window in which the old
	// saveInstances() silently dropped the write.
	h.isReloading = true

	// Drive the real create flow: create-mode dialog + Enter.
	h.groupDialog.Show()
	h.groupDialog.nameInput.SetValue("alpha")
	h.handleGroupDialogKey(tea.KeyMsg{Type: tea.KeyEnter})

	if !groupPersisted(t, storage, "alpha") {
		t.Error(`group "alpha" created during a reload window was not persisted; ` +
			`create must use forceSaveInstances (saveInstances is skipped while isReloading)`)
	}
}

func TestGroupRenamePersistsDuringReload(t *testing.T) {
	h, storage := testHomeWithStorage(t, "_group_persist_rename")

	// Seed and persist a group while NOT reloading.
	h.groupTree.CreateGroup("old")
	h.forceSaveInstances()
	if !groupPersisted(t, storage, "old") {
		t.Fatal("precondition: seed group 'old' should be persisted")
	}

	// Now rename it during a reload window.
	h.isReloading = true
	h.groupDialog.ShowRename("old", "old")
	h.groupDialog.nameInput.SetValue("new")
	h.handleGroupDialogKey(tea.KeyMsg{Type: tea.KeyEnter})

	if groupPersisted(t, storage, "old") {
		t.Error(`old group name still present after rename during reload window`)
	}
	if !groupPersisted(t, storage, "new") {
		t.Error(`renamed group "new" was not persisted during reload window; ` +
			`rename must use forceSaveInstances`)
	}
}

func TestGroupMovePersistsDuringReload(t *testing.T) {
	h, storage := testHomeWithStorage(t, "_group_persist_move")

	// Seed a destination group and persist the starting state while NOT reloading.
	h.groupTree.CreateGroup("dest")
	h.forceSaveInstances()
	if !groupPersisted(t, storage, "dest") {
		t.Fatal("precondition: destination group 'dest' should be persisted")
	}

	// The move handler acts on the session under the cursor, so build the flat
	// view and park the cursor on the seed session.
	seedID := h.instances[0].ID
	h.rebuildFlatItems()
	found := false
	for i, item := range h.flatItems {
		if item.Type == session.ItemTypeSession && item.Session != nil && item.Session.ID == seedID {
			h.cursor = i
			found = true
			break
		}
	}
	if !found {
		t.Fatal("precondition: seed session not present in flat items")
	}
	if got := sessionGroupPath(t, storage, seedID); got == "dest" {
		t.Fatalf("precondition: seed session already in 'dest' (got %q)", got)
	}

	// Move the session into 'dest' during a reload window.
	h.isReloading = true
	h.groupDialog.ShowMove([]string{"dest"})
	h.handleGroupDialogKey(tea.KeyMsg{Type: tea.KeyEnter})

	if got := sessionGroupPath(t, storage, seedID); got != "dest" {
		t.Errorf("session GroupPath = %q, want %q; move during a reload window "+
			"must use forceSaveInstances (saveInstances is skipped while isReloading)", got, "dest")
	}
}
