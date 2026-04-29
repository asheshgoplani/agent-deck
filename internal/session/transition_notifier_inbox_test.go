package session

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newInboxTestNotifier(t *testing.T, home string) *TransitionNotifier {
	t.Helper()
	dir := t.TempDir()
	n := &TransitionNotifier{
		statePath:   filepath.Join(dir, "state.json"),
		logPath:     filepath.Join(dir, "transition-notifier.log"),
		missedPath:  filepath.Join(dir, "notifier-missed.log"),
		queuePath:   filepath.Join(dir, "queue.json"),
		orphanPath:  filepath.Join(dir, "notifier-orphans.log"),
		sendTimeout: 200 * time.Millisecond,
		state: transitionNotifyState{
			Records: map[string]transitionNotifyRecord{},
		},
		targetSlots: map[string]chan struct{}{},
	}
	return n
}

// TestNotifier_BusyRetriesThreeTimesThenInboxes is the regression for the
// 109/4095 deferred-then-vanished events on conductor-innotrade. When the
// parent is busy, the notifier must retry on a fixed backoff schedule and,
// after exhaustion, persist the event to the per-conductor inbox so the
// conductor sees it on its next idle drain instead of losing it forever.
//
// The test mocks availability to always-busy and asserts:
//  1. Exactly three availability checks happen (matches busyBackoff length).
//  2. The event lands in the inbox with the correct child id.
//  3. notifier-missed.log records the exhaustion as actionable signal.
func TestNotifier_BusyRetriesThreeTimesThenInboxes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("AGENT_DECK_PROFILE", "")
	ClearUserConfigCache()
	t.Cleanup(func() { ClearUserConfigCache() })

	n := newInboxTestNotifier(t, home)
	n.busyBackoff = []time.Duration{5 * time.Millisecond, 10 * time.Millisecond, 15 * time.Millisecond}

	var checks atomic.Int32
	n.availability = func(profile, targetID string) bool {
		checks.Add(1)
		return false // always busy
	}

	var sent atomic.Int32
	n.sender = func(profile, targetID, message string) error {
		sent.Add(1)
		return nil
	}

	parent := "parent-busy-exhaust"
	ev := TransitionNotificationEvent{
		ChildSessionID:  "child-busy",
		ChildTitle:      "worker",
		Profile:         "_test",
		FromStatus:      "running",
		ToStatus:        "waiting",
		Timestamp:       time.Now(),
		TargetSessionID: parent,
		TargetKind:      "parent",
	}

	n.scheduleBusyRetry(ev)
	n.Flush()

	if got := checks.Load(); got != 3 {
		t.Fatalf("expected 3 availability checks (one per backoff step), got %d", got)
	}
	if got := sent.Load(); got != 0 {
		t.Fatalf("expected zero sends while target stays busy, got %d", got)
	}

	inboxed, err := ReadAndTruncateInbox(parent)
	if err != nil {
		t.Fatalf("ReadAndTruncateInbox: %v", err)
	}
	if len(inboxed) != 1 {
		t.Fatalf("expected event persisted to inbox after exhaustion, got %d", len(inboxed))
	}
	if inboxed[0].ChildSessionID != "child-busy" {
		t.Fatalf("inbox event mismatch: %+v", inboxed[0])
	}

	missed, err := os.ReadFile(n.missedPath)
	if err != nil {
		t.Fatalf("read missed log: %v", err)
	}
	if !strings.Contains(string(missed), "exhausted_busy_retries") {
		t.Fatalf("missed log must record exhaustion reason, got %q", missed)
	}
}

// TestNotifier_BusyRetrySendsWhenTargetFrees verifies the happy retry path:
// target is busy on the first check, free on the second. The notifier sends
// successfully on attempt 2, marks the dedup record, and does NOT write to
// the inbox.
func TestNotifier_BusyRetrySendsWhenTargetFrees(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("AGENT_DECK_PROFILE", "")
	ClearUserConfigCache()
	t.Cleanup(func() { ClearUserConfigCache() })

	n := newInboxTestNotifier(t, home)
	n.busyBackoff = []time.Duration{5 * time.Millisecond, 10 * time.Millisecond, 15 * time.Millisecond}

	var checks atomic.Int32
	n.availability = func(profile, targetID string) bool {
		count := checks.Add(1)
		return count >= 2 // busy on first check, free thereafter
	}

	var sent atomic.Int32
	n.sender = func(profile, targetID, message string) error {
		sent.Add(1)
		return nil
	}

	parent := "parent-busy-then-free"
	ev := TransitionNotificationEvent{
		ChildSessionID:  "child-recovers",
		ChildTitle:      "worker",
		Profile:         "_test",
		FromStatus:      "running",
		ToStatus:        "waiting",
		Timestamp:       time.Now(),
		TargetSessionID: parent,
		TargetKind:      "parent",
	}

	n.scheduleBusyRetry(ev)
	n.Flush()

	if got := sent.Load(); got != 1 {
		t.Fatalf("expected one successful send after target freed, got %d", got)
	}
	if got := checks.Load(); got != 2 {
		t.Fatalf("expected exactly 2 availability checks (busy then free), got %d", got)
	}

	inboxed, err := ReadAndTruncateInbox(parent)
	if err != nil {
		t.Fatalf("read inbox: %v", err)
	}
	if len(inboxed) != 0 {
		t.Fatalf("successful retry must not write to inbox, got %d", len(inboxed))
	}
}

// TestNotifier_OrphanChildLogsWarnOnce is the regression for cause A: a child
// born without a parent_session_id (e.g. via a worktree-setup hook that did
// not preserve $AGENTDECK_INSTANCE_ID). The notifier should:
//   - log a single WARN line to notifier-orphans.log per orphan child id
//   - drop the event (no dispatch attempt)
//   - NOT log the orphan again on the next transition for the same child
//
// The "once per child" property prevents log spam on long-lived orphans
// firing many transitions over their lifetime.
func TestNotifier_OrphanChildLogsWarnOnce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("AGENT_DECK_PROFILE", "")
	ClearUserConfigCache()
	t.Cleanup(func() { ClearUserConfigCache() })
	if err := os.MkdirAll(home+"/.agent-deck", 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	profile := "_test-orphan"
	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	defer storage.Close()

	now := time.Now()
	child := &Instance{
		ID:              "orphan-child-x",
		Title:           "worker",
		ProjectPath:     "/tmp/orphan",
		GroupPath:       DefaultGroupPath,
		ParentSessionID: "", // ORPHAN — no parent linkage at creation
		Tool:            "shell",
		Status:          StatusWaiting,
		CreatedAt:       now,
	}
	if err := storage.SaveWithGroups([]*Instance{child}, nil); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}

	n := NewTransitionNotifier()

	for i := 0; i < 3; i++ {
		ev := TransitionNotificationEvent{
			ChildSessionID: child.ID,
			ChildTitle:     child.Title,
			Profile:        profile,
			FromStatus:     "running",
			ToStatus:       "waiting",
			Timestamp:      now.Add(time.Duration(i+1) * 5 * time.Minute), // bypass dedup
		}
		result := n.NotifyTransition(ev)
		if result.DeliveryResult != transitionDeliveryDropped {
			t.Fatalf("orphan event #%d expected dropped, got %q", i, result.DeliveryResult)
		}
	}

	data, err := os.ReadFile(transitionNotifierOrphanLogPath())
	if err != nil {
		t.Fatalf("orphan log must exist after warn: %v", err)
	}
	count := strings.Count(string(data), child.ID)
	if count != 1 {
		t.Fatalf("expected exactly 1 WARN line per child, got %d (content=%s)", count, data)
	}
	if !strings.Contains(string(data), "orphan child detected") {
		t.Fatalf("orphan log must include actionable hint, got %q", data)
	}
}

// TestNotifier_TopLevelConductorSelfSuppress is the regression for cause C.
// A conductor session is top-level (its own ParentSessionID is empty). When
// the conductor itself transitions running→waiting, NotifyTransition must
// short-circuit without ever attempting a send — it has no upstream to
// notify, and the historical behavior of dispatching-then-dropping just
// burned cycles and clouded the metrics.
func TestNotifier_TopLevelConductorSelfSuppress(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("AGENT_DECK_PROFILE", "")
	ClearUserConfigCache()
	t.Cleanup(func() { ClearUserConfigCache() })
	if err := os.MkdirAll(home+"/.agent-deck", 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	profile := "_test-self-suppress"
	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	defer storage.Close()

	now := time.Now()
	conductor := &Instance{
		ID:              "conductor-top-1",
		Title:           "conductor-agent-deck", // matches isConductorSessionTitle
		ProjectPath:     "/tmp/conductor",
		GroupPath:       DefaultGroupPath,
		ParentSessionID: "", // top-level
		Tool:            "claude",
		Status:          StatusWaiting,
		CreatedAt:       now,
	}
	if err := storage.SaveWithGroups([]*Instance{conductor}, nil); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}

	n := NewTransitionNotifier()
	var sent atomic.Int32
	n.sender = func(profile, targetID, message string) error {
		sent.Add(1)
		return nil
	}

	ev := TransitionNotificationEvent{
		ChildSessionID: conductor.ID,
		ChildTitle:     conductor.Title,
		Profile:        profile,
		FromStatus:     "running",
		ToStatus:       "waiting",
		Timestamp:      now,
	}
	result := n.NotifyTransition(ev)
	n.Flush()

	if result.DeliveryResult != transitionDeliveryDropped {
		t.Fatalf("top-level conductor must self-suppress with dropped, got %q", result.DeliveryResult)
	}
	if got := sent.Load(); got != 0 {
		t.Fatalf("self-suppress must not invoke sender, got %d sends", got)
	}
}
