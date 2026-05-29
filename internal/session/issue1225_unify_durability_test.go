package session

// Issue #1225 — case 7 (both producers unified onto ONE inbox) and case 3
// (durability across daemon AND parent restart). The interactive producer
// (running→waiting via NotifyTransition) and the one-shot producer (run-task
// kernel-exit via DeliverCompletion) must both land in the SAME per-parent
// inbox and drain identically; and a committed record must survive a process
// restart, delivered exactly once.

import (
	"os"
	"testing"
	"time"
)

// seedParentTwoChildren creates a conductor parent and two worker children in a
// fresh profile, returning (profile, parentID, interactiveChildID, oneShotChildID).
func seedParentTwoChildren(t *testing.T) (profile, parentID, child1, child2 string) {
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
	profile = "_test-1225-unify"
	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	defer storage.Close()

	now := time.Now()
	parent := &Instance{ID: "parent-unify-1225", Title: "conductor-unify", ProjectPath: "/tmp/pu", GroupPath: DefaultGroupPath, Tool: "claude", Status: StatusRunning, CreatedAt: now}
	c1 := &Instance{ID: "child-interactive-1225", Title: "interactive-worker", ProjectPath: "/tmp/c1", GroupPath: DefaultGroupPath, ParentSessionID: parent.ID, Tool: "claude", Status: StatusWaiting, CreatedAt: now}
	c2 := &Instance{ID: "child-oneshot-1225", Title: "oneshot-worker", ProjectPath: "/tmp/c2", GroupPath: DefaultGroupPath, ParentSessionID: parent.ID, Tool: "bash", Status: StatusWaiting, CreatedAt: now}
	if err := storage.SaveWithGroups([]*Instance{parent, c1, c2}, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	return profile, parent.ID, c1.ID, c2.ID
}

// Case 7: both producers commit to the SAME per-parent inbox and drain identically.
func TestIssue1225_BothProducersUnifiedIntoOneInbox(t *testing.T) {
	profile, parentID, interactiveChild, oneShotChild := seedParentTwoChildren(t)
	n := NewTransitionNotifier()
	t.Cleanup(n.Close)

	// Producer 1 — interactive running→waiting (the daemon path). Parent is
	// StatusRunning (busy); the old path would have deferred. Now it commits.
	res := n.NotifyTransition(TransitionNotificationEvent{
		ChildSessionID: interactiveChild, ChildTitle: "interactive-worker", Profile: profile,
		FromStatus: "running", ToStatus: "waiting", LastOutputHash: "iturn", Timestamp: time.Now(),
	})
	if res.DeliveryResult != transitionDeliveryCommitted {
		t.Fatalf("interactive producer: want committed_inbox, got %q", res.DeliveryResult)
	}

	// Producer 2 — one-shot run-task kernel exit (DeliverCompletion).
	if !n.DeliverCompletion(CompletionRecord{
		ChildID: oneShotChild, Title: "oneshot-worker", Profile: profile,
		Status: "ok", Summary: "done", FinishedAt: time.Now(),
	}) {
		t.Fatalf("one-shot producer: DeliverCompletion should report durable commit")
	}

	// Both records are in the SAME parent inbox.
	inbox := readInboxLines(t, parentID)
	if len(inbox) != 2 {
		t.Fatalf("expected 2 records in the one parent inbox, got %d: %+v", len(inbox), inbox)
	}
	seen := map[string]bool{}
	for _, ev := range inbox {
		if ev.TargetSessionID != parentID {
			t.Errorf("record for %s targeted %q, want parent %q", ev.ChildSessionID, ev.TargetSessionID, parentID)
		}
		seen[ev.ChildSessionID] = true
	}
	if !seen[interactiveChild] || !seen[oneShotChild] {
		t.Fatalf("both producers must land in the inbox; saw %v", seen)
	}

	// They drain identically — one consumer call returns both, exactly once.
	drained, err := DrainInboxForParent(parentID)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(drained) != 2 {
		t.Fatalf("drain should return both producers' records, got %d", len(drained))
	}
	if again, _ := DrainInboxForParent(parentID); len(again) != 0 {
		t.Fatalf("exactly-once: re-drain must be empty, got %d", len(again))
	}
}

// Case 3: a committed record survives a daemon restart AND a parent restart, and
// is delivered exactly once — re-delivery of the same turn after restart is
// deduped via the on-disk consumed-turn ledger.
func TestIssue1225_DurabilityAcrossDaemonAndParentRestart(t *testing.T) {
	profile, parentID, interactiveChild, _ := seedParentTwoChildren(t)

	// Daemon commits a completion.
	n := NewTransitionNotifier()
	n.NotifyTransition(TransitionNotificationEvent{
		ChildSessionID: interactiveChild, ChildTitle: "interactive-worker", Profile: profile,
		FromStatus: "running", ToStatus: "waiting", LastOutputHash: "durable-turn", Timestamp: time.Now(),
	})
	n.Close()

	// Record persists on disk regardless of process lifetime.
	if _, err := os.Stat(InboxPathFor(parentID)); err != nil {
		t.Fatalf("durability: record must survive on disk, stat: %v", err)
	}

	// Simulate a DAEMON restart: fresh process drops the in-memory dedup cache.
	ResetInboxFingerprintCacheForTest()

	// Simulate a PARENT restart and its first drain: the record is delivered.
	first, err := DrainInboxForParent(parentID)
	if err != nil {
		t.Fatalf("post-restart drain: %v", err)
	}
	if len(first) != 1 || first[0].ChildSessionID != interactiveChild {
		t.Fatalf("post-restart drain must deliver the record once, got %+v", first)
	}

	// Daemon restarts AGAIN and re-commits the SAME turn (re-stamped timestamp).
	ResetInboxFingerprintCacheForTest()
	n2 := NewTransitionNotifier()
	n2.NotifyTransition(TransitionNotificationEvent{
		ChildSessionID: interactiveChild, ChildTitle: "interactive-worker", Profile: profile,
		FromStatus: "running", ToStatus: "waiting", LastOutputHash: "durable-turn",
		Timestamp: time.Now().Add(time.Hour),
	})
	n2.Close()

	// Parent restarts again and drains: the consumed-turn ledger (on disk)
	// dedups the re-delivery — exactly-once effects survive restarts.
	second, err := DrainInboxForParent(parentID)
	if err != nil {
		t.Fatalf("second post-restart drain: %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("exactly-once across restart: re-delivered same turn must be deduped, got %+v", second)
	}
}
