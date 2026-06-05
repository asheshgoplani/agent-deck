package session

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReviveDoesNotClobberConcurrentlyAddedSession reproduces the lost-update
// race between `session revive` and a concurrent `session add` (issue:
// revive's read-process-write cycle drops rows added after it loaded).
//
// The exact production sequence (see revive_cmd.go + storage.go):
//
//  1. revive loads the full instances snapshot (LoadWithGroups).
//  2. another process (TUI/CLI add) inserts a NEW session via the targeted
//     single-row path (InsertSessionAndVerify -> SaveInstance, no sweep).
//  3. revive persists its STALE snapshot. On origin/main this goes through
//     SaveWithGroups -> SaveInstances, whose `DELETE FROM instances WHERE id
//     NOT IN (<stale ids>)` sweep deletes the concurrently-added row because
//     it was never in revive's snapshot.
//
// This test drives that sequence deterministically (no goroutine timing
// needed) against a shared SQLite file. It FAILS on origin/main (the added
// session is gone) and PASSES once revive persists only the rows it actually
// touched via a targeted, sweep-free write.
func TestReviveDoesNotClobberConcurrentlyAddedSession(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")

	openStorage := func() *Storage {
		db, err := statedb.Open(dbPath)
		require.NoError(t, err)
		require.NoError(t, db.Migrate())
		t.Cleanup(func() { db.Close() })
		return &Storage{db: db, dbPath: dbPath, profile: "_test"}
	}

	reviveStorage := openStorage()
	addStorage := openStorage()

	// Seed one pre-existing session that revive will heal.
	existing := &Instance{
		ID:          "sess-existing",
		Title:       "existing",
		ProjectPath: "/tmp/existing",
		GroupPath:   "test",
		Command:     "claude",
		Tool:        "claude",
		Status:      StatusError, // errored -> revive flips to running
		CreatedAt:   time.Now().Add(-2 * time.Minute),
	}
	require.NoError(t, reviveStorage.SaveWithGroups(
		[]*Instance{existing}, NewGroupTree([]*Instance{existing})))

	// Step 1: revive loads the snapshot (only knows about sess-existing).
	snapshot, groups, err := reviveStorage.LoadWithGroups()
	require.NoError(t, err)
	require.Len(t, snapshot, 1, "revive should load exactly the pre-existing session")

	// Step 2: a concurrent `add` inserts a brand-new session via the targeted
	// single-row path (the production path InsertSessionAndVerify uses).
	added := &Instance{
		ID:          "sess-added-concurrently",
		Title:       "added-concurrently",
		ProjectPath: "/tmp/added",
		GroupPath:   "test",
		Command:     "claude",
		Tool:        "claude",
		Status:      StatusRunning,
		CreatedAt:   time.Now(),
	}
	require.NoError(t, addStorage.InsertSessionAndVerify(added, nil))

	// Step 3: revive heals the errored session and persists. On main this used
	// the stale full-table snapshot (SaveWithGroups) and clobbered sess-added.
	// The fix persists only the rows revive actually touched, via a targeted
	// sweep-free write (PersistRevivedInstances).
	_ = groups
	snapshot[0].Status = StatusRunning
	require.NoError(t, reviveStorage.PersistRevivedInstances(snapshot))

	// Assert: the concurrently-added session must survive.
	loaded, err := openStorage().Load()
	require.NoError(t, err)

	ids := map[string]bool{}
	for _, inst := range loaded {
		ids[inst.ID] = true
	}
	assert.True(t, ids["sess-added-concurrently"],
		"concurrently-added session must survive a concurrent revive (lost-update race)")
	assert.True(t, ids["sess-existing"],
		"the revived session must still be present")
}

// TestReviveSaveWithGroupsClobbersConcurrentAdd_DemonstratesBug pins down the
// root cause: it drives the OLD revive save path (SaveWithGroups on the stale
// snapshot) and asserts that it DOES clobber the concurrently-added row. This
// is the negative witness — it documents exactly why revive must NOT use the
// full-rewrite path. If a future change makes SaveWithGroups stop sweeping
// (or someone "fixes" it differently), this test flags that the assumption
// underpinning PersistRevivedInstances has shifted. The positive guarantee
// lives in TestReviveDoesNotClobberConcurrentlyAddedSession above.
func TestReviveSaveWithGroupsClobbersConcurrentAdd_DemonstratesBug(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")

	openStorage := func() *Storage {
		db, err := statedb.Open(dbPath)
		require.NoError(t, err)
		require.NoError(t, db.Migrate())
		t.Cleanup(func() { db.Close() })
		return &Storage{db: db, dbPath: dbPath, profile: "_test"}
	}

	reviveStorage := openStorage()
	addStorage := openStorage()

	existing := &Instance{
		ID: "sess-existing", Title: "existing", ProjectPath: "/tmp/existing",
		GroupPath: "test", Command: "claude", Tool: "claude",
		Status: StatusError, CreatedAt: time.Now().Add(-2 * time.Minute),
	}
	require.NoError(t, reviveStorage.SaveWithGroups(
		[]*Instance{existing}, NewGroupTree([]*Instance{existing})))

	snapshot, _, err := reviveStorage.LoadWithGroups()
	require.NoError(t, err)

	added := &Instance{
		ID: "sess-added-concurrently", Title: "added-concurrently",
		ProjectPath: "/tmp/added", GroupPath: "test", Command: "claude",
		Tool: "claude", Status: StatusRunning, CreatedAt: time.Now(),
	}
	require.NoError(t, addStorage.InsertSessionAndVerify(added, nil))

	// The old revive persistence: full rewrite of the stale snapshot. Its
	// DELETE-NOT-IN sweep drops sess-added-concurrently (absent from snapshot).
	snapshot[0].Status = StatusRunning
	require.NoError(t, reviveStorage.SaveWithGroups(snapshot, NewGroupTree(snapshot)))

	loaded, err := openStorage().Load()
	require.NoError(t, err)
	ids := map[string]bool{}
	for _, inst := range loaded {
		ids[inst.ID] = true
	}
	// The old path's DELETE-NOT-IN sweep removes the concurrently-added row.
	// This assertion documents the bug; it is the reason revive now routes
	// through PersistRevivedInstances instead of SaveWithGroups.
	assert.False(t, ids["sess-added-concurrently"],
		"witness: the old SaveWithGroups revive path clobbers the concurrently-added row")
}
