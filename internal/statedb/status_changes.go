package statedb

import (
	"fmt"
	"time"
)

// status_changes records when each session's status last changed and to
// what, so a fresh process (`list --json`, needs-me) can tell how long a
// session has been waiting. The in-memory tmux tracker only knows that for
// the process that watched the change.
//
// SQLite triggers on instances keep it, so every write path is covered
// without touching any of them: WriteStatus and WriteStatusIfCurrent,
// PersistInstanceStatusesTx, WriteRestartOutcome, the full-row
// INSERT OR REPLACE save and the snapshot merge. A write that keeps the
// status leaves the stamp alone. INSERT OR REPLACE's implicit delete fires
// no delete trigger (recursive_triggers is off), so a full save of an
// unchanged row keeps its stamp; an explicit DELETE drops it. Additive
// (CREATE ... IF NOT EXISTS), so SchemaVersion does not move. Rows that
// existed before the table get a stamp on their next status change.
//
// changed_at is Unix milliseconds from SQLite's clock (julianday('now')).
var statusChangeSchemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS status_changes (
		id         TEXT PRIMARY KEY,
		status     TEXT NOT NULL,
		changed_at INTEGER NOT NULL
	)`,
	`CREATE TRIGGER IF NOT EXISTS status_changes_after_update
		AFTER UPDATE OF status ON instances
		WHEN OLD.status IS NOT NEW.status
		BEGIN
			INSERT OR REPLACE INTO status_changes (id, status, changed_at)
			VALUES (NEW.id, NEW.status, CAST((julianday('now') - 2440587.5) * 86400000 AS INTEGER));
		END`,
	`CREATE TRIGGER IF NOT EXISTS status_changes_after_insert
		AFTER INSERT ON instances
		WHEN NEW.status IS NOT (SELECT status FROM status_changes WHERE id = NEW.id)
		BEGIN
			INSERT OR REPLACE INTO status_changes (id, status, changed_at)
			VALUES (NEW.id, NEW.status, CAST((julianday('now') - 2440587.5) * 86400000 AS INTEGER));
		END`,
	`CREATE TRIGGER IF NOT EXISTS status_changes_after_delete
		AFTER DELETE ON instances
		BEGIN
			DELETE FROM status_changes WHERE id = OLD.id;
		END`,
}

func migrateStatusChanges(e execer) error {
	for _, stmt := range statusChangeSchemaStatements {
		if _, err := e.Exec(stmt); err != nil {
			return fmt.Errorf("statedb: create status_changes: %w", err)
		}
	}
	return nil
}

// StatusStamp is when a session's status last changed and to what.
type StatusStamp struct {
	Status string
	At     time.Time
}

// ReadStatusChanges returns the status-change stamp of every session that
// has one.
func (s *StateDB) ReadStatusChanges() (map[string]StatusStamp, error) {
	rows, err := s.db.Query(`SELECT id, status, changed_at FROM status_changes`)
	if err != nil {
		return nil, fmt.Errorf("statedb: read status_changes: %w", err)
	}
	defer rows.Close()
	out := map[string]StatusStamp{}
	for rows.Next() {
		var id, status string
		var ms int64
		if err := rows.Scan(&id, &status, &ms); err != nil {
			return nil, fmt.Errorf("statedb: read status_changes: %w", err)
		}
		out[id] = StatusStamp{Status: status, At: time.UnixMilli(ms).UTC()}
	}
	return out, rows.Err()
}
