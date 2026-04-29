package costs

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Store persists and queries cost events in SQLite.
type Store struct {
	db *sql.DB
}

// NewStore creates a Store using an existing database connection.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// DB returns the underlying database for transactional operations.
func (s *Store) DB() *sql.DB {
	return s.db
}

// WriteCostEvent inserts a cost event.
func (s *Store) WriteCostEvent(ev CostEvent) error {
	_, err := s.db.Exec(`
		INSERT INTO cost_events (id, session_id, timestamp, model, input_tokens, output_tokens,
			cache_read_tokens, cache_write_tokens, cost_microdollars)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ev.ID, ev.SessionID, ev.Timestamp.UTC().Format(time.RFC3339), ev.Model,
		ev.InputTokens, ev.OutputTokens, ev.CacheReadTokens, ev.CacheWriteTokens,
		ev.CostMicrodollars,
	)
	return err
}

// WriteCostEventTx inserts a cost event within a transaction.
func (s *Store) WriteCostEventTx(tx *sql.Tx, ev CostEvent) error {
	_, err := tx.Exec(`
		INSERT INTO cost_events (id, session_id, timestamp, model, input_tokens, output_tokens,
			cache_read_tokens, cache_write_tokens, cost_microdollars)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ev.ID, ev.SessionID, ev.Timestamp.UTC().Format(time.RFC3339), ev.Model,
		ev.InputTokens, ev.OutputTokens, ev.CacheReadTokens, ev.CacheWriteTokens,
		ev.CostMicrodollars,
	)
	return err
}

// TotalBySession returns aggregated costs for a session.
func (s *Store) TotalBySession(sessionID string) (CostSummary, error) {
	return s.querySum(`WHERE session_id = ?`, sessionID)
}

// TotalToday returns today's total costs.
func (s *Store) TotalToday() (CostSummary, error) {
	return s.querySum(`WHERE timestamp >= date('now', 'start of day')`)
}

// TotalThisWeek returns this week's total costs (Monday start).
func (s *Store) TotalThisWeek() (CostSummary, error) {
	return s.querySum(`WHERE timestamp >= date('now', 'weekday 1', '-7 days')`)
}

// TotalThisMonth returns this month's total costs.
func (s *Store) TotalThisMonth() (CostSummary, error) {
	return s.querySum(`WHERE timestamp >= date('now', 'start of month')`)
}

// TopSessionsByCost returns the top N sessions by total cost.
// Joins with instances table to get session titles and groups.
func (s *Store) TopSessionsByCost(limit int) ([]SessionCost, error) {
	rows, err := s.db.Query(`
		SELECT ce.session_id, COALESCE(i.title, ce.session_id), COALESCE(i.group_path, ''),
			SUM(ce.cost_microdollars), COUNT(*)
		FROM cost_events ce
		LEFT JOIN instances i ON ce.session_id = i.id
		GROUP BY ce.session_id
		ORDER BY SUM(ce.cost_microdollars) DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []SessionCost
	for rows.Next() {
		var sc SessionCost
		if err := rows.Scan(&sc.SessionID, &sc.SessionTitle, &sc.Group, &sc.CostMicrodollars, &sc.EventCount); err != nil {
			return nil, err
		}
		result = append(result, sc)
	}
	return result, rows.Err()
}

// CostByModel returns total cost per model.
func (s *Store) CostByModel() (map[string]int64, error) {
	rows, err := s.db.Query(`
		SELECT model, SUM(cost_microdollars)
		FROM cost_events
		GROUP BY model`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]int64)
	for rows.Next() {
		var model string
		var cost int64
		if err := rows.Scan(&model, &cost); err != nil {
			return nil, err
		}
		result[model] = cost
	}
	return result, rows.Err()
}

// TotalByDateRange returns daily costs within a date range.
func (s *Store) TotalByDateRange(from, to time.Time) ([]DailyCost, error) {
	rows, err := s.db.Query(`
		SELECT date(timestamp), SUM(cost_microdollars)
		FROM cost_events
		WHERE timestamp >= ? AND timestamp < ?
		GROUP BY date(timestamp)
		ORDER BY date(timestamp)`,
		from.UTC().Format(time.RFC3339), to.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []DailyCost
	for rows.Next() {
		var dateStr string
		var dc DailyCost
		if err := rows.Scan(&dateStr, &dc.CostMicrodollars); err != nil {
			return nil, err
		}
		dc.Date, _ = time.Parse("2006-01-02", dateStr)
		result = append(result, dc)
	}
	return result, rows.Err()
}

// ProjectedMonthly estimates monthly spend based on rolling 7-day average.
func (s *Store) ProjectedMonthly() (int64, error) {
	var total sql.NullInt64
	err := s.db.QueryRow(`
		SELECT SUM(cost_microdollars)
		FROM cost_events
		WHERE timestamp >= datetime('now', '-7 days')`).Scan(&total)
	if err != nil {
		return 0, err
	}
	if !total.Valid {
		return 0, nil
	}
	dailyAvg := total.Int64 / 7
	return dailyAvg * 30, nil
}

// PurgeOlderThan deletes events older than the given number of days. Returns count deleted.
func (s *Store) PurgeOlderThan(days int) (int64, error) {
	result, err := s.db.Exec(`
		DELETE FROM cost_events
		WHERE timestamp < datetime('now', ? || ' days')`,
		fmt.Sprintf("-%d", days))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// RunningTotal returns the sum of costs for a session within a time window (for use in a transaction).
func (s *Store) RunningTotal(tx *sql.Tx, sessionID string, since time.Time) (int64, error) {
	var total sql.NullInt64
	err := tx.QueryRow(`
		SELECT SUM(cost_microdollars) FROM cost_events
		WHERE session_id = ? AND timestamp >= ?`,
		sessionID, since.UTC().Format(time.RFC3339)).Scan(&total)
	if err != nil {
		return 0, err
	}
	if !total.Valid {
		return 0, nil
	}
	return total.Int64, nil
}

// GlobalRunningTotal returns the sum of all costs within a time window (for use in a transaction).
func (s *Store) GlobalRunningTotal(tx *sql.Tx, since time.Time) (int64, error) {
	var total sql.NullInt64
	err := tx.QueryRow(`
		SELECT SUM(cost_microdollars) FROM cost_events
		WHERE timestamp >= ?`, since.UTC().Format(time.RFC3339)).Scan(&total)
	if err != nil {
		return 0, err
	}
	if !total.Valid {
		return 0, nil
	}
	return total.Int64, nil
}

// GroupRunningTotal returns the sum of costs for a set of sessions within a time window.
func (s *Store) GroupRunningTotal(tx *sql.Tx, sessionIDs []string, since time.Time) (int64, error) {
	if len(sessionIDs) == 0 {
		return 0, nil
	}
	placeholders := "?" + repeatArg(len(sessionIDs)-1)
	query := fmt.Sprintf(`SELECT COALESCE(SUM(cost_microdollars), 0) FROM cost_events WHERE session_id IN (%s) AND timestamp >= ?`, placeholders)
	args := make([]any, len(sessionIDs)+1)
	for i, id := range sessionIDs {
		args[i] = id
	}
	args[len(sessionIDs)] = since.UTC().Format(time.RFC3339)
	var total int64
	err := tx.QueryRow(query, args...).Scan(&total)
	return total, err
}

func (s *Store) querySum(where string, args ...any) (CostSummary, error) {
	var cs CostSummary
	err := s.db.QueryRow(`
		SELECT COALESCE(SUM(cost_microdollars), 0),
			COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(cache_read_tokens), 0),
			COALESCE(SUM(cache_write_tokens), 0),
			COUNT(*)
		FROM cost_events `+where, args...).Scan(
		&cs.TotalCostMicrodollars,
		&cs.TotalInputTokens,
		&cs.TotalOutputTokens,
		&cs.TotalCacheReadTokens,
		&cs.TotalCacheWriteTokens,
		&cs.EventCount,
	)
	return cs, err
}

// DailyBySession returns daily costs for a specific session.
func (s *Store) DailyBySession(sessionID string, from, to time.Time) ([]DailyCost, error) {
	rows, err := s.db.Query(`
		SELECT date(timestamp), SUM(cost_microdollars)
		FROM cost_events
		WHERE session_id = ? AND timestamp >= ? AND timestamp < ?
		GROUP BY date(timestamp)
		ORDER BY date(timestamp)`,
		sessionID, from.UTC().Format(time.RFC3339), to.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []DailyCost
	for rows.Next() {
		var dateStr string
		var dc DailyCost
		if err := rows.Scan(&dateStr, &dc.CostMicrodollars); err != nil {
			return nil, err
		}
		dc.Date, _ = time.Parse("2006-01-02", dateStr)
		result = append(result, dc)
	}
	return result, rows.Err()
}

// CostByModelForSession returns cost per model for a specific session.
func (s *Store) CostByModelForSession(sessionID string) (map[string]int64, error) {
	rows, err := s.db.Query(`
		SELECT model, SUM(cost_microdollars)
		FROM cost_events
		WHERE session_id = ?
		GROUP BY model`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]int64)
	for rows.Next() {
		var model string
		var cost int64
		if err := rows.Scan(&model, &cost); err != nil {
			return nil, err
		}
		result[model] = cost
	}
	return result, rows.Err()
}

func repeatArg(n int) string {
	s := ""
	for i := 0; i < n; i++ {
		s += ", ?"
	}
	return s
}

// PageEventsAfter returns up to `limit` cost_events with rowid > afterRowID,
// ordered by rowid ascending, plus the rowid of the last returned row (or
// afterRowID itself if no rows were returned). Use 0 as the initial
// afterRowID. Cursor-based pagination is stable under concurrent inserts.
func (s *Store) PageEventsAfter(afterRowID int64, limit int) ([]CostEvent, int64, error) {
	rows, err := s.db.Query(`
		SELECT rowid, id, session_id, timestamp, model,
			input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
			cost_microdollars
		FROM cost_events
		WHERE rowid > ?
		ORDER BY rowid ASC
		LIMIT ?`, afterRowID, limit)
	if err != nil {
		return nil, afterRowID, err
	}
	defer rows.Close()

	lastRowID := afterRowID
	var result []CostEvent
	for rows.Next() {
		var (
			rowid int64
			ev    CostEvent
			ts    string
		)
		if err := rows.Scan(
			&rowid, &ev.ID, &ev.SessionID, &ts, &ev.Model,
			&ev.InputTokens, &ev.OutputTokens, &ev.CacheReadTokens, &ev.CacheWriteTokens,
			&ev.CostMicrodollars,
		); err != nil {
			return nil, afterRowID, err
		}
		ev.Timestamp, _ = time.Parse(time.RFC3339, ts)
		lastRowID = rowid
		result = append(result, ev)
	}
	return result, lastRowID, rows.Err()
}

// ApplyCostUpdates writes a batch of cost_microdollars updates within a single
// transaction. The map key is cost_event id. Returns an error and rolls back
// on any failure; on success commits and returns nil.
func (s *Store) ApplyCostUpdates(ctx context.Context, updates map[string]int64) error {
	if len(updates) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, `UPDATE cost_events SET cost_microdollars = ? WHERE id = ?`)
	if err != nil {
		return fmt.Errorf("prepare update: %w", err)
	}
	defer stmt.Close()

	for id, value := range updates {
		if _, err := stmt.ExecContext(ctx, value, id); err != nil {
			return fmt.Errorf("update %s: %w", id, err)
		}
	}
	return tx.Commit()
}
