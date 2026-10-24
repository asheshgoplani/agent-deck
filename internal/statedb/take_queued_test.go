package statedb

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"
)

// commitAfter writes on its own connection under BEGIN IMMEDIATE, holds the
// write lock for d, then commits, as a busy TUI or monitor does.
func commitAfter(t *testing.T, dbPath string, d time.Duration, stmt string, args ...any) <-chan struct{} {
	t.Helper()
	holder, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	conn, err := holder.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, stmt, args...); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		time.Sleep(d)
		_, _ = conn.ExecContext(ctx, "COMMIT")
		_ = conn.Close()
		_ = holder.Close()
		close(done)
	}()
	return done
}

// A queue take must wait for another connection's write like the single
// UPDATE claim it replaced (busy_timeout), not fail after the short retry
// budget: a read-then-write transaction cannot upgrade past a concurrent
// commit in WAL mode.
func TestTakeQueuedWaitsForAConcurrentWriter(t *testing.T) {
	db, dbPath := newTestDBAtPath(t)
	td, _ := json.Marshal(map[string]string{"queued_message": "brief"})
	for _, id := range []string{"q", "other"} {
		row := &InstanceRow{ID: id, Title: id, ProjectPath: "/tmp", GroupPath: "g", Tool: "shell", Status: "queued", ToolData: td}
		if err := db.SaveInstance(row); err != nil {
			t.Fatal(err)
		}
	}
	done := commitAfter(t, dbPath, 600*time.Millisecond, `UPDATE instances SET status = 'idle' WHERE id = 'other'`)
	time.Sleep(50 * time.Millisecond)

	message, status, taken, err := db.TakeQueued("q", "shell", true)
	<-done
	if err != nil || !taken || message != "brief" || status != "starting" {
		t.Fatalf("TakeQueued under a concurrent writer = %q, %q, %v, %v; want brief, starting, taken", message, status, taken, err)
	}
}

// The status-change event of a take names the session's tmux session, as
// every other status write's event does.
func TestTakeQueuedEventNamesTheTmuxSession(t *testing.T) {
	db, _ := newTestDBAtPath(t)
	td, _ := json.Marshal(map[string]string{"queued_message": "brief"})
	if err := db.SaveInstance(&InstanceRow{ID: "q", Title: "q", ProjectPath: "/tmp", GroupPath: "g", Tool: "shell", Status: "queued", TmuxSession: "agentdeck_q_1", ToolData: td}); err != nil {
		t.Fatal(err)
	}
	var got []StatusChange
	SetStatusChangeObserver(func(c StatusChange) { got = append(got, c) })
	t.Cleanup(func() { SetStatusChangeObserver(nil) })

	if _, _, taken, err := db.TakeQueued("q", "shell", true); err != nil || !taken {
		t.Fatalf("take: %v %v", taken, err)
	}
	if len(got) != 1 || got[0].TmuxSession != "agentdeck_q_1" || got[0].From != "queued" || got[0].To != "starting" {
		t.Fatalf("events = %+v, want one queued -> starting for agentdeck_q_1", got)
	}
}
