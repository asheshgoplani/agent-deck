package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMain gives the CLI fixture a private HOME and tmux socket.
func TestCustomCommandExitCLI(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	script := filepath.Join(project, "fail.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nprintf 'ran\\n'\nexit 3\n"), 0o755))
	stdout, stderr, code := runAgentDeck(t, home, "-p", "_test", "add", "-t", "custom-exit-cli", "-cmd", script, "--no-parent", "--json", project)
	require.Zero(t, code, "%s %s", stdout, stderr)
	var added struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &added))
	require.NotEmpty(t, added.ID)
	startOut, startErr, startCode := runAgentDeck(t, home, "-p", "_test", "session", "start", added.ID)
	// A fast exit can make start report spawn_died_fast before the subsequent
	// list call; the status contract still needs the retained pane's exit code.

	type exitRow struct {
		Status   string `json:"status"`
		Substate string `json:"substate"`
		ExitCode *int   `json:"exit_code"`
	}
	var row exitRow
	var lastOut, lastErr string
	var lastCode int
	if !assert.Eventually(t, func() bool {
		out, stderr, code := runAgentDeck(t, home, "-p", "_test", "list", "--json")
		lastOut, lastErr, lastCode = out, stderr, code
		if code != 0 {
			return false
		}
		var rows []exitRow
		if json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 1 {
			return false
		}
		row = rows[0]
		return row.Status == "error" && row.Substate == "process-exited" && row.ExitCode != nil && *row.ExitCode == 3
	}, 15*time.Second, 100*time.Millisecond) {
		t.Fatalf("start code=%d stdout=%s stderr=%s; list code=%d stdout=%s stderr=%s row=%+v", startCode, startOut, startErr, lastCode, lastOut, lastErr, row)
	}

	stdout, stderr, code = runAgentDeck(t, home, "-p", "_test", "session", "show", added.ID, "--json")
	require.Zero(t, code, stderr)
	require.NoError(t, json.Unmarshal([]byte(stdout), &row))
	require.Equal(t, 3, *row.ExitCode)

	stdout, stderr, code = runAgentDeck(t, home, "-p", "_test", "status", "-v", "--json")
	require.Zero(t, code, stderr)
	var status struct {
		Sessions []exitRow `json:"sessions"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &status))
	require.Len(t, status.Sessions, 1)
	require.Equal(t, 3, *status.Sessions[0].ExitCode)

	stdout, stderr, code = runAgentDeck(t, home, "-p", "_test", "status", "-v")
	require.Zero(t, code, stderr)
	require.True(t, strings.Contains(stdout, "process exited: 3"), stdout)

	var remote session.RemoteSessionInfo
	require.NoError(t, json.Unmarshal([]byte(`{"status":"error","substate":"process-exited","exit_code":3}`), &remote))
	require.Equal(t, 3, *remote.ExitCode)
}
