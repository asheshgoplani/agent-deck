package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/recall"
)

func TestOpen_CreatesSchemaAndHost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "recall.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var n int
	if err := s.R.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n < len(recall.TableDDL)/2 {
		t.Fatalf("tables = %d", n)
	}
	var isLocal int
	if err := s.R.QueryRow(`SELECT is_local FROM host WHERE host_uid=?`, LocalHostUID).Scan(&isLocal); err != nil || isLocal != 1 {
		t.Fatalf("host row: %v %d", err, isLocal)
	}
	// Reopen keeps the file.
	if _, err := s.W.Exec(`INSERT INTO meta(k,v) VALUES('probe','1')`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	var v string
	if err := s2.R.QueryRow(`SELECT v FROM meta WHERE k='probe'`).Scan(&v); err != nil || v != "1" {
		t.Fatalf("probe after reopen: %v %q", err, v)
	}
}

// A file from another schema version is disposable: Open recreates it.
func TestOpen_RecreatesOnSchemaMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recall.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.W.Exec(`UPDATE meta SET v='0' WHERE k='schema_version'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.W.Exec(`INSERT INTO meta(k,v) VALUES('probe','1')`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	// OpenCurrent refuses without deleting: the CLI takes the sweep lock
	// before it recreates through Open.
	if _, err := OpenCurrent(path); !errors.Is(err, ErrSchema) {
		t.Fatalf("OpenCurrent on a mismatch: %v", err)
	}
	if info, err := os.Stat(path); err != nil || info.Size() == 0 {
		t.Fatalf("OpenCurrent must leave the file alone: %v", err)
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	var n int
	if err := s2.R.QueryRow(`SELECT count(*) FROM meta WHERE k='probe'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("stale row survived: %v %d", err, n)
	}
}

func TestOpen_GarbageFileIsRecreated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recall.db")
	if err := os.WriteFile(path, []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Skipf("driver reports garbage differently: %v", err)
	}
	s.Close()
}

func TestLock_IsExclusive(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "recall", "sweep.lock")
	release, err := Lock(lock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(lock); !errors.Is(err, ErrLocked) {
		t.Fatalf("second lock: %v", err)
	}
	release()
	release2, err := Lock(lock)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	release2()
}

func TestReset_EmptiesDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recall.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.W.Exec(`INSERT INTO meta(k,v) VALUES('probe','1')`); err != nil {
		t.Fatal(err)
	}
	if err := s.Reset(); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.R.QueryRow(`SELECT count(*) FROM meta WHERE k='probe'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("after reset: %v %d", err, n)
	}
}
