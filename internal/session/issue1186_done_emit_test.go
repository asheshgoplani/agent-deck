package session

import (
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Issue #1186: the daemon turns a worker-printed completion sentinel (persisted
// into the hook status file by the Stop-hook handler) into a distinct
// "finished" event delivered to the parent. These tests pin the emit side:
// the [DONE] message format, ok/fail outcome, and per-task idempotency.

// seedDoneParentChild creates a live conductor parent and a worker child in a
// fresh profile's storage, returning the profile and ids.
func seedDoneParentChild(t *testing.T, profile string) (parentID, childID string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("AGENT_DECK_PROFILE", "")
	ClearUserConfigCache()
	t.Cleanup(func() { ClearUserConfigCache() })
	if err := os.MkdirAll(home+"/.agent-deck", 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	defer storage.Close()

	now := time.Now()
	parent := &Instance{
		ID:          "parent-conductor-1186",
		Title:       "conductor-1186",
		ProjectPath: "/tmp/p1186",
		GroupPath:   DefaultGroupPath,
		Tool:        "claude",
		Status:      StatusIdle,
		CreatedAt:   now,
	}
	child := &Instance{
		ID:              "child-worker-1186",
		Title:           "worker",
		ProjectPath:     "/tmp/c1186",
		GroupPath:       DefaultGroupPath,
		ParentSessionID: parent.ID,
		Tool:            "claude",
		Status:          StatusWaiting,
		CreatedAt:       now,
	}
	if err := storage.SaveWithGroups([]*Instance{parent, child}, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	return parent.ID, child.ID
}

func TestNotifyFinished_EmitsDoneMessageToParent(t *testing.T) {
	profile := "_test-1186-finished-ok"
	parentID, childID := seedDoneParentChild(t, profile)

	n := NewTransitionNotifier()
	t.Cleanup(n.Close)
	var mu sync.Mutex
	var gotTarget, gotMsg string
	n.sender = func(profile, targetID, message string) error {
		mu.Lock()
		gotTarget, gotMsg = targetID, message
		mu.Unlock()
		return nil
	}

	n.NotifyFinished(TransitionNotificationEvent{
		ChildSessionID: childID,
		ChildTitle:     "worker",
		Profile:        profile,
		DoneStatus:     "ok",
		DoneSummary:    "feature shipped",
		Timestamp:      time.Now(),
	})
	n.Flush()

	mu.Lock()
	defer mu.Unlock()
	if gotTarget != parentID {
		t.Errorf("delivered to %q, want parent %q", gotTarget, parentID)
	}
	if !strings.Contains(gotMsg, "[DONE]") {
		t.Errorf("message missing [DONE] marker: %q", gotMsg)
	}
	if !strings.Contains(gotMsg, "status=ok") || !strings.Contains(gotMsg, "summary=feature shipped") {
		t.Errorf("message missing parsed outcome: %q", gotMsg)
	}
	if !strings.Contains(gotMsg, childID) {
		t.Errorf("message missing child id: %q", gotMsg)
	}
	if strings.Contains(gotMsg, "[EVENT]") {
		t.Errorf("finished event must not reuse the [EVENT] waiting format: %q", gotMsg)
	}
}

func TestNotifyFinished_FailStatus(t *testing.T) {
	profile := "_test-1186-finished-fail"
	_, childID := seedDoneParentChild(t, profile)

	n := NewTransitionNotifier()
	t.Cleanup(n.Close)
	var mu sync.Mutex
	var gotMsg string
	n.sender = func(profile, targetID, message string) error {
		mu.Lock()
		gotMsg = message
		mu.Unlock()
		return nil
	}

	n.NotifyFinished(TransitionNotificationEvent{
		ChildSessionID: childID,
		ChildTitle:     "worker",
		Profile:        profile,
		DoneStatus:     "fail",
		DoneSummary:    "build broke",
		Timestamp:      time.Now(),
	})
	n.Flush()

	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(gotMsg, "status=fail") || !strings.Contains(gotMsg, "summary=build broke") {
		t.Errorf("fail outcome not reflected: %q", gotMsg)
	}
}

func TestDaemon_EmitDoneSignals_HappyAndIdempotent(t *testing.T) {
	profile := "_test-1186-daemon-idem"
	_, childID := seedDoneParentChild(t, profile)

	d := NewTransitionDaemon()
	var sent atomic.Int32
	d.notifier.sender = func(profile, targetID, message string) error {
		sent.Add(1)
		return nil
	}
	t.Cleanup(d.notifier.Close)

	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	defer storage.Close()
	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	byID := map[string]*Instance{}
	for _, inst := range instances {
		byID[inst.ID] = inst
	}

	hookStatuses := map[string]*HookStatus{
		childID: {
			Status:      "waiting",
			Event:       "Stop",
			DoneStatus:  "ok",
			DoneSummary: "first done",
			UpdatedAt:   time.Now(),
		},
	}

	// First pass emits the finished event.
	d.emitDoneSignals(profile, byID, hookStatuses)
	d.notifier.Flush()
	if got := sent.Load(); got != 1 {
		t.Fatalf("first emit: sent=%d, want 1", got)
	}

	// Second pass with the SAME sentinel must NOT re-emit (idempotent per task).
	d.emitDoneSignals(profile, byID, hookStatuses)
	d.notifier.Flush()
	if got := sent.Load(); got != 1 {
		t.Fatalf("idempotency: sent=%d after re-poll of same sentinel, want 1", got)
	}

	// A genuinely new completion (different summary) emits again.
	hookStatuses[childID] = &HookStatus{
		Status:      "waiting",
		Event:       "Stop",
		DoneStatus:  "ok",
		DoneSummary: "second done",
		UpdatedAt:   time.Now(),
	}
	d.emitDoneSignals(profile, byID, hookStatuses)
	d.notifier.Flush()
	if got := sent.Load(); got != 2 {
		t.Fatalf("new completion: sent=%d, want 2", got)
	}
}

func TestDaemon_EmitDoneSignals_NoSentinelNoEmit(t *testing.T) {
	profile := "_test-1186-daemon-nosentinel"
	_, childID := seedDoneParentChild(t, profile)

	d := NewTransitionDaemon()
	var sent atomic.Int32
	d.notifier.sender = func(profile, targetID, message string) error {
		sent.Add(1)
		return nil
	}
	t.Cleanup(d.notifier.Close)

	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	defer storage.Close()
	instances, _, _ := storage.LoadWithGroups()
	byID := map[string]*Instance{}
	for _, inst := range instances {
		byID[inst.ID] = inst
	}

	// Ordinary mid-task Stop: hook status present but NO done fields.
	hookStatuses := map[string]*HookStatus{
		childID: {Status: "waiting", Event: "Stop", UpdatedAt: time.Now()},
	}
	d.emitDoneSignals(profile, byID, hookStatuses)
	d.notifier.Flush()
	if got := sent.Load(); got != 0 {
		t.Fatalf("no sentinel must not emit a finished event; sent=%d", got)
	}
}
