package statedb

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestOpenReadOnlyLiveSeesUncheckpointedWAL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	writer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	writer.SetMaxOpenConns(1)
	for _, query := range []string{"PRAGMA journal_mode=WAL", "PRAGMA wal_autocheckpoint=0", "CREATE TABLE evidence (value TEXT)", "PRAGMA wal_checkpoint(TRUNCATE)", "INSERT INTO evidence VALUES ('committed in WAL')"} {
		if _, err := writer.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	immutable, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := immutable.DB().QueryRow("SELECT COUNT(*) FROM evidence").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("fixture unexpectedly checkpointed: %d", count)
	}
	if err := immutable.Close(); err != nil {
		t.Fatal(err)
	}
	live, err := OpenReadOnlyLive(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := live.DB().QueryRow("SELECT COUNT(*) FROM evidence").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("live reader missed WAL: %d", count)
	}
	if _, err := live.DB().Exec("DELETE FROM evidence"); err == nil {
		t.Fatal("read-only connection accepted write")
	}
	if err := live.Close(); err != nil {
		t.Fatal(err)
	}
	if err := writer.QueryRow("SELECT COUNT(*) FROM evidence").Scan(&count); err != nil || count != 1 {
		t.Fatalf("reader changed rows: count=%d err=%v", count, err)
	}
}
