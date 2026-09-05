package session

import (
	"database/sql/driver"
	"fmt"
	"modernc.org/sqlite"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/stretchr/testify/require"
)

func groupSnapshotFixture(t *testing.T) (*Storage, []*Instance, *GroupTree, []*Instance, *GroupTree) {
	t.Helper()
	s, seed, _ := snapshotFixture(t)
	require.NoError(t, s.SaveWithGroups(seed, NewGroupTree(seed)))
	a, ga, err := s.LoadWithGroups()
	require.NoError(t, err)
	b, gb, err := s.LoadWithGroups()
	require.NoError(t, err)
	return s, a, NewGroupTreeWithGroups(a, ga), b, NewGroupTreeWithGroups(b, gb)
}

func storedGroup(t *testing.T, s *Storage, path string) *statedb.GroupRow {
	t.Helper()
	rows, err := s.db.LoadGroups()
	require.NoError(t, err)
	for _, row := range rows {
		if row.Path == path {
			return row
		}
	}
	return nil
}

func TestGroupStorageSnapshotsMergeDisjointFields(t *testing.T) {
	s, a, ta, _, tb := groupSnapshotFixture(t)
	tb.Groups["test"].DefaultPath = t.TempDir()
	require.NoError(t, s.SaveGroupsOnly(tb.ShallowCopyForSave()))
	ta.Groups["test"].MaxConcurrent = 3
	require.NoError(t, s.SaveWithGroups(a, ta.ShallowCopyForSave()))
	got := storedGroup(t, s, "test")
	require.Equal(t, tb.Groups["test"].DefaultPath, got.DefaultPath)
	require.Equal(t, 3, got.MaxConcurrent)
	// The live tree receives the successful copied save's baseline, while its
	// unedited stale default path must not become a later overwrite intent.
	ta.Groups["test"].MaxConcurrent = 4
	require.NoError(t, s.SaveGroupsOnly(ta.ShallowCopyForSave()))
	got = storedGroup(t, s, "test")
	require.Equal(t, tb.Groups["test"].DefaultPath, got.DefaultPath)
	require.Equal(t, 4, got.MaxConcurrent)
}

func TestGroupStorageSnapshotsOlderCopyCannotRebase(t *testing.T) {
	s, _, ta, _, tb := groupSnapshotFixture(t)
	older := ta.ShallowCopyForSave()
	tb.Groups["test"].DefaultPath = t.TempDir()
	require.NoError(t, s.SaveGroupsOnly(tb))
	ta.Groups["test"].MaxConcurrent = 2
	require.NoError(t, s.SaveGroupsOnly(ta.ShallowCopyForSave()))
	older.GroupList[0].DefaultPath = t.TempDir()
	require.ErrorContains(t, s.SaveGroupsOnly(older), "conflict")
	require.Equal(t, tb.Groups["test"].DefaultPath, storedGroup(t, s, "test").DefaultPath)
}

func TestGroupStorageSnapshotsDeletionDoesNotResurrect(t *testing.T) {
	s, _, ta, _, tb := groupSnapshotFixture(t)
	tb.DeleteGroup("test")
	require.NoError(t, s.SaveWithGroups(tb.GetAllInstances(), tb.ShallowCopyForSave()))
	require.Nil(t, storedGroup(t, s, "test"))
	require.ErrorContains(t, s.SaveGroupsOnly(ta), "deletion conflict")
	require.Nil(t, storedGroup(t, s, "test"))
	// Successful deletion intent is consumed on the live tree as well.
	require.NoError(t, s.db.SaveGroups([]*statedb.GroupRow{{Path: "test", Name: "explicitly recreated", MaxConcurrent: 9}}))
	require.NoError(t, s.SaveWithGroups(tb.GetAllInstances(), tb.ShallowCopyForSave()))
	require.Equal(t, 9, storedGroup(t, s, "test").MaxConcurrent)
}

func TestGroupStorageSnapshotsRenameRejectsUnknownAdditions(t *testing.T) {
	for _, kind := range []string{"session", "subgroup"} {
		t.Run(kind, func(t *testing.T) {
			s, a, ta, _, _ := groupSnapshotFixture(t)
			if kind == "session" {
				require.NoError(t, s.Save([]*Instance{{ID: "later", Title: "later", GroupPath: "test", Tool: "shell", ProjectPath: t.TempDir()}}))
			} else {
				require.NoError(t, s.db.SaveGroups([]*statedb.GroupRow{{Path: "test/later", Name: "later"}}))
			}
			require.NoError(t, ta.RenameGroup("test", "renamed"))
			require.ErrorContains(t, s.SaveWithGroups(a, ta), "conflict")
			require.NotNil(t, storedGroup(t, s, "test"))
			require.Nil(t, storedGroup(t, s, "renamed"))
			row, err := s.db.LoadInstanceByID("shared")
			require.NoError(t, err)
			require.Equal(t, "test", row.GroupPath)
		})
	}
}

func TestGroupStorageSnapshotsRenameRollsBackWithSessions(t *testing.T) {
	s, a, ta, _, _ := groupSnapshotFixture(t)
	require.NoError(t, ta.RenameGroup("test", "renamed"))
	_, err := s.db.DB().Exec("CREATE TRIGGER reject_rename BEFORE INSERT ON groups BEGIN SELECT RAISE(ABORT, 'rename rejected'); END")
	require.NoError(t, err)
	require.ErrorContains(t, s.SaveWithGroups(a, ta.ShallowCopyForSave()), "rename rejected")
	require.NotNil(t, storedGroup(t, s, "test"))
	require.Nil(t, storedGroup(t, s, "renamed"))
	row, err := s.db.LoadInstanceByID("shared")
	require.NoError(t, err)
	require.Equal(t, "test", row.GroupPath)
	_, err = s.db.DB().Exec("DROP TRIGGER reject_rename")
	require.NoError(t, err)
	require.NoError(t, s.SaveWithGroups(a, ta.ShallowCopyForSave()))
	require.Nil(t, storedGroup(t, s, "test"))
	require.NotNil(t, storedGroup(t, s, "renamed"))
}

func TestGroupStorageSnapshotsSameFieldConflicts(t *testing.T) {
	for _, field := range []string{"Name", "Expanded", "Order", "DefaultPath", "MaxConcurrent"} {
		t.Run(field, func(t *testing.T) {
			s, _, ta, _, tb := groupSnapshotFixture(t)
			set := func(group *Group, version int) {
				switch field {
				case "Name":
					group.Name = fmt.Sprintf("name%d", version)
				case "Expanded":
					group.Expanded = version == 2
				case "Order":
					group.Order = version
				case "DefaultPath":
					group.DefaultPath = fmt.Sprintf("/path%d", version)
				case "MaxConcurrent":
					group.MaxConcurrent = version
				}
			}
			set(tb.Groups["test"], 1)
			require.NoError(t, s.SaveGroupsOnly(tb.ShallowCopyForSave()))
			// Expanded has only two values: a stale unchanged value is not an edit,
			// and therefore preserves the other writer's change.
			set(ta.Groups["test"], 2)
			if field == "Expanded" {
				require.NoError(t, s.SaveGroupsOnly(ta.ShallowCopyForSave()))
				require.False(t, storedGroup(t, s, "test").Expanded)
				return
			}
			require.ErrorContains(t, s.SaveGroupsOnly(ta.ShallowCopyForSave()), field+" conflict")
			set(ta.Groups["test"], 1)
			require.NoError(t, s.SaveGroupsOnly(ta.ShallowCopyForSave()), "identical concurrent edit converges")
			set(ta.Groups["test"], 3)
			require.NoError(t, s.SaveGroupsOnly(ta.ShallowCopyForSave()), "successful convergence advances the live copy owner")
		})
	}
}

func TestGroupStorageSnapshotsOldReceiptCannotUndoNewOwner(t *testing.T) {
	s, _, tree, _, _ := groupSnapshotFixture(t)
	older := tree.ShallowCopyForSave()
	tree.Groups["test"].MaxConcurrent = 2
	require.NoError(t, s.SaveGroupsOnly(tree.ShallowCopyForSave()))
	require.NoError(t, s.SaveGroupsOnly(older), "unchanged old copy preserves current fields")
	tree.Groups["test"].MaxConcurrent = 3
	require.NoError(t, s.SaveGroupsOnly(tree.ShallowCopyForSave()), "old receipt must not roll back the live owner's baseline")
	require.Equal(t, 3, storedGroup(t, s, "test").MaxConcurrent)
}

func TestGroupStorageSnapshotsDeletionRejectsUnknownAdditions(t *testing.T) {
	for _, kind := range []string{"session", "subgroup"} {
		t.Run(kind, func(t *testing.T) {
			s, _, tree, _, _ := groupSnapshotFixture(t)
			tree.DeleteGroup("test")
			if kind == "session" {
				require.NoError(t, s.Save([]*Instance{{ID: "later", Title: "later", GroupPath: "test", Tool: "shell", ProjectPath: t.TempDir()}}))
			} else {
				require.NoError(t, s.db.SaveGroups([]*statedb.GroupRow{{Path: "test/later", Name: "later"}}))
			}
			require.ErrorContains(t, s.SaveWithGroups(tree.GetAllInstances(), tree.ShallowCopyForSave()), "conflict")
			require.NotNil(t, storedGroup(t, s, "test"))
			row, err := s.db.LoadInstanceByID("shared")
			require.NoError(t, err)
			require.Equal(t, "test", row.GroupPath)
		})
	}
}

func TestGroupStorageSnapshotsDeletionFailureRetainsIntent(t *testing.T) {
	s, _, tree, _, _ := groupSnapshotFixture(t)
	tree.DeleteGroup("test")
	_, err := s.db.DB().Exec("CREATE TRIGGER reject_delete BEFORE DELETE ON groups BEGIN SELECT RAISE(ABORT, 'delete rejected'); END")
	require.NoError(t, err)
	require.ErrorContains(t, s.SaveWithGroups(tree.GetAllInstances(), tree.ShallowCopyForSave()), "delete rejected")
	require.NotNil(t, storedGroup(t, s, "test"))
	_, err = s.db.DB().Exec("DROP TRIGGER reject_delete")
	require.NoError(t, err)
	require.NoError(t, s.SaveWithGroups(tree.GetAllInstances(), tree.ShallowCopyForSave()))
	require.Nil(t, storedGroup(t, s, "test"))
}

func TestGroupStorageSnapshotsDeletionBoundToDatabase(t *testing.T) {
	source, _, tree, _, _ := groupSnapshotFixture(t)
	other := newTestStorage(t)
	require.NoError(t, other.db.SaveGroups([]*statedb.GroupRow{storedGroup(t, source, "test")}))
	tree.DeleteGroup("test")
	require.ErrorContains(t, other.SaveGroupsOnly(tree.ShallowCopyForSave()), "different database")
	require.NotNil(t, storedGroup(t, other, "test"))
}

func TestGroupStorageSnapshotsReadErrorsFailClosed(t *testing.T) {
	s, a, tree, _, _ := groupSnapshotFixture(t)
	_, err := s.db.DB().Exec("UPDATE groups SET sort_order='unreadable' WHERE path='test'")
	require.NoError(t, err)
	a[0].Title = "must not save"
	require.ErrorContains(t, s.SaveWithGroups(a, tree), "read current groups")
	row, err := s.db.LoadInstanceByID("shared")
	require.NoError(t, err)
	require.NotEqual(t, "must not save", row.Title)
}

func TestGroupStorageSnapshotsKeepsLiveEditPending(t *testing.T) {
	var armed atomic.Bool
	var live *Group
	function := fmt.Sprintf("group_pending_edit_%d", time.Now().UnixNano())
	require.NoError(t, sqlite.RegisterScalarFunction(function, 0, func(_ *sqlite.FunctionContext, _ []driver.Value) (driver.Value, error) {
		if armed.CompareAndSwap(true, false) {
			live.MaxConcurrent = 3
		}
		return int64(1), nil
	}))
	s, _, tree, _, _ := groupSnapshotFixture(t)
	live = tree.Groups["test"]
	_, err := s.db.DB().Exec("CREATE TRIGGER pending_group_edit AFTER UPDATE ON groups BEGIN SELECT " + function + "(); END")
	require.NoError(t, err)
	live.MaxConcurrent = 2
	copy := tree.ShallowCopyForSave()
	armed.Store(true)
	require.NoError(t, s.SaveGroupsOnly(copy))
	require.Equal(t, 2, storedGroup(t, s, "test").MaxConcurrent)
	require.Equal(t, 3, live.MaxConcurrent)
	require.NoError(t, s.SaveGroupsOnly(tree.ShallowCopyForSave()))
	require.Equal(t, 3, storedGroup(t, s, "test").MaxConcurrent)
}

func TestGroupStorageSnapshotsCompletedPayloadCannotReplayDeletion(t *testing.T) {
	s, _, tree, _, _ := groupSnapshotFixture(t)
	original := storedGroup(t, s, "test")
	tree.DeleteGroup("test")
	copy := tree.ShallowCopyForSave()
	require.NoError(t, s.SaveWithGroups(tree.GetAllInstances(), copy))
	require.Nil(t, storedGroup(t, s, "test"))
	require.NoError(t, s.db.SaveGroups([]*statedb.GroupRow{original}))
	require.NoError(t, s.SaveWithGroups(tree.GetAllInstances(), copy))
	require.Equal(t, original, storedGroup(t, s, "test"), "the same completed payload cannot delete a later recreation")
}

func TestGroupStorageSnapshotsForeignEmptyDBCannotAcknowledgeDeletion(t *testing.T) {
	source, _, tree, _, _ := groupSnapshotFixture(t)
	other := newTestStorage(t)
	tree.DeleteGroup("test")
	payload := tree.ShallowCopyForSave()
	require.Error(t, other.SaveGroupsOnly(payload), "a foreign empty DB cannot consume the source deletion")
	require.NotNil(t, storedGroup(t, source, "test"))
	require.NoError(t, source.SaveWithGroups(tree.GetAllInstances(), payload))
	require.Nil(t, storedGroup(t, source, "test"))
}

func TestGroupStorageSnapshotsDerivedTreeCannotRecreateMovedGroup(t *testing.T) {
	s, a, _, b, tb := groupSnapshotFixture(t)
	derived := NewGroupTree(a)
	require.NoError(t, tb.RenameGroup("test", "renamed"))
	require.NoError(t, s.SaveWithGroups(b, tb))
	for i := 0; i < 2; i++ {
		require.NoError(t, s.SaveWithGroups(a, derived.ShallowCopyForSave()))
		require.Nil(t, storedGroup(t, s, "test"), "an implicit stale path without current members cannot become an empty group")
		row, err := s.db.LoadInstanceByID("shared")
		require.NoError(t, err)
		require.Equal(t, "renamed", row.GroupPath)
	}
}

func TestGroupStorageSnapshotsLoadUsesOneReadSnapshot(t *testing.T) {
	var armed atomic.Bool
	var other *statedb.StateDB
	function := fmt.Sprintf("group_load_interleave_%d", time.Now().UnixNano())
	writes := make(chan error, 1)
	require.NoError(t, sqlite.RegisterScalarFunction(function, 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		if armed.CompareAndSwap(true, false) {
			_, err := other.DB().Exec("BEGIN; UPDATE stored_instances SET group_path='renamed' WHERE id='shared'; UPDATE groups SET path='renamed', name='renamed' WHERE path='test'; COMMIT")
			writes <- err
		}
		return args[0], nil
	}))
	s, _, _, _, _ := groupSnapshotFixture(t)
	var err error
	other, err = statedb.Open(s.dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { other.Close() })
	columns, err := s.db.DB().Query("PRAGMA table_info(instances)")
	require.NoError(t, err)
	var selected []string
	for columns.Next() {
		var cid, notnull, pk int
		var name, kind string
		var defaultValue any
		require.NoError(t, columns.Scan(&cid, &name, &kind, &notnull, &defaultValue, &pk))
		if name == "title" {
			selected = append(selected, function+"(title) AS title")
		} else {
			selected = append(selected, `"`+strings.ReplaceAll(name, `"`, `""`)+`"`)
		}
	}
	require.NoError(t, columns.Err())
	require.NoError(t, columns.Close())
	_, err = s.db.DB().Exec("ALTER TABLE instances RENAME TO stored_instances")
	require.NoError(t, err)
	_, err = s.db.DB().Exec("CREATE VIEW instances AS SELECT " + strings.Join(selected, ",") + " FROM stored_instances")
	require.NoError(t, err)
	armed.Store(true)
	instances, groups, err := s.LoadWithGroups()
	require.NoError(t, err)
	select {
	case err := <-writes:
		require.NoError(t, err, "WAL permits the concurrent committed rename during the read snapshot")
	default:
		t.Fatal("read probe did not run")
	}
	require.Len(t, instances, 1)
	require.Len(t, groups, 1)
	require.Equal(t, instances[0].GroupPath, groups[0].Path, "instances and groups must describe the same database snapshot")
	require.ErrorContains(t, s.SaveWithGroups(instances, NewGroupTreeWithGroups(instances, groups)), "group deletion conflict")
	require.Nil(t, storedGroup(t, s, "test"))
	require.NotNil(t, storedGroup(t, s, "renamed"))
}
