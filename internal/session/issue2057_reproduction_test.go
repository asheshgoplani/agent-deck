package session

import (
	"testing"
	"time"
)

// These reproductions were added against reviewed tip 55cd3ae6 before the fix.
func TestIssue2057_TwoDistinctTurnsInsideShortWindow(t *testing.T) {
	n := NewTransitionNotifier()
	base := time.Unix(1_700_000_000, 0)
	first := TransitionNotificationEvent{ChildSessionID: "child", FromStatus: "running", ToStatus: "waiting", LastOutputHash: "turn-a", Timestamp: base}
	second := first
	second.LastOutputHash = "turn-b"
	second.Timestamp = base.Add(time.Second)
	if n.isDuplicate(first) {
		t.Fatal("first observation was duplicate")
	}
	n.markNotified(first)
	if n.isDuplicate(second) {
		t.Fatal("distinct proven turn was suppressed inside short window")
	}
	if !n.isDuplicate(first) {
		t.Fatal("same turn retry was not suppressed")
	}
}

func TestIssue2057_TwoPendingTurnsSurviveDrain(t *testing.T) {
	inboxTestHome(t)
	parent := "parent-2057"
	for _, signal := range []string{"turn-a", "turn-b"} {
		if err := CommitToInbox(parent, TransitionNotificationEvent{
			ChildSessionID: "child", Profile: "personal", FromStatus: "running", ToStatus: "waiting",
			LastOutputHash: signal, Timestamp: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if pending := readInboxLines(t, parent); len(pending) != 2 {
		t.Fatalf("pending turns = %d, want 2", len(pending))
	}
	drained, err := DrainInboxForParent(parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(drained) != 2 {
		t.Fatalf("drained turns = %d, want 2", len(drained))
	}
}

func TestIssue2057_NotifyTransitionCommitsTwoFastTurns(t *testing.T) {
	profile, parent, child, _ := seedParentTwoChildren(t)
	n := NewTransitionNotifier()
	t.Cleanup(n.Close)
	base := time.Now()
	for i, signal := range []string{"codex-turn-a", "codex-turn-b"} {
		got := n.NotifyTransition(TransitionNotificationEvent{
			ChildSessionID: child, ChildTitle: "interactive-worker", Profile: profile,
			FromStatus: "running", ToStatus: "waiting", LastOutputHash: signal,
			Timestamp: base.Add(time.Duration(i) * time.Second),
		})
		if got.DeliveryResult != transitionDeliveryCommitted {
			t.Fatalf("turn %d result=%q", i, got.DeliveryResult)
		}
	}
	if pending := readInboxLines(t, parent); len(pending) != 2 {
		t.Fatalf("notifier pending=%d want=2", len(pending))
	}
}
