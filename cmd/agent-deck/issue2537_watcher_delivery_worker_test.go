package main

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/sendqueue"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// The send worker's side of durable watcher delivery (#2537): a routed
// event's record waits for a stopped conductor instead of failing, and is
// delivered at most once.

// newStoppedTargetFixture is newRetryFixture with the target's pane not
// started: a conductor that is stopped.
func newStoppedTargetFixture(t *testing.T, profile string) retryFixture {
	t.Helper()
	skipIfNoTmuxBinaryCLI(t)
	target := session.NewInstanceWithTool("conductor-demo", t.TempDir(), "shell")
	target.Status = session.StatusStopped
	target.GroupPath = session.DefaultGroupPath
	t.Cleanup(func() { _ = target.GetTmuxSession().Kill() })
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := saveSessionData(storage, []*session.Instance{target}, nil); err != nil {
		t.Fatal(err)
	}
	_ = storage.Close()
	return retryFixture{profile: profile, dir: t.TempDir(), target: target}
}

// queueWatcherEvent writes the record ConductorOutbox queues for a routed
// event; deadline 0 means none.
func (f retryFixture) queueWatcherEvent(t *testing.T, deadline time.Duration) *sendqueue.Record {
	t.Helper()
	s := sendqueue.Send{
		SessionID: f.target.ID, SessionTitle: f.target.Title, Tool: f.target.Tool,
		Message: "[webhook] alice@example.com: issue-2537", Sender: "watcher:hook",
		Key: "watcher:w-hook:k1", WaitWhileStopped: true,
	}
	if deadline > 0 {
		s.Deadline = time.Now().Add(deadline)
	}
	rec, created, err := sendqueue.EnqueueOnce(f.profile, f.dir, s, time.Now())
	if err != nil || !created {
		t.Fatalf("queue the routed event: created %v, %v", created, err)
	}
	return rec
}

// TestIssue2537_WorkerWaitsForAStoppedConductorAndDeliversOnce: the
// conductor is stopped when the event's turn comes. The worker waits instead
// of failing it, types nothing meanwhile, and delivers it once the conductor
// is back.
func TestIssue2537_WorkerWaitsForAStoppedConductorAndDeliversOnce(t *testing.T) {
	t.Setenv("AGENTDECK_EVENTS_BUS", "off")
	t.Setenv("AGENTDECK_SEND_WORKER_POLL", "40ms")
	t.Setenv("AGENTDECK_SEND_RETRY_BACKOFF_MAX", "100ms")
	t.Setenv("AGENTDECK_SEND_LAND_WINDOW", "300ms")
	f := newStoppedTargetFixture(t, "_test_2537_wait_stopped")
	starts := stubChild(t, 0)
	rec := f.queueWatcherEvent(t, 0)
	id := rec.SendID

	done := make(chan struct{})
	go func() { deliverQueued(f.profile, f.dir, rec); close(done) }()
	time.Sleep(600 * time.Millisecond)
	waiting, err := sendqueue.Load(f.dir, id)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(starts()); n != 0 || waiting.State != sendqueue.StateQueued || waiting.TargetStatus != targetStatusStopped {
		t.Fatalf("while the conductor is stopped: %d child starts, record %+v", n, waiting)
	}

	if err := f.target.GetTmuxSession().Start("bash"); err != nil {
		t.Fatalf("start the conductor's pane: %v", err)
	}
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the worker did not deliver once the conductor was back")
	}
	got, err := sendqueue.Load(f.dir, id)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(starts()); n != 1 || got.Attempts != 1 || got.State != sendqueue.StateSubmitted || !got.Final() {
		t.Fatalf("after the conductor came back: %d child starts, record %+v", n, got)
	}
}

// TestIssue2537_WorkerFailsAStoppedConductorWhenTheDeadlinePasses: with
// [watcher] delivery_deadline set, a conductor still stopped at the deadline
// fails the delivery with that reason and nothing is typed. A send without
// WaitWhileStopped (`session send --queue`) still fails at once.
func TestIssue2537_WorkerFailsAStoppedConductorWhenTheDeadlinePasses(t *testing.T) {
	t.Setenv("AGENTDECK_EVENTS_BUS", "off")
	t.Setenv("AGENTDECK_SEND_WORKER_POLL", "40ms")
	t.Setenv("AGENTDECK_SEND_RETRY_BACKOFF_MAX", "100ms")
	f := newStoppedTargetFixture(t, "_test_2537_deadline")
	starts := stubChild(t, 0)

	rec := f.queueWatcherEvent(t, 400*time.Millisecond)
	deliverQueued(f.profile, f.dir, rec)
	got, _ := sendqueue.Load(f.dir, rec.SendID)
	if got.State != sendqueue.StateFailed || got.Reason != "target not running when the delivery deadline passed" {
		t.Fatalf("record past the deadline: %+v", got)
	}

	plain := f.queue(t, time.Hour, "cli")
	deliverQueued(f.profile, f.dir, plain)
	got, _ = sendqueue.Load(f.dir, plain.SendID)
	if got.State != sendqueue.StateFailed || got.Reason != "target not running" {
		t.Fatalf("a --queue send to a stopped target: %+v", got)
	}
	if n := len(starts()); n != 0 {
		t.Fatalf("%d child starts, want none", n)
	}
}

// TestIssue2537_AWorkerThatDiedMidDeliveryNeverTypesTheEventAgain: the
// worker died after the routed event's record moved to typing, and its child
// left no result. The next worker settles it as typed with an unknown
// outcome, even once the conductor is back: it never pastes it a second
// time.
func TestIssue2537_AWorkerThatDiedMidDeliveryNeverTypesTheEventAgain(t *testing.T) {
	t.Setenv("AGENTDECK_EVENTS_BUS", "off")
	t.Setenv("AGENTDECK_SEND_WORKER_POLL", "40ms")
	t.Setenv("AGENTDECK_SEND_LAND_WINDOW", "300ms")
	f := newStoppedTargetFixture(t, "_test_2537_crash_typing")
	calls := forbidSendChild(t)
	rec := f.queueWatcherEvent(t, 0)
	dead := exec.Command("true")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	rec, err := sendqueue.Update(f.dir, rec.SendID, time.Now(), func(r *sendqueue.Record) {
		r.State, r.Attempts, r.ChildPID = sendqueue.StateTyping, 1, dead.Process.Pid
		r.SentAt = time.Now().UTC().Format(time.RFC3339Nano)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.target.GetTmuxSession().Start("bash"); err != nil {
		t.Fatalf("start the conductor's pane: %v", err)
	}

	deliverQueued(f.profile, f.dir, rec)

	got, _ := sendqueue.Load(f.dir, rec.SendID)
	if *calls != 0 || got.Attempts != 1 {
		t.Fatalf("typed again: %d child starts, attempts %d", *calls, got.Attempts)
	}
	if got.State != sendqueue.StateTyped || !got.Settled || !strings.Contains(got.Reason, "outcome unknown") {
		t.Fatalf("record = %+v, want settled typed with an unknown outcome", got)
	}
	if next := nextPending(f.dir, f.target.ID); next != nil {
		t.Fatalf("the settled event is still pending: %+v", next)
	}
}
