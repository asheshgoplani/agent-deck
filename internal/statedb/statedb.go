package statedb

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
	_ "modernc.org/sqlite"
)

var statedbLog = logging.ForComponent(logging.CompStorage)

// withBusyRetry runs op with linear backoff (10ms, 20ms, 30ms, 40ms, 50ms;
// ~150ms total) when op fails with SQLITE_BUSY. Non-BUSY errors are returned
// immediately; the final BUSY error is returned if every attempt fails.
//
// WAL + busy_timeout=5000 handles most contention internally, but the
// modernc.org/sqlite driver still surfaces transient BUSY at the application
// level under heavy multi-writer load (multiple processes plus goroutines).
// This helper acts as a belt-and-suspenders retry. Pulled out of
// SaveWatcherEvent (the original site) so every short-lived writer can use
// the same policy.
//
// op MUST be idempotent or otherwise safe to retry (e.g. idempotent UPDATEs,
// INSERT OR IGNORE/REPLACE, DELETE).
func withBusyRetry(op func() error) error {
	const attempts = 5
	var err error
	for attempt := 0; attempt < attempts; attempt++ {
		err = op()
		if err == nil {
			return nil
		}
		if !isSQLiteBusy(err) {
			return err
		}
		if attempt == 1 {
			slog.Warn("statedb: SQLITE_BUSY retry", slog.Int("attempt", attempt+1), slog.String("err", err.Error()))
		}
		time.Sleep(time.Duration(10*(attempt+1)) * time.Millisecond)
	}
	if err != nil {
		slog.Error("statedb: SQLITE_BUSY exhausted retries", slog.Int("attempts", attempts), slog.String("err", err.Error()))
	}
	return err
}

// SchemaVersion tracks the current database schema version.
// Bump this when adding migrations.
const SchemaVersion = 7

// StateDB wraps a SQLite database for session/group persistence.
// Thread-safe for concurrent use from multiple goroutines within one process.
// Multiple OS processes can safely read/write via WAL mode + busy timeout.
type StateDB struct {
	db  *sql.DB
	pid int
}

// InstanceRow represents a session row in the database.
type InstanceRow struct {
	ID                 string
	Title              string
	ProjectPath        string
	GroupPath          string
	Order              int
	Command            string
	Wrapper            string
	Tool               string
	Status             string
	TmuxSession        string
	CreatedAt          time.Time
	LastAccessed       time.Time
	ParentSessionID    string
	IsConductor        bool
	NoTransitionNotify bool
	// TmuxSocketName mirrors Instance.TmuxSocketName (v1.7.50+, issue #687).
	// Empty for pre-v1.7.50 rows — those keep targeting the default server
	// after upgrade.
	TmuxSocketName string
	// TitleLocked blocks Claude session-name sync into Title (v1.7.52+, issue #697).
	TitleLocked    bool
	WorktreePath   string
	WorktreeRepo   string
	WorktreeBranch string
	ToolData       json.RawMessage // JSON blob for tool-specific data
}

// WatcherRow represents a watcher row in the database.
type WatcherRow struct {
	ID         string
	Name       string
	Type       string
	ConfigPath string
	Status     string
	Conductor  string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// WatcherEventRow represents a single event row from the watcher_events table.
type WatcherEventRow struct {
	ID              int64
	WatcherID       string
	DedupKey        string
	Sender          string
	Subject         string
	RoutedTo        string
	SessionID       string
	TriageSessionID string
	CreatedAt       time.Time
}

// CostEventRow mirrors the cost_events table for raw round-trip operations
// (e.g., cross-profile migration). The canonical CostEvent type lives in
// internal/costs, but we keep this minimal struct here so the statedb package
// can read/write rows without a circular import.
type CostEventRow struct {
	ID                  string
	SessionID           string
	Timestamp           string // RFC3339; preserve verbatim — cost_events stores TEXT
	Model               string
	InputTokens         int64
	OutputTokens        int64
	CacheReadTokens     int64
	CacheWriteTokens    int64
	CostMicrodollars    int64
	BudgetStopTriggered bool
}

// GroupRow represents a group row in the database.
type GroupRow struct {
	Path        string
	Name        string
	Expanded    bool
	Order       int
	DefaultPath string
	// MaxConcurrent caps simultaneous running sessions in this group (v1.9.1).
	// 0 = unlimited (legacy default for groups predating this field); 1 = serial
	// (default for newly-created groups); N>=2 = bounded parallelism.
	MaxConcurrent int
}

// StatusRow holds status + acknowledgment for a session.
type StatusRow struct {
	Status       string
	Tool         string
	Acknowledged bool
}

// RecentSessionRow captures the config of a deleted session for quick re-creation.
type RecentSessionRow struct {
	ID             string // SHA-256 dedup key (title+path+tool+group)
	Title          string
	ProjectPath    string
	GroupPath      string
	Command        string
	Wrapper        string
	Tool           string
	ToolOptions    json.RawMessage // serialized ToolOptionsWrapper
	SandboxEnabled bool
	GeminiYoloMode *bool
	DeletedAt      time.Time
}

// global singleton for cross-package access (status writes from background worker)
var (
	globalDB   *StateDB
	globalDBMu sync.RWMutex
)

// SetGlobal sets the global StateDB instance.
func SetGlobal(db *StateDB) {
	globalDBMu.Lock()
	globalDB = db
	globalDBMu.Unlock()
}

// GetGlobal returns the global StateDB instance (may be nil).
func GetGlobal() *StateDB {
	globalDBMu.RLock()
	defer globalDBMu.RUnlock()
	return globalDB
}

// Open creates or opens a SQLite database at dbPath with WAL mode and busy timeout.
//
// busy_timeout and foreign_keys are PER-CONNECTION pragmas in SQLite, so they
// MUST be passed via the DSN's `_pragma` parameter — setting them once via
// db.Exec only affects whichever pool connection happened to run the PRAGMA.
// Pre-fix, fresh connections in the pool defaulted to busy_timeout=0, which
// turned every transient lock into an immediate SQLITE_BUSY at the application
// level. journal_mode=WAL is persistent on the database file, so it can stay
// as a one-shot Exec.
func Open(dbPath string) (*StateDB, error) {
	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(dbPath), 0700); err != nil {
		return nil, fmt.Errorf("statedb: mkdir: %w", err)
	}

	dsn := dbPath + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("statedb: open: %w", err)
	}

	// WAL mode: persistent on the file, not per-connection.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("statedb: wal mode: %w", err)
	}

	return &StateDB{db: db, pid: os.Getpid()}, nil
}

// Close checkpoints WAL and closes the database.
func (s *StateDB) Close() error {
	// Checkpoint WAL to merge it back into the main database file
	_, _ = s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	return s.db.Close()
}

// DB returns the underlying sql.DB for advanced use cases (e.g., testing).
func (s *StateDB) DB() *sql.DB {
	return s.db
}

// Migrate creates tables if they don't exist and runs any pending migrations.
func (s *StateDB) Migrate() error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("statedb: begin migrate: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// metadata table
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS metadata (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("statedb: create metadata: %w", err)
	}

	// instances table
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS instances (
			id              TEXT PRIMARY KEY,
			title           TEXT NOT NULL,
			project_path    TEXT NOT NULL,
			group_path      TEXT NOT NULL DEFAULT 'my-sessions',
			sort_order      INTEGER NOT NULL DEFAULT 0,
			command         TEXT NOT NULL DEFAULT '',
			wrapper         TEXT NOT NULL DEFAULT '',
			tool            TEXT NOT NULL DEFAULT 'shell',
			status          TEXT NOT NULL DEFAULT 'error',
			tmux_session     TEXT NOT NULL DEFAULT '',
			tmux_socket_name TEXT NOT NULL DEFAULT '',
			created_at      INTEGER NOT NULL,
			last_accessed   INTEGER NOT NULL DEFAULT 0,
			parent_session_id TEXT NOT NULL DEFAULT '',
			is_conductor            INTEGER NOT NULL DEFAULT 0,
			no_transition_notify    INTEGER NOT NULL DEFAULT 0,
			title_locked            INTEGER NOT NULL DEFAULT 0,
			worktree_path     TEXT NOT NULL DEFAULT '',
			worktree_repo     TEXT NOT NULL DEFAULT '',
			worktree_branch   TEXT NOT NULL DEFAULT '',
			tool_data       TEXT NOT NULL DEFAULT '{}',
			acknowledged    INTEGER NOT NULL DEFAULT 0
		)
	`); err != nil {
		return fmt.Errorf("statedb: create instances: %w", err)
	}

	// groups table.
	// max_concurrent (v1.9.1): caps simultaneous running sessions in the
	// group. DEFAULT 0 preserves backward compat (legacy unlimited) for any
	// row inserted before this column existed; newly-created groups set 1.
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS groups (
			path           TEXT PRIMARY KEY,
			name           TEXT NOT NULL,
			expanded       INTEGER NOT NULL DEFAULT 1,
			sort_order     INTEGER NOT NULL DEFAULT 0,
			default_path   TEXT NOT NULL DEFAULT '',
			max_concurrent INTEGER NOT NULL DEFAULT 0
		)
	`); err != nil {
		return fmt.Errorf("statedb: create groups: %w", err)
	}

	// ALTER for pre-existing databases (idempotent: ignore "duplicate column").
	if _, err := tx.Exec(`ALTER TABLE groups ADD COLUMN max_concurrent INTEGER NOT NULL DEFAULT 0`); err != nil {
		// SQLite returns "duplicate column name" when the column already exists.
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("statedb: add groups.max_concurrent: %w", err)
		}
	}

	// instance heartbeats
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS instance_heartbeats (
			pid        INTEGER PRIMARY KEY,
			started    INTEGER NOT NULL,
			heartbeat  INTEGER NOT NULL,
			is_primary INTEGER NOT NULL DEFAULT 0
		)
	`); err != nil {
		return fmt.Errorf("statedb: create heartbeats: %w", err)
	}

	// recent_sessions table (schema v2)
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS recent_sessions (
			id              TEXT PRIMARY KEY,
			title           TEXT NOT NULL,
			project_path    TEXT NOT NULL,
			group_path      TEXT NOT NULL DEFAULT '',
			command         TEXT NOT NULL DEFAULT '',
			wrapper         TEXT NOT NULL DEFAULT '',
			tool            TEXT NOT NULL DEFAULT '',
			tool_options    TEXT NOT NULL DEFAULT '{}',
			sandbox_enabled INTEGER NOT NULL DEFAULT 0,
			gemini_yolo     INTEGER,
			deleted_at      INTEGER NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("statedb: create recent_sessions: %w", err)
	}

	// cost_events table (cost tracking)
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS cost_events (
			id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			timestamp DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			model TEXT NOT NULL,
			input_tokens INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			cache_read_tokens INTEGER NOT NULL DEFAULT 0,
			cache_write_tokens INTEGER NOT NULL DEFAULT 0,
			cost_microdollars INTEGER NOT NULL DEFAULT 0,
			budget_stop_triggered INTEGER NOT NULL DEFAULT 0
		)
	`); err != nil {
		return fmt.Errorf("statedb: create cost_events: %w", err)
	}

	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_cost_events_session ON cost_events(session_id)`); err != nil {
		return fmt.Errorf("statedb: create cost_events session index: %w", err)
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_cost_events_timestamp ON cost_events(timestamp)`); err != nil {
		return fmt.Errorf("statedb: create cost_events timestamp index: %w", err)
	}

	// watchers table (v5)
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS watchers (
			id          TEXT PRIMARY KEY,
			name        TEXT UNIQUE NOT NULL,
			type        TEXT NOT NULL,
			config_path TEXT NOT NULL,
			status      TEXT NOT NULL DEFAULT 'stopped',
			conductor   TEXT NOT NULL DEFAULT '',
			created_at  INTEGER NOT NULL,
			updated_at  INTEGER NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("statedb: create watchers: %w", err)
	}

	// watcher_events table (v5 + Phase 18: triage_session_id)
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS watcher_events (
			id                INTEGER PRIMARY KEY AUTOINCREMENT,
			watcher_id        TEXT NOT NULL REFERENCES watchers(id),
			dedup_key         TEXT NOT NULL,
			sender            TEXT NOT NULL DEFAULT '',
			subject           TEXT NOT NULL DEFAULT '',
			routed_to         TEXT NOT NULL DEFAULT '',
			session_id        TEXT NOT NULL DEFAULT '',
			triage_session_id TEXT NOT NULL DEFAULT '',
			created_at        INTEGER NOT NULL,
			UNIQUE(watcher_id, dedup_key)
		)
	`); err != nil {
		return fmt.Errorf("statedb: create watcher_events: %w", err)
	}

	if _, err := tx.Exec(`
		CREATE INDEX IF NOT EXISTS idx_watcher_events_watcher_created
		ON watcher_events(watcher_id, created_at DESC)
	`); err != nil {
		return fmt.Errorf("statedb: create watcher_events index: %w", err)
	}

	// ALTER TABLE migrations for existing databases.
	// CREATE TABLE IF NOT EXISTS won't add new columns to tables that already exist.
	// Each migration is idempotent: errors from "duplicate column" are silently ignored.
	// See CLAUDE.md "Schema Migration Safety": every new column MUST have a corresponding ALTER TABLE.
	alterMigrations := []string{
		"ALTER TABLE instances ADD COLUMN acknowledged INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE watcher_events ADD COLUMN triage_session_id TEXT NOT NULL DEFAULT ''",
		// v7 (issue #687, v1.7.50): per-session tmux socket isolation.
		// Default '' keeps the pre-v1.7.50 behavior for existing rows.
		"ALTER TABLE instances ADD COLUMN tmux_socket_name TEXT NOT NULL DEFAULT ''",
		// v8 (issue #697, v1.7.52): title lock blocks Claude session-name sync.
		// Default 0 keeps the pre-v1.7.52 behavior (#572 sync default-on) for existing rows.
		"ALTER TABLE instances ADD COLUMN title_locked INTEGER NOT NULL DEFAULT 0",
	}
	for _, stmt := range alterMigrations {
		if _, err := tx.Exec(stmt); err != nil {
			// Ignore "duplicate column name" errors (column already exists)
			if !strings.Contains(err.Error(), "duplicate column") {
				return fmt.Errorf("statedb: alter migration: %w", err)
			}
		}
	}

	// Set schema version only when missing or changed.
	// Avoiding a write on every open reduces lock contention between CLI processes.
	schemaVersion := fmt.Sprintf("%d", SchemaVersion)
	var existingVersion string
	err = tx.QueryRow(`SELECT value FROM metadata WHERE key = 'schema_version'`).Scan(&existingVersion)
	switch {
	case err == sql.ErrNoRows:
		if _, err := tx.Exec(`
			INSERT INTO metadata (key, value) VALUES ('schema_version', ?)
		`, schemaVersion); err != nil {
			return fmt.Errorf("statedb: insert schema version: %w", err)
		}
	case err != nil:
		return fmt.Errorf("statedb: read schema version: %w", err)
	case existingVersion != schemaVersion:
		oldVer, _ := strconv.Atoi(existingVersion)
		if oldVer < 4 {
			if _, err := tx.Exec(`ALTER TABLE instances ADD COLUMN is_conductor INTEGER NOT NULL DEFAULT 0`); err != nil {
				if !strings.Contains(err.Error(), "duplicate column") {
					return fmt.Errorf("statedb: migrate v4 is_conductor: %w", err)
				}
			}
			// Backfill: mark existing sessions whose title starts with "conductor-"
			if _, err := tx.Exec(`UPDATE instances SET is_conductor = 1 WHERE title LIKE 'conductor-%'`); err != nil {
				return fmt.Errorf("statedb: migrate v4 backfill is_conductor: %w", err)
			}
		}
		// v5: Watcher tables are new (CREATE TABLE IF NOT EXISTS handles creation).
		// No column backfill needed for v5.
		if oldVer < 6 {
			if _, err := tx.Exec(`ALTER TABLE instances ADD COLUMN no_transition_notify INTEGER NOT NULL DEFAULT 0`); err != nil {
				if !strings.Contains(err.Error(), "duplicate column") {
					return fmt.Errorf("statedb: migrate v6 no_transition_notify: %w", err)
				}
			}
		}
		if oldVer < 8 {
			if _, err := tx.Exec(`ALTER TABLE instances ADD COLUMN title_locked INTEGER NOT NULL DEFAULT 0`); err != nil {
				if !strings.Contains(err.Error(), "duplicate column") {
					return fmt.Errorf("statedb: migrate v8 title_locked: %w", err)
				}
			}
		}
		if _, err := tx.Exec(`
			UPDATE metadata SET value = ? WHERE key = 'schema_version'
		`, schemaVersion); err != nil {
			return fmt.Errorf("statedb: update schema version: %w", err)
		}
	}

	return tx.Commit()
}

// IsEmpty returns true if the instances table has no rows.
func (s *StateDB) IsEmpty() (bool, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM instances").Scan(&count)
	if err != nil {
		return false, err
	}
	return count == 0, nil
}

// --- Instance CRUD ---

// SaveInstance inserts or replaces a single instance.
func (s *StateDB) SaveInstance(inst *InstanceRow) error {
	toolData := inst.ToolData
	if len(toolData) == 0 {
		toolData = json.RawMessage("{}")
	}

	// Preserve any tool_data keys not modeled by the typed schema (e.g.,
	// manually-set clear_on_compact). Without this merge, every
	// INSERT OR REPLACE silently drops user-managed extras.
	var existingToolData []byte
	if err := s.db.QueryRow("SELECT tool_data FROM instances WHERE id = ?", inst.ID).Scan(&existingToolData); err == nil {
		toolData = MergeToolDataExtras(json.RawMessage(existingToolData), toolData)
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
		inst.CreatedAt.Unix(), inst.LastAccessed.Unix(),
		inst.ParentSessionID, isConductorInt, noTransitionNotifyInt,
		inst.WorktreePath, inst.WorktreeRepo, inst.WorktreeBranch,
		string(toolData), titleLockedInt,
	)
	return err
}

// SaveInstances inserts or replaces multiple instances in a single transaction.
// It also removes any rows from the database that are not in the provided list,
// ensuring deleted sessions don't reappear on reload.
//
// Wrapped in withBusyRetry because parallel writers (CLI + TUI + heartbeat
// daemons) contend on the WAL writer slot. The whole save is idempotent at
// the row level (INSERT OR REPLACE + DELETE WHERE NOT IN), so retrying the
// outer transaction on SQLITE_BUSY is safe. Part of the v1.9.1 #909 fix.
func (s *StateDB) SaveInstances(insts []*InstanceRow) error {
	return withBusyRetry(func() error {
		return s.saveInstancesOnce(insts)
	})
}

func (s *StateDB) saveInstancesOnce(insts []*InstanceRow) error {
	// Pre-fetch existing tool_data per instance ID so we can preserve any
	// keys not modeled by the typed schema (e.g., manually-set
	// clear_on_compact). Without this merge, every INSERT OR REPLACE
	// silently drops user-managed extras. One batch SELECT instead of N
	// individual reads.
	//
	// IMPORTANT: this read runs OUTSIDE the write transaction below.
	// In SQLite WAL mode, beginning a transaction with a read and then
	// trying to upgrade to a write fails with SQLITE_BUSY (rather than
	// waiting on busy_timeout) when another connection is currently
	// writing. Pre-reading on the raw DB handle avoids the upgrade path.
	// There is a tiny race window where a concurrent writer could modify
	// extras between this read and our commit; we accept it because
	// extras keys are rarely-mutated user-managed flags and the worst-case
	// outcome is one stale-overlay save, recoverable on next save.
	existingToolData := make(map[string]json.RawMessage, len(insts))
	if len(insts) > 0 {
		placeholders := make([]string, len(insts))
		args := make([]any, len(insts))
		for i, inst := range insts {
			placeholders[i] = "?"
			args[i] = inst.ID
		}
		query := "SELECT id, tool_data FROM instances WHERE id IN (" + strings.Join(placeholders, ",") + ")"
		rows, queryErr := s.db.Query(query, args...)
		if queryErr == nil {
			for rows.Next() {
				var id string
				var td []byte
				if scanErr := rows.Scan(&id, &td); scanErr == nil {
					existingToolData[id] = json.RawMessage(td)
				}
			}
			_ = rows.Close()
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// Delete rows not in the new list to prevent deleted sessions from reappearing.
	if len(insts) == 0 {
		if _, err := tx.Exec("DELETE FROM instances"); err != nil {
			return err
		}
	} else {
		placeholders := make([]string, len(insts))
		args := make([]any, len(insts))
		for i, inst := range insts {
			placeholders[i] = "?"
			args[i] = inst.ID
		}
		query := "DELETE FROM instances WHERE id NOT IN (" + strings.Join(placeholders, ",") + ")"
		if _, err := tx.Exec(query, args...); err != nil {
			return err
		}
	}

	stmt, err := tx.Prepare(`
		INSERT OR REPLACE INTO instances (
			id, title, project_path, group_path, sort_order,
			command, wrapper, tool, status, tmux_session, tmux_socket_name,
			created_at, last_accessed,
			parent_session_id, is_conductor, no_transition_notify,
			worktree_path, worktree_repo, worktree_branch,
			tool_data, title_locked
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, inst := range insts {
		toolData := inst.ToolData
		if len(toolData) == 0 {
			toolData = json.RawMessage("{}")
		}
		if existing, ok := existingToolData[inst.ID]; ok {
			toolData = MergeToolDataExtras(existing, toolData)
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
		if _, err := stmt.Exec(
			inst.ID, inst.Title, inst.ProjectPath, inst.GroupPath, inst.Order,
			inst.Command, inst.Wrapper, inst.Tool, inst.Status, inst.TmuxSession, inst.TmuxSocketName,
			inst.CreatedAt.Unix(), inst.LastAccessed.Unix(),
			inst.ParentSessionID, isConductorInt, noTransitionNotifyInt,
			inst.WorktreePath, inst.WorktreeRepo, inst.WorktreeBranch,
			string(toolData), titleLockedInt,
		); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// LoadInstances returns all instances ordered by sort_order.
func (s *StateDB) LoadInstances() ([]*InstanceRow, error) {
	rows, err := s.db.Query(`
		SELECT id, title, project_path, group_path, sort_order,
			command, wrapper, tool, status, tmux_session, tmux_socket_name,
			created_at, last_accessed,
			parent_session_id, is_conductor, no_transition_notify,
			worktree_path, worktree_repo, worktree_branch,
			tool_data, title_locked
		FROM instances ORDER BY sort_order
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*InstanceRow
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
		result = append(result, r)
	}
	return result, rows.Err()
}

// DeleteInstance removes an instance by ID.
//
// Wrapped in withBusyRetry because parallel `agent-deck rm` invocations
// (e.g. xargs -P 14) all contend on the same WAL writer slot. Without
// retry, transient SQLITE_BUSY silently drops the DELETE while the CLI
// still reports success — the silent-loss half of issue #909.
func (s *StateDB) DeleteInstance(id string) error {
	return withBusyRetry(func() error {
		_, err := s.db.Exec("DELETE FROM instances WHERE id = ?", id)
		return err
	})
}

// InstanceExists returns true iff a row with the given id is present.
// Used by the rm path's post-commit verify (issue #909) to detect
// resurrection by a concurrent SaveInstances rewrite.
func (s *StateDB) InstanceExists(id string) (bool, error) {
	row := s.db.QueryRow("SELECT 1 FROM instances WHERE id = ? LIMIT 1", id)
	var one int
	err := row.Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// UpdateInstanceField updates a single column for a given instance.
// field must be a valid column name (caller is responsible for safety).
func (s *StateDB) UpdateInstanceField(id, field string, value any) error {
	query := fmt.Sprintf("UPDATE instances SET %s = ? WHERE id = ?", field)
	_, err := s.db.Exec(query, value, id)
	return err
}

// --- Group CRUD ---

// SaveGroups replaces all groups in a single transaction.
func (s *StateDB) SaveGroups(groups []*GroupRow) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// Clear existing groups and re-insert (simpler than diff)
	if _, err := tx.Exec("DELETE FROM groups"); err != nil {
		return err
	}

	stmt, err := tx.Prepare(`
		INSERT INTO groups (path, name, expanded, sort_order, default_path, max_concurrent)
		VALUES (?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, g := range groups {
		expanded := 0
		if g.Expanded {
			expanded = 1
		}
		if _, err := stmt.Exec(g.Path, g.Name, expanded, g.Order, g.DefaultPath, g.MaxConcurrent); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// LoadGroups returns all groups ordered by sort_order.
func (s *StateDB) LoadGroups() ([]*GroupRow, error) {
	rows, err := s.db.Query(`
		SELECT path, name, expanded, sort_order, default_path, max_concurrent
		FROM groups ORDER BY sort_order
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*GroupRow
	for rows.Next() {
		g := &GroupRow{}
		var expanded int
		if err := rows.Scan(&g.Path, &g.Name, &expanded, &g.Order, &g.DefaultPath, &g.MaxConcurrent); err != nil {
			return nil, err
		}
		g.Expanded = expanded != 0
		result = append(result, g)
	}
	return result, rows.Err()
}

// DeleteGroup removes a group by path.
func (s *StateDB) DeleteGroup(path string) error {
	_, err := s.db.Exec("DELETE FROM groups WHERE path = ?", path)
	return err
}

// --- Status + Acknowledgment ---

// WriteStatus updates the status and tool for an instance.
//
// Wrapped in withBusyRetry: the transition daemon (#755 family) calls this
// under contention with other writers (heartbeat, status poller, hook
// handler). Without retry, transient SQLITE_BUSY drops the user-visible
// status update and the TUI shows stale state.
func (s *StateDB) WriteStatus(id, status, tool string) error {
	return withBusyRetry(func() error {
		_, err := s.db.Exec(
			`UPDATE instances
			 SET status = ?, tool = ?,
			     acknowledged = CASE WHEN ? = 'running' THEN 0 ELSE acknowledged END
			 WHERE id = ?`,
			status, tool, status, id,
		)
		return err
	})
}

// ReadAllStatuses returns status + acknowledged flag for every instance.
func (s *StateDB) ReadAllStatuses() (map[string]StatusRow, error) {
	rows, err := s.db.Query("SELECT id, status, tool, acknowledged FROM instances")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]StatusRow)
	for rows.Next() {
		var id string
		var sr StatusRow
		var ack int
		if err := rows.Scan(&id, &sr.Status, &sr.Tool, &ack); err != nil {
			return nil, err
		}
		sr.Acknowledged = ack != 0
		result[id] = sr
	}
	return result, rows.Err()
}

// SetAcknowledged sets or clears the acknowledged flag for an instance.
func (s *StateDB) SetAcknowledged(id string, ack bool) error {
	v := 0
	if ack {
		v = 1
	}
	_, err := s.db.Exec("UPDATE instances SET acknowledged = ? WHERE id = ?", v, id)
	return err
}

// --- Heartbeat ---

// RegisterInstance records this process as an active TUI instance.
func (s *StateDB) RegisterInstance(isPrimary bool) error {
	now := time.Now().Unix()
	primary := 0
	if isPrimary {
		primary = 1
	}
	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO instance_heartbeats (pid, started, heartbeat, is_primary)
		VALUES (?, ?, ?, ?)
	`, s.pid, now, now, primary)
	return err
}

// Heartbeat updates the heartbeat timestamp for this process.
func (s *StateDB) Heartbeat() error {
	_, err := s.db.Exec(
		"UPDATE instance_heartbeats SET heartbeat = ? WHERE pid = ?",
		time.Now().Unix(), s.pid,
	)
	return err
}

// UnregisterInstance removes this process from the heartbeat table.
func (s *StateDB) UnregisterInstance() error {
	_, err := s.db.Exec("DELETE FROM instance_heartbeats WHERE pid = ?", s.pid)
	return err
}

// CleanDeadInstances removes heartbeat entries that haven't been updated within timeout.
func (s *StateDB) CleanDeadInstances(timeout time.Duration) error {
	cutoff := time.Now().Add(-timeout).Unix()
	_, err := s.db.Exec("DELETE FROM instance_heartbeats WHERE heartbeat < ?", cutoff)
	return err
}

// AliveInstanceCount returns how many TUI instances have fresh heartbeats.
func (s *StateDB) AliveInstanceCount() (int, error) {
	var count int
	cutoff := time.Now().Add(-30 * time.Second).Unix()
	err := s.db.QueryRow(
		"SELECT COUNT(*) FROM instance_heartbeats WHERE heartbeat >= ?", cutoff,
	).Scan(&count)
	return count, err
}

// --- Primary Election ---

// ElectPrimary attempts to make this instance the primary.
// Returns true if this instance is now (or already was) the primary.
// Uses a transaction to atomically clear stale primaries and claim if available.
func (s *StateDB) ElectPrimary(timeout time.Duration) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, fmt.Errorf("statedb: begin elect: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	cutoff := time.Now().Add(-timeout).Unix()

	// Clear is_primary for any heartbeat older than timeout (stale primary)
	if _, err := tx.Exec(
		"UPDATE instance_heartbeats SET is_primary = 0 WHERE heartbeat < ? AND is_primary = 1",
		cutoff,
	); err != nil {
		return false, fmt.Errorf("statedb: clear stale primary: %w", err)
	}

	// Check if any alive instance already has is_primary=1
	var existingPID int
	err = tx.QueryRow(
		"SELECT pid FROM instance_heartbeats WHERE is_primary = 1 AND heartbeat >= ? LIMIT 1",
		cutoff,
	).Scan(&existingPID)

	if err == nil {
		// An alive primary exists
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("statedb: commit elect: %w", err)
		}
		return existingPID == s.pid, nil
	}

	// No alive primary exists: claim it
	if _, err := tx.Exec(
		"UPDATE instance_heartbeats SET is_primary = 1 WHERE pid = ?",
		s.pid,
	); err != nil {
		return false, fmt.Errorf("statedb: claim primary: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("statedb: commit elect: %w", err)
	}
	return true, nil
}

// ResignPrimary clears the is_primary flag for this process.
func (s *StateDB) ResignPrimary() error {
	_, err := s.db.Exec(
		"UPDATE instance_heartbeats SET is_primary = 0 WHERE pid = ?",
		s.pid,
	)
	return err
}

// --- Metadata ---

// SetMeta sets a key-value pair in the metadata table.
func (s *StateDB) SetMeta(key, value string) error {
	_, err := s.db.Exec(
		"INSERT OR REPLACE INTO metadata (key, value) VALUES (?, ?)",
		key, value,
	)
	return err
}

// GetMeta gets a value from the metadata table. Returns "" if not found.
func (s *StateDB) GetMeta(key string) (string, error) {
	var value string
	err := s.db.QueryRow("SELECT value FROM metadata WHERE key = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

// --- Change Detection (replaces fsnotify) ---

// Touch updates a metadata timestamp that other instances can poll to detect changes.
func (s *StateDB) Touch() error {
	return s.SetMeta("last_modified", fmt.Sprintf("%d", time.Now().UnixNano()))
}

// LastModified returns the last_modified timestamp from metadata.
func (s *StateDB) LastModified() (int64, error) {
	val, err := s.GetMeta("last_modified")
	if err != nil || val == "" {
		return 0, err
	}
	var ts int64
	_, err = fmt.Sscanf(val, "%d", &ts)
	return ts, err
}

// --- Recent Sessions ---

// recentSessionDedupID returns a deterministic key for deduplication.
// It includes all persisted recreation fields so different launch configs do
// not overwrite each other.
func recentSessionDedupID(row *RecentSessionRow) string {
	toolOpts := "{}"
	if len(row.ToolOptions) > 0 {
		toolOpts = string(row.ToolOptions)
	}

	geminiYolo := "unset"
	if row.GeminiYoloMode != nil {
		geminiYolo = strconv.FormatBool(*row.GeminiYoloMode)
	}

	payload := strings.Join([]string{
		row.Title,
		row.ProjectPath,
		row.GroupPath,
		row.Command,
		row.Wrapper,
		row.Tool,
		toolOpts,
		strconv.FormatBool(row.SandboxEnabled),
		geminiYolo,
	}, "\x00")

	h := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(h[:16]) // 32-char hex
}

// SaveRecentSession inserts or replaces a recent session entry, then prunes to 20.
//
// The INSERT and the prune are bundled in a single transaction so a crash
// between them cannot leave the table over-budget (the prune always sees the
// just-inserted row). The whole transaction runs under withBusyRetry to
// absorb transient SQLITE_BUSY from concurrent writers — pre-fix, these
// caused user-visible "recent session lost" reports under contention.
func (s *StateDB) SaveRecentSession(row *RecentSessionRow) error {
	id := recentSessionDedupID(row)

	toolOpts := row.ToolOptions
	if len(toolOpts) == 0 {
		toolOpts = json.RawMessage("{}")
	}

	sandbox := 0
	if row.SandboxEnabled {
		sandbox = 1
	}

	var geminiYolo *int
	if row.GeminiYoloMode != nil {
		v := 0
		if *row.GeminiYoloMode {
			v = 1
		}
		geminiYolo = &v
	}

	return withBusyRetry(func() error {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()

		if _, err := tx.Exec(`
			INSERT OR REPLACE INTO recent_sessions (
				id, title, project_path, group_path,
				command, wrapper, tool, tool_options,
				sandbox_enabled, gemini_yolo, deleted_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`,
			id, row.Title, row.ProjectPath, row.GroupPath,
			row.Command, row.Wrapper, row.Tool, string(toolOpts),
			sandbox, geminiYolo, time.Now().Unix(),
		); err != nil {
			return err
		}

		if _, err := tx.Exec(`
			DELETE FROM recent_sessions WHERE id NOT IN (
				SELECT id FROM recent_sessions ORDER BY deleted_at DESC LIMIT ?
			)
		`, 20); err != nil {
			return err
		}

		return tx.Commit()
	})
}

// LoadRecentSessions returns all recent sessions ordered by most recently deleted.
func (s *StateDB) LoadRecentSessions() ([]*RecentSessionRow, error) {
	rows, err := s.db.Query(`
		SELECT id, title, project_path, group_path,
			command, wrapper, tool, tool_options,
			sandbox_enabled, gemini_yolo, deleted_at
		FROM recent_sessions ORDER BY deleted_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*RecentSessionRow
	for rows.Next() {
		r := &RecentSessionRow{}
		var toolOptsStr string
		var sandbox int
		var geminiYolo *int
		var deletedUnix int64
		if err := rows.Scan(
			&r.ID, &r.Title, &r.ProjectPath, &r.GroupPath,
			&r.Command, &r.Wrapper, &r.Tool, &toolOptsStr,
			&sandbox, &geminiYolo, &deletedUnix,
		); err != nil {
			return nil, err
		}
		r.ToolOptions = json.RawMessage(toolOptsStr)
		r.SandboxEnabled = sandbox != 0
		if geminiYolo != nil {
			v := *geminiYolo != 0
			r.GeminiYoloMode = &v
		}
		r.DeletedAt = time.Unix(deletedUnix, 0)
		result = append(result, r)
	}
	return result, rows.Err()
}

// --- Watcher CRUD ---

// SaveWatcher inserts or replaces a watcher row.
func (s *StateDB) SaveWatcher(w *WatcherRow) error {
	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO watchers (id, name, type, config_path, status, conductor, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, w.ID, w.Name, w.Type, w.ConfigPath, w.Status, w.Conductor,
		w.CreatedAt.Unix(), w.UpdatedAt.Unix())
	return err
}

// LoadWatchers returns all watchers ordered by name.
func (s *StateDB) LoadWatchers() ([]*WatcherRow, error) {
	rows, err := s.db.Query(`SELECT id, name, type, config_path, status, conductor, created_at, updated_at FROM watchers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []*WatcherRow
	for rows.Next() {
		var w WatcherRow
		var createdAt, updatedAt int64
		if err := rows.Scan(&w.ID, &w.Name, &w.Type, &w.ConfigPath, &w.Status, &w.Conductor, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		w.CreatedAt = time.Unix(createdAt, 0)
		w.UpdatedAt = time.Unix(updatedAt, 0)
		result = append(result, &w)
	}
	return result, rows.Err()
}

// SaveWatcherEvent inserts an event with dedup via INSERT OR IGNORE.
// Returns true if the row was inserted (new event), false if it was a duplicate.
// Prunes to maxEvents after successful insert.
//
// Retries on SQLITE_BUSY: concurrent INSERTs across connections can trip the
// write lock even with WAL + busy_timeout if the driver surfaces BUSY before
// the backoff completes. Retries are cheap because the operation is
// idempotent (INSERT OR IGNORE).
func (s *StateDB) SaveWatcherEvent(watcherID, dedupKey, sender, subject, routedTo, sessionID string, maxEvents int) (bool, error) {
	var result sql.Result
	if err := withBusyRetry(func() error {
		var err error
		result, err = s.db.Exec(`
			INSERT OR IGNORE INTO watcher_events (watcher_id, dedup_key, sender, subject, routed_to, session_id, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, watcherID, dedupKey, sender, subject, routedTo, sessionID, time.Now().Unix())
		return err
	}); err != nil {
		return false, err
	}
	n, _ := result.RowsAffected()
	if n > 0 {
		// Prune errors used to be silently dropped, which let the table grow
		// unbounded under sustained BUSY. We log + retry so the next caller
		// gets the intended bound.
		if err := s.pruneWatcherEvents(watcherID, maxEvents); err != nil {
			slog.Warn("statedb: pruneWatcherEvents failed",
				slog.String("watcher_id", watcherID),
				slog.Int("max_events", maxEvents),
				slog.String("err", err.Error()))
		}
	}
	return n > 0, nil
}

// isSQLiteBusy returns true when err is a SQLITE_BUSY / "database is locked"
// transient condition that can be safely retried.
func isSQLiteBusy(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "sqlite_busy") || strings.Contains(msg, "database is locked")
}

// LookupWatcherEventSessionByDedupKey queries the session_id for a specific event.
// Returns ("", nil) if no matching event exists or session_id is empty.
func (s *StateDB) LookupWatcherEventSessionByDedupKey(watcherID, dedupKey string) (string, error) {
	var sessionID string
	err := s.db.QueryRow(
		`SELECT session_id FROM watcher_events WHERE watcher_id = ? AND dedup_key = ?`,
		watcherID, dedupKey,
	).Scan(&sessionID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return sessionID, err
}

// UpdateWatcherEventSessionID sets the session_id on an existing watcher event.
// Returns an error if no matching row exists (0 rows affected).
func (s *StateDB) UpdateWatcherEventSessionID(watcherID, dedupKey, sessionID string) error {
	result, err := s.db.Exec(
		`UPDATE watcher_events SET session_id = ? WHERE watcher_id = ? AND dedup_key = ?`,
		sessionID, watcherID, dedupKey,
	)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("no watcher event found for watcher_id=%q dedup_key=%q", watcherID, dedupKey)
	}
	return nil
}

// UpdateWatcherEventRoutedTo updates the routed_to and triage_session_id columns
// for the row matching (watcher_id, dedup_key). Returns a wrapped error if no row matches
// (0 rows affected), allowing the caller to distinguish "update OK" from "event not found".
//
// Wrapped in withBusyRetry to match its sister SaveWatcherEvent — both are
// short idempotent writes against watcher_events called from concurrent
// engine + triage_reaper paths. Without retry, SQLITE_BUSY from a sister
// INSERT silently drops the routed_to update and the watcher event sticks
// in "unrouted" forever.
func (s *StateDB) UpdateWatcherEventRoutedTo(watcherID, dedupKey, routedTo, triageSessionID string) error {
	var n int64
	if err := withBusyRetry(func() error {
		res, err := s.db.Exec(
			`UPDATE watcher_events SET routed_to = ?, triage_session_id = ? WHERE watcher_id = ? AND dedup_key = ?`,
			routedTo, triageSessionID, watcherID, dedupKey,
		)
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	}); err != nil {
		return fmt.Errorf("statedb: update routed_to: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("statedb: update routed_to: no watcher_events row for watcher_id=%s dedup_key=%s", watcherID, dedupKey)
	}
	return nil
}

// pruneWatcherEvents keeps only the newest maxCount events for a watcher.
//
// Wrapped in withBusyRetry: the DELETE ... NOT IN (SELECT ...) pattern takes
// a stronger lock than a single-row UPDATE, so it is especially BUSY-prone
// under concurrent inserts. If the prune is dropped, the watcher_events
// table grows without bound.
func (s *StateDB) pruneWatcherEvents(watcherID string, maxCount int) error {
	return withBusyRetry(func() error {
		_, err := s.db.Exec(`
			DELETE FROM watcher_events WHERE watcher_id = ? AND id NOT IN (
				SELECT id FROM watcher_events WHERE watcher_id = ?
				ORDER BY id DESC LIMIT ?
			)
		`, watcherID, watcherID, maxCount)
		return err
	})
}

// LoadWatcherByName returns the watcher with the given name, or nil if not found.
// A missing watcher is not an error; (nil, nil) is returned.
func (s *StateDB) LoadWatcherByName(name string) (*WatcherRow, error) {
	var w WatcherRow
	var createdAt, updatedAt int64
	err := s.db.QueryRow(`
		SELECT id, name, type, config_path, status, conductor, created_at, updated_at
		FROM watchers WHERE name = ?
	`, name).Scan(&w.ID, &w.Name, &w.Type, &w.ConfigPath, &w.Status, &w.Conductor, &createdAt, &updatedAt)
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

// LoadWatcherEvents returns up to limit events for the given watcher, ordered most recent first.
func (s *StateDB) LoadWatcherEvents(watcherID string, limit int) ([]WatcherEventRow, error) {
	rows, err := s.db.Query(`
		SELECT id, watcher_id, dedup_key, sender, subject, routed_to, session_id, created_at
		FROM watcher_events WHERE watcher_id = ?
		ORDER BY created_at DESC LIMIT ?
	`, watcherID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []WatcherEventRow
	for rows.Next() {
		var e WatcherEventRow
		var createdAt int64
		if err := rows.Scan(&e.ID, &e.WatcherID, &e.DedupKey, &e.Sender, &e.Subject, &e.RoutedTo, &e.SessionID, &createdAt); err != nil {
			return nil, err
		}
		e.CreatedAt = time.Unix(createdAt, 0)
		result = append(result, e)
	}
	return result, rows.Err()
}

// LookupWatcherIDByDedupKey returns the watcher_id for the first watcher_events
// row with the given dedup_key. Returns an error if no row is found.
// Used by the triageReaper to correlate a result.json back to its originating event (D-08).
func (s *StateDB) LookupWatcherIDByDedupKey(dedupKey string) (string, error) {
	var id string
	err := s.db.QueryRow(
		`SELECT watcher_id FROM watcher_events WHERE dedup_key = ? LIMIT 1`,
		dedupKey,
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("statedb: lookup watcher_id by dedup_key %q: %w", dedupKey, err)
	}
	return id, nil
}

// UpdateWatcherStatus sets the status field on a watcher row.
// Returns an error if no watcher with the given ID exists.
func (s *StateDB) UpdateWatcherStatus(watcherID string, status string) error {
	result, err := s.db.Exec(`
		UPDATE watchers SET status = ?, updated_at = ? WHERE id = ?
	`, status, time.Now().Unix(), watcherID)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("no watcher found with id=%q", watcherID)
	}
	return nil
}
