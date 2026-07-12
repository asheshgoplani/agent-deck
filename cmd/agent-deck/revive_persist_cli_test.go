package main

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newReviveCLIStorage opens a real Storage in the isolated _test profile
// (TestMain forces AGENTDECK_PROFILE=_test under an isolated HOME). Each call
// returns a fresh handle on the SAME on-disk state.db, so we can simulate two
// concurrent processes (one reviving, one adding) like production does.
func newReviveCLIStorage(t *testing.T) *session.Storage {
	t.Helper()
	s, err := session.NewStorageWithProfile("")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// stubReviverHealingErrored returns a Reviver wired so that the named tmux
// session probes as ClassErrored and ReviveAction heals it (StatusError →
// StatusRunning) — exactly what defaultReviveAction does in CLI mode, but
// without touching real tmux. This lets the CLI-level test drive
// reviveAndPersist deterministically.
func stubReviverHealingErrored(t *testing.T) *session.Reviver {
	t.Helper()
	return &session.Reviver{
		// tmux server is "alive" for every probed session.
		TmuxExists: func(name, _ string) bool { return true },
		// pipe is dead → forces ClassErrored for StatusError rows.
		PipeAlive: func(name string) bool { return false },
		// Mirror defaultReviveAction's status-only heal.
		ReviveAction: func(i *session.Instance) error {
			if i.Status == session.StatusError {
				i.Status = session.StatusRunning
			}
			return nil
		},
		Stagger: 0,
	}
}

// TestReviveCLI_PersistViaTargetedPath_DoesNotClobberConcurrentAdd is the
// command-level regression guard for the lost-update race. It drives
// reviveAndPersist — the exact seam handleSessionRevive uses — instead of
// poking storage directly, so a future regression that swaps the persist back
// to saveSessionData / SaveWithGroups / SaveInstances (the DELETE-NOT-IN sweep
// path) IS caught here, not just in the storage unit test.
//
// Sequence (production order):
//  1. revive loads its snapshot (only the pre-existing errored session).
//  2. a concurrent `add` inserts a brand-new session via the targeted path.
//  3. revive heals + persists through reviveAndPersist.
//
// Invariant: the concurrently-added session survives, and the errored session
// is now running.
func TestReviveCLI_PersistViaTargetedPath_DoesNotClobberConcurrentAdd(t *testing.T) {
	// Own profile: saves are upsert-only (#1551), so rows written by other
	// tests in the shared _test profile would leak into this test's snapshot.
	t.Setenv("AGENTDECK_PROFILE", "_test_revive_clobber")
	reviveStorage := newReviveCLIStorage(t)

	existing := &session.Instance{
		ID:          "revive-cli-existing",
		Title:       "existing",
		ProjectPath: "/tmp/existing",
		GroupPath:   "test",
		Command:     "claude",
		Tool:        "claude",
		Status:      session.StatusError,
		CreatedAt:   time.Now().Add(-2 * time.Minute),
	}
	require.NoError(t, reviveStorage.SaveWithGroups(
		[]*session.Instance{existing},
		session.NewGroupTree([]*session.Instance{existing}),
	))

	// Step 1: revive loads its (now-stale-once-add-happens) snapshot.
	snapshot, _, err := reviveStorage.LoadWithGroups()
	require.NoError(t, err)
	require.Len(t, snapshot, 1)

	// Step 2: a concurrent process inserts a new session via the targeted path.
	addStorage := newReviveCLIStorage(t)
	added := &session.Instance{
		ID:          "revive-cli-added-concurrently",
		Title:       "added-concurrently",
		ProjectPath: "/tmp/added",
		GroupPath:   "test",
		Command:     "claude",
		Tool:        "claude",
		Status:      session.StatusRunning,
		CreatedAt:   time.Now(),
	}
	require.NoError(t, addStorage.InsertSessionAndVerify(added, nil))

	// Step 3: drive the CLI persist seam exactly as handleSessionRevive does.
	summary, err := reviveAndPersist(reviveStorage, snapshot, stubReviverHealingErrored(t))
	require.NoError(t, err)
	require.Equal(t, 1, summary.Revived, "the errored session should have been revived")

	// Verify against a fresh handle (independent read).
	loaded, err := newReviveCLIStorage(t).Load()
	require.NoError(t, err)
	ids := map[string]session.Status{}
	for _, inst := range loaded {
		ids[inst.ID] = inst.Status
	}
	assert.Contains(t, ids, "revive-cli-added-concurrently",
		"concurrently-added session must survive a CLI revive (lost-update race)")
	require.Contains(t, ids, "revive-cli-existing", "the revived session must persist")
	assert.Equal(t, session.StatusRunning, ids["revive-cli-existing"],
		"revive must have healed StatusError → StatusRunning on disk")
}

// TestReviveCLI_TargetedUpdate_PreservesConcurrentEditToRevivedRow guards
// concern #2: the persist must be a status-only UPDATE, not a full-row write.
// A concurrent process edits a NON-status field (Title) of the very row revive
// is about to heal, AFTER revive loaded its snapshot. A full-row INSERT OR
// REPLACE from revive's stale snapshot would clobber that edit; the targeted
// status UPDATE must leave it intact.
func TestReviveCLI_TargetedUpdate_PreservesConcurrentEditToRevivedRow(t *testing.T) {
	// Own profile: saves are upsert-only (#1551), so rows written by other
	// tests in the shared _test profile would leak into this test's snapshot.
	t.Setenv("AGENTDECK_PROFILE", "_test_revive_edit")
	reviveStorage := newReviveCLIStorage(t)

	existing := &session.Instance{
		ID:          "revive-cli-edit-target",
		Title:       "original-title",
		ProjectPath: "/tmp/edit",
		GroupPath:   "test",
		Command:     "claude",
		Tool:        "claude",
		Status:      session.StatusError,
		CreatedAt:   time.Now().Add(-2 * time.Minute),
	}
	require.NoError(t, reviveStorage.SaveWithGroups(
		[]*session.Instance{existing},
		session.NewGroupTree([]*session.Instance{existing}),
	))

	// revive loads its snapshot — Title is still "original-title" here.
	snapshot, _, err := reviveStorage.LoadWithGroups()
	require.NoError(t, err)
	require.Len(t, snapshot, 1)

	// A concurrent process renames the SAME row via the targeted single-row
	// path, after revive's snapshot was taken.
	editStorage := newReviveCLIStorage(t)
	editInstances, editGroups, err := editStorage.LoadWithGroups()
	require.NoError(t, err)
	editInstances[0].Title = "renamed-concurrently"
	require.NoError(t, editStorage.InsertSessionAndVerify(
		editInstances[0], session.NewGroupTreeWithGroups(editInstances, editGroups)))

	// revive heals + persists from its stale snapshot (Title="original-title").
	_, err = reviveAndPersist(reviveStorage, snapshot, stubReviverHealingErrored(t))
	require.NoError(t, err)

	loaded, err := newReviveCLIStorage(t).Load()
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	assert.Equal(t, "renamed-concurrently", loaded[0].Title,
		"targeted status UPDATE must NOT clobber a concurrent edit to a non-status field")
	assert.Equal(t, session.StatusRunning, loaded[0].Status,
		"revive must still have healed status")
}
