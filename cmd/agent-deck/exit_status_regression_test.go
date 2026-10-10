package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CORE-CHANGES 13 regression tests. They use only the CLI and its JSON so they
// run unchanged against an older core, where they fail: an exited custom
// command reported plain idle/error with no substate and no exit_code.

type exitStatusRow struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Substate string `json:"substate"`
	ExitCode *int   `json:"exit_code"`
}

func addExitStatusSession(t *testing.T, home, title, script string) string {
	t.Helper()
	project := t.TempDir()
	path := filepath.Join(project, "cmd.sh")
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	stdout, stderr, code := runAgentDeck(t, home, "-p", "_test", "add", "-t", title, "-cmd", path, "--no-parent", "--json", project)
	require.Zero(t, code, "%s %s", stdout, stderr)
	var added struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &added))
	require.NotEmpty(t, added.ID)
	return added.ID
}

func listExitStatusRow(t *testing.T, home, id string) (exitStatusRow, string, bool) {
	t.Helper()
	out, _, code := runAgentDeck(t, home, "-p", "_test", "list", "--json")
	if code != 0 {
		return exitStatusRow{}, out, false
	}
	var rows []exitStatusRow
	if json.Unmarshal([]byte(out), &rows) != nil {
		return exitStatusRow{}, out, false
	}
	for _, row := range rows {
		if row.ID == id {
			return row, out, true
		}
	}
	return exitStatusRow{}, out, false
}

func waitExitStatusRow(t *testing.T, home, id string, ok func(exitStatusRow) bool) exitStatusRow {
	t.Helper()
	var row exitStatusRow
	var last string
	if !assert.Eventually(t, func() bool {
		var found bool
		row, last, found = listExitStatusRow(t, home, id)
		return found && ok(row)
	}, 15*time.Second, 100*time.Millisecond) {
		t.Fatalf("list --json never reached the expected row; last=%s", last)
	}
	return row
}

// A zero exit stays idle but is still reported as an observed exit with code 0.
func TestExitStatus1628_ZeroExitIsIdleWithCodeZero(t *testing.T) {
	home := t.TempDir()
	id := addExitStatusSession(t, home, "exit-zero", "#!/bin/sh\nprintf 'done\\n'\nexit 0\n")
	_, _, _ = runAgentDeck(t, home, "-p", "_test", "session", "start", id)

	waitExitStatusRow(t, home, id, func(r exitStatusRow) bool {
		return r.Status == "idle" && r.Substate == "process-exited" && r.ExitCode != nil && *r.ExitCode == 0
	})

	stdout, stderr, code := runAgentDeck(t, home, "-p", "_test", "session", "show", id, "--json")
	require.Zero(t, code, stderr)
	var shown exitStatusRow
	require.NoError(t, json.Unmarshal([]byte(stdout), &shown))
	require.NotNil(t, shown.ExitCode, stdout)
	require.Equal(t, 0, *shown.ExitCode)
	require.Equal(t, "process-exited", shown.Substate)
}

// A slower failing command (it exits after the start has settled) reports
// error + process-exited + its exit code in list and session show.
func TestExitStatus1628_DelayedFailureReportsErrorAndCode(t *testing.T) {
	home := t.TempDir()
	id := addExitStatusSession(t, home, "exit-late", "#!/bin/sh\nsleep 2\nexit 42\n")
	_, _, _ = runAgentDeck(t, home, "-p", "_test", "session", "start", id)

	waitExitStatusRow(t, home, id, func(r exitStatusRow) bool {
		return r.Status == "error" && r.Substate == "process-exited" && r.ExitCode != nil && *r.ExitCode == 42
	})
}

// #2453 must hold for tracked commands: a deliberate stop after the command
// exited stays stopped and drops the exit code, across repeated refreshes.
func TestExitStatus1628_StopAfterExitStaysStoppedWithoutCode(t *testing.T) {
	home := t.TempDir()
	id := addExitStatusSession(t, home, "exit-stop", "#!/bin/sh\nexit 3\n")
	_, _, _ = runAgentDeck(t, home, "-p", "_test", "session", "start", id)
	waitExitStatusRow(t, home, id, func(r exitStatusRow) bool {
		return r.Status == "error" && r.ExitCode != nil && *r.ExitCode == 3
	})

	stdout, stderr, code := runAgentDeck(t, home, "-p", "_test", "session", "stop", id)
	require.Zero(t, code, "%s %s", stdout, stderr)
	for n := 0; n < 5; n++ {
		row, out, found := listExitStatusRow(t, home, id)
		require.True(t, found, out)
		require.Equal(t, "stopped", row.Status, out)
		require.Nil(t, row.ExitCode, out)
		require.NotEqual(t, "process-exited", row.Substate, out)
		time.Sleep(200 * time.Millisecond)
	}
}

// `session start` of a tracked one-shot command that finishes at once is a
// successful start, whatever the command's own exit code: the run completed and
// its result is the process-exited row, not a spawn failure. An older core typed
// the command into an interactive shell and also reported "Started session".
func TestExitStatus1628_SessionStartSucceedsForFastExit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		code   int
		status string
	}{
		{"zero", 0, "idle"},
		{"three", 3, "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			id := addExitStatusSession(t, home, "exit-fast-"+tc.name, fmt.Sprintf("#!/bin/sh\necho ok\nexit %d\n", tc.code))
			stdout, stderr, code := runAgentDeck(t, home, "-p", "_test", "session", "start", id)
			require.Zero(t, code, "session start must succeed for a completed command: %s %s", stdout, stderr)
			require.NotContains(t, stdout+stderr, "spawn_died_fast")

			waitExitStatusRow(t, home, id, func(r exitStatusRow) bool {
				return r.Status == tc.status && r.Substate == "process-exited" && r.ExitCode != nil && *r.ExitCode == tc.code
			})
			shown, stderr, code := runAgentDeck(t, home, "-p", "_test", "session", "show", id)
			require.Zero(t, code, stderr)
			require.NotContains(t, shown, "session failed to start", "a completed command must not leave a spawn failure record")
		})
	}
}
