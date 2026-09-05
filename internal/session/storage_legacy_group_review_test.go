package session

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/stretchr/testify/require"
)

func legacyGroupReviewFixture(t *testing.T, member bool) (*Storage, []*Instance, *GroupTree) {
	t.Helper()
	s := newTestStorage(t)
	require.NoError(t, s.db.SaveGroups([]*statedb.GroupRow{{Path: DefaultGroupName, Name: DefaultGroupName, Expanded: true, MaxConcurrent: 2}}))
	if member {
		require.NoError(t, s.db.SaveInstance(&statedb.InstanceRow{ID: "legacy", Title: "legacy", Tool: "shell", ProjectPath: t.TempDir(), GroupPath: DefaultGroupName}))
	}
	instances, groups, err := s.LoadWithGroups()
	require.NoError(t, err)
	return s, instances, NewGroupTreeWithGroups(instances, groups)
}

func TestLegacyGroupReviewMigrationCommitsOnce(t *testing.T) {
	for _, member := range []bool{false, true} {
		s, instances, tree := legacyGroupReviewFixture(t, member)
		for attempt := 0; attempt < 2; attempt++ {
			require.NoError(t, s.SaveWithGroups(instances, tree.ShallowCopyForSave()))
			groups, err := s.db.LoadGroups()
			require.NoError(t, err)
			require.Len(t, groups, 1)
			require.Equal(t, DefaultGroupPath, groups[0].Path)
			require.Equal(t, 2, groups[0].MaxConcurrent)
			if member {
				row, err := s.db.LoadInstanceByID("legacy")
				require.NoError(t, err)
				require.Equal(t, DefaultGroupPath, row.GroupPath)
			}
		}
	}
}

func TestLegacyGroupReviewMigrationPreservesConcurrentMetadata(t *testing.T) {
	s, instances, tree := legacyGroupReviewFixture(t, true)
	_, err := s.db.DB().Exec("UPDATE groups SET max_concurrent = 7 WHERE path = ?", DefaultGroupName)
	require.NoError(t, err)
	require.NoError(t, s.SaveWithGroups(instances, tree.ShallowCopyForSave()))
	require.Nil(t, storedGroup(t, s, DefaultGroupName))
	require.Equal(t, 7, storedGroup(t, s, DefaultGroupPath).MaxConcurrent)
}

func TestLegacyGroupReviewExistingCanonicalCollisionFailsClosed(t *testing.T) {
	s := newTestStorage(t)
	require.NoError(t, s.db.SaveGroups([]*statedb.GroupRow{
		{Path: DefaultGroupName, Name: "legacy", MaxConcurrent: 2},
		{Path: DefaultGroupPath, Name: "canonical", MaxConcurrent: 7},
	}))
	_, _, err := s.LoadWithGroups()
	require.ErrorContains(t, err, "conflict")
	require.Equal(t, 2, storedGroup(t, s, DefaultGroupName).MaxConcurrent)
	require.Equal(t, 7, storedGroup(t, s, DefaultGroupPath).MaxConcurrent)
}

func TestLegacyGroupReviewMigrationConflictsAtomically(t *testing.T) {
	for _, change := range []string{"target", "member", "group field"} {
		t.Run(change, func(t *testing.T) {
			s, instances, tree := legacyGroupReviewFixture(t, true)
			switch change {
			case "target":
				require.NoError(t, s.db.SaveGroups([]*statedb.GroupRow{{Path: DefaultGroupPath, Name: "existing canonical"}}))
			case "member":
				require.NoError(t, s.db.SaveInstance(&statedb.InstanceRow{ID: "new-member", Tool: "shell", GroupPath: DefaultGroupName}))
			case "group field":
				_, err := s.db.DB().Exec("UPDATE groups SET max_concurrent = 7 WHERE path = ?", DefaultGroupName)
				require.NoError(t, err)
				tree.Groups[DefaultGroupPath].MaxConcurrent = 8
			}
			require.ErrorContains(t, s.SaveWithGroups(instances, tree.ShallowCopyForSave()), "conflict")
			require.NotNil(t, storedGroup(t, s, DefaultGroupName))
			row, err := s.db.LoadInstanceByID("legacy")
			require.NoError(t, err)
			require.Equal(t, DefaultGroupName, row.GroupPath)
			if change != "target" {
				require.Nil(t, storedGroup(t, s, DefaultGroupPath))
			}
		})
	}
}
