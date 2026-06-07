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

// TestPersistRevivedInstances_IsSweepFree is the contrastive guard for the
// chosen persist path. It proves the PROPERTY revive depends on — that
// PersistRevivedInstances never deletes a row absent from its argument — WITHOUT
// pinning any particular behavior of SaveWithGroups.
//
// (It deliberately replaces an earlier "negative witness" test that asserted
// SaveWithGroups DOES sweep. That made buggy full-rewrite behavior a required
// contract: the day SaveWithGroups is globally de-swept — a desirable hardening
// — the old test would fail for the wrong reason. What revive actually needs is
// only that ITS path is sweep-free, which is exactly what we assert here.)
//
// Sequence: revive loads a 1-row snapshot, a concurrent add inserts a 2nd row,
// then revive persists ONLY its snapshot row via PersistRevivedInstances. The
// concurrently-added row must remain — proving no DELETE-NOT-IN sweep happened.
func TestPersistRevivedInstances_IsSweepFree(t *testing.T) {
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

	// revive loads its snapshot (only sess-existing).
	snapshot, _, err := reviveStorage.LoadWithGroups()
	require.NoError(t, err)
	require.Len(t, snapshot, 1)

	// A concurrent add inserts a second row after the snapshot was taken.
	added := &Instance{
		ID: "sess-added-concurrently", Title: "added-concurrently",
		ProjectPath: "/tmp/added", GroupPath: "test", Command: "claude",
		Tool: "claude", Status: StatusRunning, CreatedAt: time.Now(),
	}
	require.NoError(t, addStorage.InsertSessionAndVerify(added, nil))

	// revive persists ONLY its snapshot row through the targeted path.
	snapshot[0].Status = StatusRunning
	require.NoError(t, reviveStorage.PersistRevivedInstances(snapshot))

	loaded, err := openStorage().Load()
	require.NoError(t, err)
	ids := map[string]bool{}
	for _, inst := range loaded {
		ids[inst.ID] = true
	}
	// The targeted path performs NO sweep: a row absent from its argument is
	// left untouched. This is the property revive relies on.
	assert.True(t, ids["sess-added-concurrently"],
		"PersistRevivedInstances must be sweep-free: a row absent from its argument must survive")
	assert.True(t, ids["sess-existing"],
		"the revived row must persist")
}
