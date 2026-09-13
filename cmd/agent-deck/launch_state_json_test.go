package main

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/stretchr/testify/require"
)

// Issue #2209: the launch JSON must describe the row as committed after the
// post-start save merged with a concurrent detector, not the pre-save guess.
func TestAddLaunchStateJSONReportsMergedState(t *testing.T) {
	inst := &session.Instance{ID: "sess", Title: "worker", Tool: "claude", Status: session.StatusWaiting,
		ClaudeSessionID: "conv-1", ClaudeDetectedAt: time.Unix(2000, 0)}
	jsonData := map[string]interface{}{}
	addLaunchStateJSON(jsonData, inst, "agentdeck_worker_c5322ee1")
	require.Equal(t, "waiting", jsonData["status"])
	require.Equal(t, "agentdeck_worker_c5322ee1", jsonData["tmux_session"])
	require.Equal(t, "conv-1", jsonData["claude_session_id"])

	bare := map[string]interface{}{}
	addLaunchStateJSON(bare, &session.Instance{Status: session.StatusRunning}, "")
	require.Equal(t, "running", bare["status"])
	require.NotContains(t, bare, "tmux_session")
	require.NotContains(t, bare, "claude_session_id")
}
