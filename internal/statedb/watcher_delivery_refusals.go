package statedb

import (
	"fmt"
	"time"
)

// watcherDeliveryRefusalsSchema keeps, per watcher and conductor, how many
// routed events the watcher outbox refused because that watcher's queue for
// the conductor was full, and when the first and the last were refused. Such
// an event is stored in watcher_events like a delivered one but is never
// delivered, so this row is its only durable trace (`watcher status`, the
// watcher's health state). Additive CREATE TABLE IF NOT EXISTS, run by every
// Migrate, as for the trigger tables: SchemaVersion is unchanged, an older
// state.db gains the table on its next open, and an older binary ignores it.
const watcherDeliveryRefusalsSchema = `
	CREATE TABLE IF NOT EXISTS watcher_delivery_refusals (
		watcher_id       TEXT NOT NULL,
		conductor        TEXT NOT NULL,
		refused          INTEGER NOT NULL DEFAULT 0,
		first_refused_at INTEGER NOT NULL,
		last_refused_at  INTEGER NOT NULL,
		PRIMARY KEY (watcher_id, conductor)
	)
`

func migrateWatcherDeliveryRefusals(e execer) error {
	if _, err := e.Exec(watcherDeliveryRefusalsSchema); err != nil {
		return fmt.Errorf("statedb: create watcher_delivery_refusals: %w", err)
	}
	return nil
}

// WatcherDeliveryRefusalRow is one watcher's refused routed events for one
// conductor.
type WatcherDeliveryRefusalRow struct {
	WatcherID      string
	Conductor      string
	Count          int
	FirstRefusedAt time.Time
	LastRefusedAt  time.Time
}

// RecordWatcherDeliveryRefusal counts one routed event of watcherID refused
// for conductor at at, and returns the pair's total so far.
func (s *StateDB) RecordWatcherDeliveryRefusal(watcherID, conductor string, at time.Time) (int, error) {
	var total int
	err := withBusyRetry(func() error {
		return s.db.QueryRow(`
			INSERT INTO watcher_delivery_refusals (watcher_id, conductor, refused, first_refused_at, last_refused_at)
			VALUES (?, ?, 1, ?, ?)
			ON CONFLICT (watcher_id, conductor) DO UPDATE SET
				refused = refused + 1,
				last_refused_at = MAX(last_refused_at, excluded.last_refused_at)
			RETURNING refused
		`, watcherID, conductor, at.Unix(), at.Unix()).Scan(&total)
	})
	if err != nil {
		return 0, fmt.Errorf("statedb: record watcher delivery refusal: %w", err)
	}
	return total, nil
}

// LoadWatcherDeliveryRefusals returns the refusal counts of watcherID, or of
// every watcher when watcherID is "", ordered by watcher id then conductor.
func (s *StateDB) LoadWatcherDeliveryRefusals(watcherID string) ([]WatcherDeliveryRefusalRow, error) {
	rows, err := s.db.Query(`
		SELECT watcher_id, conductor, refused, first_refused_at, last_refused_at
		FROM watcher_delivery_refusals
		WHERE ? = '' OR watcher_id = ?
		ORDER BY watcher_id, conductor
	`, watcherID, watcherID)
	if err != nil {
		return nil, fmt.Errorf("statedb: load watcher delivery refusals: %w", err)
	}
	defer rows.Close()
	var out []WatcherDeliveryRefusalRow
	for rows.Next() {
		var r WatcherDeliveryRefusalRow
		var first, last int64
		if err := rows.Scan(&r.WatcherID, &r.Conductor, &r.Count, &first, &last); err != nil {
			return nil, fmt.Errorf("statedb: scan watcher delivery refusal: %w", err)
		}
		r.FirstRefusedAt, r.LastRefusedAt = time.Unix(first, 0), time.Unix(last, 0)
		out = append(out, r)
	}
	return out, rows.Err()
}
