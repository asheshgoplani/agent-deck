package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// remoteLimitsPayload is a Claude status-line payload carrying 5h/7d.
const remoteLimitsPayload = `{"session_id":"remote-native","rate_limits":{"five_hour":{"used_percentage":23.5,"resets_at":1790860000},"seven_day":{"used_percentage":48,"resets_at":1791400000}}}`

// CORE-CHANGES 21: `remote <name> limits --json` is forwarded read-only, with
// a closed option set.
func TestRemoteLimitsCommandArgs(t *testing.T) {
	for _, args := range [][]string{{"limits"}, {"limits", "--json"}, {"limits", "-json"}, {"limits", "--help"}, {"limits", "-h"}} {
		got, err := remoteCommandArgs(args)
		if err != nil || !reflect.DeepEqual(got, args) {
			t.Fatalf("remoteCommandArgs(%v) = %v, %v; want the args unchanged", args, got, err)
		}
	}
	for _, args := range [][]string{{"limits", "work"}, {"limits", "--remote", "other"}, {"limits", "--json=false"}, {"limits", "--", "x"}} {
		if _, err := remoteCommandArgs(args); err == nil {
			t.Fatalf("remoteCommandArgs(%v) accepted; only limits [--json] is forwarded", args)
		}
	}
}

// remoteLimitsController writes a controller config with one remote "lab"
// whose agent-deck is server, and returns a runner for `agent-deck remote ...`.
func remoteLimitsController(t *testing.T, shim, server string) func(args ...string) (string, string, int) {
	t.Helper()
	controller := t.TempDir()
	configDir := filepath.Join(controller, ".config", "agent-deck")
	require.NoError(t, os.MkdirAll(configDir, 0o700))
	// The controller has [macapp] plugins off and no account slots: every
	// account in the answer must come from the remote.
	config := fmt.Sprintf("[remotes.lab]\nhost = 'test-host'\nagent_deck_path = '%s'\n", server)
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(config), 0o600))
	return func(args ...string) (string, string, int) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, channelsCLIBinary(t), append([]string{"remote"}, args...)...)
		for _, kv := range cliEnvForIssue1031(controller) {
			if !strings.HasPrefix(kv, "PATH=") && !strings.HasPrefix(kv, "XDG_") {
				cmd.Env = append(cmd.Env, kv)
			}
		}
		cmd.Env = append(cmd.Env, "PATH="+shim+string(os.PathListSeparator)+os.Getenv("PATH"),
			"XDG_CONFIG_HOME="+filepath.Join(controller, ".config"), "XDG_DATA_HOME="+filepath.Join(controller, ".local", "share"), "XDG_CACHE_HOME="+filepath.Join(controller, ".cache"))
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		require.NoError(t, ctx.Err(), "remote limits did not return")
		code := 0
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return stdout.String(), stderr.String(), code
	}
}

// The forwarded read returns the REMOTE host's own accounts and 5h/7d
// windows, over the real ssh client and an in-process ssh server running
// this build of agent-deck as the remote.
func TestRemoteLimitsForwardsRemoteAccounts(t *testing.T) {
	t.Setenv("CODEX_HOME", filepath.Join(t.TempDir(), "absent-codex"))
	remote, shim := t.TempDir(), t.TempDir()
	startParitySSH(t, remote, shim)
	writeMacappConfig(t, remote, "[macapp]\nplugins = true\n[profiles.remote-acct.claude]\nconfig_dir = '"+filepath.Join(remote, "claude-remote")+"'\n")
	ingest := runUsageCLI(t, remote, remoteLimitsPayload, "-p", "remote-acct", "usage", "ingest", "claude")
	require.Equal(t, 0, ingest.exitCode, ingest.stderr)
	run := remoteLimitsController(t, shim, channelsCLIBinary(t))

	stdout, stderr, code := run("lab", "limits", "--json")
	require.Equal(t, 0, code, "stdout=%s stderr=%s", stdout, stderr)
	var report struct {
		Accounts []limitAccountJSON `json:"accounts"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &report), stdout)
	require.Len(t, report.Accounts, 1, stdout)
	acc := report.Accounts[0]
	require.Equal(t, "claude", acc.Harness)
	require.Equal(t, "remote-acct", acc.Name)
	windows := map[string]float64{}
	for _, w := range acc.Windows {
		windows[w.Window] = w.UsedPct
	}
	require.Equal(t, 23.5, windows["5h"])
	require.Equal(t, 48.0, windows["7d"])

	// The explicit exec form forwards the same way.
	stdout, stderr, code = run("exec", "lab", "limits", "--json")
	require.Equal(t, 0, code, "stdout=%s stderr=%s", stdout, stderr)
	require.Contains(t, stdout, `"remote-acct"`)
}

// A remote whose agent-deck predates `limits` gets the standard refusal,
// never the remote's raw "not a recognized command" text, and under --json a
// single {error, remote, remote_version} object on stdout.
func TestRemoteLimitsOlderRemoteRefuses(t *testing.T) {
	remote, shim := t.TempDir(), t.TempDir()
	startParitySSH(t, remote, shim)
	server := filepath.Join(shim, "old-agent-deck")
	script := "#!/bin/sh\nif [ \"$1\" = version ]; then echo 'Agent Deck v1.16.10'; exit 0; fi\n" +
		"printf 'Error: \"%s\" is not a recognized command and stdout is not a terminal, so the interactive UI cannot open; run agent-deck help for the command list\\n' \"$1\" >&2\nexit 2\n"
	require.NoError(t, os.WriteFile(server, []byte(script), 0o700))
	run := remoteLimitsController(t, shim, server)
	want := `unsupported remote command "limits" on remote "lab"; update its agent-deck`

	stdout, stderr, code := run("lab", "limits", "--json")
	require.Equal(t, 1, code, "stdout=%s stderr=%s", stdout, stderr)
	var refusal map[string]string
	require.NoError(t, json.Unmarshal([]byte(stdout), &refusal), stdout)
	require.Equal(t, map[string]string{"error": want, "remote": "lab", "remote_version": "1.16.10"}, refusal)
	require.NotContains(t, stdout+stderr, "not a recognized command")

	stdout, stderr, code = run("lab", "limits")
	require.Equal(t, 1, code, "stdout=%s stderr=%s", stdout, stderr)
	require.Empty(t, stdout)
	require.Contains(t, stderr, want)
	require.NotContains(t, stderr, "not a recognized command")
}

// A remote that has `limits` but answers with its own error (here: [macapp]
// plugins off) is passed through unchanged, not relabelled as unsupported.
func TestRemoteLimitsPassesRemoteErrorThrough(t *testing.T) {
	t.Setenv("CODEX_HOME", filepath.Join(t.TempDir(), "absent-codex"))
	remote, shim := t.TempDir(), t.TempDir()
	startParitySSH(t, remote, shim)
	run := remoteLimitsController(t, shim, channelsCLIBinary(t))
	stdout, stderr, code := run("lab", "limits", "--json")
	require.Equal(t, 2, code, "stdout=%s stderr=%s", stdout, stderr)
	require.Contains(t, stdout+stderr, "limits is off")
	require.NotContains(t, stdout+stderr, "unsupported remote command")
}
