package statedb

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func supersessionRow(id, tool, account, command, nativeID string, archived bool) *InstanceRow {
	data := map[string]string{}
	if tool == "claude" {
		data["claude_session_id"] = nativeID
	} else {
		data["codex_session_id"] = nativeID
	}
	encoded, _ := json.Marshal(data)
	row := &InstanceRow{ID: id, Title: id, Tool: tool, Account: account, ProjectPath: "/project", Command: command, GroupPath: "group", Order: 7, CreatedAt: time.Unix(100, 0), ToolData: encoded}
	if archived {
		row.ArchivedAt = time.Unix(200, 0).UTC()
	}
	return row
}

func supersessionIdentity(row *InstanceRow) NativeHarnessSwitchIdentity {
	claudeID, codexID, _ := nativeSessionIDs(row.ToolData)
	return NativeHarnessSwitchIdentity{ID: row.ID, Tool: row.Tool, Account: row.Account, ProjectPath: row.ProjectPath, Command: row.Command, ClaudeSessionID: claudeID, CodexSessionID: codexID, ParentSessionID: row.ParentSessionID}
}

func rowByID(t *testing.T, db *StateDB, id string) *InstanceRow {
	t.Helper()
	rows, err := db.LoadInstances()
	require.NoError(t, err)
	for _, row := range rows {
		if row.ID == id {
			return row
		}
	}
	t.Fatalf("row %q not found", id)
	return nil
}

func TestCommitCrossHarnessSupersession_CASRetryAndChainPreserveLineage(t *testing.T) {
	db := newTestDB(t)
	a := supersessionRow("A", "claude", "personal", "claude", "claude-A", false)
	b := supersessionRow("B", "codex", "work", "codex", "codex-B", true)
	require.NoError(t, db.SaveInstance(a))
	require.NoError(t, db.SaveInstance(b))

	// The source identity is intentionally a stale display snapshot. The CAS
	// may mutate only archive/lineage fields; current title/group/order/native
	// data must survive rather than being overwritten from this pointer.
	source, target := supersessionIdentity(a), supersessionIdentity(b)
	require.NoError(t, db.SaveInstance(&InstanceRow{ID: "A", Title: "renamed while target started", Tool: a.Tool, Account: a.Account, ProjectPath: a.ProjectPath, Command: a.Command, GroupPath: "moved", Order: 99, CreatedAt: a.CreatedAt, ToolData: a.ToolData}))
	archivedAt := time.Unix(300, 0).UTC()
	committedA, committedB, err := db.CommitCrossHarnessSupersession(source, target, archivedAt)
	require.NoError(t, err)
	require.Equal(t, "renamed while target started", committedA.Title)
	require.Equal(t, "moved", committedA.GroupPath)
	require.Equal(t, 99, committedA.Order)
	require.Equal(t, committedA.Title, committedB.Title)
	require.Equal(t, committedA.GroupPath, committedB.GroupPath)
	require.Equal(t, committedA.Order, committedB.Order)
	require.Equal(t, committedA.CreatedAt, committedB.CreatedAt)
	require.Equal(t, "B", crossHarnessLineage(committedA.ToolData, "cross_harness_superseded_by"))
	require.Equal(t, "A", crossHarnessLineage(committedB.ToolData, "cross_harness_supersedes"))
	require.False(t, committedA.ArchivedAt.IsZero())
	require.True(t, committedB.ArchivedAt.IsZero())

	// A retry after a journal acknowledgement failure is idempotent: it does
	// not erase either side's lineage or create a second active row.
	retryA, retryB, err := db.CommitCrossHarnessSupersession(source, target, time.Unix(400, 0).UTC())
	require.NoError(t, err)
	require.Equal(t, committedA.ArchivedAt, retryA.ArchivedAt)
	require.Equal(t, "B", crossHarnessLineage(retryA.ToolData, "cross_harness_superseded_by"))
	require.Equal(t, "A", crossHarnessLineage(retryB.ToolData, "cross_harness_supersedes"))

	// B -> C retains the full A -> B -> C chain while leaving C as the sole
	// active row. B's existing predecessor lineage is not overwritten.
	c := supersessionRow("C", "claude", "personal", "claude", "claude-C", true)
	require.NoError(t, db.SaveInstance(c))
	_, committedC, err := db.CommitCrossHarnessSupersession(supersessionIdentity(rowByID(t, db, "B")), supersessionIdentity(c), time.Unix(500, 0).UTC())
	require.NoError(t, err)
	storedA, storedB := rowByID(t, db, "A"), rowByID(t, db, "B")
	require.Equal(t, "B", crossHarnessLineage(storedA.ToolData, "cross_harness_superseded_by"))
	require.Equal(t, "A", crossHarnessLineage(storedB.ToolData, "cross_harness_supersedes"))
	require.Equal(t, "C", crossHarnessLineage(storedB.ToolData, "cross_harness_superseded_by"))
	require.Equal(t, "B", crossHarnessLineage(committedC.ToolData, "cross_harness_supersedes"))
	active := 0
	for _, row := range []*InstanceRow{storedA, storedB, committedC} {
		if row.ArchivedAt.IsZero() {
			active++
		}
	}
	require.Equal(t, 1, active)
}

func TestCommitCrossHarnessSupersession_RetriesBusyCAS(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	db := openSingleConnDB(t, dbPath)
	require.NoError(t, db.Migrate())
	a := supersessionRow("A", "claude", "personal", "claude", "claude-A", false)
	b := supersessionRow("B", "codex", "work", "codex", "codex-B", true)
	require.NoError(t, db.SaveInstance(a))
	require.NoError(t, db.SaveInstance(b))
	released := briefWriteLock(t, dbPath, 20*time.Millisecond)
	_, _, err := db.CommitCrossHarnessSupersession(supersessionIdentity(a), supersessionIdentity(b), time.Now().UTC())
	<-released
	require.NoError(t, err)
	require.False(t, rowByID(t, db, "A").ArchivedAt.IsZero())
	require.True(t, rowByID(t, db, "B").ArchivedAt.IsZero())
}

func TestCommitCrossHarnessSupersession_RejectsCurrentDependentChildButAllowsOrdinaryWorkerParent(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(*InstanceRow)
		wantErr   string
	}{
		{
			name: "dependent child added", configure: func(source *InstanceRow) {
				// A target can retain source's parent, but a source cannot be archived
				// while a new child still routes to it.
			}, wantErr: "dependent child child was added",
		},
		{
			name: "managed conductor", configure: func(source *InstanceRow) {
				source.IsConductor = true
			}, wantErr: "source is a managed conductor",
		},
		{
			name: "ordinary worker parent", configure: func(source *InstanceRow) {
				source.ParentSessionID = "ordinary-parent"
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newTestDB(t)
			source := supersessionRow("source", "claude", "personal", "claude", "claude-source", false)
			target := supersessionRow("target", "codex", "work", "codex", "codex-target", true)
			tc.configure(source)
			require.NoError(t, db.SaveInstance(source))
			require.NoError(t, db.SaveInstance(target))
			if tc.name == "dependent child added" {
				require.NoError(t, db.SaveInstance(&InstanceRow{ID: "child", Title: "child", Tool: "claude", ProjectPath: "/project", Command: "claude", ParentSessionID: source.ID, ToolData: json.RawMessage(`{}`)}))
			}
			_, _, err := db.CommitCrossHarnessSupersession(supersessionIdentity(source), supersessionIdentity(target), time.Now().UTC())
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				require.True(t, rowByID(t, db, source.ID).ArchivedAt.IsZero())
				require.False(t, rowByID(t, db, target.ID).ArchivedAt.IsZero())
			} else {
				require.NoError(t, err)
				require.False(t, rowByID(t, db, source.ID).ArchivedAt.IsZero())
				storedTarget := rowByID(t, db, target.ID)
				require.True(t, storedTarget.ArchivedAt.IsZero())
				if tc.name == "ordinary worker parent" {
					require.Equal(t, source.ParentSessionID, storedTarget.ParentSessionID)
				}
			}
		})
	}
}

func TestCommitCrossHarnessSupersession_RejectsSourceAndTargetRoutingDrift(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(source, target *InstanceRow)
		wantErr string
	}{
		{
			name: "source parent changed after validation",
			mutate: func(source, _ *InstanceRow) {
				source.ParentSessionID = "new-parent"
			},
			wantErr: "source routing conflict",
		},
		{
			name: "target parent changed after validation",
			mutate: func(_, target *InstanceRow) {
				target.ParentSessionID = "other-parent"
			},
			wantErr: "target routing conflict",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newTestDB(t)
			source := supersessionRow("source", "claude", "personal", "claude", "claude-source", false)
			target := supersessionRow("target", "codex", "work", "codex", "codex-target", true)
			source.ParentSessionID, target.ParentSessionID = "original-parent", "original-parent"
			require.NoError(t, db.SaveInstance(source))
			require.NoError(t, db.SaveInstance(target))
			sourceIdentity, targetIdentity := supersessionIdentity(source), supersessionIdentity(target)

			tc.mutate(source, target)
			require.NoError(t, db.SaveInstance(source))
			require.NoError(t, db.SaveInstance(target))
			wantSource, wantTarget := rowByID(t, db, source.ID), rowByID(t, db, target.ID)

			_, _, err := db.CommitCrossHarnessSupersession(sourceIdentity, targetIdentity, time.Now().UTC())
			require.ErrorContains(t, err, tc.wantErr)
			// A routing conflict cannot publish a destination with stale routing or
			// archive its source; both current rows remain byte-for-byte intact.
			require.Equal(t, wantSource, rowByID(t, db, source.ID))
			require.Equal(t, wantTarget, rowByID(t, db, target.ID))
		})
	}
}

func TestCommitCrossHarnessSupersession_WrongIdentityLeavesSourceVisibleAndTargetHidden(t *testing.T) {
	db := newTestDB(t)
	a := supersessionRow("A", "claude", "personal", "claude", "claude-A", false)
	b := supersessionRow("B", "codex", "work", "codex", "codex-B", true)
	require.NoError(t, db.SaveInstance(a))
	require.NoError(t, db.SaveInstance(b))
	wrong := supersessionIdentity(a)
	wrong.Account = "other"
	_, _, err := db.CommitCrossHarnessSupersession(wrong, supersessionIdentity(b), time.Now().UTC())
	require.ErrorContains(t, err, "source identity conflict")
	storedA, storedB := rowByID(t, db, "A"), rowByID(t, db, "B")
	require.True(t, storedA.ArchivedAt.IsZero())
	require.False(t, storedB.ArchivedAt.IsZero())
	require.Empty(t, crossHarnessLineage(storedA.ToolData, "cross_harness_superseded_by"))
	require.Empty(t, crossHarnessLineage(storedB.ToolData, "cross_harness_supersedes"))
}
