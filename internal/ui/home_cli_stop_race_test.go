package ui

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// statusWriteFixture seeds one instance row at rowStatus and a Home whose
// status memo says it last persisted memo for that session.
func statusWriteFixture(t *testing.T, rowStatus, memo string) (*statedb.StateDB, *Home) {
	t.Helper()
	db := newClaimsTestDB(t)
	now := time.Now()
	if err := db.SaveInstance(&statedb.InstanceRow{
		ID: "worker", Title: "worker", ProjectPath: "/p", GroupPath: "g",
		Tool: "shell", Status: rowStatus, TmuxSession: "worker-pane",
		CreatedAt: now, LastAccessed: now,
	}); err != nil {
		t.Fatalf("SaveInstance: %v", err)
	}
	h := &Home{lastPersistedStatus: map[string]string{}}
	if memo != "" {
		h.lastPersistedStatus["worker"] = memo
	}
	return db, h
}

func rowStatus(t *testing.T, db *statedb.StateDB) string {
	t.Helper()
	rows, err := db.ReadAllStatuses()
	if err != nil {
		t.Fatalf("ReadAllStatuses: %v", err)
	}
	return rows["worker"].Status
}

// #2669: `agent-deck session stop` from the CLI commits "stopped" while the
// web server's Home still holds the session at waiting in memory. Its next
// sweep classifies the vanished pane as "error"; that write must not
// overwrite the deliberate stop, and Home must adopt "stopped".
func TestCLIStopSurvivesHomeStatusWrite(t *testing.T) {
	db, h := statusWriteFixture(t, "waiting", "waiting")
	if err := db.WriteStatus("worker", "stopped", "shell"); err != nil { // the CLI stop
		t.Fatalf("WriteStatus: %v", err)
	}
	inst := &session.Instance{ID: "worker", Tool: "shell", Status: session.StatusError}

	h.persistPolledStatuses(db, []*session.Instance{inst})

	if got := rowStatus(t, db); got != "stopped" {
		t.Fatalf("row status = %q, want stopped (CLI stop overwritten)", got)
	}
	if got := inst.GetStatusThreadSafe(); got != session.StatusStopped {
		t.Fatalf("in-memory status = %q, want stopped", got)
	}
	if got := h.lastPersistedStatus["worker"]; got != "stopped" {
		t.Fatalf("status memo = %q, want stopped", got)
	}
}

func TestStatusWriteAppliesWhenRowUnchanged(t *testing.T) {
	db, h := statusWriteFixture(t, "waiting", "waiting")
	inst := &session.Instance{ID: "worker", Tool: "shell", Status: session.StatusRunning}

	h.persistPolledStatuses(db, []*session.Instance{inst})

	if got := rowStatus(t, db); got != "running" {
		t.Fatalf("row status = %q, want running", got)
	}
	if got := inst.GetStatusThreadSafe(); got != session.StatusRunning {
		t.Fatalf("in-memory status = %q, want running", got)
	}
}

func TestStatusWriteFirstWriteIsUnconditional(t *testing.T) {
	db, h := statusWriteFixture(t, "waiting", "")
	inst := &session.Instance{ID: "worker", Tool: "shell", Status: session.StatusIdle}

	h.persistPolledStatuses(db, []*session.Instance{inst})

	if got := rowStatus(t, db); got != "idle" {
		t.Fatalf("row status = %q, want idle", got)
	}
}
