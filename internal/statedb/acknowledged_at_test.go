package statedb

import (
	"testing"
	"time"
)

// Marking a session seen records when it happened, so a reader can tell a
// turn that finished before the visit from one that finished after it.
func TestSetAcknowledged_StampsAcknowledgedAt(t *testing.T) {
	db := newTestDB(t)
	if err := db.SaveInstances([]*InstanceRow{ackRow("ack-at")}); err != nil {
		t.Fatalf("SaveInstances: %v", err)
	}

	before := time.Now().Add(-time.Second)
	if err := db.SetAcknowledged("ack-at", true); err != nil {
		t.Fatalf("SetAcknowledged(true): %v", err)
	}
	after := time.Now().Add(time.Second)

	statuses, err := db.ReadAllStatuses()
	if err != nil {
		t.Fatalf("ReadAllStatuses: %v", err)
	}
	got := statuses["ack-at"]
	if !got.Acknowledged {
		t.Fatal("Acknowledged = false after SetAcknowledged(true)")
	}
	if got.AcknowledgedAt.Before(before) || got.AcknowledgedAt.After(after) {
		t.Fatalf("AcknowledgedAt = %v, want between %v and %v", got.AcknowledgedAt, before, after)
	}

	if err := db.SetAcknowledged("ack-at", false); err != nil {
		t.Fatalf("SetAcknowledged(false): %v", err)
	}
	statuses, err = db.ReadAllStatuses()
	if err != nil {
		t.Fatalf("ReadAllStatuses: %v", err)
	}
	if got := statuses["ack-at"]; got.Acknowledged || !got.AcknowledgedAt.IsZero() {
		t.Fatalf("after SetAcknowledged(false): Acknowledged=%v AcknowledgedAt=%v, want false and zero",
			got.Acknowledged, got.AcknowledgedAt)
	}
}
