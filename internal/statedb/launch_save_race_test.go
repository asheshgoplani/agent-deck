package statedb

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// launchRaceFixture inserts the row `agent-deck launch` writes BEFORE it spawns
// anything (save 1): configuration complete, nothing the spawn produces yet.
// It returns that row as the launch's baseline for its post-start save.
func launchRaceFixture(t *testing.T) (*StateDB, *InstanceRow) {
	t.Helper()
	db := newTestDB(t)
	require.NoError(t, db.SaveInstance(&InstanceRow{
		ID: "sess", Title: "worker", ProjectPath: "/repo/wt/sess", GroupPath: "fleet",
		Command: "claude", Tool: "claude", Status: "starting", ParentSessionID: "parent",
		WorktreePath: "/repo/wt/sess", WorktreeRepo: "/repo", WorktreeBranch: "sess",
		CreatedAt: time.Unix(1000, 0), LastAccessed: time.Unix(1000, 0),
		ToolData: json.RawMessage(`{"loaded_mcp_names":["neo4j"]}`),
	}))
	rows, err := db.LoadInstances()
	require.NoError(t, err)
	require.Len(t, rows, 1)
	return db, rows[0]
}

// postStartSave is the launch's third save: the spawn receipt (tmux session
// name), its own pane-derived status, and its own detection stamp.
func postStartSave(base *InstanceRow) *InstanceRow {
	desired := CloneInstanceRow(base)
	desired.TmuxSession = "agentdeck_worker_c5322ee1"
	desired.Status = "running"
	desired.LastAccessed = time.Unix(1999, 0)
	desired.ToolData = json.RawMessage(`{"loaded_mcp_names":["neo4j"],"claude_session_id":"conv-1","claude_detected_at":1999}`)
	return desired
}

func TestLaunchPostStartSaveMergesConcurrentDetection(t *testing.T) {
	db, base := launchRaceFixture(t)

	// A concurrent UpdateHookStatus caller (TUI tick, daemon, another CLI)
	// binds the SAME conversation while the launch is inside PostStartSync,
	// stamping its own time.Now(), and the monitor samples the pane.
	require.NoError(t, db.WriteClaudeSessionBinding("sess", "conv-1", time.Unix(2000, 0)))
	require.NoError(t, db.WriteStatus("sess", "waiting", "claude"))

	committed, err := db.MergeInstanceSnapshots(
		[]InstanceSnapshot{{Original: base, Stored: base, Desired: postStartSave(base)}}, nil)
	require.NoError(t, err, "post-start save must merge, not abort")
	require.Len(t, committed, 1)
	got := committed[0]

	// The spawn receipt is launch-owned and must never be lost.
	require.Equal(t, "agentdeck_worker_c5322ee1", got.TmuxSession)
	// Liveness: the committed observation wins, it is the fresher one.
	require.Equal(t, "waiting", got.Status)
	// Neither detector write touched last_accessed, so the launch's own later
	// activity time is the fresher observation here.
	require.Equal(t, int64(1999), got.LastAccessed.Unix(), "later LastAccessed wins")
	require.JSONEq(t,
		`{"loaded_mcp_names":["neo4j"],"claude_session_id":"conv-1","claude_detected_at":2000}`,
		string(got.ToolData))

	// No field loss on the launch-owned configuration, and no duplicate rows.
	rows, err := db.LoadInstances()
	require.NoError(t, err)
	require.Len(t, rows, 1)
	persisted := rows[0]
	require.Equal(t, "parent", persisted.ParentSessionID)
	require.Equal(t, "/repo/wt/sess", persisted.WorktreePath)
	require.Equal(t, "sess", persisted.WorktreeBranch)
	require.Equal(t, "fleet", persisted.GroupPath)
	require.Equal(t, got.TmuxSession, persisted.TmuxSession)
	require.Equal(t, got.Status, persisted.Status)
	require.JSONEq(t, string(got.ToolData), string(persisted.ToolData))
}

func TestLaunchPostStartSaveMergesStatusVariant(t *testing.T) {
	db, base := launchRaceFixture(t)
	// The monitor samples the pane before the launch's own stamp lands, with
	// no conversation binding at all: the `stale concurrent Status conflict`
	// sibling from the issue.
	require.NoError(t, db.WriteStatus("sess", "idle", "claude"))
	desired := CloneInstanceRow(base)
	desired.TmuxSession = "agentdeck_worker_c5322ee1"
	desired.Status = "running"
	committed, err := db.MergeInstanceSnapshots(
		[]InstanceSnapshot{{Original: base, Stored: base, Desired: desired}}, nil)
	require.NoError(t, err)
	require.Equal(t, "idle", committed[0].Status)
	require.Equal(t, "agentdeck_worker_c5322ee1", committed[0].TmuxSession)
}

func TestLivenessMergeLastAccessedKeepsLater(t *testing.T) {
	for _, tc := range []struct {
		name            string
		committed, mine int64
		want            int64
	}{
		{"committed is later", 3000, 1999, 3000},
		{"mine is later", 1500, 1999, 1999},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, base := launchRaceFixture(t)
			_, err := db.DB().Exec("UPDATE instances SET last_accessed = ? WHERE id = 'sess'", tc.committed)
			require.NoError(t, err)
			desired := CloneInstanceRow(base)
			desired.LastAccessed = time.Unix(tc.mine, 0)
			committed, err := db.MergeInstanceSnapshots(
				[]InstanceSnapshot{{Original: base, Stored: base, Desired: desired}}, nil)
			require.NoError(t, err)
			require.Equal(t, tc.want, committed[0].LastAccessed.Unix())
		})
	}
}

// Everything that is intent rather than observation must still conflict.
func TestLivenessMergeStillConflictsOnIntent(t *testing.T) {
	for _, tc := range []struct {
		name      string
		committed func(t *testing.T, db *StateDB)
		edit      func(desired *InstanceRow)
		wantErr   string
	}{
		{
			"stopped is intent, not observation",
			func(t *testing.T, db *StateDB) { require.NoError(t, db.WriteStatus("sess", "stopped", "claude")) },
			func(d *InstanceRow) { d.Status = "running" },
			"stale concurrent Status conflict",
		},
		{
			"queued is intent, not observation",
			func(t *testing.T, db *StateDB) { require.NoError(t, db.WriteStatus("sess", "waiting", "claude")) },
			func(d *InstanceRow) { d.Status = "queued" },
			"stale concurrent Status conflict",
		},
		{
			"a diverging session id is a real lost update",
			func(t *testing.T, db *StateDB) {
				require.NoError(t, db.WriteClaudeSessionBinding("sess", "conv-other", time.Unix(2000, 0)))
			},
			func(d *InstanceRow) {
				d.ToolData = json.RawMessage(`{"loaded_mcp_names":["neo4j"],"claude_session_id":"conv-1","claude_detected_at":1999}`)
			},
			"stale concurrent tool_data.claude_session_id conflict",
		},
		{
			"an explicit clear racing a stamp is intent",
			func(t *testing.T, db *StateDB) {
				require.NoError(t, db.WriteClaudeSessionBinding("sess", "conv-1", time.Unix(2000, 0)))
			},
			func(d *InstanceRow) {
				d.ToolData = json.RawMessage(`{"loaded_mcp_names":["neo4j"],"claude_session_id":"conv-1","claude_detected_at":0}`)
			},
			"stale concurrent tool_data.claude_detected_at conflict",
		},
		{
			"configuration keys keep the hard conflict",
			func(t *testing.T, db *StateDB) {
				_, err := db.DB().Exec(`UPDATE instances SET tool_data = json_set(tool_data, '$.notes', 'theirs') WHERE id = 'sess'`)
				require.NoError(t, err)
			},
			func(d *InstanceRow) {
				d.ToolData = json.RawMessage(`{"loaded_mcp_names":["neo4j"],"notes":"mine"}`)
			},
			"stale concurrent tool_data.notes conflict",
		},
		{
			"scalar configuration keeps the hard conflict",
			func(t *testing.T, db *StateDB) {
				_, err := db.DB().Exec(`UPDATE instances SET group_path = 'theirs' WHERE id = 'sess'`)
				require.NoError(t, err)
			},
			func(d *InstanceRow) { d.GroupPath = "mine" },
			"stale concurrent GroupPath conflict",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, base := launchRaceFixture(t)
			tc.committed(t, db)
			desired := CloneInstanceRow(base)
			desired.TmuxSession = "agentdeck_worker_c5322ee1"
			tc.edit(desired)
			_, err := db.MergeInstanceSnapshots(
				[]InstanceSnapshot{{Original: base, Stored: base, Desired: desired}}, nil)
			require.ErrorContains(t, err, tc.wantErr)
			rows, err := db.LoadInstances()
			require.NoError(t, err)
			require.Len(t, rows, 1)
			require.Empty(t, rows[0].TmuxSession, "a refused save must not partially commit")
		})
	}
}

func TestLivenessMergeStampConflictsWhenPairedIDDiverges(t *testing.T) {
	db := newTestDB(t)
	// The caller already carries a bound conversation and only re-stamps it.
	require.NoError(t, db.SaveInstance(&InstanceRow{
		ID: "sess", Title: "worker", Tool: "claude", Status: "running", CreatedAt: time.Unix(1000, 0),
		ToolData: json.RawMessage(`{"claude_session_id":"conv-1","claude_detected_at":1500}`),
	}))
	rows, err := db.LoadInstances()
	require.NoError(t, err)
	base := rows[0]
	// Another writer rebound the row to a different conversation: the two
	// stamps are timing two different conversations, so committed-wins on
	// the stamp alone would hide a real lost update.
	require.NoError(t, db.WriteClaudeSessionBinding("sess", "conv-other", time.Unix(2000, 0)))
	desired := CloneInstanceRow(base)
	desired.ToolData = json.RawMessage(`{"claude_session_id":"conv-1","claude_detected_at":1999}`)
	_, err = db.MergeInstanceSnapshots(
		[]InstanceSnapshot{{Original: base, Stored: base, Desired: desired}}, nil)
	require.ErrorContains(t, err, "stale concurrent tool_data.claude_detected_at conflict")
}
