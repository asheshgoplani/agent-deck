package session

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Issue #1580: a failing custom command (e.g. a broken `codex.command` npx
// wrapper) whose initial process dies at/near spawn produced a bare "error"
// with no preview and nothing in the logs, because tmux tears the pane down on
// exit for non-remain-on-exit sessions and status detection only sees "tmux
// session missing".
//
// These tests prove the new observability surfaces: a durable spawn-failure
// sidecar, a preview fallback, and lifecycle-log traces.

// TestIssue1580_SpawnFailureRecordRoundTrip verifies write → read → clear of
// the durable sidecar (no tmux needed).
func TestIssue1580_SpawnFailureRecordRoundTrip(t *testing.T) {
	inst := NewInstance("test-1580-roundtrip", "/tmp")

	// No record yet.
	require.Nil(t, inst.SpawnFailure())

	rec := SpawnFailureRecord{
		InstanceID:  inst.ID,
		Tool:        "codex",
		Command:     "npx codex@0.144",
		Reason:      "spawn_died_fast",
		DyingOutput: "npm ERR! could not resolve codex@0.144",
		ElapsedMs:   420,
	}
	require.NoError(t, writeSpawnFailureRecord(rec))

	got := inst.SpawnFailure()
	require.NotNil(t, got)
	assert.Equal(t, "spawn_died_fast", got.Reason)
	assert.Equal(t, "npx codex@0.144", got.Command)
	assert.Equal(t, int64(420), got.ElapsedMs)
	assert.NotZero(t, got.Timestamp, "timestamp must be stamped on write")

	// FormatForDisplay must surface the dying output + command.
	disp := got.FormatForDisplay()
	assert.Contains(t, disp, "npm ERR! could not resolve codex@0.144")
	assert.Contains(t, disp, "npx codex@0.144")
	assert.Contains(t, disp, "420ms")

	// Preview fallback returns the same block.
	assert.Equal(t, disp, inst.spawnFailurePreview())

	// Clear removes it.
	clearSpawnFailureRecord(inst.ID)
	assert.Nil(t, inst.SpawnFailure())
	assert.Empty(t, inst.spawnFailurePreview())
}

// TestIssue1580_RecordSpawnAttemptClearsStale verifies that a new start attempt
// clears a stale record so a now-healthy session is not masked by an old
// failure, and emits a spawn_attempt lifecycle event.
func TestIssue1580_RecordSpawnAttemptClearsStale(t *testing.T) {
	inst := NewInstance("test-1580-clearstale", "/tmp")
	inst.Tool = "codex"

	require.NoError(t, writeSpawnFailureRecord(SpawnFailureRecord{
		InstanceID: inst.ID, Reason: "spawn_died_fast", DyingOutput: "old",
	}))
	require.NotNil(t, inst.SpawnFailure())

	inst.recordSpawnAttempt()
	assert.Nil(t, inst.SpawnFailure(), "recordSpawnAttempt must clear the stale sidecar")

	// The spawn_attempt event should be in the lifecycle log.
	assert.Contains(t, readLifecycleLog(t), "spawn_attempt")
}

// TestIssue1580_TmuxStartFailureRecorded verifies the tmux-level start failure
// path writes a record and a spawn_failed lifecycle event (no tmux needed).
func TestIssue1580_TmuxStartFailureRecorded(t *testing.T) {
	inst := NewInstance("test-1580-tmuxfail", "/tmp")
	inst.Tool = "codex"

	inst.recordTmuxStartFailure("npx codex@0.144", os.ErrPermission)

	rec := inst.SpawnFailure()
	require.NotNil(t, rec)
	assert.Equal(t, "tmux_start_failed", rec.Reason)
	assert.Contains(t, rec.DyingOutput, os.ErrPermission.Error())

	disp := rec.FormatForDisplay()
	assert.Contains(t, disp, "could not be created")
	assert.Contains(t, readLifecycleLog(t), "spawn_failed")
}

// TestIssue1580_FastDeathWatcherCapturesDyingOutput is the behavioral repro. A
// real tmux session runs a custom command whose initial process prints a marker
// then exits non-zero. The watcher must capture the dying output and persist a
// spawn_died_fast record; PreviewFull must surface it once the pane is gone.
//
// Pre-fix, this scenario produced no record, an erroring preview, and no
// lifecycle event — the surfaces did not exist.
func TestIssue1580_FastDeathWatcherCapturesDyingOutput(t *testing.T) {
	skipIfNoTmuxBinary(t)

	inst := NewInstance("test-1580-fastdeath", "/tmp")
	// A non-built-in tool with no ToolDef runs Command verbatim as the pane's
	// initial process (RunCommandAsInitialProcess is true for tool != "shell").
	inst.Tool = "customfail1580"
	// Print a marker, stay alive briefly so the watcher captures the pane, then
	// exit non-zero so tmux tears the session down.
	inst.Command = "sh -c 'echo boom-1580-dying-output; sleep 1; exit 7'"

	require.NoError(t, inst.Start())
	defer func() { _ = inst.Kill() }()

	// Wait for the watcher (250ms tick, 15s window) to observe the death and
	// persist the record.
	var rec *SpawnFailureRecord
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if rec = inst.SpawnFailure(); rec != nil && rec.Reason == "spawn_died_fast" {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}

	require.NotNil(t, rec, "spawn-failure record must be written when the process dies fast")
	assert.Equal(t, "spawn_died_fast", rec.Reason)
	assert.Contains(t, rec.DyingOutput, "boom-1580-dying-output",
		"the dying pane output must be captured before tmux discards it")
	assert.Greater(t, rec.ElapsedMs, int64(0), "elapsed time must be positive")

	// The lifecycle log must carry the spawn_died_fast trace.
	assert.Contains(t, readLifecycleLog(t), "spawn_died_fast")

	// PreviewFull falls back to the record now that the pane is gone.
	preview, err := inst.PreviewFull()
	require.NoError(t, err, "PreviewFull must not error once the record exists")
	assert.Contains(t, preview, "boom-1580-dying-output")
	assert.Contains(t, preview, "failed to start")
}

func readLifecycleLog(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(GetSessionIDLifecycleLogPath())
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		require.NoError(t, err)
	}
	return string(data)
}

// guard against an accidental empty-string tool matching a real one.
var _ = strings.TrimSpace
