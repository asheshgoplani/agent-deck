package session

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
	"github.com/stretchr/testify/require"
)

// Issue #2209: `agent-deck launch` inserts its row before the spawn (save 1),
// then persists the spawn receipt after PostStartSync (save 3). Anything else
// that detects the same Claude session in between (TUI tick, daemon, another
// CLI) commits its own status and detection stamp first. That save must merge
// with the committed liveness observation instead of aborting, so the launch
// never loses the tmux session name it alone knows about.
func TestInsertSessionAndVerifyMergesConcurrentDetection(t *testing.T) {
	s := newTestStorage(t)
	inst := &Instance{
		ID: "sess", Title: "worker", ProjectPath: t.TempDir(), GroupPath: "fleet",
		Tool: "claude", Command: "claude", Status: StatusStarting,
		ParentSessionID: "parent", LoadedMCPNames: []string{"neo4j"},
		WorktreeBranch: "sess", CreatedAt: time.Unix(1000, 0), LastAccessedAt: time.Unix(1000, 0),
	}
	require.NoError(t, s.InsertSessionAndVerify(inst, nil))

	// Concurrent detector: binds the same conversation with its own clock and
	// samples the pane, exactly what UpdateHookStatus / the monitor do.
	require.NoError(t, s.db.WriteClaudeSessionBinding("sess", "conv-1", time.Unix(2000, 0)))
	require.NoError(t, s.db.WriteStatus("sess", "waiting", "claude"))

	// The launch's in-memory instance after Start + PostStartSync.
	inst.tmuxSession = &tmux.Session{Name: "agentdeck_worker_c5322ee1"}
	inst.Status = StatusRunning
	inst.ClaudeSessionID = "conv-1"
	inst.ClaudeDetectedAt = time.Unix(1999, 0)
	require.NoError(t, s.InsertSessionAndVerify(inst, nil), "post-start save must merge, not abort")

	// The reporting instance reflects the merged state: committed wins for
	// liveness, the launch keeps everything it owns.
	require.Equal(t, StatusWaiting, inst.Status)
	require.Equal(t, int64(2000), inst.ClaudeDetectedAt.Unix())
	require.Equal(t, "conv-1", inst.ClaudeSessionID)

	got, err := s.Load()
	require.NoError(t, err)
	require.Len(t, got, 1, "no duplicate rows")
	row := got[0]
	require.Equal(t, "agentdeck_worker_c5322ee1", row.GetTmuxSession().Name, "spawn receipt must never be lost")
	require.Equal(t, StatusWaiting, row.Status)
	require.Equal(t, "conv-1", row.ClaudeSessionID)
	require.Equal(t, int64(2000), row.ClaudeDetectedAt.Unix())
	require.Equal(t, "parent", row.ParentSessionID)
	require.Equal(t, []string{"neo4j"}, row.LoadedMCPNames)
	require.Equal(t, "sess", row.WorktreeBranch)
	require.Equal(t, "fleet", row.GroupPath)

	// A follow-up save from the same process must not re-raise the merged
	// values as stale intent.
	inst.Title = "worker-renamed"
	require.NoError(t, s.Save([]*Instance{inst}))
}
