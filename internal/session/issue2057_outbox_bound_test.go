package session

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestIssue2057_DistinctTurnQueueIsBoundedAndOverflowObservable(t *testing.T) {
	inboxTestHome(t)
	const parent = "parent-bounded-2057"
	const child = "child-bounded-2057"

	for i := 0; i < maxPendingTurnsPerChild; i++ {
		ev := TransitionNotificationEvent{
			ChildSessionID: child, FromStatus: "running", ToStatus: "waiting",
			LastOutputHash: fmt.Sprintf("turn-%d", i), Timestamp: time.Unix(int64(i+1), 0),
		}
		if err := CommitToInbox(parent, ev); err != nil {
			t.Fatalf("commit %d below bound: %v", i, err)
		}
	}
	overflow := TransitionNotificationEvent{
		ChildSessionID: child, FromStatus: "running", ToStatus: "waiting",
		LastOutputHash: "one-turn-too-many", Timestamp: time.Unix(999, 0),
	}
	if err := CommitToInbox(parent, overflow); !errors.Is(err, ErrInboxTurnOverflow) {
		t.Fatalf("overflow error = %v, want ErrInboxTurnOverflow", err)
	}
	got := readInboxLines(t, parent)
	if len(got) != maxPendingTurnsPerChild {
		t.Fatalf("pending turns = %d, want hard bound %d", len(got), maxPendingTurnsPerChild)
	}

	// A retry at capacity remains accepted and idempotent; the bound must not
	// turn ordinary durable retry into a false overflow.
	if err := CommitToInbox(parent, got[0]); err != nil {
		t.Fatalf("retry at capacity: %v", err)
	}
	if got := readInboxLines(t, parent); len(got) != maxPendingTurnsPerChild {
		t.Fatalf("retry changed queue size to %d", len(got))
	}
}
