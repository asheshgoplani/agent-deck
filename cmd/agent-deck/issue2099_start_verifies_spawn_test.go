package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
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

// issue2099HelperEnv marks the re-exec'd test binary that runs the real
// `session start` / `session restart` command (they call os.Exit).
const issue2099HelperEnv = "AGENT_DECK_ISSUE2099_HELPER_PROCESS"

func TestIssue2099HelperProcess(t *testing.T) {
	if os.Getenv(issue2099HelperEnv) != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) > 0 {
		args = args[1:] // drop the "--"
	}
	if len(args) < 2 || args[0] != "session" {
		os.Exit(2)
	}
	handleSession(session.DefaultProfile, args[1:])
	os.Exit(0)
}

// seedIssue2099Session writes one session whose initial process exits before
// the fast-death watcher's first tick into an isolated HOME, and returns that
// HOME plus the seeded instance.
func seedIssue2099Session(t *testing.T, title string) (string, *session.Instance) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("AGENTDECK_PROFILE", session.DefaultProfile)
	session.ClearUserConfigCache()

	prev := statedb.GetGlobal()
	statedb.SetGlobal(nil)
	t.Cleanup(func() { statedb.SetGlobal(prev) })

	inst := session.NewInstanceWithTool(title, t.TempDir(), "customfail2099")
	// Dies before the watcher's first 250ms tick, so Start() returns nil and
	// the pane is already gone by the time the CLI decides what to print.
	inst.Command = "sh -c 'exit 3'"
	inst.GroupPath = session.DefaultGroupPath

	storage, err := session.NewStorageWithProfile(session.DefaultProfile)
	require.NoError(t, err)
	require.NoError(t, storage.Save([]*session.Instance{inst}))
	require.NoError(t, storage.Close())
	return home, inst
}

// runIssue2099CLI re-execs the test binary as `agent-deck session <args>`
// with the isolated HOME and returns combined output plus the exit code.
func runIssue2099CLI(t *testing.T, home string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestIssue2099HelperProcess$", "--", "session"}, args...)...)
	var env []string
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "HOME=") || strings.HasPrefix(e, "XDG_") || strings.HasPrefix(e, "AGENTDECK_PROFILE=") {
			continue
		}
		env = append(env, e)
	}
	cmd.Env = append(env,
		issue2099HelperEnv+"=1",
		// Tells TestMain to keep the inherited sandboxed HOME instead of
		// re-isolating it (see runTestMain).
		"AGENT_DECK_TASK6_HELPER_PROCESS=1",
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"XDG_DATA_HOME="+filepath.Join(home, ".local", "share"),
		"XDG_CACHE_HOME="+filepath.Join(home, ".cache"),
		"AGENTDECK_PROFILE="+session.DefaultProfile,
	)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		exit, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("helper process failed to run: %v: %s", err, out)
		}
		code = exit.ExitCode()
	}
	return string(out), code
}

// decodeIssue2099JSON extracts the command's JSON object from the helper's
// combined output (the `go test` harness appends its own PASS/FAIL lines).
func decodeIssue2099JSON(t *testing.T, out string) map[string]interface{} {
	t.Helper()
	start := strings.Index(out, "{")
	end := strings.LastIndex(out, "}")
	require.True(t, start >= 0 && end > start, "no JSON object in output:\n%s", out)
	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(out[start:end+1]), &payload), "output:\n%s", out)
	return payload
}

// TestIssue2099_CLIStartDiesBeforePane is the CLI-level repro from the issue:
// `session start` against a spawn whose pane dies at once must exit non-zero
// and carry the failure in --json. Pre-fix it printed "Started" and exited 0.
func TestIssue2099_CLIStartDiesBeforePane(t *testing.T) {
	skipIfNoTmuxBinaryCLI(t)
	home, inst := seedIssue2099Session(t, "issue-2099-cli-start")

	out, code := runIssue2099CLI(t, home, "start", inst.Title, "--json")

	assert.NotEqual(t, 0, code, "session start must exit non-zero when no pane survives; output:\n%s", out)
	assert.NotContains(t, out, "Started session", "no false success line")
	payload := decodeIssue2099JSON(t, out)
	assert.Equal(t, false, payload["success"])
	assert.Contains(t, payload["error"], "failed to start session")
	assert.Equal(t, "spawn_died_fast", payload["reason"])
	assert.Equal(t, inst.ID, payload["id"])
	sf, ok := payload["spawn_failure"].(map[string]interface{})
	require.True(t, ok, "spawn_failure must be present in --json; payload: %v", payload)
	assert.Equal(t, "spawn_died_fast", sf["reason"])
}

// TestIssue2099_CLIRestartDiesBeforePane covers the restart path the issue
// reports as equally silent.
func TestIssue2099_CLIRestartDiesBeforePane(t *testing.T) {
	skipIfNoTmuxBinaryCLI(t)
	home, inst := seedIssue2099Session(t, "issue-2099-cli-restart")

	out, code := runIssue2099CLI(t, home, "restart", inst.Title, "--json")

	assert.NotEqual(t, 0, code, "session restart must exit non-zero when no pane survives; output:\n%s", out)
	assert.NotContains(t, out, "Restarted session", "no false success line")
	payload := decodeIssue2099JSON(t, out)
	assert.Equal(t, false, payload["success"])
	assert.Contains(t, payload["error"], "failed to restart session")
	// The restart respawn path records tmux-level and prepare failures but
	// runs no fast-death watcher, so a pane that dies at once is reported as
	// the missing tmux session itself; a record, when one exists, is carried.
	assert.Contains(t, []interface{}{"spawn_died_fast", "tmux_session_missing"}, payload["reason"])
	assert.Equal(t, inst.Title, payload["title"])
	assert.NotEmpty(t, payload["tmux"], "the missing tmux session must be named")
	if payload["reason"] == "spawn_died_fast" {
		_, ok := payload["spawn_failure"].(map[string]interface{})
		assert.True(t, ok, "spawn_failure must accompany a recorded reason; payload: %v", payload)
	}
}
