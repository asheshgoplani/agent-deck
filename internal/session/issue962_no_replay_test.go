package session

import (
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// TestTransitionNotifier_SkipsRemovedSessions_RegressionFor962 is the RED test
// for issue #962. A deferred transition event held in the retry queue must NOT
// be dispatched once the child it names has been removed from the registry —
// otherwise the conductor receives "[EVENT] Child '<id>' is waiting" messages
// for sessions that no longer exist (the all-day stale-event spam reported on
// conductor-innotrade and others).
//
// rm-time cleanup at cmd/agent-deck/session_remove_cmd.go and main.go already
// sweeps the per-conductor inbox (SweepInboxesForChildSession) and the dedup
// ledger (RemoveNotifyStateRecord, both for #910). The deferred retry queue
// at runtime/transition-deferred-queue.json is the remaining replay vector:
// DrainRetryQueueWithResolver only checks target availability, never child
// presence. So a child that gets rm'd while one of its events sits queued
// gets redelivered on every poll cycle until rm-state is manually wiped.
//
// The fix is consumer-side defense-in-depth: filter the queue against the
// current registry on every drain. This survives concurrent rm + drain
// races, rm paths that bypass the sweeper, and stale queue files left behind
// by older agent-deck versions on upgrade.
func TestTransitionNotifier_SkipsRemovedSessions_RegressionFor962(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("AGENT_DECK_PROFILE", "")
	ClearUserConfigCache()
	t.Cleanup(func() { ClearUserConfigCache() })
	if err := os.MkdirAll(home+"/.agent-deck", 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	profile := "_test-962-no-replay"
	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	defer storage.Close()

	now := time.Now()
	parent := &Instance{
		ID:          "parent-conductor-962",
		Title:       "conductor-962",
		ProjectPath: "/tmp/p962",
		GroupPath:   DefaultGroupPath,
		Tool:        "claude",
		Status:      StatusIdle,
		CreatedAt:   now,
	}
	child := &Instance{
		ID:              "child-removed-962",
		Title:           "worker",
		ProjectPath:     "/tmp/c962",
		GroupPath:       DefaultGroupPath,
		ParentSessionID: parent.ID,
		Tool:            "shell",
		Status:          StatusWaiting,
		CreatedAt:       now,
	}
	if err := storage.SaveWithGroups([]*Instance{parent, child}, nil); err != nil {
		t.Fatalf("save initial: %v", err)
	}

	n := NewTransitionNotifier()
	var sent atomic.Int32
	n.sender = func(profile, targetID, message string) error {
		sent.Add(1)
		return nil
	}
	t.Cleanup(n.Close)

	event := TransitionNotificationEvent{
		ChildSessionID:  child.ID,
		ChildTitle:      child.Title,
		Profile:         profile,
		FromStatus:      "running",
		ToStatus:        "waiting",
		Timestamp:       now,
		TargetSessionID: parent.ID,
		TargetKind:      "parent",
	}
	n.EnqueueDeferred(event)

	// Simulate `agent-deck rm <child>` landing between the enqueue and the
	// next drain. The deferred queue file still references the child id.
	if err := storage.DeleteInstance(child.ID); err != nil {
		t.Fatalf("delete child: %v", err)
	}

	// Drain with availability always true: target IS free, so on current main
	// the queued event redispatches. The only thing that should stop it is the
	// registry-presence filter we expect this test to drive into existence.
	n.DrainRetryQueueWithResolver(profile, func(p, id string) bool { return true })
	n.Flush()

	if got := sent.Load(); got != 0 {
		t.Fatalf("issue #962: notifier dispatched %d events for a removed child; expected 0", got)
	}

	if entries := n.snapshotQueueForTest(); len(entries) != 0 {
		t.Fatalf("issue #962: queue still holds %d entries for removed child; expected drained", len(entries))
	}
}
