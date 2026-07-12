package session

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSaveWithGroupsDoesNotClobberConcurrentlyAddedSession reproduces issue
// #1550: concurrent TUIs silently delete each other's sessions because the
// routine TUI save path rewrote the whole instances table from a stale
// in-memory snapshot.
//
// The exact production sequence (see home.go saveInstancesWithForce):
//
//  1. TUI A loads the full instances snapshot (LoadWithGroups).
//  2. TUI B creates a NEW session via the targeted single-row path
//     (InsertSessionAndVerify -> SaveInstance, no sweep — the #1031 fix).
//  3. TUI A performs any routine save (rename, cursor move, detection
//     result, ...) with its STALE snapshot. On origin/main this went through
//     SaveWithGroups -> SaveInstances, whose `DELETE FROM instances WHERE id
//     NOT IN (<stale ids>)` sweep deleted B's row because it was never in
//     A's snapshot — while B's tmux session and agent kept running.
//
// This test drives that sequence deterministically against a shared SQLite
// file. It FAILS before the #1550 fix (B's row is gone) and PASSES once
// SaveWithGroups is upsert-only (statedb.UpsertInstances, no sweep).
func TestSaveWithGroupsDoesNotClobberConcurrentlyAddedSession(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")

	openStorage := func() *Storage {
		db, err := statedb.Open(dbPath)
		require.NoError(t, err)
		require.NoError(t, db.Migrate())
		t.Cleanup(func() { db.Close() })
		return &Storage{db: db, dbPath: dbPath, profile: "_test"}
	}

	tuiA := openStorage()
	tuiB := openStorage()

	// Seed one pre-existing session both TUIs know about.
	existing := &Instance{
		ID:          "sess-existing",
		Title:       "existing",
		ProjectPath: "/tmp/existing",
		GroupPath:   "test",
		Command:     "claude",
		Tool:        "claude",
		Status:      StatusIdle,
		CreatedAt:   time.Now().Add(-2 * time.Minute),
	}
	require.NoError(t, tuiA.SaveWithGroups(
		[]*Instance{existing}, NewGroupTree([]*Instance{existing})))

	// Step 1: TUI A loads its snapshot (only knows about sess-existing).
	snapshot, _, err := tuiA.LoadWithGroups()
	require.NoError(t, err)
	require.Len(t, snapshot, 1, "TUI A should load exactly the pre-existing session")

	// Step 2: TUI B creates a brand-new session via the targeted single-row
	// path (the production path InsertSessionAndVerify uses).
	added := &Instance{
		ID:          "sess-added-by-other-tui",
		Title:       "added-by-other-tui",
		ProjectPath: "/tmp/added",
		GroupPath:   "test",
		Command:     "claude",
		Tool:        "claude",
		Status:      StatusRunning,
		CreatedAt:   time.Now(),
	}
	require.NoError(t, tuiB.InsertSessionAndVerify(added, nil))

	// Step 3: TUI A performs a routine save with its stale snapshot (this is
	// what every rename / reorder / detection-result save in the TUI does).
	snapshot[0].Title = "existing-renamed"
	require.NoError(t, tuiA.SaveWithGroups(snapshot, NewGroupTree(snapshot)))

	// Assert: B's session must survive A's stale save.
	loaded, err := openStorage().Load()
	require.NoError(t, err)

	ids := map[string]string{}
	for _, inst := range loaded {
		ids[inst.ID] = inst.Title
	}
	assert.Contains(t, ids, "sess-added-by-other-tui",
		"session created by a concurrent TUI must survive another TUI's stale full-snapshot save (issue #1550)")
	assert.Equal(t, "existing-renamed", ids["sess-existing"],
		"TUI A's own edit must still persist")
}
