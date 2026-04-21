package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The next four tests drive the v1.7.45 async-dispatch layer (L2): per-target
// semaphore + 30s timeout + notifier-missed.log. Each test is RED on v1.7.43
// because the production NotifyTransition calls SendSessionMessageReliable
// serially on the daemon poll goroutine with no per-target slot, no timeout,
// and no missed log. GREEN means the async layer landed without regressing
// the sync short-circuits that earlier tests pin down.

func newAsyncTestNotifier(t *testing.T) *TransitionNotifier {
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

func readMissedLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read missed log %q: %v", path, err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Fatalf("unmarshal missed line %q: %v", line, err)
		}
		out = append(out, obj)
	}
	return out
}

func readDeliveryLines(t *testing.T, path string) []TransitionNotificationEvent {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read delivery log %q: %v", path, err)
	}
	var out []TransitionNotificationEvent
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var ev TransitionNotificationEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("unmarshal delivery line %q: %v", line, err)
		}
		out = append(out, ev)
	}
	return out
}

// TestAsyncDispatch_SlowTargetDoesNotBlockFastTarget is the core throughput
// guarantee. Before v1.7.45, one slow tmux send-keys call serialized every
// subsequent notification for unrelated targets. After v1.7.45, each target
// runs in its own goroutine with its own per-target semaphore.
func TestAsyncDispatch_SlowTargetDoesNotBlockFastTarget(t *testing.T) {
	n := newAsyncTestNotifier(t)

	fastDone := make(chan struct{})
	releaseSlow := make(chan struct{})
	// releaseSlow gates the slow sender so the test can end deterministically
	// instead of leaving a time.Sleep goroutine alive past t.TempDir cleanup;
	// CI hit "directory not empty" when the leaked sender wrote logs into a
	// directory that the framework was mid-delete of.
	n.sender = func(profile, targetID, message string) error {
		if targetID == "slow" {
			<-releaseSlow
			return nil
		}
		close(fastDone)
		return nil
	}

	slowEvent := TransitionNotificationEvent{
		ChildSessionID: "child-slow",
		ChildTitle:     "slow-child",
		Profile:        "_test",
		FromStatus:     "running",
		ToStatus:       "waiting",
		Timestamp:      time.Now(),
	}
	fastEvent := slowEvent
	fastEvent.ChildSessionID = "child-fast"
	fastEvent.ChildTitle = "fast-child"

	start := time.Now()
	n.dispatchAsync("slow", "msg-slow", slowEvent)
	n.dispatchAsync("fast", "msg-fast", fastEvent)
	elapsed := time.Since(start)

	if elapsed > 100*time.Millisecond {
		t.Fatalf("dispatchAsync must return immediately, two calls took %v", elapsed)
	}

	select {
	case <-fastDone:
		// success: fast target fired while slow target is still blocked
	case <-time.After(500 * time.Millisecond):
		close(releaseSlow)
		n.Flush()
		t.Fatalf("fast target did not run within 500ms; slow target head-of-line blocking regressed")
	}

	// Let the slow sender finish and wait for it so t.TempDir cleanup is safe.
	close(releaseSlow)
	n.Flush()
}

// TestAsyncDispatch_SendTimeoutWritesMissedLog covers the 30s timeout knob
// (short-circuited to 200ms in tests). When the sender hangs past the
// deadline, the notifier records an actionable entry in notifier-missed.log
// so operators can see which transitions were lost without having to
// correlate timestamps across logs.
func TestAsyncDispatch_SendTimeoutWritesMissedLog(t *testing.T) {
	n := newAsyncTestNotifier(t)

	release := make(chan struct{})
	// Sender parks on a channel so (1) it definitely outlives the 200ms
	// sendTimeout, triggering the watcher's timeout branch, and (2) we can
	// drain it before t.TempDir cleanup to avoid leaking a goroutine that
	// would race the directory removal.
	n.sender = func(profile, targetID, message string) error {
		<-release
		return nil
	}

	event := TransitionNotificationEvent{
		ChildSessionID:  "child-hang",
		ChildTitle:      "hang-child",
		Profile:         "_test",
		FromStatus:      "running",
		ToStatus:        "waiting",
		Timestamp:       time.Now(),
		TargetSessionID: "hang-target",
		TargetKind:      "parent",
	}

	n.dispatchAsync("hang-target", "msg", event)
	n.waitWatchers()
	defer func() {
		close(release)
		n.Flush()
	}()

	entries := readMissedLines(t, n.missedPath)
	if len(entries) != 1 {
		t.Fatalf("expected exactly one missed entry, got %d: %v", len(entries), entries)
	}
	entry := entries[0]
	if got := entry["reason"]; got != "timeout" {
		t.Fatalf("expected reason=timeout, got %v", got)
	}
	if got := entry["target"]; got != "hang-target" {
		t.Fatalf("expected target=hang-target, got %v", got)
	}
	if got := entry["child"]; got != "child-hang" {
		t.Fatalf("expected child=child-hang, got %v", got)
	}
	if got, _ := entry["event"].(string); !strings.Contains(got, "running") || !strings.Contains(got, "waiting") {
		t.Fatalf("expected event to describe running→waiting, got %q", got)
	}
	// Delivery log stays clean when the send was never confirmed.
	if got := readDeliveryLines(t, n.logPath); len(got) != 0 {
		t.Fatalf("timeout must not write to transition-notifier.log, got %+v", got)
	}
}

// TestAsyncDispatch_ConcurrentSameTargetReportsBusy enforces per-target
// serialization. Two concurrent dispatches to the same target must result
// in the second one being short-circuited as missed/busy rather than
// racing a send-keys call to the same tmux pane.
func TestAsyncDispatch_ConcurrentSameTargetReportsBusy(t *testing.T) {
	n := newAsyncTestNotifier(t)
	n.sendTimeout = 2 * time.Second

	release := make(chan struct{})
	var calls int32
	n.sender = func(profile, targetID, message string) error {
		atomic.AddInt32(&calls, 1)
		<-release
		return nil
	}

	event := TransitionNotificationEvent{
		ChildSessionID: "child-a",
		ChildTitle:     "a",
		Profile:        "_test",
		FromStatus:     "running",
		ToStatus:       "waiting",
		Timestamp:      time.Now(),
	}

	n.dispatchAsync("same-target", "msg", event)
	// Let the first dispatch grab the slot before issuing the second.
	time.Sleep(30 * time.Millisecond)

	eventB := event
	eventB.ChildSessionID = "child-b"
	eventB.ChildTitle = "b"
	n.dispatchAsync("same-target", "msg", eventB)

	entries := readMissedLines(t, n.missedPath)
	if len(entries) != 1 {
		t.Fatalf("expected one missed busy entry, got %d: %+v", len(entries), entries)
	}
	if got := entries[0]["reason"]; got != "busy" {
		t.Fatalf("expected reason=busy, got %v", got)
	}
	if got := entries[0]["child"]; got != "child-b" {
		t.Fatalf("expected the second event (child-b) to be the busy miss, got %v", got)
	}

	close(release)
	n.waitWatchers()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("sender must have been invoked exactly once (busy one skipped), got %d", got)
	}
}

// TestAsyncDispatch_SuccessfulSendLogsSent is the green-path regression. The
// existing transition-notifier.log format must still receive a delivery_result
// of "sent" for successful async dispatches — downstream dashboards parse it.
func TestAsyncDispatch_SuccessfulSendLogsSent(t *testing.T) {
	n := newAsyncTestNotifier(t)
	n.sender = func(profile, targetID, message string) error { return nil }

	event := TransitionNotificationEvent{
		ChildSessionID: "child-ok",
		ChildTitle:     "ok",
		Profile:        "_test",
		FromStatus:     "running",
		ToStatus:       "waiting",
		Timestamp:      time.Now(),
	}

	n.dispatchAsync("ok-target", "msg", event)
	n.waitWatchers()

	entries := readDeliveryLines(t, n.logPath)
	if len(entries) != 1 {
		t.Fatalf("expected one delivery entry, got %d: %+v", len(entries), entries)
	}
	if got := entries[0].DeliveryResult; got != transitionDeliverySent {
		t.Fatalf("expected sent, got %q", got)
	}
	if got := entries[0].TargetSessionID; got != "ok-target" {
		t.Fatalf("expected target ok-target on delivered event, got %q", got)
	}

	if missed := readMissedLines(t, n.missedPath); len(missed) != 0 {
		t.Fatalf("successful send must not touch missed log, got %+v", missed)
	}
}

// TestAsyncDispatch_SenderErrorLogsFailed pins the failure path: if the
// underlying tmux send returns an error (not a timeout), the delivery log
// gets a "failed" entry and the missed log stays empty. This guards against
// a refactor that collapses all error paths into "missed".
func TestAsyncDispatch_SenderErrorLogsFailed(t *testing.T) {
	n := newAsyncTestNotifier(t)
	n.sender = func(profile, targetID, message string) error {
		return errors.New("tmux send-keys failed")
	}

	event := TransitionNotificationEvent{
		ChildSessionID: "child-err",
		ChildTitle:     "err",
		Profile:        "_test",
		FromStatus:     "running",
		ToStatus:       "waiting",
		Timestamp:      time.Now(),
	}

	n.dispatchAsync("err-target", "msg", event)
	n.waitWatchers()

	entries := readDeliveryLines(t, n.logPath)
	if len(entries) != 1 {
		t.Fatalf("expected one failed delivery entry, got %d: %+v", len(entries), entries)
	}
	if got := entries[0].DeliveryResult; got != transitionDeliveryFailed {
		t.Fatalf("expected failed, got %q", got)
	}
	if missed := readMissedLines(t, n.missedPath); len(missed) != 0 {
		t.Fatalf("explicit send errors must not be miscategorized as missed, got %+v", missed)
	}
}
