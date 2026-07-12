package session

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestIssue1396_FinishLastWorktreeDoesNotTripEmptySweepGuard is the regression
// test for issue #1396.
//
// `agent-deck worktree finish <session>`, when that session is the ONLY one in
// the registry, used to persist the row removal via SaveWithGroups([]) which
// flows into SaveInstances([]) and trips the S1 empty-sweep data-loss guard
// (ErrRefusingEmptySweep). The guard rejection fired only AFTER the
// irreversible git steps (merge / remove-worktree / delete-branch) had already
// run, leaving an orphaned session row pointing at a deleted worktree.
//
// The fix routes the last-session removal through the targeted
// RemoveSessionAndVerify path (DELETE + SaveGroupsOnly) instead, which removes
// the row without ever calling SaveInstances([]).
func TestIssue1396_FinishLastWorktreeDoesNotTripEmptySweepGuard(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")

	// Seed a SINGLE worktree session — exactly the "only session in registry"
	// condition that triggers the bug.
	seed := rmTestStorage(t, dbPath)
	only := &Instance{
		ID:               "wt-finish-last-001",
		Title:            "wtsess",
		ProjectPath:      "/tmp/issue1396-repo",
		GroupPath:        DefaultGroupPath,
		Command:          "bash",
		Tool:             "bash",
		Status:           StatusStopped,
		CreatedAt:        time.Now(),
		WorktreeRepoRoot: "/tmp/issue1396-repo",
		WorktreePath:     "/tmp/issue1396-repo/.worktrees/feat1",
		WorktreeBranch:   "feature/feat1",
	}
	require.NoError(t, seed.SaveWithGroups([]*Instance{only}, NewGroupTree([]*Instance{only})))
	require.True(t, only.IsWorktree(), "seed instance must be a worktree session")

	// Sanity: the OLD persistence path cannot remove the last session. At #1396
	// time SaveWithGroups([]) tripped the S1 empty-sweep guard
	// (ErrRefusingEmptySweep); since #1550 SaveWithGroups is upsert-only, so an
	// empty save is a benign no-op that deletes nothing. Either way the row
	// survives — locking in WHY removal needs the targeted path.
	t.Run("SaveWithGroups path cannot remove the last session", func(t *testing.T) {
		s := rmTestStorage(t, dbPath)
		var remaining []*Instance // empty: the finished session was the only one
		err := s.SaveWithGroups(remaining, NewGroupTreeWithGroups(remaining, nil))
		require.NoError(t, err, "upsert-only SaveWithGroups([]) must be a benign no-op (#1550)")

		// The row is still present — an empty save must never wipe the table.
		exists, exErr := s.InstanceExists(only.ID)
		require.NoError(t, exErr)
		require.True(t, exists, "row must survive an empty save (the #1396 orphan / #1550 no-sweep)")
	})

	// The FIX: RemoveSessionAndVerify cleanly removes the last session's row
	// without tripping the guard, leaving an empty registry.
	t.Run("RemoveSessionAndVerify removes the last session cleanly", func(t *testing.T) {
		s := rmTestStorage(t, dbPath)
		var remaining []*Instance // empty: the finished session was the only one
		require.Empty(t, remaining, "after dropping the only session, remaining must be empty")

		groupTree := NewGroupTreeWithGroups(remaining, nil)
		err := s.RemoveSessionAndVerify(only.ID, remaining, groupTree)
		require.NoError(t, err, "finishing the last worktree session must not trip the guard")

		exists, exErr := s.InstanceExists(only.ID)
		require.NoError(t, exErr)
		require.False(t, exists, "the finished session's row must be gone (no orphan)")
	})
}
