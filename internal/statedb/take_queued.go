package statedb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// TakeQueued takes a session off its group queue in one transaction: it
// removes the pending queued -m message from tool_data and, when the row is
// queued, moves it to starting, in the same one-row write. It returns the
// message it removed and the status it left the row at. Of concurrent takers exactly one gets the message, so
// a caller hands it off only after this returns and nothing can queue it
// twice.
//
// With claim set (a queue drain), the row must still be queued: taken is
// false, and nothing is written, when another process got there first. Without
// it (a session that was started or restarted another way), any pending
// message is taken whatever the status.
func (s *StateDB) TakeQueued(id, tool string, claim bool) (message, status string, taken bool, err error) {
	var from, tmuxName string
	err = withBusyRetry(func() error {
		message, status, taken, from, tmuxName = "", "", false, "", ""
		// BEGIN IMMEDIATE on a pinned connection, as mergeRegistrySnapshotsOnce
		// does: it takes the write lock up front and waits for a concurrent
		// writer on busy_timeout. A deferred transaction that reads first
		// cannot upgrade to a write past a concurrent commit in WAL mode; it
		// fails with SQLITE_BUSY at once, and the retry budget is far shorter
		// than busy_timeout.
		ctx := context.Background()
		conn, err := s.db.Conn(ctx)
		if err != nil {
			return err
		}
		defer conn.Close()
		if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
			return err
		}
		committed := false
		defer func() {
			if !committed {
				_, _ = conn.ExecContext(ctx, "ROLLBACK")
			}
		}()
		var current string
		var raw []byte
		if err := conn.QueryRowContext(ctx, `SELECT status, tool_data, tmux_session FROM instances WHERE id = ?`, id).Scan(&current, &raw, &tmuxName); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		if claim && current != "queued" {
			return nil
		}
		td := map[string]json.RawMessage{}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &td); err != nil {
				return fmt.Errorf("reading tool_data of %s: %w", id, err)
			}
		}
		if v, ok := td["queued_message"]; ok {
			_ = json.Unmarshal(v, &message)
			delete(td, "queued_message")
		}
		if !claim && message == "" && current != "queued" {
			return nil // nothing to take
		}
		next := current
		if current == "queued" {
			next = "starting"
		}
		out, err := json.Marshal(td)
		if err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `UPDATE instances SET status = ?, tool = ?, tool_data = ? WHERE id = ?`, next, tool, string(out), id); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
			return err
		}
		committed = true
		taken, from, status = true, current, next
		return nil
	})
	if err == nil && taken && from == "queued" {
		if observe := loadStatusChangeObserver(); observe != nil {
			observe(StatusChange{DBPath: s.path, ID: id, TmuxSession: tmuxName, From: from, To: "starting", Tool: tool, At: time.Now()})
		}
	}
	return message, status, taken, err
}
