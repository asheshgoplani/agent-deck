package trace

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// SchemaVersion is the trace.db schema version (schema_version table).
const SchemaVersion = 1

const schemaSQL = `
CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS traces (
	trace_id        TEXT PRIMARY KEY,
	session_id      TEXT,
	tool            TEXT NOT NULL,
	profile         TEXT,
	instance_id     TEXT,
	parent_trace_id TEXT,
	host            TEXT,
	started_at      INTEGER,
	ended_at        INTEGER,
	extras          TEXT
);
CREATE TABLE IF NOT EXISTS spans (
	span_id            TEXT PRIMARY KEY,
	trace_id           TEXT NOT NULL,
	parent_span_id     TEXT,
	kind               TEXT NOT NULL CHECK (kind IN ('turn','tool_call','subagent','wait')),
	name               TEXT,
	started_at         INTEGER,
	ended_at           INTEGER,
	duration_ms        INTEGER,
	status             TEXT NOT NULL DEFAULT 'unset' CHECK (status IN ('ok','error','unset')),
	tokens_in          INTEGER,
	tokens_out         INTEGER,
	tokens_cache_read  INTEGER,
	tokens_cache_write INTEGER,
	cost_usd           REAL,
	model              TEXT,
	attrs              TEXT
);
CREATE INDEX IF NOT EXISTS spans_trace_started ON spans(trace_id, started_at);
CREATE INDEX IF NOT EXISTS spans_kind_name ON spans(kind, name);
CREATE TABLE IF NOT EXISTS events (
	id       TEXT PRIMARY KEY,
	trace_id TEXT NOT NULL,
	span_id  TEXT,
	kind     TEXT NOT NULL,
	at       INTEGER NOT NULL,
	attrs    TEXT
);
CREATE INDEX IF NOT EXISTS events_trace_at ON events(trace_id, at);
`

// Store is an open trace.db.
type Store struct {
	db   *sql.DB
	path string
}

// Open opens (creating if needed) trace.db at path with WAL and a busy
// timeout, the same DSN rules as internal/statedb.Open (per-connection
// pragmas in the DSN, WAL once on the file), and applies the schema.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("trace: mkdir: %w", err)
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("trace: open: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("trace: wal: %w", err)
	}
	s := &Store{db: db, path: path}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	_ = os.Chmod(path, 0o600)
	return s, nil
}

// OpenReadOnly opens an existing trace.db for queries without creating it.
func OpenReadOnly(path string) (*Store, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("trace: open read-only: %w", err)
	}
	return &Store{db: db, path: path}, nil
}

func (s *Store) migrate() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(schemaSQL); err != nil {
		return fmt.Errorf("trace: schema: %w", err)
	}
	var n int
	if err := tx.QueryRow("SELECT COUNT(*) FROM schema_version").Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		if _, err := tx.Exec("INSERT INTO schema_version(version) VALUES (?)", SchemaVersion); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DB exposes the handle for tests and ad-hoc queries.
func (s *Store) DB() *sql.DB { return s.db }

// Close checkpoints the WAL and closes the database.
func (s *Store) Close() error {
	_, _ = s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	return s.db.Close()
}
