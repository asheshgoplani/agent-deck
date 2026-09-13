package session

// Issue #2240: notify-daemon restart re-delivery regression tests.
//
// Field report (v1.16.5): every restart of `agent-deck notify-daemon` (the
// systemd RuntimeMaxSec recycle, or the recycle after an auto-update) re-emitted
// a running→waiting transition for EVERY child currently parked at a terminal
// status, however old. The consumed-turn ledger collapsed the records on drain
// (`inbox drain self` printed "No pending events"), but the [INBOX] wake-nudge
// had already been typed into every conductor pane: one wasted turn per
// conductor per restart.
//
// Two independent guards close it, each pinned here:
//
//  1. The daemon seeds its per-child turn baseline from the registry on its
//     first pass, against the persisted last-notified state, so a restart
//     notifies nothing until a REAL transition happens after start — while a
//     child whose status changed WHILE the daemon was down is still notified
//     once.
//  2. The wake-nudge fires only when the committed record is one the parent's
//     next drain would actually deliver, never for a turn the parent's
//     consumed-turn ledger already holds.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// restartFixture is one profile with a parked child under a live parent, a
// "live TUI" heartbeat (so syncProfile reads statuses from the DB rows rather
// than probing tmux), and a spy wake-nudge wiring on the daemon's notifier.
type restartFixture struct {
	profile string
	storage *Storage
	child   *Instance
	parent  *Instance

	mu    sync.Mutex
	nudge int
}

func newRestartFixture(t *testing.T, childStatus string) *restartFixture {
	t.Helper()
	const profile = "_test_notifier_restart"
	_, storage := bootstrapDaemonProfile(t, profile)
	ResetInboxFingerprintCacheForTest()
	t.Cleanup(ResetInboxFingerprintCacheForTest)

	now := time.Now()
	f := &restartFixture{profile: profile, storage: storage}
	f.child = &Instance{
		ID:              "restart-child-1",
		Title:           "worker",
		ProjectPath:     "/tmp/restart-child-1",
		GroupPath:       DefaultGroupPath,
		ParentSessionID: "restart-parent-1",
		Tool:            "claude",
		Status:          Status(childStatus),
		CreatedAt:       now,
	}
	f.parent = &Instance{
		ID:          "restart-parent-1",
		Title:       "orchestrator", // NOT "conductor-": resolve skips the tmux probe
		ProjectPath: "/tmp/restart-parent-1",
		GroupPath:   DefaultGroupPath,
		Tool:        "claude",
		Status:      StatusIdle,
		CreatedAt:   now,
	}
	if err := storage.SaveWithGroups([]*Instance{f.child, f.parent}, nil); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}
	db := storage.GetDB()
	if err := db.RegisterInstance(false); err != nil {
		t.Fatalf("RegisterInstance: %v", err)
	}
	f.setChildStatus(t, childStatus)
	if err := db.WriteStatus(f.parent.ID, "idle", "claude"); err != nil {
		t.Fatalf("WriteStatus parent: %v", err)
	}
	return f
}

func (f *restartFixture) setChildStatus(t *testing.T, status string) {
	t.Helper()
	if err := f.storage.GetDB().WriteStatus(f.child.ID, status, "claude"); err != nil {
		t.Fatalf("WriteStatus child: %v", err)
	}
}

// newDaemon models one daemon PROCESS: a fresh TransitionDaemon whose notifier
// loads whatever state the previous process persisted. Instances here have no
// tmux session, so the liveness probe is stubbed to true.
func (f *restartFixture) newDaemon() *TransitionDaemon {
	d := NewTransitionDaemon()
	d.storages[f.profile] = f.storage
	d.turnLiveCheck = func(*Instance) bool { return true }
	d.notifier.wake = &wakeNudgeWiring{
		nudger: NewWakeNudger(0),
		now:    time.Now,
		isIdle: func(*Instance) bool { return true },
		send: func(*Instance, string) error {
			f.mu.Lock()
			f.nudge++
			f.mu.Unlock()
			return nil
		},
	}
	return d
}

func (f *restartFixture) nudges() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.nudge
}

// ageNotifyRecord rewrites the persisted last-notified record for the child so
// it looks `age` old. The report's phantom pings were for children long past
// the 2h output-hash dedup window; anything younger is already silenced by the
// existing isDuplicate layer, which is NOT the mechanism under test.
func ageNotifyRecord(t *testing.T, childID string, age time.Duration) {
	t.Helper()
	path := transitionNotifyStatePath()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read notify state: %v", err)
	}
	var state transitionNotifyState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("decode notify state: %v", err)
	}
	rec, ok := state.Records[childID]
	if !ok {
		t.Fatalf("notify state has no record for %s: %s", childID, raw)
	}
	rec.At = time.Now().Add(-age).Unix()
	state.Records[childID] = rec
	out, _ := json.Marshal(state)
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatalf("write notify state: %v", err)
	}
}

// A brand-new install (no state file) must not publish every already-parked
// child as a fresh completion on its first pass.
func TestNotifierRestart_FirstEverStartDoesNotReplayParkedChildren(t *testing.T) {
	f := newRestartFixture(t, "waiting")
	if _, err := os.Stat(transitionNotifyStatePath()); !os.IsNotExist(err) {
		t.Fatalf("precondition: no state file expected, stat err=%v", err)
	}

	d := f.newDaemon()
	d.syncProfile(f.profile)

	if got := readInboxLines(t, f.parent.ID); len(got) != 0 {
		t.Fatalf("first-ever start replayed history into the parent inbox: %+v", got)
	}
	if n := f.nudges(); n != 0 {
		t.Fatalf("first-ever start fired %d wake-nudges, want 0", n)
	}
}

// The reported bug: a child notified long ago and still parked must NOT be
// re-notified (and the parent must NOT be nudged) when the daemon restarts.
func TestNotifierRestart_RestartDoesNotRenotifyStillParkedChild(t *testing.T) {
	f := newRestartFixture(t, "running")

	// Process 1 observes the real transition and notifies once.
	d1 := f.newDaemon()
	d1.syncProfile(f.profile)
	f.setChildStatus(t, "waiting")
	d1.syncProfile(f.profile)
	if got := readInboxLines(t, f.parent.ID); len(got) != 1 {
		t.Fatalf("real transition must commit exactly one record, got %+v", got)
	}
	if n := f.nudges(); n != 1 {
		t.Fatalf("real transition must nudge once, got %d", n)
	}
	if _, err := DrainInboxForParent(f.parent.ID); err != nil {
		t.Fatalf("drain: %v", err)
	}
	ageNotifyRecord(t, f.child.ID, 3*time.Hour)

	// Process 2 (the RuntimeMaxSec / auto-update recycle) starts with the child
	// still parked at waiting.
	d2 := f.newDaemon()
	d2.syncProfile(f.profile)
	d2.syncProfile(f.profile)

	if got := readInboxLines(t, f.parent.ID); len(got) != 0 {
		t.Fatalf("restart re-committed an already-notified turn: %+v", got)
	}
	if n := f.nudges(); n != 1 {
		t.Fatalf("restart fired a phantom wake-nudge: total nudges %d, want 1", n)
	}
}

// A child whose status changed while the daemon was down is still notified
// exactly once after the restart: the seed compares the registry against the
// persisted last-notified status, it does not blindly silence everything.
func TestNotifierRestart_ChildChangedWhileDownIsNotifiedOnce(t *testing.T) {
	f := newRestartFixture(t, "running")

	d1 := f.newDaemon()
	d1.syncProfile(f.profile)
	f.setChildStatus(t, "idle")
	d1.syncProfile(f.profile)
	if got := readInboxLines(t, f.parent.ID); len(got) != 1 || got[0].ToStatus != "idle" {
		t.Fatalf("precondition: one idle record expected, got %+v", got)
	}
	if _, err := DrainInboxForParent(f.parent.ID); err != nil {
		t.Fatalf("drain: %v", err)
	}
	ageNotifyRecord(t, f.child.ID, 3*time.Hour)

	// Daemon down; the child finished another turn and parked at waiting.
	f.setChildStatus(t, "waiting")

	d2 := f.newDaemon()
	d2.syncProfile(f.profile)
	d2.syncProfile(f.profile)

	got := readInboxLines(t, f.parent.ID)
	if len(got) != 1 || got[0].ToStatus != "waiting" {
		t.Fatalf("a status change during downtime must be notified exactly once, got %+v", got)
	}
	if n := f.nudges(); n != 2 {
		t.Fatalf("expected exactly one nudge for the downtime transition (2 total), got %d", n)
	}
}

// A corrupt state file is treated as a fresh seed: no replay, no crash, and
// the daemon keeps notifying real transitions afterwards.
func TestNotifierRestart_CorruptStateFileSeedsFresh(t *testing.T) {
	f := newRestartFixture(t, "waiting")
	path := transitionNotifyStatePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write corrupt state: %v", err)
	}

	d := f.newDaemon()
	d.syncProfile(f.profile)
	if got := readInboxLines(t, f.parent.ID); len(got) != 0 {
		t.Fatalf("corrupt state must seed fresh, not replay: %+v", got)
	}
	if n := f.nudges(); n != 0 {
		t.Fatalf("corrupt state fired %d nudges, want 0", n)
	}

	// A real transition after start still lands.
	f.setChildStatus(t, "running")
	d.syncProfile(f.profile)
	f.setChildStatus(t, "waiting")
	d.syncProfile(f.profile)
	if got := readInboxLines(t, f.parent.ID); len(got) != 1 {
		t.Fatalf("real transition after a corrupt-state start must still commit, got %+v", got)
	}
}

// A real transition after start is still delivered and nudged: seeding must
// not turn the daemon silent.
func TestNotifierRestart_RealTransitionAfterStartStillNotifies(t *testing.T) {
	f := newRestartFixture(t, "waiting")

	d := f.newDaemon()
	d.syncProfile(f.profile) // seeds the parked child silently
	f.setChildStatus(t, "running")
	d.syncProfile(f.profile)
	f.setChildStatus(t, "waiting")
	d.syncProfile(f.profile)

	if got := readInboxLines(t, f.parent.ID); len(got) != 1 {
		t.Fatalf("real transition after seed must commit once, got %+v", got)
	}
	if n := f.nudges(); n != 1 {
		t.Fatalf("real transition after seed must nudge once, got %d", n)
	}
}

// Guard 2, at the commit chokepoint: a record whose turn the parent has already
// consumed is not nudged for, because the drain would be empty.
func TestCommit_NoWakeNudgeForAlreadyConsumedTurn(t *testing.T) {
	n, parentID, event := newWakeNudgeFixture(t)
	sent := 0
	n.wake = &wakeNudgeWiring{
		nudger: NewWakeNudger(0),
		now:    time.Now,
		isIdle: func(*Instance) bool { return true },
		send:   func(*Instance, string) error { sent++; return nil },
	}

	if res := n.NotifyFinished(event); res.DeliveryResult != transitionDeliveryCommitted {
		t.Fatalf("first commit = %q", res.DeliveryResult)
	}
	if sent != 1 {
		t.Fatalf("first commit must nudge once, got %d", sent)
	}
	delivered, err := DrainInboxForParent(parentID)
	if err != nil || len(delivered) != 1 {
		t.Fatalf("drain: delivered=%d err=%v", len(delivered), err)
	}

	// The same turn is committed again (a replay). The record may land, but the
	// parent's next drain drops it, so no nudge may fire.
	if res := n.NotifyFinished(event); res.DeliveryResult != transitionDeliveryCommitted {
		t.Fatalf("replay commit = %q", res.DeliveryResult)
	}
	if sent != 1 {
		t.Fatalf("replay of a consumed turn fired a wake-nudge: sent=%d, want 1", sent)
	}
	if delivered, _ := DrainInboxForParent(parentID); len(delivered) != 0 {
		t.Fatalf("consumed-turn ledger semantics changed: drain delivered %+v", delivered)
	}
}
