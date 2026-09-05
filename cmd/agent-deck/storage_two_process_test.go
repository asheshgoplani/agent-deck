package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/stretchr/testify/require"
)

// These are independently executed production CLI binaries, not two handles or
// test-helper processes. Only tmux is substituted: its ordinary set-environment call
// pauses a session-ID edit after loading and before saving, without a production test hook.
func TestStorageTwoCLIProcesses(t *testing.T) {
	for _, scenario := range []string{"disjoint account", "same session id", "deleted row", "new addition", "group metadata"} {
		t.Run(scenario, func(t *testing.T) {
			home := t.TempDir()
			configDir := filepath.Join(home, ".config", "agent-deck")
			require.NoError(t, os.MkdirAll(configDir, 0700))
			require.NoError(t, os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("[profiles.seminno]\n"), 0600))
			out, stderr, code := runAgentDeck(t, home, "list", "--json")
			require.Zero(t, code, "%s %s", out, stderr)
			db, err := statedb.Open(filepath.Join(home, ".local", "share", "agent-deck", "profiles", "ch_support_test", "state.db"))
			require.NoError(t, err)
			t.Cleanup(func() { db.Close() })
			require.NoError(t, db.SaveInstance(&statedb.InstanceRow{
				ID: "shared", Title: "original", ProjectPath: home, GroupPath: "test",
				Tool: "shell", Status: "idle", Account: "personal", CreatedAt: time.Now(),
				TmuxSession: "agentdeck_storage_acceptance",
			}))
			require.NoError(t, db.SaveGroups([]*statedb.GroupRow{{Path: "test", Name: "test", Expanded: true}}))
			binDir := filepath.Join(home, "bin")
			require.NoError(t, os.MkdirAll(binDir, 0700))
			require.NoError(t, os.WriteFile(filepath.Join(binDir, "tmux"), []byte(`#!/bin/sh
for arg in "$@"; do
  if [ "$arg" = "set-environment" ] && [ -n "$STORAGE_BARRIER" ]; then
    : > "$STORAGE_BARRIER.ready"
    attempts=0
    while [ ! -f "$STORAGE_BARRIER.release" ]; do
      attempts=$((attempts + 1))
      [ "$attempts" -lt 1000 ] || exit 1
      sleep 0.02
    done
    break
  fi
done
exit 0
`), 0700))
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			start := func(barrier string, args ...string) (*exec.Cmd, *bytes.Buffer) {
				cmd := exec.CommandContext(ctx, channelsCLIBinary(t), args...)
				for _, item := range cliEnvForIssue1031(home) {
					if !strings.HasPrefix(item, "PATH=") && !strings.HasPrefix(item, "XDG_") {
						cmd.Env = append(cmd.Env, item)
					}
				}
				cmd.Env = append(cmd.Env, "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
					"XDG_CONFIG_HOME="+filepath.Join(home, ".config"), "XDG_DATA_HOME="+filepath.Join(home, ".local", "share"),
					"XDG_CACHE_HOME="+filepath.Join(home, ".cache"), "STORAGE_BARRIER="+barrier)
				output := new(bytes.Buffer)
				cmd.Stdout, cmd.Stderr = output, output
				require.NoError(t, cmd.Start())
				return cmd, output
			}
			waitReady := func(path string) {
				require.Eventually(t, func() bool { _, err := os.Stat(path + ".ready"); return err == nil }, 15*time.Second, 10*time.Millisecond, "CLI did not reach the post-load barrier")
			}
			aBarrier := filepath.Join(home, "a")
			a, aOut := start(aBarrier, "session", "set", "shared", "gemini-session-id", "alice", "--json")
			waitReady(aBarrier)
			var b *exec.Cmd
			var bOut *bytes.Buffer
			switch scenario {
			case "same session id":
				bBarrier := filepath.Join(home, "b")
				b, bOut = start(bBarrier, "session", "set", "shared", "gemini-session-id", "bob", "--json")
				waitReady(bBarrier)
				require.NoError(t, os.WriteFile(bBarrier+".release", nil, 0600))
			case "group metadata":
				b, bOut = start("", "group", "update", "test", "--max-concurrent=2", "--json")
			case "disjoint account":
				b, bOut = start("", "session", "set", "shared", "account", "seminno", "--json")
			case "deleted row":
				b, bOut = start("", "rm", "shared")
			case "new addition":
				b, bOut = start("", "add", home, "--title", "bob", "--no-parent", "--json")
			}
			require.NoError(t, b.Wait(), "second CLI: %s", bOut)
			require.NoError(t, os.WriteFile(aBarrier+".release", nil, 0600))
			err = a.Wait()
			if scenario == "same session id" || scenario == "deleted row" {
				require.Error(t, err, "stale first CLI must fail: %s", aOut)
				require.Contains(t, aOut.String(), "conflict")
			} else {
				require.NoError(t, err, "first CLI: %s", aOut)
			}
			// A fresh third database reader verifies both process outcomes.
			check, err := statedb.Open(filepath.Join(home, ".local", "share", "agent-deck", "profiles", "ch_support_test", "state.db"))
			require.NoError(t, err)
			defer check.Close()
			rows, err := check.LoadInstances()
			require.NoError(t, err)
			var toolData struct {
				GeminiSessionID string `json:"gemini_session_id"`
			}
			if len(rows) > 0 {
				require.NoError(t, json.Unmarshal(rows[0].ToolData, &toolData))
			}
			switch scenario {
			case "deleted row":
				require.Empty(t, rows)
			case "new addition":
				require.Len(t, rows, 2)
			case "same session id":
				require.Len(t, rows, 1)
				require.Equal(t, "bob", toolData.GeminiSessionID)
			case "group metadata":
				groups, err := check.LoadGroups()
				require.NoError(t, err)
				require.Len(t, groups, 1)
				require.Equal(t, 2, groups[0].MaxConcurrent)
				require.Equal(t, "alice", toolData.GeminiSessionID)
			case "disjoint account":
				require.Len(t, rows, 1)
				require.Equal(t, "alice", toolData.GeminiSessionID)
				require.Equal(t, "seminno", rows[0].Account)
			}
		})
	}
}
