package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The deferred-retry queue (L1) is the bug-fix core of v1.7.45. Before this
// release, when a child transitioned running→waiting while its parent was
// mid-tool-call (StatusRunning), the notifier logged delivery_result=deferred
// and returned — but the daemon then updated lastStatus[id]="waiting", so
// the next poll saw waiting→waiting (no transition) and the event was gone
// forever. Production logs over the last 24h show 105/198 events (53%) taking
// this silent-loss path.
//
// Each test below is RED on v1.7.43 and GREEN after the queue lands.

func newQueueTestNotifier(t *testing.T) *TransitionNotifier {
	t.Helper()
	dir := t.TempDir()
	return &TransitionNotifier{
		statePath:   filepath.Join(dir, "state.json"),
		logPath:     filepath.Join(dir, "transition-notifier.log"),
		missedPath:  filepath.Join(dir, "notifier-missed.log"),
		queuePath:   filepath.Join(dir, "queue.json"),
		sendTimeout: 200 * time.Millisecond,
		state: transitionNotifyState{
			Records: map[string]transitionNotifyRecord{},
		},
	}
}

// TestQueue_EnqueueDeferredEvent asserts that when EnqueueDeferred is called,
// the event is persisted to disk so that a notifier restart (e.g. daemon
// reload) does not lose it. The deferred queue must survive the same kinds
// of restarts that transition-notify-state.json already survives.
func TestQueue_EnqueueDeferredEvent(t *testing.T) {
	n := newQueueTestNotifier(t)

	event := TransitionNotificationEvent{
		ChildSessionID:  "child-x",
		ChildTitle:      "worker",
		Profile:         "_test",
		FromStatus:      "running",
		ToStatus:        "waiting",
		Timestamp:       time.Now(),
		TargetSessionID: "parent-1",
		TargetKind:      "parent",
	}

	n.EnqueueDeferred(event)

	data, err := os.ReadFile(n.queuePath)
	if err != nil {
		t.Fatalf("queue file must exist after enqueue: %v", err)
	}
	if !strings.Contains(string(data), "child-x") {
		t.Fatalf("queue file missing child id, got: %s", data)
	}
	if !strings.Contains(string(data), "parent-1") {
		t.Fatalf("queue file missing target id, got: %s", data)
	}

	entries := n.snapshotQueueForTest()
	if len(entries) != 1 {
		t.Fatalf("expected 1 queued entry, got %d", len(entries))
	}
	if entries[0].Event.ChildSessionID != "child-x" {
		t.Fatalf("entry child mismatch: %+v", entries[0])
	}
	if entries[0].Attempts != 0 {
		t.Fatalf("attempts should start at 0, got %d", entries[0].Attempts)
	}
}

// TestQueue_DrainDispatchesWhenTargetFree is the primary repro: parent was
// busy at event time, gets queued, then becomes free. DrainRetryQueue must
// re-dispatch using the async sender, increment attempts, and remove the
// entry on successful dispatch.
func TestQueue_DrainDispatchesWhenTargetFree(t *testing.T) {
	n := newQueueTestNotifier(t)

	var sent int32
	n.sender = func(profile, targetID, message string) error {
		atomic.AddInt32(&sent, 1)
		return nil
	}

	event := TransitionNotificationEvent{
		ChildSessionID:  "child-drain",
		ChildTitle:      "worker",
		Profile:         "_test",
		FromStatus:      "running",
		ToStatus:        "waiting",
		Timestamp:       time.Now(),
		TargetSessionID: "parent-free",
		TargetKind:      "parent",
	}
	n.EnqueueDeferred(event)

	// Target is "free" (the resolver always returns false = not running).
	available := func(profile, targetID string) bool { return true }
	n.DrainRetryQueueWithResolver("_test", available)
	n.waitWatchers()

	if got := atomic.LoadInt32(&sent); got != 1 {
		t.Fatalf("expected exactly one dispatch from drain, got %d", got)
	}

	// Queue must be empty after successful drain.
	entries := n.snapshotQueueForTest()
	if len(entries) != 0 {
		t.Fatalf("queue should be empty after successful drain, got %d: %+v", len(entries), entries)
	}

	// The successful drain writes to transition-notifier.log, not missed.log.
	delivered := readDeliveryLines(t, n.logPath)
	if len(delivered) != 1 || delivered[0].DeliveryResult != transitionDeliverySent {
		t.Fatalf("expected one sent entry in delivery log, got %+v", delivered)
	}
	if missed := readMissedLines(t, n.missedPath); len(missed) != 0 {
		t.Fatalf("successful drain must not log missed, got %+v", missed)
	}
}

// TestQueue_DrainLeavesEntriesWhenTargetStillBusy asserts we don't lose the
// event just because the retry cycle caught the target still busy. The entry
// stays enqueued and we retry on the next cycle.
func TestQueue_DrainLeavesEntriesWhenTargetStillBusy(t *testing.T) {
	n := newQueueTestNotifier(t)

	var sent int32
	n.sender = func(profile, targetID, message string) error {
		atomic.AddInt32(&sent, 1)
		return nil
	}

	event := TransitionNotificationEvent{
		ChildSessionID:  "child-busy",
		ChildTitle:      "worker",
		Profile:         "_test",
		FromStatus:      "running",
		ToStatus:        "waiting",
		Timestamp:       time.Now(),
		TargetSessionID: "parent-busy",
		TargetKind:      "parent",
	}
	n.EnqueueDeferred(event)

	// Target is never free.
	busy := func(profile, targetID string) bool { return false }
	n.DrainRetryQueueWithResolver("_test", busy)
	n.waitWatchers()

	if got := atomic.LoadInt32(&sent); got != 0 {
		t.Fatalf("expected zero dispatches while target busy, got %d", got)
	}

	entries := n.snapshotQueueForTest()
	if len(entries) != 1 {
		t.Fatalf("queue must retain entry while target busy, got %d", len(entries))
	}
}

// TestQueue_DrainExpiresOldEntriesToMissedLog enforces the age-out. If the
// queue holds an event past QueueMaxAge, we stop trying and log it to
// notifier-missed.log with reason=expired so the operator has actionable
// signal rather than a silently-stale queue file.
func TestQueue_DrainExpiresOldEntriesToMissedLog(t *testing.T) {
	n := newQueueTestNotifier(t)

	var sent int32
	n.sender = func(profile, targetID, message string) error {
		atomic.AddInt32(&sent, 1)
		return nil
	}

	// Pretend the event has been sitting in the queue for an hour, well past
	// the default max age. Use the testing hook to inject a first-deferred
	// timestamp in the past.
	oldEvent := TransitionNotificationEvent{
		ChildSessionID:  "child-old",
		ChildTitle:      "worker",
		Profile:         "_test",
		FromStatus:      "running",
		ToStatus:        "waiting",
		Timestamp:       time.Now().Add(-2 * time.Hour),
		TargetSessionID: "parent-busy",
		TargetKind:      "parent",
	}
	n.enqueueDeferredAtForTest(oldEvent, time.Now().Add(-2*time.Hour))

	// Even with target free, the drain should expire before dispatching.
	free := func(profile, targetID string) bool { return true }
	n.DrainRetryQueueWithResolver("_test", free)
	n.waitWatchers()

	if got := atomic.LoadInt32(&sent); got != 0 {
		t.Fatalf("expired entries must not dispatch, got %d sends", got)
	}

	entries := n.snapshotQueueForTest()
	if len(entries) != 0 {
		t.Fatalf("expired entries must be removed from queue, got %d", len(entries))
	}

	missed := readMissedLines(t, n.missedPath)
	if len(missed) != 1 {
		t.Fatalf("expected one expired miss entry, got %d: %+v", len(missed), missed)
	}
	if got := missed[0]["reason"]; got != "expired" {
		t.Fatalf("expected reason=expired, got %v", got)
	}
	if got := missed[0]["child"]; got != "child-old" {
		t.Fatalf("expected child=child-old, got %v", got)
	}
}

// TestQueue_PersistsAcrossNotifierReload is the reliability guarantee: if
// the daemon process restarts between enqueue and drain, the deferred event
// survives because the queue is on disk. Without this, a daemon restart
// silently loses every in-flight deferred notification.
func TestQueue_PersistsAcrossNotifierReload(t *testing.T) {
	dir := t.TempDir()
	queuePath := filepath.Join(dir, "queue.json")

	first := &TransitionNotifier{
		statePath:   filepath.Join(dir, "state.json"),
		logPath:     filepath.Join(dir, "transition-notifier.log"),
		missedPath:  filepath.Join(dir, "notifier-missed.log"),
		queuePath:   queuePath,
		sendTimeout: 200 * time.Millisecond,
		state: transitionNotifyState{
			Records: map[string]transitionNotifyRecord{},
		},
	}

	event := TransitionNotificationEvent{
		ChildSessionID:  "child-persist",
		ChildTitle:      "worker",
		Profile:         "_test",
		FromStatus:      "running",
		ToStatus:        "waiting",
		Timestamp:       time.Now(),
		TargetSessionID: "parent-persist",
		TargetKind:      "parent",
	}
	first.EnqueueDeferred(event)

	raw, err := os.ReadFile(queuePath)
	if err != nil {
		t.Fatalf("read queue file: %v", err)
	}
	var peek map[string]any
	if err := json.Unmarshal(raw, &peek); err != nil {
		t.Fatalf("queue file must be valid JSON, got %s: %v", raw, err)
	}

	// Reload by constructing a fresh notifier pointing at the same queue file.
	second := &TransitionNotifier{
		statePath:   filepath.Join(dir, "state.json"),
		logPath:     filepath.Join(dir, "transition-notifier.log"),
		missedPath:  filepath.Join(dir, "notifier-missed.log"),
		queuePath:   queuePath,
		sendTimeout: 200 * time.Millisecond,
		state: transitionNotifyState{
			Records: map[string]transitionNotifyRecord{},
		},
	}
	entries := second.snapshotQueueForTest()
	if len(entries) != 1 {
		t.Fatalf("reloaded notifier must see 1 queued entry, got %d", len(entries))
	}
	if entries[0].Event.ChildSessionID != "child-persist" {
		t.Fatalf("reloaded entry mismatch: %+v", entries[0])
	}
}

// TestNotifyTransition_ParentBusyEnqueuesNotMarksDone ties the queue back
// into the top-level NotifyTransition call. When parent is StatusRunning,
// the event should be enqueued (not silently discarded) and NOT marked in
// the dedup records — the successful drain will do that.
func TestNotifyTransition_ParentBusyEnqueuesNotMarksDone(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("AGENT_DECK_PROFILE", "")
	ClearUserConfigCache()
	t.Cleanup(func() { ClearUserConfigCache() })

	if err := os.MkdirAll(tmpHome+"/.agent-deck", 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	profile := "_test-busy-enqueue"
	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	defer storage.Close()

	now := time.Now()
	child := &Instance{
		ID:              "child-busy-1",
		Title:           "worker",
		ProjectPath:     "/tmp/child",
		GroupPath:       DefaultGroupPath,
		ParentSessionID: "parent-busy-1",
		Tool:            "shell",
		Status:          StatusWaiting,
		CreatedAt:       now,
	}
	parent := &Instance{
		ID:          "parent-busy-1",
		Title:       "orchestrator",
		ProjectPath: "/tmp/parent",
		GroupPath:   DefaultGroupPath,
		Tool:        "shell",
		Status:      StatusRunning, // parent is mid-task; notifier must defer+enqueue
		CreatedAt:   now,
	}
	if err := storage.SaveWithGroups([]*Instance{child, parent}, nil); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}

	notifier := NewTransitionNotifier()
	event := TransitionNotificationEvent{
		ChildSessionID: child.ID,
		ChildTitle:     child.Title,
		Profile:        profile,
		FromStatus:     "running",
		ToStatus:       "waiting",
		Timestamp:      now,
	}

	result := notifier.NotifyTransition(event)
	if result.DeliveryResult != transitionDeliveryDeferred {
		t.Fatalf("expected deferred result when parent is running, got %q", result.DeliveryResult)
	}

	entries := notifier.snapshotQueueForTest()
	if len(entries) != 1 {
		t.Fatalf("expected 1 queued entry after deferred dispatch, got %d: %+v", len(entries), entries)
	}
	if entries[0].Event.ChildSessionID != child.ID {
		t.Fatalf("queue entry wrong child: %+v", entries[0])
	}

	// Crucial: deferred events must not be marked in the dedup state, otherwise
	// an identical transition within 90s would be dropped as duplicate instead
	// of being handled by drain.
	if notifier.isDuplicate(event) {
		t.Fatal("deferred event must not be recorded in dedup state")
	}
}
