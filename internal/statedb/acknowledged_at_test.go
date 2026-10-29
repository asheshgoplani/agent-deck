package statedb

import (
	"testing"
	"time"
)

// A session marked seen before acknowledged_at existed (or by an older binary
// sharing the database) has no visit time. Migrate gives it "now": had the
// agent run since the visit, writing "running" would already have cleared
// acknowledged. Without this, every pre-upgrade acknowledgment keeps the old
// flicker until the user opens that session again.
func TestMigrate_BackfillsAcknowledgedAtForLegacyAcks(t *testing.T) {
	db := newTestDB(t)
	if err := db.SaveInstances([]*InstanceRow{ackRow("legacy-ack"), ackRow("unseen")}); err != nil {
		t.Fatalf("SaveInstances: %v", err)
	}
	if _, err := db.db.Exec(`UPDATE instances SET acknowledged = 1, acknowledged_at = 0 WHERE id = 'legacy-ack'`); err != nil {
		t.Fatalf("seed legacy ack: %v", err)
	}

	before := time.Now().Add(-time.Second)
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	after := time.Now().Add(time.Second)

	statuses, err := db.ReadAllStatuses()
	if err != nil {
		t.Fatalf("ReadAllStatuses: %v", err)
	}
	if got := statuses["legacy-ack"].AcknowledgedAt; got.Before(before) || got.After(after) {
		t.Fatalf("legacy ack AcknowledgedAt = %v, want backfilled between %v and %v", got, before, after)
	}
	if got := statuses["unseen"]; got.Acknowledged || !got.AcknowledgedAt.IsZero() {
		t.Fatalf("unseen session got Acknowledged=%v AcknowledgedAt=%v, want untouched", got.Acknowledged, got.AcknowledgedAt)
	}
}

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
