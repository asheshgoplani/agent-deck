package main

import (
	"errors"
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

// TestIssue2537_AnEventForAReplacedConductorSessionMovesToItsSuccessor: a
// cross-harness switch archived the conductor session the event was queued
// for, kept its pane running, and activated a successor under the same
// title. The worker moves the event to the successor and starts the
// successor's worker, typing nothing into the replaced pane; that worker
// then delivers it once.
func TestIssue2537_AnEventForAReplacedConductorSessionMovesToItsSuccessor(t *testing.T) {
	t.Setenv("AGENTDECK_EVENTS_BUS", "off")
	t.Setenv("AGENTDECK_SEND_WORKER_POLL", "40ms")
	t.Setenv("AGENTDECK_SEND_RETRY_BACKOFF_MAX", "100ms")
	t.Setenv("AGENTDECK_SEND_LAND_WINDOW", "300ms")
	skipIfNoTmuxBinaryCLI(t)
	profile := "_test_2537_handoff_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	source := session.NewInstanceWithTool("conductor-demo", t.TempDir(), "shell")
	successor := session.NewInstanceWithTool("conductor-demo", t.TempDir(), "shell")
	for _, inst := range []*session.Instance{source, successor} {
		inst.Status = session.StatusRunning
		inst.GroupPath = session.DefaultGroupPath
		pane := inst.GetTmuxSession()
		if err := pane.Start("bash"); err != nil {
			t.Fatalf("start a conductor pane: %v", err)
		}
		t.Cleanup(func() { _ = pane.Kill() })
	}
	source.ArchivedAt, source.SupersededBy = time.Now().UTC(), successor.ID
	successor.Supersedes = source.ID
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := saveSessionData(storage, []*session.Instance{source, successor}, nil); err != nil {
		t.Fatal(err)
	}
	_ = storage.Close()
	f := retryFixture{profile: profile, dir: t.TempDir(), target: source}
	starts := stubChild(t, 0)
	var mu sync.Mutex
	var workers []string
	prev := startSendWorker
	startSendWorker = func(_, id string) error {
		mu.Lock()
		defer mu.Unlock()
		workers = append(workers, id)
		return nil
	}
	t.Cleanup(func() { startSendWorker = prev })

	rec := f.queueWatcherEvent(t, 0)
	done := make(chan struct{})
	go func() { deliverQueued(f.profile, f.dir, rec); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the worker held on to the event of a replaced conductor session")
	}
	moved, err := sendqueue.Load(f.dir, rec.SendID)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(starts()); n != 0 || moved.SessionID != successor.ID || moved.State != sendqueue.StateQueued {
		t.Fatalf("after the handoff: %d child starts, record %+v; want it queued for %s, nothing typed", n, moved, successor.ID)
	}
	mu.Lock()
	started := strings.Join(workers, ",")
	mu.Unlock()
	if started != successor.ID {
		t.Fatalf("workers started for %q, want only the successor %s", started, successor.ID)
	}

	deliverQueued(f.profile, f.dir, moved)
	got, err := sendqueue.Load(f.dir, rec.SendID)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(starts()); n != 1 || got.SessionID != successor.ID || got.State != sendqueue.StateSubmitted {
		t.Fatalf("the successor's worker: %d child starts, record %+v; want it submitted once", n, got)
	}
}

// newHandoffFixture is a conductor session that a cross-harness switch
// replaced: source is archived with its pane still running, and successor is
// the active session under the same title. Both panes run bash.
func newHandoffFixture(t *testing.T, name string) (f retryFixture, source, successor *session.Instance) {
	t.Helper()
	skipIfNoTmuxBinaryCLI(t)
	profile := name + "_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	source = session.NewInstanceWithTool("conductor-demo", t.TempDir(), "shell")
	successor = session.NewInstanceWithTool("conductor-demo", t.TempDir(), "shell")
	for _, inst := range []*session.Instance{source, successor} {
		inst.Status = session.StatusRunning
		inst.GroupPath = session.DefaultGroupPath
		pane := inst.GetTmuxSession()
		if err := pane.Start("bash"); err != nil {
			t.Fatalf("start a conductor pane: %v", err)
		}
		t.Cleanup(func() { _ = pane.Kill() })
	}
	source.ArchivedAt, source.SupersededBy = time.Now().UTC(), successor.ID
	successor.Supersedes = source.ID
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := saveSessionData(storage, []*session.Instance{source, successor}, nil); err != nil {
		t.Fatal(err)
	}
	_ = storage.Close()
	return retryFixture{profile: profile, dir: t.TempDir(), target: source}, source, successor
}

// queueRoutedEvent writes a routed event's record for target under key.
func (f retryFixture) queueRoutedEvent(t *testing.T, target *session.Instance, key, message string) *sendqueue.Record {
	t.Helper()
	rec, created, err := sendqueue.EnqueueOnce(f.profile, f.dir, sendqueue.Send{
		SessionID: target.ID, SessionTitle: target.Title, Tool: target.Tool,
		Message: message, Sender: "watcher:hook", Key: key, WaitWhileStopped: true,
	}, time.Now())
	if err != nil || !created {
		t.Fatalf("queue %s: created %v, %v", key, created, err)
	}
	return rec
}

// TestIssue2537_AReplacedSessionsEventsGoBeforeItsSuccessorsOwn: E1 waits
// for a conductor session a switch replaced, whose worker is still running
// (its lock is held), and E2 is routed to the successor afterwards. The
// successor's worker types nothing while the replaced session's worker may
// still hand E1 over; once that worker is gone it moves E1 itself, under
// that worker's lock, and types E1 before E2.
func TestIssue2537_AReplacedSessionsEventsGoBeforeItsSuccessorsOwn(t *testing.T) {
	t.Setenv("AGENTDECK_EVENTS_BUS", "off")
	t.Setenv("AGENTDECK_SEND_WORKER_POLL", "40ms")
	t.Setenv("AGENTDECK_SEND_RETRY_BACKOFF_MAX", "100ms")
	f, source, successor := newHandoffFixture(t, "_test_2537_handoff_order")
	var mu sync.Mutex
	var typed []string
	prevChild := sendChild
	sendChild = func(_, id, message, resultPath string) (int, func() int, error) {
		mu.Lock()
		typed = append(typed, message)
		mu.Unlock()
		_ = os.WriteFile(resultPath, []byte(`{"success":true,"submitted":true,"delivery":"submitted"}`), 0o600)
		return 4242, func() int { return 0 }, nil
	}
	t.Cleanup(func() { sendChild = prevChild })
	prevStart := startSendWorker
	startSendWorker = func(string, string) error { return nil }
	t.Cleanup(func() { startSendWorker = prevStart })
	typedNow := func() string {
		mu.Lock()
		defer mu.Unlock()
		return strings.Join(typed, ",")
	}

	e1 := f.queueRoutedEvent(t, source, "watcher:w-hook:e1", "E1")
	e2 := f.queueRoutedEvent(t, successor, "watcher:w-hook:e2", "E2")
	sourceWorker, ok, err := sendqueue.TryLock(f.dir, source.ID)
	if err != nil || !ok {
		t.Fatalf("hold the replaced session's worker lock: %v %v", ok, err)
	}
	done := make(chan struct{})
	go func() { runSendWorker(f.profile, f.dir, successor.ID); close(done) }()
	time.Sleep(400 * time.Millisecond)
	if got := typedNow(); got != "" {
		t.Fatalf("typed %q while the replaced session's worker still held E1, want nothing", got)
	}

	if err := sourceWorker.Release(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatalf("the successor's worker did not finish (typed %q)", typedNow())
	}
	if got := typedNow(); got != "E1,E2" {
		t.Fatalf("typed %q, want E1 then E2", got)
	}
	for _, id := range []string{e1.SendID, e2.SendID} {
		r, err := sendqueue.Load(f.dir, id)
		if err != nil || r.SessionID != successor.ID || r.State != sendqueue.StateSubmitted {
			t.Fatalf("record %s: %+v, %v; want it submitted to the successor", id, r, err)
		}
	}
}

// TestIssue2537_BothWorkersRunningKeepTheRoutedOrder: the replaced
// session's worker and the successor's worker run at once, with E1 queued
// on the replaced session and E2 on the successor. Whichever moves E1, the
// successor's worker types E1 before E2 and both reach the successor.
func TestIssue2537_BothWorkersRunningKeepTheRoutedOrder(t *testing.T) {
	t.Setenv("AGENTDECK_EVENTS_BUS", "off")
	t.Setenv("AGENTDECK_SEND_WORKER_POLL", "40ms")
	t.Setenv("AGENTDECK_SEND_RETRY_BACKOFF_MAX", "100ms")
	f, source, successor := newHandoffFixture(t, "_test_2537_handoff_both")
	var mu sync.Mutex
	var typed []string
	prevChild := sendChild
	sendChild = func(_, id, message, resultPath string) (int, func() int, error) {
		mu.Lock()
		typed = append(typed, message+"@"+id)
		mu.Unlock()
		_ = os.WriteFile(resultPath, []byte(`{"success":true,"submitted":true,"delivery":"submitted"}`), 0o600)
		return 4242, func() int { return 0 }, nil
	}
	t.Cleanup(func() { sendChild = prevChild })
	prevStart := startSendWorker
	startSendWorker = func(string, string) error { return nil }
	t.Cleanup(func() { startSendWorker = prevStart })

	f.queueRoutedEvent(t, source, "watcher:w-hook:e1", "E1")
	f.queueRoutedEvent(t, successor, "watcher:w-hook:e2", "E2")
	var wg sync.WaitGroup
	for _, id := range []string{successor.ID, source.ID} {
		wg.Add(1)
		go func(id string) { defer wg.Done(); runSendWorker(f.profile, f.dir, id) }(id)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the workers did not finish")
	}
	mu.Lock()
	got := strings.Join(typed, ",")
	mu.Unlock()
	if want := "E1@" + successor.ID + ",E2@" + successor.ID; got != want {
		t.Fatalf("typed %q, want %q", got, want)
	}
}

// TestIssue2537_ARoutedEventOutlastsASessionLoadError: the sessions cannot
// be read for a while. A routed event keeps waiting instead of failing, and
// is delivered once they read again.
func TestIssue2537_ARoutedEventOutlastsASessionLoadError(t *testing.T) {
	t.Setenv("AGENTDECK_EVENTS_BUS", "off")
	t.Setenv("AGENTDECK_SEND_WORKER_POLL", "40ms")
	t.Setenv("AGENTDECK_SEND_RETRY_BACKOFF_MAX", "100ms")
	t.Setenv("AGENTDECK_SEND_LAND_WINDOW", "300ms")
	f := newStoppedTargetFixture(t, "_test_2537_load_error_"+strconv.FormatInt(time.Now().UnixNano(), 36))
	starts := stubChild(t, 0)
	if err := f.target.GetTmuxSession().Start("bash"); err != nil {
		t.Fatalf("start the conductor's pane: %v", err)
	}
	var mu sync.Mutex
	unreadable, failed := true, 0
	prev := loadSessions
	loadSessions = func(profile string) ([]*session.Instance, error) {
		mu.Lock()
		defer mu.Unlock()
		if unreadable {
			failed++
			return nil, errors.New("database is locked")
		}
		return prev(profile)
	}
	t.Cleanup(func() { loadSessions = prev })
	rec := f.queueWatcherEvent(t, 0)
	id := rec.SendID

	done := make(chan struct{})
	go func() { deliverQueued(f.profile, f.dir, rec); close(done) }()
	time.Sleep(500 * time.Millisecond)
	waiting, err := sendqueue.Load(f.dir, id)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	tries := failed
	unreadable = false
	mu.Unlock()
	if waiting.State != sendqueue.StateQueued || len(starts()) != 0 || tries < 2 {
		t.Fatalf("after %d failed loads: record %+v, %d child starts; want it still queued, retried", tries, waiting, len(starts()))
	}

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the event was not delivered once the sessions read again")
	}
	got, err := sendqueue.Load(f.dir, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != sendqueue.StateSubmitted || len(starts()) != 1 {
		t.Fatalf("after the sessions read again: record %+v, %d child starts; want it submitted once", got, len(starts()))
	}
}

// TestIssue2537_ASuccessorWaitingOnAHandoffStillAnswersQueueControls: while
// the successor's worker waits for the replaced session's worker to hand
// E1 over, a cancel for the successor's own E2 is answered at once, not
// left to time out.
func TestIssue2537_ASuccessorWaitingOnAHandoffStillAnswersQueueControls(t *testing.T) {
	t.Setenv("AGENTDECK_EVENTS_BUS", "off")
	t.Setenv("AGENTDECK_SEND_WORKER_POLL", "40ms")
	f, source, successor := newHandoffFixture(t, "_test_2537_handoff_control")
	starts := stubChild(t, 0)
	prevStart := startSendWorker
	startSendWorker = func(string, string) error { return nil }
	t.Cleanup(func() { startSendWorker = prevStart })

	f.queueRoutedEvent(t, source, "watcher:w-hook:e1", "E1")
	e2 := f.queueRoutedEvent(t, successor, "watcher:w-hook:e2", "E2")
	sourceWorker, ok, err := sendqueue.TryLock(f.dir, source.ID)
	if err != nil || !ok {
		t.Fatalf("hold the replaced session's worker lock: %v %v", ok, err)
	}
	t.Cleanup(func() { _ = sourceWorker.Release() })
	done := make(chan struct{})
	go func() { runSendWorker(f.profile, f.dir, successor.ID); close(done) }()
	t.Cleanup(func() {
		_ = sourceWorker.Release()
		<-done
	})

	if err := requestQueueControl(f.dir, e2.SendID, "cancel"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(sendqueue.ControlResultPath(f.dir, e2.SendID)); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the cancel was not answered while the worker waited on the handoff")
		}
		time.Sleep(20 * time.Millisecond)
	}
	got, err := sendqueue.Load(f.dir, e2.SendID)
	if err != nil || got.State != sendqueue.StateCancelled || len(starts()) != 0 {
		t.Fatalf("E2 after the cancel: %+v (%v), %d child starts; want it cancelled, nothing typed", got, err, len(starts()))
	}
}

// TestIssue2537_AHungChildSendIsKilledAndTheQueueMovesOn: a `session send`
// child that never exits is killed after the child wait, and its send
// settles as typed with an unknown outcome, never typed again, so the
// target's queue moves on (and a successor waiting on it is not held up
// for good).
func TestIssue2537_AHungChildSendIsKilledAndTheQueueMovesOn(t *testing.T) {
	t.Setenv("AGENTDECK_EVENTS_BUS", "off")
	t.Setenv("AGENTDECK_SEND_WORKER_POLL", "40ms")
	t.Setenv("AGENTDECK_SEND_CHILD_WAIT", "300ms")
	f := newRetryFixture(t, "_test_2537_hung_child_"+strconv.FormatInt(time.Now().UnixNano(), 36))
	hang := make(chan struct{})
	var dead sync.Once
	t.Cleanup(func() { dead.Do(func() { close(hang) }) })
	var mu sync.Mutex
	var starts int
	var killed []int
	prevChild := sendChild
	sendChild = func(_, _, _, _ string) (int, func() int, error) {
		mu.Lock()
		starts++
		mu.Unlock()
		return 4242, func() int { <-hang; return -1 }, nil
	}
	t.Cleanup(func() { sendChild = prevChild })
	prevKill := killChild
	killChild = func(pid int) {
		mu.Lock()
		killed = append(killed, pid)
		mu.Unlock()
		dead.Do(func() { close(hang) }) // the killed child exits
	}
	t.Cleanup(func() { killChild = prevKill })

	rec := f.queue(t, time.Hour, "cli")
	done := make(chan struct{})
	go func() { deliverQueuedAsync(f.profile, f.dir, rec); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the worker stayed on a hung child send")
	}
	got, err := sendqueue.Load(f.dir, rec.SendID)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if got.State != sendqueue.StateTyped || starts != 1 || len(killed) != 1 || killed[0] != 4242 {
		t.Fatalf("after the child wait: record %+v, %d child starts, killed %v; want it typed once and the child killed", got, starts, killed)
	}
}

// TestIssue2537_WaitChildKillsAHungChildThroughItsHandleAndReapsIt: a real
// child that outlives the wait is killed through its process handle and
// reaped before waitChild returns, and once reaped it leaves liveChildren,
// so no later kill can reach a process that reused its pid.
func TestIssue2537_WaitChildKillsAHungChildThroughItsHandleAndReapsIt(t *testing.T) {
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Skipf("start sleep: %v", err)
	}
	pid := cmd.Process.Pid
	liveChildren.Store(pid, cmd.Process)
	wait := func() int {
		_ = cmd.Wait()
		liveChildren.Delete(pid)
		return -1
	}
	start := time.Now()
	if _, exited := waitChild(pid, wait, 200*time.Millisecond); exited {
		t.Fatal("waitChild reported a hung child as exited on its own")
	}
	if waited := time.Since(start); waited > 5*time.Second {
		t.Fatalf("waitChild took %s for a killed child", waited)
	}
	if cmd.ProcessState == nil {
		t.Fatal("the killed child was not reaped before waitChild returned")
	}
	if _, ok := liveChildren.Load(pid); ok {
		t.Fatal("the reaped child is still registered for a kill")
	}
}

// TestIssue2537_AHandoffWhoseSuccessorWorkerCannotStartIsTriedAgain: the
// successor's worker fails to start. The event stays with the replaced
// session, untyped, instead of moving to a session no worker serves, and
// moves on a later poll once the worker starts.
func TestIssue2537_AHandoffWhoseSuccessorWorkerCannotStartIsTriedAgain(t *testing.T) {
	t.Setenv("AGENTDECK_EVENTS_BUS", "off")
	t.Setenv("AGENTDECK_SEND_WORKER_POLL", "40ms")
	t.Setenv("AGENTDECK_SEND_RETRY_BACKOFF_MAX", "100ms")
	f, source, successor := newHandoffFixture(t, "_test_2537_handoff_spawn")
	starts := stubChild(t, 0)
	var mu sync.Mutex
	var tries []string
	prev := startSendWorker
	startSendWorker = func(_, id string) error {
		mu.Lock()
		defer mu.Unlock()
		tries = append(tries, id)
		if len(tries) == 1 {
			return errors.New("fork failed")
		}
		return nil
	}
	t.Cleanup(func() { startSendWorker = prev })

	rec := f.queueRoutedEvent(t, source, "watcher:w-hook:k1", "routed")
	done := make(chan struct{})
	go func() { deliverQueued(f.profile, f.dir, rec); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the worker never handed the event over")
	}
	got, err := sendqueue.Load(f.dir, rec.SendID)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	n := len(tries)
	mu.Unlock()
	if n != 2 || got.SessionID != successor.ID || got.State != sendqueue.StateQueued || len(starts()) != 0 {
		t.Fatalf("after %d worker starts: record %+v, %d child starts; want it moved on the second start, untyped", n, got, len(starts()))
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
