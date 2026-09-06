package session

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConcurrentStorageWrites verifies that two Storage instances backed by
// the same SQLite file can write concurrently without data loss or errors,
// and that dedup semantics are preserved after both writes complete (DEDUP-03).
func TestConcurrentStorageWrites(t *testing.T) {
	// 1. Create a temp dir with a single shared state.db path.
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")

	// openStorage opens a new Storage instance against the shared SQLite file.
	openStorage := func() *Storage {
		db, err := statedb.Open(dbPath)
		require.NoError(t, err)
		require.NoError(t, db.Migrate())
		t.Cleanup(func() { db.Close() })
		return &Storage{db: db, dbPath: dbPath, profile: "_test"}
	}

	s1 := openStorage()
	s2 := openStorage()

	sharedClaudeID := "claude-session-shared-123"

	// 2. Build instances with the same ClaudeSessionID but different creation times.
	//    The older session (s1) should retain the ID after dedup.
	instances1 := []*Instance{{
		ID:              "sess-from-s1",
		Title:           "S1 Session",
		ProjectPath:     "/tmp/s1",
		GroupPath:       "test",
		Command:         "claude",
		Tool:            "claude",
		Status:          StatusRunning,
		ClaudeSessionID: sharedClaudeID,
		CreatedAt:       time.Now().Add(-1 * time.Minute), // older — keeps ID after dedup
	}}

	instances2 := []*Instance{{
		ID:              "sess-from-s2",
		Title:           "S2 Session",
		ProjectPath:     "/tmp/s2",
		GroupPath:       "test",
		Command:         "claude",
		Tool:            "claude",
		Status:          StatusRunning,
		ClaudeSessionID: sharedClaudeID,
		CreatedAt:       time.Now(), // newer — loses ID after dedup
	}}

	// 3. Write both storages concurrently (exercising SQLite WAL concurrent access).
	var wg sync.WaitGroup
	var err1, err2 error

	wg.Add(2)
	go func() {
		defer wg.Done()
		gt1 := NewGroupTree(instances1)
		err1 = s1.SaveWithGroups(instances1, gt1)
	}()
	go func() {
		defer wg.Done()
		gt2 := NewGroupTree(instances2)
		err2 = s2.SaveWithGroups(instances2, gt2)
	}()
	wg.Wait()

	// Both writes must succeed; SQLite WAL mode supports concurrent writers.
	require.NoError(t, err1, "s1 SaveWithGroups should succeed")
	require.NoError(t, err2, "s2 SaveWithGroups should succeed")

	// 4. Load from a third independent storage. Both rows must survive: #1550
	//    made SaveWithGroups upsert-only, so neither concurrent writer can
	//    sweep the other's row. (Before that fix this test observed only one
	//    surviving row — the "dedup" was really the destructive DELETE-NOT-IN.)
	s3 := openStorage()
	loaded, err := s3.Load()
	require.NoError(t, err)
	require.Len(t, loaded, 2, "both concurrently-written sessions must survive (#1550)")

	// 5. Dedup is a read-side invariant: each writer only dedups its own slice,
	//    so cross-process duplicates are resolved when a full set is loaded
	//    (the TUI runs UpdateClaudeSessionsWithDedup after every reload).
	UpdateClaudeSessionsWithDedup(loaded)
	holdersCount := 0
	for _, inst := range loaded {
		if inst.ClaudeSessionID == sharedClaudeID {
			holdersCount++
		}
	}
	assert.LessOrEqual(t, holdersCount, 1,
		"at most one session should retain the shared ClaudeSessionID after load-time dedup")
}

// A stale TUI snapshot changing the title must not erase a concurrent CLI
// status update on the same row. This is intentionally deterministic: both
// handles load before either writer saves.
func TestStaleSnapshotPreservesDisjointFieldUpdate(t *testing.T) {
	tmpDir := t.TempDir()
	open := func() *Storage {
		db, err := statedb.Open(filepath.Join(tmpDir, "state.db"))
		require.NoError(t, err)
		require.NoError(t, db.Migrate())
		return &Storage{db: db, dbPath: filepath.Join(tmpDir, "state.db"), profile: "_test"}
	}
	seed := open()
	base := &Instance{ID: "shared", Title: "original", ProjectPath: tmpDir, GroupPath: "test", Tool: "shell", Status: StatusIdle, CreatedAt: time.Now()}
	require.NoError(t, seed.SaveWithGroups([]*Instance{base}, NewGroupTree([]*Instance{base})))
	tui, cli := open(), open()
	tuiRows, _, err := tui.LoadWithGroups()
	require.NoError(t, err)
	cliRows, _, err := cli.LoadWithGroups()
	require.NoError(t, err)
	cliRows[0].Status = StatusRunning
	require.NoError(t, cli.SaveWithGroups(cliRows, nil))
	tuiRows[0].Title = "renamed"
	require.NoError(t, tui.SaveWithGroups(tuiRows, nil))
	check := open()
	got, err := check.Load()
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "renamed", got[0].Title)
	assert.Equal(t, StatusRunning, got[0].Status, "stale title save must preserve disjoint CLI status update")
}
