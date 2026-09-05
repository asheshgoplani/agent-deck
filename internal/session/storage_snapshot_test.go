package session

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func snapshotFixture(t *testing.T) (*Storage, []*Instance, []*Instance) {
	t.Helper()
	s := newTestStorage(t)
	seed := &Instance{ID: "shared", Title: "original", ProjectPath: t.TempDir(), GroupPath: "test", Tool: "shell", Status: StatusIdle, Account: "personal", CreatedAt: time.Now()}
	require.NoError(t, s.Save([]*Instance{seed}))
	a, err := s.Load()
	require.NoError(t, err)
	b, err := s.Load()
	require.NoError(t, err)
	return s, a, b
}

func TestStorageSnapshotDisjointFields(t *testing.T) {
	for _, tc := range []struct {
		name  string
		edit  func(*Instance)
		check func(*testing.T, *Instance)
	}{
		{"account", func(i *Instance) { i.Account = "seminno" }, func(t *testing.T, i *Instance) { require.Equal(t, "seminno", i.Account) }},
		{"parent", func(i *Instance) { i.ParentSessionID = "parent" }, func(t *testing.T, i *Instance) { require.Equal(t, "parent", i.ParentSessionID) }},
		{"worktree", func(i *Instance) { i.WorktreeBranch = "feature" }, func(t *testing.T, i *Instance) { require.Equal(t, "feature", i.WorktreeBranch) }},
		{"notes", func(i *Instance) { i.Notes = "remote notes" }, func(t *testing.T, i *Instance) { require.Equal(t, "remote notes", i.Notes) }},
		{"color", func(i *Instance) { i.Color = "203" }, func(t *testing.T, i *Instance) { require.Equal(t, "203", i.Color) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, a, b := snapshotFixture(t)
			tc.edit(b[0])
			require.NoError(t, s.Save(b))
			a[0].Title = "renamed"
			require.NoError(t, s.Save(a))
			got, err := s.Load()
			require.NoError(t, err)
			require.Equal(t, "renamed", got[0].Title)
			tc.check(t, got[0])
			// The caller still has its original account/notes. An unrelated second
			// edit must not turn those stale values into new overwrite intent.
			a[0].Title = "renamed again"
			require.NoError(t, s.Save(a))
			got, err = s.Load()
			require.NoError(t, err)
			tc.check(t, got[0])
		})
	}
}

func TestStorageSnapshotRepeatedSaves(t *testing.T) {
	s, a, _ := snapshotFixture(t)
	a[0].Title = "first"
	require.NoError(t, s.Save(a))
	a[0].Title = "second"
	require.NoError(t, s.Save(a), "a writer must not conflict with its own last save")
}

func TestStorageSnapshotIncidentalLoadDoesNotRebase(t *testing.T) {
	s, a, b := snapshotFixture(t)
	b[0].Title = "other writer"
	require.NoError(t, s.Save(b))
	_, err := s.Load()
	require.NoError(t, err)
	a[0].Title = "pending older edit"
	require.ErrorContains(t, s.Save(a), "conflict")
	got, err := s.Load()
	require.NoError(t, err)
	require.Equal(t, "other writer", got[0].Title)
}

func TestStorageSnapshotReadErrorFailsClosed(t *testing.T) {
	s, a, _ := snapshotFixture(t)
	// SQLite's dynamic typing lets us make SELECT scanning fail while the
	// subsequent unconditional old UPSERT would succeed and erase the evidence.
	_, err := s.db.DB().Exec("UPDATE instances SET sort_order = 'unreadable' WHERE id = 'shared'")
	require.NoError(t, err)
	a[0].Title = "must not commit"
	require.Error(t, s.Save(a))
	var title string
	require.NoError(t, s.db.DB().QueryRow("SELECT title FROM instances WHERE id = 'shared'").Scan(&title))
	require.Equal(t, "original", title)
}

func TestStorageSnapshotSameFieldConflictsAndRetry(t *testing.T) {
	s, a, b := snapshotFixture(t)
	b[0].Account = "seminno"
	require.NoError(t, s.Save(b))
	a[0].Account = "work"
	require.ErrorContains(t, s.Save(a), "conflict")
	// Failure must not advance a's baseline. Converging on the already committed
	// value is allowed, and later edits use that successful save as their origin.
	a[0].Account = "seminno"
	require.NoError(t, s.Save(a))
	a[0].Account = "personal"
	require.NoError(t, s.Save(a))
}

func TestStorageSnapshotDeletedRowsAreNotRecreated(t *testing.T) {
	s, a, _ := snapshotFixture(t)
	require.NoError(t, s.DeleteInstance(a[0].ID))
	a[0].Title = "stale"
	require.ErrorContains(t, s.Save(a), "deletion conflict")
	exists, err := s.InstanceExists(a[0].ID)
	require.NoError(t, err)
	require.False(t, exists)
}

func TestStorageSnapshotGroupFailureRollsBackAndKeepsBaseline(t *testing.T) {
	s, a, _ := snapshotFixture(t)
	_, err := s.db.DB().Exec("CREATE TRIGGER reject_group BEFORE INSERT ON groups BEGIN SELECT RAISE(ABORT, 'group rejected'); END")
	require.NoError(t, err)
	a[0].Title = "pending"
	require.ErrorContains(t, s.SaveWithGroups(a, NewGroupTree(a)), "group rejected")
	var title string
	require.NoError(t, s.db.DB().QueryRow("SELECT title FROM instances WHERE id='shared'").Scan(&title))
	require.Equal(t, "original", title)
	_, err = s.db.DB().Exec("DROP TRIGGER reject_group")
	require.NoError(t, err)
	a[0].Title = "retry"
	require.NoError(t, s.SaveWithGroups(a, NewGroupTree(a)))
}

func TestStorageSnapshotNoOpPreservesTargetedChanges(t *testing.T) {
	s, a, _ := snapshotFixture(t)
	_, err := s.db.DB().Exec("UPDATE instances SET title='new title', account='seminno', tool_data=json_set(tool_data, '$.unmodeled', 42) WHERE id='shared'")
	require.NoError(t, err)
	require.NoError(t, s.Save(a))
	require.NoError(t, s.Save(a))
	got, err := s.Load()
	require.NoError(t, err)
	require.Equal(t, "new title", got[0].Title)
	require.Equal(t, "seminno", got[0].Account)
	var extra int
	require.NoError(t, s.db.DB().QueryRow("SELECT json_extract(tool_data, '$.unmodeled') FROM instances WHERE id='shared'").Scan(&extra))
	require.Equal(t, 42, extra)
}

func TestStorageSnapshotWriteThroughClearConverges(t *testing.T) {
	s, a, _ := snapshotFixture(t)
	a[0].GenericSessionID = "bound"
	a[0].GenericDetectedAt = time.Now()
	require.NoError(t, s.Save(a))
	a[0].GenericSessionID = ""
	a[0].GenericDetectedAt = time.Time{}
	a[0].genericSessionIDCleared = true
	require.NoError(t, PersistGenericSessionBinding(s.db, a[0]))
	require.NoError(t, s.Save(a), "targeted json_remove and explicit-empty full save must converge")
	require.NoError(t, s.db.WriteGenericSessionBinding(a[0].ID, "new binding", "shell", "", "local", time.Now()))
	a[0].Title = "unrelated edit"
	require.NoError(t, s.Save(a), "a consumed clear marker must not clear a later binding")
	got, err := s.Load()
	require.NoError(t, err)
	require.Equal(t, "new binding", got[0].GenericSessionID)
}
