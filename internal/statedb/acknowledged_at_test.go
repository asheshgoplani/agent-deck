package statedb

import (
	"testing"
	"time"
)

// Acks saved before acknowledged_at existed get the session's last visit
// (last_accessed) as their time, so a Stop that finished after that visit,
// even while agent-deck was closed, still shows as unseen. An ack with no
// recorded visit stays at zero and any Stop clears it, as before the column.
func TestMigrate_BackfillsAcknowledgedAtForLegacyAcks(t *testing.T) {
	db := newTestDB(t)
	if err := db.SaveInstances([]*InstanceRow{ackRow("legacy-ack"), ackRow("legacy-no-visit"), ackRow("unseen")}); err != nil {
		t.Fatalf("SaveInstances: %v", err)
	}
	visit := time.Unix(time.Now().Add(-time.Hour).Unix(), 0)
	if _, err := db.db.Exec(`UPDATE instances SET acknowledged = 1, acknowledged_at = 0, last_accessed = ? WHERE id = 'legacy-ack'`, visit.Unix()); err != nil {
		t.Fatalf("seed legacy ack: %v", err)
	}
	if _, err := db.db.Exec(`UPDATE instances SET acknowledged = 1, acknowledged_at = 0, last_accessed = 0 WHERE id = 'legacy-no-visit'`); err != nil {
		t.Fatalf("seed legacy ack without a visit: %v", err)
	}

	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	statuses, err := db.ReadAllStatuses()
	if err != nil {
		t.Fatalf("ReadAllStatuses: %v", err)
	}
	if got := statuses["legacy-ack"].AcknowledgedAt; !got.Equal(visit) {
		t.Fatalf("legacy ack AcknowledgedAt = %v, want the last visit %v", got, visit)
	}
	if got := statuses["legacy-no-visit"]; !got.Acknowledged || !got.AcknowledgedAt.IsZero() {
		t.Fatalf("legacy ack without a visit got Acknowledged=%v AcknowledgedAt=%v, want true and zero", got.Acknowledged, got.AcknowledgedAt)
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
