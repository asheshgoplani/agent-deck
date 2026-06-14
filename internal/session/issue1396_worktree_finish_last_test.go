package session

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
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

	// Sanity: the OLD persistence path (the pre-fix bug) trips the guard when
	// removing the last session. This locks in WHY the targeted path is needed.
	t.Run("old SaveWithGroups path trips the empty-sweep guard", func(t *testing.T) {
		s := rmTestStorage(t, dbPath)
		var remaining []*Instance // empty: the finished session was the only one
		err := s.SaveWithGroups(remaining, NewGroupTreeWithGroups(remaining, nil))
		require.Error(t, err, "SaveWithGroups([]) on a populated table must be refused")
		require.True(t, errors.Is(err, statedb.ErrRefusingEmptySweep),
			"expected ErrRefusingEmptySweep, got: %v", err)

		// And the row is still present — proving the orphan in the bug report.
		exists, exErr := s.InstanceExists(only.ID)
		require.NoError(t, exErr)
		require.True(t, exists, "row must still exist after the guard rejection (the orphan)")
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
