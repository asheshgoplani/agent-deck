package main

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Issue #2099: `session start` / `session restart` must not report success
// when the tmux session was never created. spawnFailureOutput builds the
// error message and the --json payload from the verification error.

func TestIssue2099_SpawnFailureOutputCarriesRecord(t *testing.T) {
	inst := session.NewInstance("issue-2099", "/tmp")
	rec := &session.SpawnFailureRecord{
		InstanceID:  inst.ID,
		Tool:        "codex",
		Command:     "npx codex@0.144",
		Reason:      "spawn_died_fast",
		DyingOutput: "npm ERR! 404",
		ElapsedMs:   310,
		Timestamp:   1700000000,
	}
	err := &session.SpawnFailedError{TmuxName: "agentdeck_issue-2099", Record: rec}

	msg, data := spawnFailureOutput("start", inst, err)

	assert.Contains(t, msg, "failed to start session")
	assert.Contains(t, msg, "spawn_died_fast")
	assert.Contains(t, msg, "npm ERR! 404")

	assert.Equal(t, inst.ID, data["id"])
	assert.Equal(t, inst.Title, data["title"])
	assert.Equal(t, "agentdeck_issue-2099", data["tmux"])
	sf, ok := data["spawn_failure"].(map[string]interface{})
	require.True(t, ok, "spawn_failure must be a structured object in --json")
	assert.Equal(t, "spawn_died_fast", sf["reason"])
	assert.Equal(t, "npx codex@0.144", sf["command"])
	assert.Equal(t, "npm ERR! 404", sf["dying_output"])
	assert.Equal(t, int64(310), sf["elapsed_ms"])
	assert.Equal(t, int64(1700000000), sf["ts"])
}

func TestIssue2099_SpawnFailureOutputWithoutRecord(t *testing.T) {
	inst := session.NewInstance("issue-2099-norec", "/tmp")
	err := &session.SpawnFailedError{TmuxName: "agentdeck_issue-2099-norec"}

	msg, data := spawnFailureOutput("restart", inst, err)

	assert.Contains(t, msg, "failed to restart session")
	assert.Contains(t, msg, "agentdeck_issue-2099-norec")
	assert.Equal(t, "agentdeck_issue-2099-norec", data["tmux"])
	_, hasRecord := data["spawn_failure"]
	assert.False(t, hasRecord, "no record → no spawn_failure key")
	assert.Equal(t, "tmux_session_missing", data["reason"])
}

// spawnFailureJSON is shared with `session show`: one shape for tooling.
func TestIssue2099_SpawnFailureJSONShape(t *testing.T) {
	rec := &session.SpawnFailureRecord{Reason: "tmux_start_failed", DyingOutput: "boom", Timestamp: 5}
	got := spawnFailureJSON(rec)
	assert.Equal(t, map[string]interface{}{
		"reason":       "tmux_start_failed",
		"command":      "",
		"dying_output": "boom",
		"elapsed_ms":   int64(0),
		"ts":           int64(5),
	}, got)
}
