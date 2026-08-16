package ui

import (
	"os"
	"os/exec"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// A restart gives a session a NEW tmux session name: restart() kills the old
// tmux session and calls recreateTmuxSession, whose tmux.NewSession mints
// SessionPrefix + name + "_" + a fresh short id unconditionally. That name is
// the only handle anything has on the live process.
//
// The name and the status the restart ended in are made durable where they are
// produced, by a targeted two-column write inside Instance.restart. What is
// under test here is the TUI half: the save that follows a restart must persist
// the rest of the restart WITHOUT ever pushing this TUI's whole in-memory
// snapshot, because that snapshot may be stale and a whole-snapshot write
// reverts whatever another process changed in the meantime.

func skipIfNoTmuxBinaryUI(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
}

// TestRestartMsg_ConcurrentRenameSurvivesAndOutcomePersists is the regression
// guard for the pair, and for the failure mode that matters most.
//
// A CLI renames one session while the TUI is holding a snapshot from before
// that rename, and the TUI then restarts a DIFFERENT session. Two things have
// to be true afterwards: the rename is still there, and the restart's outcome
// reached the database anyway.
//
// Force-saving here — the first shape of this fix — got the second and lost the
// first: it pushed the stale snapshot, and the renamed title reverted. That is
// the lost-update class behind this repository's documented data-loss
// incidents, and it is strictly worse than the stale tmux name it was curing.
func TestRestartMsg_ConcurrentRenameSurvivesAndOutcomePersists(t *testing.T) {
	skipIfNoTmuxBinaryUI(t)

	home, storage := newRestartSaveHome(t, "_restartsaveconcurrent")
	keeper, target := home.instances[0], home.instances[1]

	// A second process — a `agent-deck session set <id> title` — renames the
	// bystander after this TUI loaded, so the TUI's snapshot is now stale.
	const renamed = "renamed-by-cli"
	renameThroughAnotherProcess(t, keeper.ID, renamed)

	// The TUI restarts the OTHER session for real. This mints a new tmux name
	// and records it, plus the status, at the chokepoint.
	if err := target.Restart(); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	t.Cleanup(func() {
		if sess := target.GetTmuxSession(); sess != nil {
			_ = sess.Kill()
		}
	})
	minted := target.GetTmuxSession()
	if minted == nil {
		t.Fatal("restart left no tmux session")
	}

	model, _ := home.Update(sessionRestartedMsg{sessionID: target.ID})
	if _, ok := model.(*Home); !ok {
		t.Fatalf("Update returned %T, want *Home", model)
	}

	if got := storedTitle(t, storage, keeper.ID); got != renamed {
		t.Errorf("stored title = %q, want %q: the restart save pushed this TUI's stale snapshot "+
			"and reverted a rename another process had already committed", got, renamed)
	}
	if got := storedTmuxName(t, storage, target.ID); got != minted.Name {
		t.Errorf("stored tmux name = %q, want the minted %q: the restart's outcome has to be "+
			"durable whether or not the save that follows it aborts", got, minted.Name)
	}
	if got := storedStatus(t, storage, target.ID); got != string(target.Status) {
		t.Errorf("stored status = %q, want %q: a row naming a live pane while still marked error "+
			"misreports the session just as badly as one naming a dead pane", got, target.Status)
	}
}

// TestRestartMsg_OwnWriteIsNotTreatedAsExternal pins the other half. With no
// other writer, the restart's own targeted write must not make the TUI mistake
// itself for a competing process — that false positive is what aborted the save
// after every restart and dropped the rest of the restart's in-memory state.
func TestRestartMsg_OwnWriteIsNotTreatedAsExternal(t *testing.T) {
	skipIfNoTmuxBinaryUI(t)

	home, storage := newRestartSaveHome(t, "_restartsaveown")
	target := home.instances[1]

	if err := target.Restart(); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	t.Cleanup(func() {
		if sess := target.GetTmuxSession(); sess != nil {
			_ = sess.Kill()
		}
	})

	// Stand in for the in-memory state a restart leaves behind that only the
	// snapshot save carries. If the save aborts, this never reaches disk.
	target.Notes = "restart-mutation"

	model, _ := home.Update(sessionRestartedMsg{sessionID: target.ID})
	if _, ok := model.(*Home); !ok {
		t.Fatalf("Update returned %T, want *Home", model)
	}

	if got := storedNotes(t, storage, target.ID); got != "restart-mutation" {
		t.Errorf("stored notes = %q, want %q: with no competing writer the save must go through, "+
			"otherwise the TUI is aborting on its own restart write", got, "restart-mutation")
	}
}

// TestRestartMsg_FailureDoesNotSave pins an invariant rather than the fix: a
// failed restart recreated nothing, so it must not push this TUI's snapshot
// over another process's rows.
func TestRestartMsg_FailureDoesNotSave(t *testing.T) {
	home, storage := newRestartSaveHome(t, "_restartsavefail")
	target := home.instances[1]

	const renamed = "renamed-during-failed-restart"
	renameThroughAnotherProcess(t, home.instances[0].ID, renamed)
	target.Notes = "must-not-reach-disk"

	model, _ := home.Update(sessionRestartedMsg{
		sessionID: target.ID,
		err:       os.ErrPermission,
	})
	if _, ok := model.(*Home); !ok {
		t.Fatalf("Update returned %T, want *Home", model)
	}

	if got := storedTitle(t, storage, home.instances[0].ID); got != renamed {
		t.Errorf("stored title = %q, want %q: a failed restart wrote nothing worth risking "+
			"another process's rows for", got, renamed)
	}
	if got := storedNotes(t, storage, target.ID); got == "must-not-reach-disk" {
		t.Error("a failed restart persisted this TUI's snapshot anyway")
	}
}

// renameThroughAnotherProcess edits one row the way a separate agent-deck CLI
// process would: its own handle on the same database, a targeted write, and a
// last_modified bump the TUI has not seen.
func renameThroughAnotherProcess(t *testing.T, id, title string) {
	t.Helper()
	db := statedb.GetGlobal()
	if db == nil {
		t.Fatal("no state database registered")
	}
	applied, err := db.UpdateTitleIfUnlocked(id, title)
	if err != nil {
		t.Fatalf("UpdateTitleIfUnlocked: %v", err)
	}
	if !applied {
		t.Fatalf("rename of %q was not applied", id)
	}
	if err := db.Touch(); err != nil {
		t.Fatalf("Touch: %v", err)
	}
}

func storedRow(t *testing.T, storage *session.Storage, id string) *session.Instance {
	t.Helper()
	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("LoadWithGroups: %v", err)
	}
	for _, inst := range instances {
		if inst.ID == id {
			return inst
		}
	}
	t.Fatalf("session %q missing from storage", id)
	return nil
}

func storedTitle(t *testing.T, storage *session.Storage, id string) string {
	t.Helper()
	return storedRow(t, storage, id).Title
}

func storedNotes(t *testing.T, storage *session.Storage, id string) string {
	t.Helper()
	return storedRow(t, storage, id).Notes
}

func storedStatus(t *testing.T, storage *session.Storage, id string) string {
	t.Helper()
	return string(storedRow(t, storage, id).Status)
}

// storedTmuxName round-trips through storage and returns the tmux session name
// recorded for id, which is what a reload would restore.
func storedTmuxName(t *testing.T, storage *session.Storage, id string) string {
	t.Helper()
	if sess := storedRow(t, storage, id).GetTmuxSession(); sess != nil {
		return sess.Name
	}
	return ""
}

// newRestartSaveHome builds a Home backed by real storage under the package's
// isolated HOME, holding a bystander session plus a restart target whose stored
// tmux session is dead — what a session looks like after a reboot, and what
// forces restart() down the path that mints a new name.
//
// storageWatcher is nil so an aborted save cannot trigger a real reload
// mid-test; the assertions read the database directly instead.
func newRestartSaveHome(t *testing.T, profile string) (*Home, *session.Storage) {
	t.Helper()

	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("NewStorageWithProfile(%q): %v", profile, err)
	}
	t.Cleanup(func() { _ = storage.Close() })

	// main.go and NewHome both register the process-wide handle; the targeted
	// restart write goes through it.
	prev := statedb.GetGlobal()
	statedb.SetGlobal(storage.GetDB())
	t.Cleanup(func() { statedb.SetGlobal(prev) })

	home := NewHome()
	home.width, home.height = 100, 30
	home.storage = storage
	home.profile = profile
	home.storageWatcher = nil

	keeper := session.NewInstance("bystander", t.TempDir())
	keeper.Tool = "shell"
	keeper.Status = session.StatusRunning
	keeper.GroupPath = session.DefaultGroupPath

	target := session.NewInstance("restart-target", t.TempDir())
	target.Tool = "shell"
	target.Status = session.StatusRunning
	target.GroupPath = session.DefaultGroupPath
	target.SetTmuxSessionForTest(tmux.ReconnectSessionLazy(
		"agentdeck_target_deadbeef", target.ID, target.ProjectPath, "shell", "running",
	))

	instances := []*session.Instance{keeper, target}
	home.instancesMu.Lock()
	home.instances = instances
	for _, inst := range instances {
		home.instanceByID[inst.ID] = inst
	}
	home.instancesMu.Unlock()
	home.groupTree = session.NewGroupTree(home.instances)
	home.rebuildFlatItems()

	home.forceSaveInstances()
	return home, storage
}
