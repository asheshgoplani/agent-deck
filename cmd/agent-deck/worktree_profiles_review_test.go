package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/stretchr/testify/require"
)

// A real CLI waits at its confirmation prompt. Changes to another profile then
// exercise the actual destructive callback, not a substitute ownership callback.
func TestCleanupReviewCrossProfileBoundary(t *testing.T) {
	for _, change := range []string{"owned before scan", "corrupt before scan", "unreadable before scan", "claimed after scan", "corrupt after scan", "unclaimed"} {
		t.Run(change, func(t *testing.T) {
			home := t.TempDir()
			stdout, stderr, code := runAgentDeck(t, home, "list", "--json")
			require.Zero(t, code, "%s %s", stdout, stderr)
			main, _, _, candidate, _ := cleanupFixture(t)
			otherDir := filepath.Join(home, ".local", "share", "agent-deck", "profiles", "other")
			changeProfile := func() {
				require.NoError(t, os.MkdirAll(otherDir, 0700))
				path := filepath.Join(otherDir, "state.db")
				if strings.Contains(change, "corrupt") {
					require.NoError(t, os.WriteFile(path, []byte("not a database"), 0600))
					return
				}
				db, err := statedb.Open(path)
				require.NoError(t, err)
				require.NoError(t, db.Migrate())
				require.NoError(t, db.SaveInstance(&statedb.InstanceRow{ID: "owner", Title: "owner", Tool: "shell", ProjectPath: candidate, WorktreePath: candidate}))
				require.NoError(t, db.Close())
				if strings.Contains(change, "unreadable") {
					require.NoError(t, os.Chmod(otherDir, 0000))
					t.Cleanup(func() { _ = os.Chmod(otherDir, 0700) })
					_, err := os.Stat(path)
					require.Error(t, err, "fixture must deny access; sandbox must drop DAC_OVERRIDE")
				}
			}
			if strings.Contains(change, "before scan") {
				changeProfile()
			}
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, channelsCLIBinary(t), "worktree", "cleanup", "--force")
			cmd.Dir, cmd.Env = main, cliEnvForIssue1031(home)
			input, err := cmd.StdinPipe()
			require.NoError(t, err)
			logPath := filepath.Join(home, "cleanup.log")
			log, err := os.Create(logPath)
			require.NoError(t, err)
			defer log.Close()
			cmd.Stdout, cmd.Stderr = log, log
			require.NoError(t, cmd.Start())
			if strings.Contains(change, "after scan") {
				require.Eventually(t, func() bool {
					data, _ := os.ReadFile(logPath)
					return strings.Contains(string(data), "Continue? [y/N]")
				}, 15*time.Second, 10*time.Millisecond)
				changeProfile()
			}
			_, _ = input.Write([]byte("y\n"))
			_ = input.Close()
			err = cmd.Wait()
			data, readErr := os.ReadFile(logPath)
			require.NoError(t, readErr)
			if strings.Contains(change, "before scan") && change != "owned before scan" {
				require.Error(t, err, "%s", data)
			} else {
				require.NoError(t, err, "%s", data)
			}
			_, statErr := os.Stat(candidate)
			if change == "unclaimed" {
				require.True(t, os.IsNotExist(statErr), "unclaimed positive control was not removed: %s", data)
			} else {
				require.NoError(t, statErr, "owned/uncertain worktree removed: %s", data)
			}
		})
	}
}
