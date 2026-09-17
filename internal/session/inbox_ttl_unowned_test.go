package session

import (
	"testing"
	"time"
)

// Messaging audit P3-1: the hourly TTL sweep also emptied the _unowned ledger,
// which has no consumer and no ack path, so dead-letter counts drifted
// (9→7, 26→10) with no operator action and the records were gone for good.
// Until an ack/replay path exists the ledger is excluded from the TTL sweep;
// ordinary parent inboxes are still swept.
func TestSweepInboxByTTL_SkipsUnownedLedger(t *testing.T) {
	inboxTestHome(t)
	old := time.Now().Add(-30 * 24 * time.Hour)
	stale := TransitionNotificationEvent{
		Profile: "default", ChildSessionID: "child-stale", FromStatus: "running", ToStatus: "waiting",
		Timestamp: old,
	}
	if err := WriteInboxEvent(UnownedInboxID, stale); err != nil {
		t.Fatal(err)
	}
	if err := WriteInboxEvent("parent-ttl", stale); err != nil {
		t.Fatal(err)
	}

	dropped, err := SweepInboxByTTL(7 * 24 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if dropped != 1 {
		t.Fatalf("dropped = %d, want 1 (the parent inbox record only)", dropped)
	}
	if got := readInboxLines(t, UnownedInboxID); len(got) != 1 {
		t.Fatalf("_unowned must survive the TTL sweep, got %d records", len(got))
	}
	if got := readInboxLines(t, "parent-ttl"); len(got) != 0 {
		t.Fatalf("parent inbox must still be swept, got %d records", len(got))
	}
}
