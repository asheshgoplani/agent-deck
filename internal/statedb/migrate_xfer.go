package statedb

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Low-level helpers used by the cross-profile migration code in
// internal/session/profile_migrate.go (issue #928). They are deliberately
// small wrappers over single-row SQL: the migration orchestrator composes
// them into a target-write-then-source-delete sequence with best-effort
// rollback. Each writer is wrapped in withBusyRetry because cross-profile
// moves race against background heartbeat/status writers on the source DB.

// LoadInstanceByID returns the row with the given id, or (nil, nil) if it
// does not exist. Any other error (driver, schema, etc.) is returned as-is.
func (s *StateDB) LoadInstanceByID(id string) (*InstanceRow, error) {
	row := &InstanceRow{}
	var createdUnix, accessedUnix int64
	var toolDataStr string
	var isConductorInt, noTransitionNotifyInt, titleLockedInt int
	err := s.db.QueryRow(`
		SELECT id, title, project_path, group_path, sort_order,
			command, wrapper, tool, status, tmux_session, tmux_socket_name,
			created_at, last_accessed,
			parent_session_id, is_conductor, no_transition_notify,
			worktree_path, worktree_repo, worktree_branch,
			tool_data, title_locked
		FROM instances WHERE id = ?
	`, id).Scan(
		&row.ID, &row.Title, &row.ProjectPath, &row.GroupPath, &row.Order,
		&row.Command, &row.Wrapper, &row.Tool, &row.Status, &row.TmuxSession, &row.TmuxSocketName,
		&createdUnix, &accessedUnix,
		&row.ParentSessionID, &isConductorInt, &noTransitionNotifyInt,
		&row.WorktreePath, &row.WorktreeRepo, &row.WorktreeBranch,
		&toolDataStr, &titleLockedInt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	row.CreatedAt = time.Unix(createdUnix, 0)
	if accessedUnix > 0 {
		row.LastAccessed = time.Unix(accessedUnix, 0)
	}
	row.IsConductor = isConductorInt != 0
	row.NoTransitionNotify = noTransitionNotifyInt != 0
	row.TitleLocked = titleLockedInt != 0
	row.ToolData = json.RawMessage(toolDataStr)
	return row, nil
}

// LoadInstanceChildren returns rows whose parent_session_id matches the given id.
func (s *StateDB) LoadInstanceChildren(parentID string) ([]*InstanceRow, error) {
	rows, err := s.db.Query(`
		SELECT id, title, project_path, group_path, sort_order,
			command, wrapper, tool, status, tmux_session, tmux_socket_name,
			created_at, last_accessed,
			parent_session_id, is_conductor, no_transition_notify,
			worktree_path, worktree_repo, worktree_branch,
			tool_data, title_locked
		FROM instances WHERE parent_session_id = ? ORDER BY sort_order
	`, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*InstanceRow
	for rows.Next() {
		r := &InstanceRow{}
		var createdUnix, accessedUnix int64
		var toolDataStr string
		var isConductorInt, noTransitionNotifyInt, titleLockedInt int
		if err := rows.Scan(
			&r.ID, &r.Title, &r.ProjectPath, &r.GroupPath, &r.Order,
			&r.Command, &r.Wrapper, &r.Tool, &r.Status, &r.TmuxSession, &r.TmuxSocketName,
			&createdUnix, &accessedUnix,
			&r.ParentSessionID, &isConductorInt, &noTransitionNotifyInt,
			&r.WorktreePath, &r.WorktreeRepo, &r.WorktreeBranch,
			&toolDataStr, &titleLockedInt,
		); err != nil {
			return nil, err
		}
		r.CreatedAt = time.Unix(createdUnix, 0)
		if accessedUnix > 0 {
			r.LastAccessed = time.Unix(accessedUnix, 0)
		}
		r.IsConductor = isConductorInt != 0
		r.NoTransitionNotify = noTransitionNotifyInt != 0
		r.TitleLocked = titleLockedInt != 0
		r.ToolData = json.RawMessage(toolDataStr)
		out = append(out, r)
	}
	return out, rows.Err()
}

// LoadInstancesByGroup returns rows whose group_path exactly matches the given path.
func (s *StateDB) LoadInstancesByGroup(groupPath string) ([]*InstanceRow, error) {
	rows, err := s.db.Query(`
		SELECT id, title, project_path, group_path, sort_order,
			command, wrapper, tool, status, tmux_session, tmux_socket_name,
			created_at, last_accessed,
			parent_session_id, is_conductor, no_transition_notify,
			worktree_path, worktree_repo, worktree_branch,
			tool_data, title_locked
		FROM instances WHERE group_path = ? ORDER BY sort_order
	`, groupPath)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*InstanceRow
	for rows.Next() {
		r := &InstanceRow{}
		var createdUnix, accessedUnix int64
		var toolDataStr string
		var isConductorInt, noTransitionNotifyInt, titleLockedInt int
		if err := rows.Scan(
			&r.ID, &r.Title, &r.ProjectPath, &r.GroupPath, &r.Order,
			&r.Command, &r.Wrapper, &r.Tool, &r.Status, &r.TmuxSession, &r.TmuxSocketName,
			&createdUnix, &accessedUnix,
			&r.ParentSessionID, &isConductorInt, &noTransitionNotifyInt,
			&r.WorktreePath, &r.WorktreeRepo, &r.WorktreeBranch,
			&toolDataStr, &titleLockedInt,
		); err != nil {
			return nil, err
		}
		r.CreatedAt = time.Unix(createdUnix, 0)
		if accessedUnix > 0 {
			r.LastAccessed = time.Unix(accessedUnix, 0)
		}
		r.IsConductor = isConductorInt != 0
		r.NoTransitionNotify = noTransitionNotifyInt != 0
		r.TitleLocked = titleLockedInt != 0
		r.ToolData = json.RawMessage(toolDataStr)
		out = append(out, r)
	}
	return out, rows.Err()
}

// InsertInstanceRow inserts (or replaces) a single instance row. Unlike
// SaveInstance it does not merge tool_data extras — cross-profile migration
// is a verbatim transfer and the caller has already prepared the row.
func (s *StateDB) InsertInstanceRow(inst *InstanceRow) error {
	toolData := inst.ToolData
	if len(toolData) == 0 {
		toolData = json.RawMessage("{}")
	}
	isConductorInt := 0
	if inst.IsConductor {
		isConductorInt = 1
	}
	noTransitionNotifyInt := 0
	if inst.NoTransitionNotify {
		noTransitionNotifyInt = 1
	}
	titleLockedInt := 0
	if inst.TitleLocked {
		titleLockedInt = 1
	}
	return withBusyRetry(func() error {
		_, err := s.db.Exec(`
			INSERT OR REPLACE INTO instances (
				id, title, project_path, group_path, sort_order,
				command, wrapper, tool, status, tmux_session, tmux_socket_name,
				created_at, last_accessed,
				parent_session_id, is_conductor, no_transition_notify,
				worktree_path, worktree_repo, worktree_branch,
				tool_data, title_locked
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`,
			inst.ID, inst.Title, inst.ProjectPath, inst.GroupPath, inst.Order,
			inst.Command, inst.Wrapper, inst.Tool, inst.Status, inst.TmuxSession, inst.TmuxSocketName,
			inst.CreatedAt.Unix(), instLastAccessedUnix(inst),
			inst.ParentSessionID, isConductorInt, noTransitionNotifyInt,
			inst.WorktreePath, inst.WorktreeRepo, inst.WorktreeBranch,
			string(toolData), titleLockedInt,
		)
		return err
	})
}

// DeleteInstanceRow removes a single row by ID. Cost / watcher rows are NOT
// touched — those are deleted explicitly by the migration so the orchestrator
// can sequence cleanup with the target-write phase.
func (s *StateDB) DeleteInstanceRow(id string) error {
	return withBusyRetry(func() error {
		_, err := s.db.Exec(`DELETE FROM instances WHERE id = ?`, id)
		return err
	})
}

// --- cost_events round-trip ---

// LoadCostEventsForSession returns every cost_events row matching session_id.
// Timestamp is preserved verbatim as TEXT — we can't reliably round-trip
// through time.Time without risking timezone drift.
func (s *StateDB) LoadCostEventsForSession(sessionID string) ([]*CostEventRow, error) {
	rows, err := s.db.Query(`
		SELECT id, session_id, timestamp, model,
			input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
			cost_microdollars, budget_stop_triggered
		FROM cost_events WHERE session_id = ?
	`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*CostEventRow
	for rows.Next() {
		r := &CostEventRow{}
		var budgetStop int
		if err := rows.Scan(
			&r.ID, &r.SessionID, &r.Timestamp, &r.Model,
			&r.InputTokens, &r.OutputTokens, &r.CacheReadTokens, &r.CacheWriteTokens,
			&r.CostMicrodollars, &budgetStop,
		); err != nil {
			return nil, err
		}
		r.BudgetStopTriggered = budgetStop != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// InsertCostEventRow inserts a single cost_events row verbatim, preserving id
// (which is also the dedup key in costs.WriteCostEvent — using INSERT OR
// IGNORE makes the migration safely retriable).
func (s *StateDB) InsertCostEventRow(ev *CostEventRow) error {
	budgetStop := 0
	if ev.BudgetStopTriggered {
		budgetStop = 1
	}
	return withBusyRetry(func() error {
		_, err := s.db.Exec(`
			INSERT OR IGNORE INTO cost_events (
				id, session_id, timestamp, model,
				input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
				cost_microdollars, budget_stop_triggered
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`,
			ev.ID, ev.SessionID, ev.Timestamp, ev.Model,
			ev.InputTokens, ev.OutputTokens, ev.CacheReadTokens, ev.CacheWriteTokens,
			ev.CostMicrodollars, budgetStop,
		)
		return err
	})
}

// DeleteCostEventsForSession removes every cost_events row matching session_id.
func (s *StateDB) DeleteCostEventsForSession(sessionID string) error {
	return withBusyRetry(func() error {
		_, err := s.db.Exec(`DELETE FROM cost_events WHERE session_id = ?`, sessionID)
		return err
	})
}

// --- watcher_events round-trip ---

// LoadWatcherEventsForSession returns every watcher_events row whose
// session_id OR triage_session_id matches sessionID. The dual match preserves
// triage links when migrating a triage target.
func (s *StateDB) LoadWatcherEventsForSession(sessionID string) ([]*WatcherEventRow, error) {
	rows, err := s.db.Query(`
		SELECT id, watcher_id, dedup_key, sender, subject, routed_to,
			session_id, triage_session_id, created_at
		FROM watcher_events WHERE session_id = ? OR triage_session_id = ?
	`, sessionID, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*WatcherEventRow
	for rows.Next() {
		var r WatcherEventRow
		var createdAt int64
		if err := rows.Scan(
			&r.ID, &r.WatcherID, &r.DedupKey, &r.Sender, &r.Subject, &r.RoutedTo,
			&r.SessionID, &r.TriageSessionID, &createdAt,
		); err != nil {
			return nil, err
		}
		r.CreatedAt = time.Unix(createdAt, 0)
		out = append(out, &r)
	}
	return out, rows.Err()
}

// InsertWatcherEventRow inserts a watcher_events row with INSERT OR IGNORE
// against the (watcher_id, dedup_key) UNIQUE constraint — safe to retry.
// Note: the source row's `id` (auto-increment) is intentionally NOT preserved;
// the unique constraint is what dedupes across DBs.
func (s *StateDB) InsertWatcherEventRow(ev *WatcherEventRow) error {
	createdAt := ev.CreatedAt.Unix()
	if createdAt <= 0 {
		createdAt = time.Now().Unix()
	}
	return withBusyRetry(func() error {
		_, err := s.db.Exec(`
			INSERT OR IGNORE INTO watcher_events (
				watcher_id, dedup_key, sender, subject, routed_to,
				session_id, triage_session_id, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`,
			ev.WatcherID, ev.DedupKey, ev.Sender, ev.Subject, ev.RoutedTo,
			ev.SessionID, ev.TriageSessionID, createdAt,
		)
		return err
	})
}

// DeleteWatcherEventsForSession removes watcher_events rows whose session_id
// OR triage_session_id matches the given sessionID.
func (s *StateDB) DeleteWatcherEventsForSession(sessionID string) error {
	return withBusyRetry(func() error {
		_, err := s.db.Exec(
			`DELETE FROM watcher_events WHERE session_id = ? OR triage_session_id = ?`,
			sessionID, sessionID,
		)
		return err
	})
}

// LoadWatcherByID returns the watcher row with the given id, or (nil, nil)
// if absent. Used by cross-profile migration to copy referenced watcher rows
// from src to dst before inserting watcher_events (the events table FK-
// references watchers(id) with foreign_keys=on).
func (s *StateDB) LoadWatcherByID(id string) (*WatcherRow, error) {
	var w WatcherRow
	var createdAt, updatedAt int64
	err := s.db.QueryRow(`
		SELECT id, name, type, config_path, status, conductor, created_at, updated_at
		FROM watchers WHERE id = ?
	`, id).Scan(&w.ID, &w.Name, &w.Type, &w.ConfigPath, &w.Status, &w.Conductor, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	w.CreatedAt = time.Unix(createdAt, 0)
	w.UpdatedAt = time.Unix(updatedAt, 0)
	return &w, nil
}

// --- group round-trip ---

// LoadGroup returns the row for the given path, or (nil, nil) if absent.
func (s *StateDB) LoadGroup(path string) (*GroupRow, error) {
	var g GroupRow
	var expanded int
	err := s.db.QueryRow(`
		SELECT path, name, expanded, sort_order, default_path
		FROM groups WHERE path = ?
	`, path).Scan(&g.Path, &g.Name, &expanded, &g.Order, &g.DefaultPath)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	g.Expanded = expanded != 0
	return &g, nil
}

// SaveGroup inserts or replaces a single group row.
func (s *StateDB) SaveGroup(g *GroupRow) error {
	if g == nil || g.Path == "" {
		return fmt.Errorf("statedb: SaveGroup requires non-empty path")
	}
	expanded := 0
	if g.Expanded {
		expanded = 1
	}
	return withBusyRetry(func() error {
		_, err := s.db.Exec(`
			INSERT OR REPLACE INTO groups (path, name, expanded, sort_order, default_path)
			VALUES (?, ?, ?, ?, ?)
		`, g.Path, g.Name, expanded, g.Order, g.DefaultPath)
		return err
	})
}

// instLastAccessedUnix returns LastAccessed as a unix timestamp, or 0 if zero.
// SaveInstance uses .Unix() directly which surfaces -6795364578871 for the
// zero time — avoid that for cross-profile migration so rows look natural in
// the destination DB.
func instLastAccessedUnix(inst *InstanceRow) int64 {
	if inst.LastAccessed.IsZero() {
		return 0
	}
	return inst.LastAccessed.Unix()
}
