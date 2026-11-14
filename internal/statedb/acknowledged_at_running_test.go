package statedb

import (
	"testing"
	"time"
)

func rawAckAt(t *testing.T, db *StateDB, id string) (ack int, at int64) {
	t.Helper()
	if err := db.db.QueryRow(`SELECT acknowledged, acknowledged_at FROM instances WHERE id = ?`, id).Scan(&ack, &at); err != nil {
		t.Fatalf("read ack columns: %v", err)
	}
	return ack, at
}

// A "running" write clears the seen mark. It must clear the visit time with
// it, so an unacked row never keeps an old visit time behind.
func TestRunningWrite_ClearsAcknowledgedAt(t *testing.T) {
	writers := map[string]func(db *StateDB, id string) error{
		"WriteStatus": func(db *StateDB, id string) error { return db.WriteStatus(id, "running", "claude") },
		"WriteStatusIfCurrent": func(db *StateDB, id string) error {
			_, err := db.WriteStatusIfCurrent(id, "idle", "running", "claude")
			return err
		},
		"PersistInstanceStatusesTx": func(db *StateDB, id string) error {
			return db.PersistInstanceStatusesTx([]InstanceStatusUpdate{{ID: id, Status: "running"}})
		},
		"WriteRestartOutcome": func(db *StateDB, id string) error {
			_, err := db.WriteRestartOutcome(id, "agentdeck_target_deadbeef", "running", "claude")
			return err
		},
	}
	for name, write := range writers {
		t.Run(name, func(t *testing.T) {
			db := newTestDB(t)
			if err := db.SaveInstances([]*InstanceRow{ackRow("run-ack")}); err != nil {
				t.Fatalf("SaveInstances: %v", err)
			}
			if err := db.SetAcknowledged("run-ack", true); err != nil {
				t.Fatalf("SetAcknowledged: %v", err)
			}
			if err := write(db, "run-ack"); err != nil {
				t.Fatalf("running write: %v", err)
			}
			if ack, at := rawAckAt(t, db, "run-ack"); ack != 0 || at != 0 {
				t.Fatalf("after a running write: acknowledged=%d acknowledged_at=%d, want 0 and 0", ack, at)
			}
		})
	}
}

// The older-binary case: after a running write, a binary that predates
// acknowledged_at sets acknowledged=1 without a time. The next Migrate must
// re-stamp the row from the last visit, not leave the old visit time.
func TestRunningWrite_ThenLegacyAck_MigrateRestamps(t *testing.T) {
	db := newTestDB(t)
	if err := db.SaveInstances([]*InstanceRow{ackRow("legacy-after-run")}); err != nil {
		t.Fatalf("SaveInstances: %v", err)
	}
	if err := db.SetAcknowledged("legacy-after-run", true); err != nil {
		t.Fatalf("SetAcknowledged: %v", err)
	}
	if _, err := db.db.Exec(`UPDATE instances SET acknowledged_at = ? WHERE id = 'legacy-after-run'`, time.Now().Add(-time.Hour).UnixMilli()); err != nil {
		t.Fatalf("age the visit: %v", err)
	}
	if err := db.WriteStatus("legacy-after-run", "running", "claude"); err != nil {
		t.Fatalf("WriteStatus running: %v", err)
	}
	visit := time.Unix(time.Now().Add(-time.Minute).Unix(), 0)
	if _, err := db.db.Exec(`UPDATE instances SET acknowledged = 1, last_accessed = ? WHERE id = 'legacy-after-run'`, visit.Unix()); err != nil {
		t.Fatalf("legacy ack: %v", err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	statuses, err := db.ReadAllStatuses()
	if err != nil {
		t.Fatalf("ReadAllStatuses: %v", err)
	}
	if got := statuses["legacy-after-run"].AcknowledgedAt; !got.Equal(visit) {
		t.Fatalf("AcknowledgedAt = %v, want the last visit %v (re-stamped)", got, visit)
	}
	// A second Migrate leaves the stamp alone.
	if err := db.Migrate(); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if _, at := rawAckAt(t, db, "legacy-after-run"); at != visit.UnixMilli() {
		t.Fatalf("second Migrate changed acknowledged_at to %d, want %d", at, visit.UnixMilli())
	}
}
