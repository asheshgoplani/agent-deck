// Package store opens recall.db: one dedicated writer connection, one
// read-only pool, the pragmas the design fixes, the schema check that makes
// the file disposable, and the machine-global sweep lock.
package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/asheshgoplani/agent-deck/internal/recall"
	_ "modernc.org/sqlite"
)

// LocalHostUID is the host row every locally read source hangs off. Remote
// rows (phase 4) carry the remote agent-deck's own machine id.
const LocalHostUID = "local"

// Store is an open recall.db.
type Store struct {
	Path string
	// W is the single writer: SetMaxOpenConns(1), so two goroutines cannot
	// interleave statements of one batch.
	W *sql.DB
	// R is the read-only pool with a 15 s busy timeout.
	R *sql.DB
}

const writerPragmas = "?_pragma=busy_timeout(15000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)" +
	"&_pragma=wal_autocheckpoint(1000)&_pragma=journal_size_limit(67108864)&_pragma=cache_size(-8000)&_pragma=mmap_size(0)"

const readerPragmas = "?mode=ro&_pragma=busy_timeout(15000)&_pragma=query_only(1)&_pragma=cache_size(-8000)&_pragma=mmap_size(0)"

// ErrSchema means the file at path was written by another schema version.
var ErrSchema = errors.New("recall: recall.db has a different schema version")

// Open opens or creates recall.db at path. A file whose meta.schema_version
// is not recall.SchemaVersion is deleted and recreated: everything in it is
// reproducible from transcripts, and that is the whole point of the split
// with state.db. Callers that may run beside a sweep take the sweep lock
// before recreating: OpenCurrent first, then Open under the lock.
func Open(path string) (*Store, error) {
	s, err := OpenCurrent(path)
	if !errors.Is(err, ErrSchema) {
		return s, err
	}
	if err := RemoveFiles(path); err != nil {
		return nil, err
	}
	return createNew(path)
}

// OpenCurrent opens recall.db at path, creating it when absent, and returns
// ErrSchema (with nothing deleted) when the file has another schema version.
func OpenCurrent(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("recall: mkdir: %w", err)
	}
	s, err := open(path)
	if err != nil {
		return nil, err
	}
	ok, err := s.schemaCurrent()
	if err != nil {
		s.Close()
		return nil, err
	}
	if !ok {
		var n int
		empty := s.W.QueryRow(`SELECT count(*) FROM sqlite_master`).Scan(&n) == nil && n == 0
		s.Close()
		if !empty {
			return nil, ErrSchema
		}
		return createNew(path)
	}
	return s, nil
}

// createNew opens a fresh recall.db at path and lays down the schema.
func createNew(path string) (*Store, error) {
	s, err := open(path)
	if err != nil {
		return nil, err
	}
	if err := s.create(); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func open(path string) (*Store, error) {
	w, err := sql.Open("sqlite", "file:"+path+writerPragmas)
	if err != nil {
		return nil, fmt.Errorf("recall: open writer: %w", err)
	}
	w.SetMaxOpenConns(1)
	if err := w.Ping(); err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("recall: open writer: %w", err)
	}
	r, err := sql.Open("sqlite", "file:"+path+readerPragmas)
	if err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("recall: open reader: %w", err)
	}
	r.SetMaxOpenConns(4)
	return &Store{Path: path, W: w, R: r}, nil
}

// schemaCurrent reports whether meta says this file is ours and current.
// An empty file (no tables) is treated as not current so it gets created.
func (s *Store) schemaCurrent() (bool, error) {
	var n int
	if err := s.W.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='meta'`).Scan(&n); err != nil {
		return false, fmt.Errorf("recall: inspect: %w", err)
	}
	if n == 0 {
		return false, nil
	}
	var v string
	err := s.W.QueryRow(`SELECT v FROM meta WHERE k='schema_version'`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("recall: read schema version: %w", err)
	}
	got, _ := strconv.Atoi(v)
	return got == recall.SchemaVersion, nil
}

func (s *Store) create() error {
	// Must precede the first table so gc can hand freed pages back with
	// PRAGMA incremental_vacuum instead of a full VACUUM copy.
	for _, stmt := range []string{`PRAGMA auto_vacuum=INCREMENTAL`, `VACUUM`} {
		if _, err := s.W.Exec(stmt); err != nil {
			return fmt.Errorf("recall: %s: %w", stmt, err)
		}
	}
	var av int
	if err := s.W.QueryRow(`PRAGMA auto_vacuum`).Scan(&av); err != nil || av != 2 {
		return fmt.Errorf("recall: auto_vacuum not incremental (%d, %v)", av, err)
	}
	tx, err := s.W.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, stmt := range recall.AllDDL() {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("recall: create schema: %w", err)
		}
	}
	if _, err := tx.Exec(`INSERT INTO meta(k, v) VALUES ('schema_version', ?)`, strconv.Itoa(recall.SchemaVersion)); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO host(host_uid, alias, is_local, last_seen) VALUES (?, 'this machine', 1, 0)`, LocalHostUID); err != nil {
		return err
	}
	return tx.Commit()
}

// Close closes both handles.
func (s *Store) Close() {
	if s == nil {
		return
	}
	_ = s.R.Close()
	_ = s.W.Close()
}

// Reset deletes the database and recreates it empty (recall rebuild).
func (s *Store) Reset() error {
	_ = s.R.Close()
	_ = s.W.Close()
	if err := RemoveFiles(s.Path); err != nil {
		return err
	}
	n, err := open(s.Path)
	if err != nil {
		return err
	}
	*s = *n
	return s.create()
}

// RemoveFiles deletes recall.db and its WAL sidecars. It only ever runs on
// the disposable file.
func RemoveFiles(path string) error {
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if err := os.Remove(path + suffix); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("recall: remove %s: %w", path+suffix, err)
		}
	}
	return nil
}

// FileSize is the on-disk size of the main file plus WAL.
func FileSize(path string) int64 {
	var total int64
	for _, suffix := range []string{"", "-wal"} {
		if info, err := os.Stat(path + suffix); err == nil {
			total += info.Size()
		}
	}
	return total
}

// ErrLocked means another sweep or backfill holds the machine-global lock.
var ErrLocked = errors.New("recall: another sweep is running")

// Lock takes the machine-global sweep lock (a flock beside recall.db) without
// waiting. All profiles contend on this one file.
func Lock(lockPath string) (release func(), err error) {
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// metaHostUID is the meta key holding this machine's recall identity.
const metaHostUID = "host_uid"

// HostUID returns this machine's stable recall identity: 32 hex characters
// minted on first use and kept in meta. Every card this machine exports is
// stamped with it, so a card stream is bound to the machine that produced
// it and not to the alias it was reached under (an alias can be renamed or
// repointed; the uid cannot). A rebuild mints a new one, which is correct:
// a rebuilt index is a new corpus for a puller's cursor.
func (s *Store) HostUID() (string, error) {
	var v string
	err := s.W.QueryRow(`SELECT v FROM meta WHERE k=?`, metaHostUID).Scan(&v)
	if err == nil && v != "" {
		return v, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("recall: mint host uid: %w", err)
	}
	v = hex.EncodeToString(buf)
	if _, err := s.W.Exec(`INSERT INTO meta(k, v) VALUES (?, ?) ON CONFLICT(k) DO NOTHING`, metaHostUID, v); err != nil {
		return "", err
	}
	// Another opener may have won the race: read back what is stored.
	if err := s.W.QueryRow(`SELECT v FROM meta WHERE k=?`, metaHostUID).Scan(&v); err != nil {
		return "", err
	}
	return v, nil
}
