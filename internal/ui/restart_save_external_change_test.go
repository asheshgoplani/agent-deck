package ui

import (
	"os"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// A restart gives a session a NEW tmux session name: restart() kills the old
// tmux session and calls recreateTmuxSession (internal/session/instance.go),
// which builds a fresh one through tmux.NewSession -- and that mints
// SessionPrefix + name + "_" + a new short id unconditionally. The new name
// lives only on the in-memory Instance until the sessionRestartedMsg handler
// saves it.
//
// A routine save aborts when it detects the state DB changed since this TUI
// last loaded, and the reload it schedules instead replaces h.instances
// wholesale with the on-disk rows -- whose tmux_session column still holds the
// DEAD name. The TUI then polls a tmux session that does not exist and renders
// "error" for a session whose process is running fine, and retrying cannot
// clear it because the abort leaves lastLoadMtime behind a concurrent writer.

// TestRestartMsg_PersistsNewTmuxNameDespiteExternalChange is the regression
// guard. It asserts the real contract: after a successful restart under a
// detected external change, the name that survives a round-trip through storage
// is the NEW one, not the dead one.
func TestRestartMsg_PersistsNewTmuxNameDespiteExternalChange(t *testing.T) {
	home, storage := newRestartSaveHome(t, "_restartsaveext")
	defer storage.Close()

	inst := home.instances[0]

	// Stand in for what restart() does to a live session: the Instance now
	// carries a tmux session under a freshly minted name, while the row on
	// disk still names the old one.
	const oldName = "agentdeck_target_deadbeef"
	const newName = "agentdeck_target_f00dcafe"

	inst.SetTmuxSessionForTest(tmux.ReconnectSessionLazy(
		oldName, inst.ID, "/tmp/proj", "shell", "running",
	))
	home.forceSaveInstances()

	before, err := storage.GetFileMtime()
	if err != nil {
		t.Fatalf("GetFileMtime: %v", err)
	}
	if got := persistedTmuxName(t, storage, inst.ID); got != oldName {
		t.Fatalf("setup: persisted tmux name = %q, want %q", got, oldName)
	}

	// The mint. From here the new name exists ONLY in memory.
	inst.SetTmuxSessionForTest(tmux.ReconnectSessionLazy(
		newName, inst.ID, "/tmp/proj", "shell", "running",
	))

	// Arm the abort: the DB has been written since we loaded. Backdating
	// lastLoadMtime reproduces exactly the condition saveInstancesWithForce
	// tests (currentMtime.After(ourLoadMtime)).
	armed := before.Add(-time.Hour)
	home.reloadMu.Lock()
	home.lastLoadMtime = armed
	home.reloadMu.Unlock()

	model, _ := home.Update(sessionRestartedMsg{sessionID: inst.ID, err: nil})
	h, ok := model.(*Home)
	if !ok {
		t.Fatalf("Update returned %T, want *Home", model)
	}

	if got := persistedTmuxName(t, storage, inst.ID); got != newName {
		t.Fatalf("persisted tmux name = %q, want %q: the restart's new tmux name "+
			"was not written, so a reload restores the dead name and the TUI reports "+
			"a false error for a session whose process is running", got, newName)
	}

	// The save must also advance lastLoadMtime past the value that armed the
	// abort. Otherwise the next restart aborts on the same stale reading, which
	// is what made the original failure self-sustaining instead of a one-off.
	h.reloadMu.Lock()
	gotLoadMtime := h.lastLoadMtime
	h.reloadMu.Unlock()
	if !gotLoadMtime.After(armed) {
		t.Errorf("lastLoadMtime = %v, want advanced past the armed value %v: leaving it "+
			"stale makes the next restart abort for the same reason, so retries never persist",
			gotLoadMtime, armed)
	}
}

// A failed restart must not write: nothing was recreated, so there is no new
// name to persist and no reason to push this TUI's snapshot over a peer's rows.
// This pins an invariant rather than guarding the fix -- it passes either way.
func TestRestartMsg_FailureDoesNotSave(t *testing.T) {
	home, storage := newRestartSaveHome(t, "_restartsavefail")
	defer storage.Close()

	inst := home.instances[0]
	const name = "agentdeck_target_deadbeef"
	inst.SetTmuxSessionForTest(tmux.ReconnectSessionLazy(
		name, inst.ID, "/tmp/proj", "shell", "running",
	))
	home.forceSaveInstances()

	// A name that must NOT reach storage, since the restart fails.
	inst.SetTmuxSessionForTest(tmux.ReconnectSessionLazy(
		"agentdeck_target_never", inst.ID, "/tmp/proj", "shell", "running",
	))

	model, _ := home.Update(sessionRestartedMsg{
		sessionID: inst.ID,
		err:       os.ErrPermission,
	})
	if _, ok := model.(*Home); !ok {
		t.Fatalf("Update returned %T, want *Home", model)
	}

	if got := persistedTmuxName(t, storage, inst.ID); got != name {
		t.Errorf("persisted tmux name = %q, want %q: a failed restart recreated "+
			"nothing, so it must not overwrite the stored name", got, name)
	}
}

// persistedTmuxName round-trips through storage and returns the tmux session
// name recorded for id, which is what a reload would restore.
func persistedTmuxName(t *testing.T, storage *session.Storage, id string) string {
	t.Helper()
	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("LoadWithGroups: %v", err)
	}
	for _, inst := range instances {
		if inst.ID != id {
			continue
		}
		sess := inst.GetTmuxSession()
		if sess == nil {
			return ""
		}
		return sess.Name
	}
	t.Fatalf("session %q missing from storage", id)
	return ""
}

// newRestartSaveHome builds a Home backed by real storage under an isolated
// HOME, holding one session. storageWatcher is nil so an aborted save cannot
// trigger a real reload mid-test.
func newRestartSaveHome(t *testing.T, profile string) (*Home, *session.Storage) {
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
		t.Fatalf("NewStorageWithProfile(%q): %v", profile, err)
	}

	home := NewHome()
	home.width, home.height = 100, 30
	home.storage = storage
	home.profile = profile
	home.storageWatcher = nil

	// A real session so the "refuse to save empty over non-empty" guard does
	// not short-circuit the save under test.
	inst := session.NewInstance("restart-target", "/tmp/proj")
	inst.Tool = "shell"
	inst.Status = session.StatusRunning
	inst.GroupPath = session.DefaultGroupPath

	home.instancesMu.Lock()
	home.instances = []*session.Instance{inst}
	home.instanceByID[inst.ID] = inst
	home.instancesMu.Unlock()
	home.groupTree = session.NewGroupTree(home.instances)
	home.rebuildFlatItems()

	return home, storage
}
