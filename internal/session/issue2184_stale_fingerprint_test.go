package session

// Issue #2184: `inbox drain` hid a genuinely new completion when the child's
// transcript signal (LastOutputHash, the transcript size) had NOT advanced since
// the child's last notified turn. TurnFingerprint keyed the turn on that stale
// signal, so the new completion carried the fingerprint of an already-consumed
// turn and the consumer dropped it as a duplicate; since #2240 the wake-nudge is
// withheld for such a record too, so the parent never learned about it at all.
//
// The fix: the notifier compares the event's LastOutputHash with the persisted
// last-notified hash for the child. When a NEW transition arrives with the SAME
// hash, the hash is stale and the record is flagged (OutputHashStale). A
// flagged record's TurnFingerprint falls through to the flip + emit-instant
// signal, so the new completion is delivered once, while a true redelivery of
// the same stamped record (same emit instant) still collapses.

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newStaleHashNotifierFixture registers a live parent+child pair and returns a
// notifier with a counting wake-nudge, plus a builder for interactive
// running→waiting events for that child.
func newStaleHashNotifierFixture(t *testing.T) (*TransitionNotifier, string, func(hash string, at time.Time) TransitionNotificationEvent, *int) {
	t.Helper()
	n, parentID, done := newWakeNudgeFixture(t)
	sent := 0
	n.wake = &wakeNudgeWiring{
		nudger: NewWakeNudger(0),
		now:    time.Now,
		isIdle: func(*Instance) bool { return true },
		send:   func(*Instance, string) error { sent++; return nil },
	}
	build := func(hash string, at time.Time) TransitionNotificationEvent {
		return TransitionNotificationEvent{
			ChildSessionID: done.ChildSessionID,
			ChildTitle:     done.ChildTitle,
			Profile:        done.Profile,
			FromStatus:     "running",
			ToStatus:       "waiting",
			Timestamp:      at,
			LastOutputHash: hash,
		}
	}
	return n, parentID, build, &sent
}

// The reported bug: a second completed turn whose transcript signal did not
// advance must still reach the parent (delivered once, nudged once).
func TestIssue2184_StaleFingerprintNewCompletionDelivered(t *testing.T) {
	n, parentID, build, sent := newStaleHashNotifierFixture(t)
	t0 := time.Now().Add(-4 * time.Hour)

	first := n.NotifyTransition(build("jsonl:6022505", t0))
	if first.DeliveryResult != transitionDeliveryCommitted {
		t.Fatalf("first turn = %q, want committed", first.DeliveryResult)
	}
	if first.OutputHashStale {
		t.Fatalf("first turn must not be flagged stale: %+v", first)
	}
	firstDrained, err := DrainInboxForParent(parentID)
	if err != nil || len(firstDrained) != 1 {
		t.Fatalf("first drain: delivered=%d err=%v", len(firstDrained), err)
	}
	if *sent != 1 {
		t.Fatalf("first turn must nudge once, got %d", *sent)
	}

	// A later turn: the daemon observed running→waiting again, well outside the
	// output-hash dedup TTL, but the transcript signal is unchanged.
	second := n.NotifyTransition(build("jsonl:6022505", t0.Add(3*time.Hour)))
	if second.DeliveryResult != transitionDeliveryCommitted {
		t.Fatalf("second turn = %q, want committed", second.DeliveryResult)
	}
	if !second.OutputHashStale {
		t.Fatalf("second turn with an unchanged transcript signal must be flagged stale: %+v", second)
	}
	if lines := readInboxLines(t, parentID); len(lines) != 1 || !lines[0].OutputHashStale {
		t.Fatalf("stale flag must be visible on the durable record: %+v", lines)
	}
	got, err := DrainInboxForParent(parentID)
	if err != nil || len(got) != 1 {
		t.Fatalf("second drain must deliver the new completion: delivered=%d err=%v", len(got), err)
	}
	if got[0].TurnFingerprint == "" || got[0].TurnFingerprint == firstDrained[0].TurnFingerprint {
		t.Fatalf("a new completion with a stale hash must not reuse the consumed turn_fingerprint %q", firstDrained[0].TurnFingerprint)
	}
	if *sent != 2 {
		t.Fatalf("a deliverable stale-hash completion must nudge the parent: sent=%d, want 2", *sent)
	}
}

// True duplicates are still suppressed at every layer: a re-fire inside the
// short window, a same-hash re-fire inside the output-hash TTL, and a replay of
// an already-drained stale record (same stamped fingerprint).
func TestIssue2184_IdenticalRepeatStillDeduped(t *testing.T) {
	n, parentID, build, sent := newStaleHashNotifierFixture(t)
	t0 := time.Now().Add(-4 * time.Hour)

	if res := n.NotifyTransition(build("jsonl:6022505", t0)); res.DeliveryResult != transitionDeliveryCommitted {
		t.Fatalf("first turn = %q", res.DeliveryResult)
	}
	if _, err := DrainInboxForParent(parentID); err != nil {
		t.Fatalf("drain: %v", err)
	}
	stale := n.NotifyTransition(build("jsonl:6022505", t0.Add(3*time.Hour)))
	if stale.DeliveryResult != transitionDeliveryCommitted || !stale.OutputHashStale {
		t.Fatalf("stale-hash turn = %+v, want committed+stale", stale)
	}
	staleDrained, err := DrainInboxForParent(parentID)
	if err != nil || len(staleDrained) != 1 {
		t.Fatalf("stale-hash drain: delivered=%d err=%v", len(staleDrained), err)
	}
	staleRecord := staleDrained[0]
	if *sent != 2 {
		t.Fatalf("precondition: two nudges, got %d", *sent)
	}

	// Re-fire inside the 90s short window (a second daemon path observing the
	// same flip) and inside the output-hash TTL: both dropped by the notifier.
	if res := n.NotifyTransition(build("jsonl:6022505", t0.Add(3*time.Hour+30*time.Second))); res.DeliveryResult != transitionDeliveryDropped {
		t.Fatalf("short-window re-fire = %q, want dropped", res.DeliveryResult)
	}
	if res := n.NotifyTransition(build("jsonl:6022505", t0.Add(3*time.Hour+20*time.Minute))); res.DeliveryResult != transitionDeliveryDropped {
		t.Fatalf("same-hash re-fire inside TTL = %q, want dropped", res.DeliveryResult)
	}

	// Replay of the already-drained stale record (daemon restart re-delivering
	// the same stamped record): the consumed-turn ledger collapses it and no
	// nudge fires.
	if err := CommitToInbox(parentID, staleRecord); err != nil {
		t.Fatalf("replay commit: %v", err)
	}
	if got, err := DrainInboxForParent(parentID); err != nil || len(got) != 0 {
		t.Fatalf("replayed stale record must be deduped: delivered=%+v err=%v", got, err)
	}
	if *sent != 2 {
		t.Fatalf("duplicates fired a wake-nudge: sent=%d, want 2", *sent)
	}
}

// TurnFingerprint contract for the stale case: unchanged for healthy records,
// distinct from the stale-hash turn it would otherwise collide with, stable
// across a retry of the same stamped event, distinct across emit instants.
func TestIssue2184_TurnFingerprint_StaleFallsThroughToFlipAndInstant(t *testing.T) {
	healthy := TransitionNotificationEvent{
		ChildSessionID: "child-x", FromStatus: "running", ToStatus: "waiting",
		LastOutputHash: "jsonl:6022505", Timestamp: time.Unix(100, 0),
	}
	stale := healthy
	stale.OutputHashStale = true
	stale.Timestamp = time.Unix(200, 0)
	if TurnFingerprint(stale) == TurnFingerprint(healthy) {
		t.Fatalf("stale-hash turn must not share the fingerprint of the healthy turn")
	}
	retry := stale
	if TurnFingerprint(retry) != TurnFingerprint(stale) {
		t.Fatalf("retry of the same stale event must keep its fingerprint")
	}
	later := stale
	later.Timestamp = time.Unix(300, 0)
	if TurnFingerprint(later) == TurnFingerprint(stale) {
		t.Fatalf("two stale-hash turns at different instants must be distinct")
	}
	// A healthy record is untouched by the flag's absence: the re-emit contract
	// from #1225 still holds.
	reEmit := healthy
	reEmit.Timestamp = time.Unix(999, 0)
	if TurnFingerprint(reEmit) != TurnFingerprint(healthy) {
		t.Fatalf("healthy re-emit must keep its fingerprint")
	}
	// A finished event never falls through: the completion outcome is its
	// identity regardless of the flag.
	finished := TransitionNotificationEvent{
		ChildSessionID: "child-x", Kind: transitionKindFinished, DoneStatus: "ok",
		DoneSummary: "done", Timestamp: time.Unix(100, 0),
	}
	flagged := finished
	flagged.OutputHashStale = true
	flagged.Timestamp = time.Unix(200, 0)
	if TurnFingerprint(flagged) != TurnFingerprint(finished) {
		t.Fatalf("finished events must keep their outcome-keyed fingerprint")
	}
}

// attachTranscript gives the restart fixture's child a real transcript so the
// daemon derives a non-empty transcript signal for it, and returns the path.
func attachTranscript(t *testing.T, f *restartFixture, body string) string {
	t.Helper()
	f.child.ClaudeSessionID = "0f0f0f0f-2184-4184-8184-000000002184"
	if err := f.storage.SaveWithGroups([]*Instance{f.child, f.parent}, nil); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}
	dir := filepath.Join(GetClaudeConfigDir(), "projects", ConvertToClaudeDirName(f.child.ProjectPath))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir transcript dir: %v", err)
	}
	path := filepath.Join(dir, f.child.ClaudeSessionID+".jsonl")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	if got := transitionEventOutputHash(f.child); got == "" {
		t.Fatalf("precondition: transcript signal must resolve for %s", path)
	}
	return path
}

// Daemon-level reproduction: the child completes a second turn but its
// transcript signal is unchanged. The second turn must be committed as a
// stale-flagged record, delivered by the drain, and nudged.
func TestIssue2184_DaemonDeliversSecondTurnWithUnchangedTranscriptSignal(t *testing.T) {
	f := newRestartFixture(t, "running")
	attachTranscript(t, f, "{\"type\":\"user\"}\n")

	d := f.newDaemon()
	d.syncProfile(f.profile)
	f.setChildStatus(t, "waiting")
	d.syncProfile(f.profile)
	if got := readInboxLines(t, f.parent.ID); len(got) != 1 || got[0].OutputHashStale {
		t.Fatalf("first turn: one healthy record expected, got %+v", got)
	}
	if got, err := DrainInboxForParent(f.parent.ID); err != nil || len(got) != 1 {
		t.Fatalf("first drain: delivered=%d err=%v", len(got), err)
	}
	ageNotifyRecord(t, f.child.ID, 3*time.Hour)

	// Second turn, hours later, transcript untouched (the stale-signal condition
	// from the field report). A recycled daemon process observes it, as in the
	// report; the seed keeps the parked child silent until the real flip.
	d2 := f.newDaemon()
	d2.syncProfile(f.profile)
	f.setChildStatus(t, "running")
	d2.syncProfile(f.profile)
	f.setChildStatus(t, "waiting")
	d2.syncProfile(f.profile)

	got := readInboxLines(t, f.parent.ID)
	if len(got) != 1 || !got[0].OutputHashStale {
		t.Fatalf("second turn: one stale-flagged record expected, got %+v", got)
	}
	delivered, err := DrainInboxForParent(f.parent.ID)
	if err != nil || len(delivered) != 1 {
		t.Fatalf("second drain must deliver the new completion: delivered=%d err=%v", len(delivered), err)
	}
	if n := f.nudges(); n != 2 {
		t.Fatalf("expected a nudge per delivered turn (2), got %d", n)
	}
}

// #2242 guard: a daemon restart with the child still parked at the same status
// and the same transcript signal seeds silently. The stale-hash detection must
// not turn the restart into a re-notification.
func TestIssue2184_RestartSeedingUnchangedWithStableTranscriptSignal(t *testing.T) {
	f := newRestartFixture(t, "running")
	attachTranscript(t, f, "{\"type\":\"user\"}\n")

	d1 := f.newDaemon()
	d1.syncProfile(f.profile)
	f.setChildStatus(t, "waiting")
	d1.syncProfile(f.profile)
	if got := readInboxLines(t, f.parent.ID); len(got) != 1 {
		t.Fatalf("real transition must commit exactly one record, got %+v", got)
	}
	if _, err := DrainInboxForParent(f.parent.ID); err != nil {
		t.Fatalf("drain: %v", err)
	}
	ageNotifyRecord(t, f.child.ID, 3*time.Hour)

	d2 := f.newDaemon()
	d2.syncProfile(f.profile)
	d2.syncProfile(f.profile)

	if got := readInboxLines(t, f.parent.ID); len(got) != 0 {
		t.Fatalf("restart re-committed an already-notified turn: %+v", got)
	}
	if n := f.nudges(); n != 1 {
		t.Fatalf("restart fired a phantom wake-nudge: total nudges %d, want 1", n)
	}

	// A turn that completed while the daemon was down (transcript grew) is
	// still notified once after the restart, exactly as before.
	f.setChildStatus(t, "idle")
	d3 := f.newDaemon()
	d3.syncProfile(f.profile)
	d3.syncProfile(f.profile)
	if got := readInboxLines(t, f.parent.ID); len(got) != 1 || got[0].ToStatus != "idle" {
		t.Fatalf("status change during downtime must be notified once, got %+v", got)
	}
}
