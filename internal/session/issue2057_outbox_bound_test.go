package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// Duplicate stored records for one logical turn must not consume capacity.
// rewriteInboxLocked keeps lines it cannot attribute, and a crash between
// rewrite and append can leave two rows for the same TurnFingerprint, so the
// bound has to count distinct turns rather than rows.
func TestIssue2057_DuplicateStoredTurnsDoNotConsumeCapacity(t *testing.T) {
	inboxTestHome(t)
	const parent = "parent-dupbound-2057"
	const child = "child-dupbound-2057"

	path := InboxPathFor(parent)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	distinct := maxPendingTurnsPerChild / 2
	for i := 0; i < distinct; i++ {
		ev := TransitionNotificationEvent{
			ChildSessionID: child, FromStatus: "running", ToStatus: "waiting",
			LastOutputHash: fmt.Sprintf("dup-turn-%d", i), Timestamp: time.Unix(int64(i+1), 0),
		}
		ev.TurnFingerprint = TurnFingerprint(ev)
		for copyIdx := 0; copyIdx < 2; copyIdx++ {
			line, err := json.Marshal(inboxWireEvent{TransitionNotificationEvent: ev, Fingerprint: EventFingerprint(ev)})
			if err != nil {
				t.Fatal(err)
			}
			buf.Write(line)
			buf.WriteByte('\n')
		}
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := len(readInboxLines(t, parent)); got != maxPendingTurnsPerChild {
		t.Fatalf("seeded rows = %d, want %d", got, maxPendingTurnsPerChild)
	}

	fresh := TransitionNotificationEvent{
		ChildSessionID: child, FromStatus: "running", ToStatus: "waiting",
		LastOutputHash: "dup-turn-fresh", Timestamp: time.Unix(9001, 0),
	}
	if err := CommitToInbox(parent, fresh); err != nil {
		t.Fatalf("commit with %d distinct turns stored: %v", distinct, err)
	}
	if err := CommitToInbox(parent, fresh); err != nil {
		t.Fatalf("retry of the fresh turn: %v", err)
	}
	seen := map[string]struct{}{}
	for _, ev := range readInboxLines(t, parent) {
		fp := ev.TurnFingerprint
		if fp == "" {
			fp = TurnFingerprint(ev)
		}
		seen[fp] = struct{}{}
	}
	if len(seen) != distinct+1 {
		t.Fatalf("distinct turns = %d, want %d", len(seen), distinct+1)
	}
}
