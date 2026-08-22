package session

import (
	"testing"
	"time"
)

func TestIssue2007_RunningToErrorDeliversToLinkedParent(t *testing.T) {
	inboxTestHome(t)
	profile := "_test-2007-linked-error"
	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	now := time.Now()
	parent := &Instance{ID: "parent-2007", Title: "supervisor", ProjectPath: "/tmp/p", GroupPath: DefaultGroupPath, Tool: "shell", Status: StatusRunning, CreatedAt: now}
	child := &Instance{ID: "child-2007", Title: "worker", ProjectPath: "/tmp/c", GroupPath: DefaultGroupPath, ParentSessionID: parent.ID, Tool: "shell", Status: StatusError, CreatedAt: now}
	if err := storage.SaveWithGroups([]*Instance{parent, child}, nil); err != nil {
		t.Fatal(err)
	}

	got := NewTransitionNotifier().NotifyTransition(TransitionNotificationEvent{
		ChildSessionID: child.ID, ChildTitle: child.Title, Profile: profile,
		FromStatus: "running", ToStatus: "error", Timestamp: now,
	})
	if got.DeliveryResult != transitionDeliveryCommitted {
		t.Fatalf("running→error must commit to linked parent, got %+v", got)
	}
	events, err := DrainInboxForParent(parent.ID)
	if err != nil || len(events) != 1 || events[0].ToStatus != "error" {
		t.Fatalf("linked parent did not receive error transition: events=%+v err=%v", events, err)
	}
}

func TestIssue2007_UnresolvableParentAlsoLandsInUnownedLedger(t *testing.T) {
	inboxTestHome(t)
	profile := "_test-2007-unowned"
	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close()
	now := time.Now()
	child := &Instance{ID: "child-unowned-2007", Title: "worker", ProjectPath: "/tmp/c", GroupPath: DefaultGroupPath, ParentSessionID: "missing-parent", Tool: "shell", Status: StatusError, CreatedAt: now}
	if err := storage.SaveWithGroups([]*Instance{child}, nil); err != nil {
		t.Fatal(err)
	}

	got := NewTransitionNotifier().NotifyTransition(TransitionNotificationEvent{
		ChildSessionID: child.ID, ChildTitle: child.Title, Profile: profile,
		FromStatus: "running", ToStatus: "error", Timestamp: now,
	})
	if got.DeliveryResult != transitionDeliveryCommitted {
		t.Fatalf("durable _unowned append must report committed, got %+v", got)
	}
	events, err := ReadAndTruncateInbox(UnownedInboxID)
	if err != nil || len(events) != 1 || events[0].ChildSessionID != child.ID {
		t.Fatalf("unresolvable transition was not preserved in _unowned: events=%+v err=%v", events, err)
	}
	if events[0].DeadLetterReason != deadLetterReasonParentMissing {
		t.Fatalf("_unowned record lost resolution reason: got %q want %q", events[0].DeadLetterReason, deadLetterReasonParentMissing)
	}
}

func TestIssue2007_TrueOrphanPreservesIssue805DropContract(t *testing.T) {
	if isUnownedReason(deadLetterReasonOrphan) {
		t.Fatal("a true parentless orphan must not enter the unowned discovery ledger")
	}
}
