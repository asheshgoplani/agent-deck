package session

import (
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// Issue #2806: an idle child whose process dies on its own flips waiting ->
// error, which no path reported. Driven through syncProfile on the status-DB
// (tuiAlive) path, as TestB13, so no tmux and no Claude.

func issue2806Setup(t *testing.T, profile string) (*TransitionDaemon, *statedb.StateDB, *Instance) {
	t.Helper()
	inboxTestHome(t)
	// Report at the second pass; TestIssue2806_SlowCLIStopIsNotADeath sets the grace.
	t.Setenv("AGENTDECK_IDLE_EXIT_GRACE", "0s")
	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	t.Cleanup(func() { storage.Close() })
	now := time.Now()
	parent := &Instance{ID: "parent-2806", Title: "orchestrator", ProjectPath: "/tmp/p", GroupPath: DefaultGroupPath, Tool: "claude", Status: StatusRunning, CreatedAt: now}
	child := &Instance{ID: "child-2806", Title: "worker", ProjectPath: "/tmp/c", GroupPath: DefaultGroupPath, ParentSessionID: parent.ID, Tool: "claude", Status: StatusWaiting, CreatedAt: now}
	if err := storage.SaveWithGroups([]*Instance{child, parent}, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	db := storage.GetDB()
	if err := db.RegisterInstance(false); err != nil {
		t.Fatalf("register instance: %v", err)
	}
	if err := db.Heartbeat(); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	for id, st := range map[string]string{child.ID: "waiting", parent.ID: "running"} {
		if err := db.WriteStatus(id, st, "claude"); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	d := NewTransitionDaemon()
	// No tmux here: the child's pane counts as gone.
	d.idleExitAlive = func(*Instance) bool { return false }
	d.syncProfile(profile) // baseline: the child idle at its prompt
	return d, db, child
}

func TestIssue2806_IdleChildDeathIsOneUrgentRecord(t *testing.T) {
	profile := "_test-2806-death"
	d, db, child := issue2806Setup(t, profile)

	if err := db.WriteStatus(child.ID, "error", "claude"); err != nil {
		t.Fatalf("write error: %v", err)
	}
	d.syncProfile(profile) // first error sample: not yet a death
	d.syncProfile(profile) // error holds and the pane is gone: the death
	d.syncProfile(profile) // still dead: no second record

	events, err := DrainInboxForParent(child.ParentSessionID)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("an idle child that died gives its parent exactly one record, got %d: %+v", len(events), events)
	}
	ev := events[0]
	if ev.ChildSessionID != child.ID || ev.FromStatus != "waiting" || ev.ToStatus != "error" || ev.Tier != TurnTierUrgent {
		t.Fatalf("want one urgent waiting -> error record for the child, got %+v", ev)
	}
	if !strings.Contains(ev.Text, "exited while idle") {
		t.Fatalf("the record must say the process exited, got %q", ev.Text)
	}
}

func TestIssue2806_RestartKillIsNotADeath(t *testing.T) {
	profile := "_test-2806-restart"
	d, db, child := issue2806Setup(t, profile)

	// The restart left its token (#2802) before killing the old process.
	writeRestartFile(child.ID, ".json", restartToken{At: time.Now(), Tool: "claude", Signal: "sig"})
	t.Cleanup(func() { clearRestartToken(child.ID) })
	if err := db.WriteStatus(child.ID, "error", "claude"); err != nil {
		t.Fatalf("write error: %v", err)
	}
	d.syncProfile(profile)
	d.syncProfile(profile)
	// The restart reported its own verdict and cleared the token; the child
	// stays dead. That is still the restart's record, not a second one.
	clearRestartToken(child.ID)
	d.syncProfile(profile)
	d.syncProfile(profile)

	events, err := DrainInboxForParent(child.ParentSessionID)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("a restart's own kill is not a death, got %+v", events)
	}
}

// Claude's /clear, pi's /new and a Hermes reset end the session and start a
// new one in the same live process: one error sample between two waits.
func TestIssue2806_TransientErrorIsNotADeath(t *testing.T) {
	profile := "_test-2806-transient"
	d, db, child := issue2806Setup(t, profile)

	for _, st := range []string{"error", "waiting", "error", "waiting", "waiting"} {
		if err := db.WriteStatus(child.ID, st, "claude"); err != nil {
			t.Fatalf("write %s: %v", st, err)
		}
		d.syncProfile(profile)
	}

	events, err := DrainInboxForParent(child.ParentSessionID)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("a brief error between two waits is not a death, got %+v", events)
	}
}

// An error that holds while the pane still runs (an error banner, a late
// async SessionEnd after /clear, a fresh restart's new process) is not a
// death. When that pane later dies, the parent is told once.
func TestIssue2806_ErrorWithLiveProcessIsNotADeath(t *testing.T) {
	profile := "_test-2806-alive"
	d, db, child := issue2806Setup(t, profile)
	alive := true
	d.idleExitAlive = func(*Instance) bool { return alive }

	if err := db.WriteStatus(child.ID, "error", "claude"); err != nil {
		t.Fatalf("write error: %v", err)
	}
	for i := 0; i < 4; i++ {
		d.syncProfile(profile)
	}
	if events, err := ReadInboxEvents(child.ParentSessionID); err != nil || len(events) != 0 {
		t.Fatalf("an error with a live process is not a death, got %+v (err %v)", events, err)
	}

	alive = false
	d.syncProfile(profile)
	d.syncProfile(profile)
	events, err := DrainInboxForParent(child.ParentSessionID)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(events) != 1 || events[0].FromStatus != "waiting" || events[0].ToStatus != "error" {
		t.Fatalf("once the pane is gone the parent gets one waiting -> error record, got %d: %+v", len(events), events)
	}
}

// A CLI `session stop` / `session remove --force` / worktree kill takes the
// pane down first and saves the stopped (or removed) row only after the kill
// and its cleanup. A daemon pass in between reads waiting -> error with no
// pane; a slow CLI spans more than one pass. That is not a death.
func TestIssue2806_SlowCLIStopIsNotADeath(t *testing.T) {
	profile := "_test-2806-slowstop"
	d, db, child := issue2806Setup(t, profile)
	t.Setenv("AGENTDECK_IDLE_EXIT_GRACE", "") // the real grace

	if err := db.WriteStatus(child.ID, "error", "claude"); err != nil { // pane gone, row not saved yet
		t.Fatalf("write error: %v", err)
	}
	d.syncProfile(profile)
	d.syncProfile(profile)
	d.syncProfile(profile)
	if err := db.WriteStatus(child.ID, "stopped", "claude"); err != nil { // the CLI's save lands
		t.Fatalf("write stopped: %v", err)
	}
	d.syncProfile(profile)
	d.syncProfile(profile)

	events, err := DrainInboxForParent(child.ParentSessionID)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("a stop whose save lands after a few passes is not a death, got %+v", events)
	}
}

// The stop or removal can land between the pass's status read and the
// record: the saved row is read again right before sending.
func TestIssue2806_StopSavedDuringThePassIsNotADeath(t *testing.T) {
	for _, tc := range []struct {
		name string
		end  func(db *statedb.StateDB, id string) error
	}{
		{"stopped", func(db *statedb.StateDB, id string) error { return db.WriteStatus(id, "stopped", "claude") }},
		{"removed", func(db *statedb.StateDB, id string) error { return db.DeleteInstance(id) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile := "_test-2806-midpass-" + tc.name
			d, db, child := issue2806Setup(t, profile)
			cliLands := false
			d.idleExitAlive = func(*Instance) bool {
				if cliLands {
					if err := tc.end(db, child.ID); err != nil {
						t.Errorf("end the session: %v", err)
					}
				}
				return false
			}

			if err := db.WriteStatus(child.ID, "error", "claude"); err != nil {
				t.Fatalf("write error: %v", err)
			}
			d.syncProfile(profile) // the flip
			cliLands = true
			d.syncProfile(profile) // error read, then the CLI saves before the record
			d.syncProfile(profile)

			events, err := DrainInboxForParent(child.ParentSessionID)
			if err != nil {
				t.Fatalf("drain: %v", err)
			}
			if len(events) != 0 {
				t.Fatalf("a session %s on purpose is not a death, got %+v", tc.name, events)
			}
		})
	}
}
