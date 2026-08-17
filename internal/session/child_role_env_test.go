package session

import (
	"os"
	"os/exec"
	"strings"
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
	t.Cleanup(func() {
		_ = inst.KillAndWait()
		_ = inst.CleanupRepositorySessionTemp()
		cleanupShellSessions(inst.Title)
	})
	return inst
}

func TestNewShellInstanceCleanupStopsProcessBeforeTempDirRemoval(t *testing.T) {
	var sessionName, projectPath string
	if ok := t.Run("owned lifecycle", func(t *testing.T) {
		inst := newShellInstance(t, "CleanupOwnership")
		if err := inst.Start(); err != nil {
			t.Fatalf("Start failed: %v", err)
		}
		if !waitForTmuxSession(inst.tmuxSession.Name, time.Second) {
			t.Fatalf("tmux session %q never appeared after Start", inst.tmuxSession.Name)
		}
		sessionName = inst.tmuxSession.Name
		projectPath = inst.ProjectPath
	}); !ok {
		t.Fatal("lifecycle subtest failed")
	}
	if exec.Command("tmux", "has-session", "-t", sessionName).Run() == nil {
		t.Fatalf("newShellInstance cleanup left tmux session %q alive", sessionName)
	}
	if _, err := os.Stat(projectPath); !os.IsNotExist(err) {
		t.Fatalf("newShellInstance cleanup left project temp path %q: %v", projectPath, err)
	}
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

// TestRestart_ShellSession_CarriesRoleEnv is the end-to-end restart contract,
// mirroring TestRestart_ShellSession_CarriesProfileEnv. Seven of the nine
// ensureRoleEnv call sites live inside restart(), and until this test none of
// them was reached by anything: deleting all seven left `go build` green and
// the suite at its two accepted pre-existing failures, so a future edit that
// dropped one would ship green.
//
// What that would break is not cosmetic. agent-deck restarts children — the
// reviver does it unattended — and hooks.json matches `resume` precisely
// because of that. A restarted child that lost AGENTDECK_ROLE gets the
// INTERACTIVE preamble instead of the executor one, and starts asking a user
// who is not there to approve a design.
//
// A shell session takes the fallback recreate path (no resume support), which
// is the branch at the end of restart(); the six respawn branches each return
// before it, which is why they carry their own calls.
func TestRestart_ShellSession_CarriesRoleEnv(t *testing.T) {
	skipIfNoTmuxBinary(t)
	isolateUserHomeForShellRestart(t)

	inst := newShellInstance(t, "RestartRoleEnv")
	const parentID = "parent-across-restart"
	inst.SetParentWithPath(parentID, t.TempDir())

	if err := inst.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	t.Cleanup(func() { cleanupShellSessions(inst.Title) })

	if !waitForTmuxSession(inst.tmuxSession.Name, 1*time.Second) {
		t.Fatalf("tmux session %q never appeared after Start", inst.tmuxSession.Name)
	}

	if err := inst.Restart(); err != nil {
		t.Fatalf("Restart returned error: %v", err)
	}
	if !waitForTmuxSession(inst.tmuxSession.Name, 1*time.Second) {
		t.Fatalf("tmux session %q does not exist after Restart", inst.tmuxSession.Name)
	}

	role, err := inst.tmuxSession.GetEnvironment("AGENTDECK_ROLE")
	if err != nil {
		t.Fatalf("GetEnvironment(AGENTDECK_ROLE) after Restart failed: %v", err)
	}
	if role != "child" {
		t.Errorf("after Restart, AGENTDECK_ROLE = %q, want %q", role, "child")
	}

	gotParent, err := inst.tmuxSession.GetEnvironment("AGENTDECK_PARENT_ID")
	if err != nil {
		t.Fatalf("GetEnvironment(AGENTDECK_PARENT_ID) after Restart failed: %v", err)
	}
	if gotParent != parentID {
		t.Errorf("after Restart, AGENTDECK_PARENT_ID = %q, want %q", gotParent, parentID)
	}
}

// TestRoleEnvPairedWithProfileEnvAtEverySpawnSite pins the 9-for-9 invariant
// that TestRestart_ShellSession_CarriesRoleEnv cannot reach: that test drives
// only the fallback recreate path, while six respawn branches each return
// earlier and carry their own call. Executing all six needs a live session per
// tool (claude, codex, opencode, …), which this package cannot do.
//
// So pin it structurally instead. ensureProfileEnv is the established
// precedent — it must be published at exactly the same set of spawn/respawn
// sites — so any site that publishes one and not the other is a wiring bug.
// This was previously a manual `grep -c` in the plan, which no CI run
// executes; a dropped call would have shipped green.
func TestRoleEnvPairedWithProfileEnvAtEverySpawnSite(t *testing.T) {
	src, err := os.ReadFile("instance.go")
	if err != nil {
		t.Fatalf("read instance.go: %v", err)
	}
	body := string(src)

	// Count call sites only — skip the declarations and this file's own refs.
	roleCalls := strings.Count(body, "i.ensureRoleEnv()")
	profileCalls := strings.Count(body, "i.ensureProfileEnv()")

	if roleCalls != profileCalls {
		t.Errorf("i.ensureRoleEnv() appears %d times, i.ensureProfileEnv() %d — "+
			"every spawn/respawn site must publish BOTH. A site that sets the "+
			"profile but not the role silently strips AGENTDECK_ROLE from a "+
			"restarted child, which then receives the interactive preamble.",
			roleCalls, profileCalls)
	}
	if roleCalls == 0 {
		t.Fatal("found no i.ensureRoleEnv() call sites — the scan is broken, " +
			"not the code")
	}
}

// TestEnsureRoleEnv_NilTmuxSession_NoPanic pins the nil guard: a respawn branch
// must never panic when the instance has no tmux session.
func TestEnsureRoleEnv_NilTmuxSession_NoPanic(t *testing.T) {
	inst := NewInstanceWithTool("test", "/tmp/test", "claude")
	inst.tmuxSession = nil
	inst.ensureRoleEnv() // must not panic
}
