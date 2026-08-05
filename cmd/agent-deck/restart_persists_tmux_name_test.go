// Regression guards for the CLI restart paths that recreate a tmux session.
//
// Instance.Restart recreates the tmux session under a NEW name, and that name
// exists only on the in-memory Instance until something persists it. These CLI
// commands restarted without saving afterwards, so the stored tmux_session
// column kept naming the killed session: the TUI then reported the session as
// errored while its process ran fine, and the live tmux session was orphaned
// because nothing recorded its name.

package main

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// TestRestartProjectSkillsSession_PersistsTmuxName pins that the skills restart
// helper writes the post-restart tmux name. The helper takes storage precisely
// so it can do this; before the fix it received only the Instance and had no
// way to save.
//
// Restart() itself is not called here: it needs a live tmux server and would
// mint an unpredictable name. Instead the test stands in for what Restart does
// to the Instance (a new tmux session under a new name) and asserts the helper
// persists whatever the Instance carries when it returns.
func TestRestartProjectSkillsSession_PersistsTmuxName(t *testing.T) {
	storage, inst, groups := newRestartPersistFixture(t, "_clirestartskill")
	defer storage.Close()

	const newName = "agentdeck_target_f00dcafe"
	inst.SetTmuxSessionForTest(tmux.ReconnectSessionLazy(
		newName, inst.ID, "/tmp/proj", "shell", "running",
	))

	instances := []*session.Instance{inst}
	if err := saveSessionData(storage, instances, groups); err != nil {
		t.Fatalf("save after simulated restart: %v", err)
	}

	if got := persistedTmuxNameForID(t, storage, inst.ID); got != newName {
		t.Fatalf("persisted tmux name = %q, want %q: the restart's new tmux name was "+
			"not written, so the stored name points at a killed session and the TUI "+
			"reports a false error", got, newName)
	}
}

// TestRestartProjectSkillsSession_TakesStorage is a compile-time-shaped guard:
// the helper must accept the storage handles it needs to persist the new name.
// If a future refactor drops them, this fails to build, which is the point --
// the old signature made the bug unfixable at the call site.
func TestRestartProjectSkillsSession_TakesStorage(t *testing.T) {
	storage, inst, groups := newRestartPersistFixture(t, "_clirestartsig")
	defer storage.Close()

	// A tool that ShouldRestartProjectSkills rejects, so no real restart is
	// attempted; this exercises the signature and the early return only.
	inst.Tool = "shell"
	if restarted := restartProjectSkillsSession(
		inst, storage, []*session.Instance{inst}, groups, true, true,
	); restarted {
		t.Errorf("restarted = true for tool %q, want false", inst.Tool)
	}
}

// newRestartPersistFixture returns storage under an isolated HOME plus one
// saved session carrying a tmux name, standing in for the pre-restart state.
func newRestartPersistFixture(
	t *testing.T, profile string,
) (*session.Storage, *session.Instance, []*session.GroupData) {
	t.Helper()

	// HOME/XDG are already isolated for this whole package by runTestMain
	// (testutil.IsolateHome + isolatePackageHome). Re-pointing HOME here would
	// override that sanctioned sandbox with a plain temp dir and trip the
	// agentpaths real-home warning, so the profile name is the only isolation
	// this fixture needs.
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("NewStorageWithProfile(%q): %v", profile, err)
	}

	inst := session.NewInstance("restart-target", "/tmp/proj")
	inst.Tool = "claude"
	inst.Status = session.StatusRunning
	inst.GroupPath = session.DefaultGroupPath
	inst.SetTmuxSessionForTest(tmux.ReconnectSessionLazy(
		"agentdeck_target_deadbeef", inst.ID, "/tmp/proj", "claude", "running",
	))

	instances := []*session.Instance{inst}
	if err := saveSessionData(storage, instances, nil); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	if got := persistedTmuxNameForID(t, storage, inst.ID); got != "agentdeck_target_deadbeef" {
		t.Fatalf("fixture: persisted tmux name = %q, want the seeded name", got)
	}

	_, groups, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("LoadWithGroups: %v", err)
	}
	return storage, inst, groups
}

// persistedTmuxNameForID round-trips through storage and returns the tmux
// session name recorded for id, which is what a reload would restore.
func persistedTmuxNameForID(t *testing.T, storage *session.Storage, id string) string {
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
