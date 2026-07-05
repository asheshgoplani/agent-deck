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
