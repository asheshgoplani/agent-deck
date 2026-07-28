package session

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
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

// TestParentMutators_RepublishRoleEnvOnLiveSession pins that re-parenting a
// LIVE session refreshes the tmux markers without any explicit ensureRoleEnv
// call. The markers used to be published at spawn/respawn only, while
// set-parent / unset-parent (and the group re-homing paths) mutated
// ParentSessionID and left the tmux env stale. Because the SessionStart hook
// matches clear|compact|resume as well as startup, that stale marker is read
// again on the session's next /clear: a re-parented session would be told it
// is interactive and instructed to go design something, and an un-parented one
// would be told it is a dispatched child with no user present.
func TestParentMutators_RepublishRoleEnvOnLiveSession(t *testing.T) {
	skipIfNoTmuxBinary(t)
	isolateUserHomeForShellRestart(t)

	inst := newShellInstance(t, "ReparentRoleEnv")

	if err := inst.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	t.Cleanup(func() { cleanupShellSessions(inst.Title) })

	if !waitForTmuxSession(inst.tmuxSession.Name, 1*time.Second) {
		t.Fatalf("tmux session %q never appeared after Start", inst.tmuxSession.Name)
	}

	// Started unparented: no markers.
	if got, err := inst.tmuxSession.GetEnvironment("AGENTDECK_ROLE"); err == nil {
		t.Fatalf("precondition: unparented session carries AGENTDECK_ROLE = %q", got)
	}

	// Re-parent while live — no explicit ensureRoleEnv call.
	const parentID = "parent-attached-at-runtime"
	inst.SetParentWithPath(parentID, t.TempDir())

	role, err := inst.tmuxSession.GetEnvironment("AGENTDECK_ROLE")
	if err != nil {
		t.Fatalf("after SetParentWithPath, AGENTDECK_ROLE unset: %v", err)
	}
	if role != "child" {
		t.Errorf("AGENTDECK_ROLE = %q, want %q", role, "child")
	}
	if got, err := inst.tmuxSession.GetEnvironment("AGENTDECK_PARENT_ID"); err != nil || got != parentID {
		t.Errorf("AGENTDECK_PARENT_ID = %q (err %v), want %q", got, err, parentID)
	}

	// Un-parent while live — markers must go away again.
	inst.ClearParent()

	if got, err := inst.tmuxSession.GetEnvironment("AGENTDECK_ROLE"); err == nil {
		t.Errorf("after ClearParent, AGENTDECK_ROLE = %q, want it unset", got)
	}
	if got, err := inst.tmuxSession.GetEnvironment("AGENTDECK_PARENT_ID"); err == nil {
		t.Errorf("after ClearParent, AGENTDECK_PARENT_ID = %q, want it unset", got)
	}

	// SetParent (the path-less mutator) must republish too.
	inst.SetParent(parentID)
	if role, err := inst.tmuxSession.GetEnvironment("AGENTDECK_ROLE"); err != nil || role != "child" {
		t.Errorf("after SetParent, AGENTDECK_ROLE = %q (err %v), want %q", role, err, "child")
	}
}

// TestRefreshRoleEnv_LogsBelowWarnFromParentMutator pins the LOG LEVEL, which
// is the entire point of the ensureRoleEnv/refreshRoleEnv split and is not
// covered by the republish tests above — they assert the markers land, and stay
// green whether the failure path logs at Warn or Debug.
//
// The regression this guards: ensureRoleEnv's only guard is tmuxSession == nil,
// but NewInstance populates tmuxSession at construction, so the guard never
// fires for a not-yet-started instance. `launch` sets the parent long before
// Start(), so routing the mutators at the loud variant makes EVERY parented
// launch — the orchestrate/fleet hot path — emit two spurious Warn lines into
// the debug.log this repo triages live incidents from.
//
// An unstarted instance is exactly that case: tmuxSession is non-nil, no tmux
// session exists, so set-environment fails and the failure must be logged below
// Warn. Asserting the record EXISTS as well as its level keeps the test
// non-vacuous — a silently skipped publish would otherwise pass.
func TestRefreshRoleEnv_LogsBelowWarnFromParentMutator(t *testing.T) {
	skipIfNoTmuxBinary(t)

	dir := t.TempDir()
	logging.Shutdown()
	logging.Init(logging.Config{Debug: true, LogDir: dir, Level: "debug"})
	defer logging.Shutdown()

	// Never started: tmuxSession is non-nil but no tmux session exists.
	inst := newShellInstance(t, "QuietRoleEnv")
	inst.SetParentWithPath("parent-that-never-started", t.TempDir())

	logging.Shutdown() // flush before reading
	records := readLogRecords(t, dir)

	rec := findRecord(records, "set_role_failed")
	if rec == nil {
		t.Fatalf("expected a set_role_failed record from the mutator's failed "+
			"publish; got %d records — if the publish was skipped entirely this "+
			"test is no longer pinning anything", len(records))
	}
	if rec["level"] == "WARN" {
		t.Errorf("set_role_failed logged at WARN from a parent mutator; want DEBUG — "+
			"every parented launch would spam the log (record: %v)", rec)
	}
	if rec["level"] != "DEBUG" {
		t.Errorf("set_role_failed level = %v, want DEBUG", rec["level"])
	}
}

// TestEnsureRoleEnv_NilTmuxSession_NoPanic pins the nil guard: a respawn branch
// must never panic when the instance has no tmux session.
func TestEnsureRoleEnv_NilTmuxSession_NoPanic(t *testing.T) {
	inst := NewInstanceWithTool("test", "/tmp/test", "claude")
	inst.tmuxSession = nil
	inst.ensureRoleEnv() // must not panic
}
