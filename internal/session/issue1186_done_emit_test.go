package session

import (
	"os"
	"strings"
	"testing"
	"time"
)

// Issue #1186 + #1225: the daemon turns a worker-printed completion sentinel
// (persisted into the hook status file by the Stop-hook handler) into a
// distinct "finished" event committed to the parent's durable outbox. These
// tests pin the emit side: the finished event lands in the parent inbox with
// the parsed ok/fail outcome, and per-task idempotency (one record, last-wins).

// seedDoneParentChild creates a live conductor parent and a worker child in a
// fresh profile's storage, returning the profile and ids.
func seedDoneParentChild(t *testing.T, profile string) (parentID, childID string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("AGENT_DECK_PROFILE", "")
	ClearUserConfigCache()
	ResetInboxFingerprintCacheForTest()
	t.Cleanup(func() {
		ClearUserConfigCache()
		ResetInboxFingerprintCacheForTest()
	})
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

// TestNotifyFinished_EmitsDoneMessageToParent: after NotifyFinished, the
// finished event lands in the PARENT's durable inbox (issue #1225 pull model),
// carrying Kind=finished and DoneStatus=ok. No push, no fake sender.
func TestNotifyFinished_EmitsDoneMessageToParent(t *testing.T) {
	profile := "_test-1186-finished-ok"
	parentID, childID := seedDoneParentChild(t, profile)

	n := NewTransitionNotifier()
	t.Cleanup(n.Close)

	n.NotifyFinished(TransitionNotificationEvent{
		ChildSessionID: childID,
		ChildTitle:     "worker",
		Profile:        profile,
		DoneStatus:     "ok",
		DoneSummary:    "feature shipped",
		Timestamp:      time.Now(),
	})
	n.Flush()

	inbox := readInboxLines(t, parentID)
	if len(inbox) != 1 {
		t.Fatalf("expected exactly 1 finished record in parent inbox, got %d: %+v", len(inbox), inbox)
	}
	ev := inbox[0]
	if ev.TargetSessionID != parentID {
		t.Errorf("record targeted %q, want parent %q", ev.TargetSessionID, parentID)
	}
	if ev.ChildSessionID != childID {
		t.Errorf("record child %q, want %q", ev.ChildSessionID, childID)
	}
	if ev.Kind != transitionKindFinished {
		t.Errorf("record kind %q, want finished", ev.Kind)
	}
	if ev.DoneStatus != "ok" {
		t.Errorf("record done_status %q, want ok", ev.DoneStatus)
	}
	if ev.DoneSummary != "feature shipped" {
		t.Errorf("record done_summary %q, want %q", ev.DoneSummary, "feature shipped")
	}
}

// TestNotifyFinished_FailStatus: a finished event with DoneStatus "fail" lands
// in the parent inbox with the fail outcome preserved.
func TestNotifyFinished_FailStatus(t *testing.T) {
	profile := "_test-1186-finished-fail"
	parentID, childID := seedDoneParentChild(t, profile)

	n := NewTransitionNotifier()
	t.Cleanup(n.Close)

	n.NotifyFinished(TransitionNotificationEvent{
		ChildSessionID: childID,
		ChildTitle:     "worker",
		Profile:        profile,
		DoneStatus:     "fail",
		DoneSummary:    "build broke",
		Timestamp:      time.Now(),
	})
	n.Flush()

	inbox := readInboxLines(t, parentID)
	if len(inbox) != 1 {
		t.Fatalf("expected 1 finished record, got %d: %+v", len(inbox), inbox)
	}
	if inbox[0].DoneStatus != "fail" || inbox[0].DoneSummary != "build broke" {
		t.Errorf("fail outcome not reflected: status=%q summary=%q", inbox[0].DoneStatus, inbox[0].DoneSummary)
	}
}

// TestDaemon_EmitDoneSignals_HappyAndIdempotent: the daemon's done-signal emit
// commits to the parent inbox once; re-polling the SAME sentinel is idempotent
// (last-wins / turn_fingerprint keeps exactly one pending record), and a
// genuinely new completion produces a fresh record.
func TestDaemon_EmitDoneSignals_HappyAndIdempotent(t *testing.T) {
	profile := "_test-1186-daemon-idem"
	parentID, childID := seedDoneParentChild(t, profile)

	d := NewTransitionDaemon()
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

	// First pass commits the finished event to the parent inbox.
	d.emitDoneSignals(profile, byID, hookStatuses)
	d.notifier.Flush()
	if got := readInboxLines(t, parentID); len(got) != 1 {
		t.Fatalf("first emit: inbox has %d records, want 1", len(got))
	}

	// Second pass with the SAME sentinel must NOT add a duplicate record
	// (per-child last-wins: still exactly one pending record).
	d.emitDoneSignals(profile, byID, hookStatuses)
	d.notifier.Flush()
	if got := readInboxLines(t, parentID); len(got) != 1 {
		t.Fatalf("idempotency: inbox has %d records after re-poll of same sentinel, want 1", len(got))
	}

	// A genuinely new completion (different summary) supersedes via last-wins —
	// still one pending record, but now carrying the new summary.
	hookStatuses[childID] = &HookStatus{
		Status:      "waiting",
		Event:       "Stop",
		DoneStatus:  "ok",
		DoneSummary: "second done",
		UpdatedAt:   time.Now(),
	}
	d.emitDoneSignals(profile, byID, hookStatuses)
	d.notifier.Flush()
	got := readInboxLines(t, parentID)
	if len(got) != 1 {
		t.Fatalf("new completion: inbox has %d records, want 1 (last-wins)", len(got))
	}
	if got[0].DoneSummary != "second done" {
		t.Fatalf("new completion: pending record summary=%q, want %q", got[0].DoneSummary, "second done")
	}

	// And draining yields the new turn exactly once.
	drained, err := DrainInboxForParent(parentID)
	if err != nil {
		t.Fatalf("DrainInboxForParent: %v", err)
	}
	if len(drained) != 1 || drained[0].DoneSummary != "second done" {
		t.Fatalf("drain yielded %+v, want one record summary=second done", drained)
	}
}

func TestDaemon_EmitDoneSignals_NoSentinelNoEmit(t *testing.T) {
	profile := "_test-1186-daemon-nosentinel"
	parentID, childID := seedDoneParentChild(t, profile)

	d := NewTransitionDaemon()
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
	if got := readInboxLines(t, parentID); len(got) != 0 {
		t.Fatalf("no sentinel must not commit a finished event; inbox has %d records", len(got))
	}
	_ = strings.TrimSpace // keep imports stable across edits
}
