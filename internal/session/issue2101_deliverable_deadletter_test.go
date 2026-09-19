package session

import (
	"os"
	"strings"
	"testing"
	"time"
)

// Messaging audit P1-4 (#2101, #2062): a completion for a REGISTERED parent
// whose conductor happened not to be "live" (running|waiting|idle) at resolve
// time was diverted to _unowned + dead-letter and never re-attached, and a
// worker the conductor removed within one daemon pass of its last turn became
// a `child_removed` dead letter. Both then made `inbox drain` exit 4 forever
// because nothing can ack a dead letter. The durable inbox exists precisely
// so that a parent that is not live right now receives the record at its next
// turn: commit to any registered parent; treat child_removed as log-only.

func seedRegisteredNotLiveConductor(t *testing.T, profile string) (parentID, childID string) {
	t.Helper()
	inboxTestHome(t)
	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	now := time.Now()
	// A conductor-titled parent with no tmux session behind it: UpdateStatus
	// resolves it to a non-live status (stopped/error), the exact case the
	// liveness gate used to reject.
	parent := &Instance{ID: "conductor-2101-parent", Title: "conductor-2101", ProjectPath: "/tmp/p", GroupPath: DefaultGroupPath, Tool: "claude", Status: StatusStopped, CreatedAt: now}
	child := &Instance{ID: "worker-2101", Title: "worker", ProjectPath: "/tmp/c", GroupPath: DefaultGroupPath, ParentSessionID: parent.ID, Tool: "claude", Status: StatusWaiting, CreatedAt: now}
	if err := storage.SaveWithGroups([]*Instance{parent, child}, nil); err != nil {
		t.Fatal(err)
	}
	return parent.ID, child.ID
}

func TestResolveParentNotificationTarget_RegisteredParentNotLiveStillResolves(t *testing.T) {
	child := &Instance{ID: "child", Title: "task", ParentSessionID: "parent"}
	parent := &Instance{ID: "parent", Title: "conductor-x", Status: StatusStopped, Tool: "claude", ProjectPath: t.TempDir()}
	got := resolveParentNotificationTarget(child, map[string]*Instance{"child": child, "parent": parent})
	if got == nil || got.ID != "parent" {
		t.Fatalf("a registered parent must resolve regardless of conductor liveness, got %#v", got)
	}
}

func TestIssue2101_FinishedForRegisteredNotLiveParentCommitsToItsInbox(t *testing.T) {
	profile := "_test-2101-not-live"
	parentID, childID := seedRegisteredNotLiveConductor(t, profile)

	n := NewTransitionNotifier()
	t.Cleanup(n.Close)
	got := n.NotifyFinished(TransitionNotificationEvent{
		ChildSessionID: childID, ChildTitle: "worker", Profile: profile,
		DoneStatus: "ok", DoneSummary: "shipped", Timestamp: time.Now(),
	})
	if got.DeliveryResult != transitionDeliveryCommitted {
		t.Fatalf("completion must commit, got %+v", got)
	}
	events, err := DrainInboxForParent(parentID)
	if err != nil || len(events) != 1 || events[0].DoneSummary != "shipped" {
		t.Fatalf("parent inbox did not receive the completion: events=%+v err=%v", events, err)
	}
	if got := readInboxLines(t, UnownedInboxID); len(got) != 0 {
		t.Fatalf("a deliverable completion must not be parked in _unowned: %+v", got)
	}
	if n, _ := CountDeadLetterRecords(); n != 0 {
		t.Fatalf("a deliverable completion must not be dead-lettered, count=%d", n)
	}
}

func TestIssue2101_ChildRemovedIsLogOnlyNotDeadLetter(t *testing.T) {
	inboxTestHome(t)
	profile := "_test-2101-child-removed"
	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	// Registry with only the parent: the child was removed between observe
	// and resolve.
	parent := &Instance{ID: "parent-2101", Title: "manager", ProjectPath: "/tmp/p", GroupPath: DefaultGroupPath, Tool: "shell", Status: StatusWaiting, CreatedAt: time.Now()}
	if err := storage.SaveWithGroups([]*Instance{parent}, nil); err != nil {
		t.Fatal(err)
	}
	storage.Close()

	n := NewTransitionNotifier()
	t.Cleanup(n.Close)
	got := n.NotifyTransition(TransitionNotificationEvent{
		ChildSessionID: "gone-child-2101", ChildTitle: "worker", Profile: profile,
		FromStatus: "running", ToStatus: "waiting", Timestamp: time.Now(),
	})
	if got.DeliveryResult != transitionDeliveryDropped || got.DeadLetterReason != deadLetterReasonChildMissing {
		t.Fatalf("removed child must be a terminal drop with reason child_removed, got %+v", got)
	}
	if count, _ := CountDeadLetterRecords(); count != 0 {
		t.Fatalf("child_removed must not create a dead letter (nothing can ack it), count=%d", count)
	}
	if _, err := os.Stat(DeadLetterPathFor("gone-child-2101")); !os.IsNotExist(err) {
		t.Fatalf("dead-letter file must not exist for child_removed: %v", err)
	}
	// It IS logged for the operator.
	raw, err := os.ReadFile(n.missedPath)
	if err != nil || !strings.Contains(string(raw), "gone-child-2101") || !strings.Contains(string(raw), deadLetterReasonChildMissing) {
		t.Fatalf("child_removed must be logged to the missed log: err=%v raw=%q", err, raw)
	}
}

func TestIssue2101_ReplayAcksChildRemovedCompletionWithoutDeadLetter(t *testing.T) {
	inboxTestHome(t)
	profile := "_test-2101-replay"
	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.SaveWithGroups([]*Instance{{ID: "parent-2101r", Title: "manager", ProjectPath: "/tmp/p", GroupPath: DefaultGroupPath, Tool: "shell", Status: StatusWaiting, CreatedAt: time.Now()}}, nil); err != nil {
		t.Fatal(err)
	}
	storage.Close()
	if err := WriteCompletionRecord(CompletionRecord{ChildID: "gone-child-2101r", Title: "worker", Profile: profile, Status: "ok", Summary: "done", FinishedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	d := NewTransitionDaemon()
	for i := 0; i < MaxUnresolvedAttempts+1; i++ {
		d.ReplayUnackedCompletions(profile)
	}
	recs, err := LoadCompletionRecords(profile)
	if err != nil || len(recs) != 1 {
		t.Fatalf("records=%+v err=%v", recs, err)
	}
	if !recs[0].Acked {
		t.Fatalf("a completion whose child is gone must be acked (it can never be delivered), got %+v", recs[0])
	}
	if count, _ := CountDeadLetterRecords(); count != 0 {
		t.Fatalf("child_removed replay must not dead-letter, count=%d", count)
	}
}
