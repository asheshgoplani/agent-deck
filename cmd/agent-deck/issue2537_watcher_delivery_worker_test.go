package main

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/sendqueue"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/watcher"
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

// composerGuardChild stands in for the `session send` child at a conductor
// whose composer is occupied: while occupied it answers what the child
// answers when the composer guard refuses (nothing typed, the draft kept);
// once the composer is free it types the message and reports it submitted.
type composerGuardChild struct {
	mu       sync.Mutex
	occupied bool
	refusals int
	typed    []string
}

func (c *composerGuardChild) send(_, _, message, resultPath string) (int, func() int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.occupied {
		c.refusals++
		_ = os.WriteFile(resultPath, []byte(`{"success":false,"error":"message not delivered to 'conductor-demo': message not sent: composer is occupied or unreadable; existing draft preserved","code":"DELIVERY_FAILED","delivery":"composer_blocked","submitted":false,"confirmation":"failed"}`), 0o600)
		return 4242, func() int { return 1 }, nil
	}
	c.typed = append(c.typed, message)
	_ = os.WriteFile(resultPath, []byte(`{"success":true,"delivery":"submitted","submitted":true,"confirmation":"confirmed"}`), 0o600)
	return 4242, func() int { return 0 }, nil
}

func (c *composerGuardChild) state() (refusals int, typed []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.refusals, append([]string(nil), c.typed...)
}

func (c *composerGuardChild) free() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.occupied = false
}

// TestIssue2537_ARoutedEventTheComposerGuardRefusesIsDeliveredOnceItClears:
// the case behind #2537 that the composer guard (#1409) used to end. The
// engine routes an event to a conductor whose composer stays occupied, so
// every delivery attempt is refused with nothing typed. Before, the relay
// dropped the event on the first refusal. Now it stays queued, the worker
// retries it with the capped backoff, with no deadline by default, and types
// it exactly once when the composer clears, with no manual step.
func TestIssue2537_ARoutedEventTheComposerGuardRefusesIsDeliveredOnceItClears(t *testing.T) {
	t.Setenv("AGENTDECK_EVENTS_BUS", "off")
	t.Setenv("AGENTDECK_SEND_WORKER_POLL", "40ms")
	t.Setenv("AGENTDECK_SEND_RETRY_BACKOFF_MAX", "200ms")
	t.Setenv("AGENTDECK_SEND_LAND_WINDOW", "300ms")
	// A profile of its own per run: the outbox finds the conductor by its
	// title, and a rerun must not find the previous run's session.
	f := newStoppedTargetFixture(t, "_test_2537_composer_guard_"+strconv.FormatInt(time.Now().UnixNano(), 36))
	if err := f.target.GetTmuxSession().Start("bash"); err != nil {
		t.Fatalf("start the conductor's pane: %v", err)
	}
	child := &composerGuardChild{occupied: true}
	prev := sendChild
	sendChild = child.send
	t.Cleanup(func() { sendChild = prev })

	// The engine's outbox queues the routed event for the conductor.
	storage, err := session.NewStorageWithProfile(f.profile)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	outbox := watcher.NewConductorOutbox(watcher.OutboxConfig{
		Profile: f.profile, Dir: f.dir, DB: storage.GetDB(),
		SpawnWorker: func(string, string) error { return nil },
	})
	evt := watcher.Event{Source: "slack", Sender: "slack:alice", Subject: "deploy", Body: "can you check the deploy?", RoutedTo: "demo"}
	if err := outbox.Enqueue("w-slack", "slack", evt); err != nil {
		t.Fatal(err)
	}
	recs, err := sendqueue.List(f.dir, f.target.ID)
	if err != nil || len(recs) != 1 {
		t.Fatalf("queued %d records (%v), want the routed event", len(recs), err)
	}
	rec := recs[0]
	if rec.Deadline != "" {
		t.Fatalf("deadline %q: a routed event must not run out while the composer stays occupied", rec.Deadline)
	}
	id := rec.SendID

	done := make(chan struct{})
	go func() { deliverQueued(f.profile, f.dir, rec); close(done) }()
	// Several refused attempts, each back to queued (between attempts the
	// record is typing while the child runs).
	deadline := time.Now().Add(20 * time.Second)
	for {
		n, typed := child.state()
		if len(typed) != 0 {
			t.Fatalf("typed %q while the composer was occupied", typed)
		}
		if waiting, err := sendqueue.Load(f.dir, id); err == nil && n >= 3 &&
			waiting.State == sendqueue.StateQueued && strings.Contains(waiting.Reason, "composer_blocked") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the worker stopped retrying the refused event after %d refusals", n)
		}
		time.Sleep(10 * time.Millisecond)
	}

	child.free() // the composer clears; nobody touches the queue
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the event was not delivered after the composer cleared")
	}
	refusals, typed := child.state()
	if len(typed) != 1 || typed[0] != watcher.ConductorMessage(evt) {
		t.Fatalf("typed %q, want the routed event once", typed)
	}
	got, err := sendqueue.Load(f.dir, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != sendqueue.StateSubmitted || !got.Final() || got.Attempts != refusals+1 {
		t.Fatalf("record = %+v after %d refusals, want submitted on the attempt after them", got, refusals)
	}
}
