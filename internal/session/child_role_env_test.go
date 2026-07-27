package session

import (
	"testing"
	"time"
)

// Child role markers (AGENTDECK_ROLE / AGENTDECK_PARENT_ID) in the tmux session
// environment.
//
// The plugin's SessionStart hook decides which preamble to inject by reading
// ONLY the tmux session environment — no DB lookup — so a parented launch must
// publish the markers and an unparented launch must publish nothing. Anything
// else silently gives an interactive session the executor preamble (or the
// reverse), which is invisible until a child starts brainstorming its task.

// newShellInstance builds an unstarted bare-shell Instance with a unique title.
// The caller sets any parent, calls Start, and waits for the tmux session.
func newShellInstance(t *testing.T, tag string) *Instance {
	t.Helper()
	title := uniqueShellTestTitle(tag)
	inst := NewInstance(title, t.TempDir())
	inst.Command = ""
	return inst
}

func TestStart_ParentedSession_ExportsChildRoleEnv(t *testing.T) {
	skipIfNoTmuxBinary(t)
	isolateUserHomeForShellRestart(t)

	inst := newShellInstance(t, "ChildRoleEnv")
	const parentID = "parent-instance-id-42"
	inst.SetParentWithPath(parentID, t.TempDir())

	if err := inst.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	t.Cleanup(func() { cleanupShellSessions(inst.Title) })

	if !waitForTmuxSession(inst.tmuxSession.Name, 1*time.Second) {
		t.Fatalf("tmux session %q never appeared after Start", inst.tmuxSession.Name)
	}

	role, err := inst.tmuxSession.GetEnvironment("AGENTDECK_ROLE")
	if err != nil {
		t.Fatalf("GetEnvironment(AGENTDECK_ROLE) failed: %v", err)
	}
	if role != "child" {
		t.Errorf("AGENTDECK_ROLE = %q, want %q", role, "child")
	}

	gotParent, err := inst.tmuxSession.GetEnvironment("AGENTDECK_PARENT_ID")
	if err != nil {
		t.Fatalf("GetEnvironment(AGENTDECK_PARENT_ID) failed: %v", err)
	}
	if gotParent != parentID {
		t.Errorf("AGENTDECK_PARENT_ID = %q, want %q", gotParent, parentID)
	}
}

func TestStart_UnparentedSession_HasNoChildRoleEnv(t *testing.T) {
	skipIfNoTmuxBinary(t)
	isolateUserHomeForShellRestart(t)

	inst := newShellInstance(t, "NoRoleEnv")

	if err := inst.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	t.Cleanup(func() { cleanupShellSessions(inst.Title) })

	if !waitForTmuxSession(inst.tmuxSession.Name, 1*time.Second) {
		t.Fatalf("tmux session %q never appeared after Start", inst.tmuxSession.Name)
	}

	if got, err := inst.tmuxSession.GetEnvironment("AGENTDECK_ROLE"); err == nil {
		t.Errorf("unparented session exported AGENTDECK_ROLE=%q, want it unset", got)
	}
	if got, err := inst.tmuxSession.GetEnvironment("AGENTDECK_PARENT_ID"); err == nil {
		t.Errorf("unparented session exported AGENTDECK_PARENT_ID=%q, want it unset", got)
	}
}

// TestEnsureRoleEnv_ClearsStaleMarkersWhenUnparented pins the removal path: a
// session that carried the markers and then lost its parent must stop
// announcing itself as a child on the next spawn/respawn.
func TestEnsureRoleEnv_ClearsStaleMarkersWhenUnparented(t *testing.T) {
	skipIfNoTmuxBinary(t)
	isolateUserHomeForShellRestart(t)

	inst := newShellInstance(t, "StaleRoleEnv")
	inst.SetParentWithPath("parent-to-be-removed", t.TempDir())

	if err := inst.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	t.Cleanup(func() { cleanupShellSessions(inst.Title) })

	if !waitForTmuxSession(inst.tmuxSession.Name, 1*time.Second) {
		t.Fatalf("tmux session %q never appeared after Start", inst.tmuxSession.Name)
	}

	if _, err := inst.tmuxSession.GetEnvironment("AGENTDECK_ROLE"); err != nil {
		t.Fatalf("precondition: parented session should carry AGENTDECK_ROLE: %v", err)
	}

	inst.ClearParent()
	inst.ensureRoleEnv()

	if got, err := inst.tmuxSession.GetEnvironment("AGENTDECK_ROLE"); err == nil {
		t.Errorf("after ClearParent, AGENTDECK_ROLE = %q, want it unset", got)
	}
	if got, err := inst.tmuxSession.GetEnvironment("AGENTDECK_PARENT_ID"); err == nil {
		t.Errorf("after ClearParent, AGENTDECK_PARENT_ID = %q, want it unset", got)
	}
}

// TestEnsureRoleEnv_NilTmuxSession_NoPanic pins the nil guard: a respawn branch
// must never panic when the instance has no tmux session.
func TestEnsureRoleEnv_NilTmuxSession_NoPanic(t *testing.T) {
	inst := NewInstanceWithTool("test", "/tmp/test", "claude")
	inst.tmuxSession = nil
	inst.ensureRoleEnv() // must not panic
}
