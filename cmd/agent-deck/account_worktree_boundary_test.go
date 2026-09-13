package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Exercise real Git branches, worktrees and setup scripts so a rejected
// account cannot appear safe merely because the fixture never reached setup.
func TestAccountWorktreeBoundary(t *testing.T) {
	cases := []struct {
		name, command, flagAccount, envAccount string
		valid                                  bool
	}{
		{"missing flag", "claude", "missing", "", false},
		{"incompatible flag", "claude", "incompatible", "", false},
		{"incompatible passthrough", "claude mcp", "incompatible", "", false},
		{"missing environment", "claude", "", "missing", false},
		{"valid overrides environment", "claude", "valid", "missing", true},
		{"valid environment", "claude", "", "valid", true},
		{"default remains shell", "", "incompatible", "", true},
	}
	for _, operation := range []string{"add", "launch"} {
		for _, tc := range cases {
			t.Run(operation+"/"+tc.name, func(t *testing.T) {
				home := t.TempDir()
				repo := filepath.Join(home, "project")
				require.NoError(t, os.MkdirAll(repo, 0700))
				runFixtureGit(t, repo, "init")
				runFixtureGit(t, repo, "config", "user.name", "Fixture")
				runFixtureGit(t, repo, "config", "user.email", "fixture@example.invalid")
				scriptDir := filepath.Join(repo, ".agent-deck")
				require.NoError(t, os.MkdirAll(scriptDir, 0700))
				require.NoError(t, os.WriteFile(filepath.Join(scriptDir, "worktree-setup.sh"), []byte("#!/bin/sh\nprintf '%s' \"$AGENT_DECK_WORKTREE_PATH\" > \"$AGENT_DECK_REPO_ROOT/setup-ran\"\n"), 0755))
				runFixtureGit(t, repo, "add", ".agent-deck/worktree-setup.sh")
				runFixtureGit(t, repo, "commit", "-m", "setup fixture")
				configDir := filepath.Join(home, ".config", "agent-deck")
				require.NoError(t, os.MkdirAll(configDir, 0700))
				config := fmt.Sprintf("[profiles.incompatible]\n[profiles.valid.claude]\nconfig_dir = %q\n[mcps.probe]\ncommand = 'echo'\n[groups.guarded.claude]\nmcps = ['probe']\n", filepath.Join(home, "claude-slot"))
				require.NoError(t, os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(config), 0600))
				// The real CLI may start this inert provider, never a real account.
				binDir := filepath.Join(home, "bin")
				require.NoError(t, os.MkdirAll(binDir, 0700))
				require.NoError(t, os.WriteFile(filepath.Join(binDir, "claude"), []byte("#!/bin/sh\nexec sleep 120\n"), 0755))
				env := []string{"HOME=" + home, "PATH=" + binDir + ":" + os.Getenv("PATH"), "SHELL=/bin/bash", "TERM=dumb", "AGENTDECK_PROFILE=ch_support_test", "AGENTDECK_ACCOUNT=" + tc.envAccount, "XDG_CONFIG_HOME=" + filepath.Join(home, ".config"), "XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"), "XDG_CACHE_HOME=" + filepath.Join(home, ".cache"), "TMUX_TMPDIR=" + os.Getenv("TMUX_TMPDIR")}
				run := func(args ...string) (string, error) {
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					cmd := exec.CommandContext(ctx, channelsCLIBinary(t), args...)
					cmd.Env = env
					out, err := cmd.CombinedOutput()
					return string(out), err
				}
				gitState := func(args ...string) string {
					cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
					out, err := cmd.CombinedOutput()
					require.NoError(t, err, "%s", out)
					return string(out)
				}
				branches := gitState("for-each-ref", "refs/heads")
				worktrees := gitState("worktree", "list", "--porcelain")
				args := []string{operation, repo, "--title", "guarded", "--no-parent", "--group", "guarded", "--worktree", "guarded-branch", "--new-branch", "--location", "subdirectory", "--allow-repo-scripts", "--json"}
				if tc.command != "" {
					args = append(args, "-c", tc.command)
				}
				if tc.flagAccount != "" {
					args = append(args, "--account", tc.flagAccount)
				}
				if operation == "launch" {
					args = append(args, "--no-wait")
					t.Cleanup(func() { _, _ = run("session", "stop", "guarded") })
				}
				output, runErr := run(args...)
				marker := filepath.Join(repo, "setup-ran")
				if tc.valid {
					require.NoError(t, runErr, "%s", output)
					require.FileExists(t, marker, "positive control must run setup")
					assert.NotEqual(t, branches, gitState("for-each-ref", "refs/heads"))
					assert.NotEqual(t, worktrees, gitState("worktree", "list", "--porcelain"))
				} else {
					require.Error(t, runErr, "%s", output)
					require.Contains(t, output, "account")
					assert.NoFileExists(t, marker, "rejected account ran repository setup")
					assert.Equal(t, branches, gitState("for-each-ref", "refs/heads"), "rejected account created a branch")
					assert.Equal(t, worktrees, gitState("worktree", "list", "--porcelain"), "rejected account registered a worktree")
					assert.NoDirExists(t, filepath.Join(repo, ".worktrees"), "rejected account created worktree directories")
					assert.NoFileExists(t, filepath.Join(repo, ".mcp.json"))
				}
				db, err := statedb.Open(filepath.Join(home, ".local", "share", "agent-deck", "profiles", "ch_support_test", "state.db"))
				require.NoError(t, err)
				defer db.Close()
				require.NoError(t, db.Migrate())
				rows, err := db.LoadInstances()
				require.NoError(t, err)
				if !tc.valid {
					assert.Empty(t, rows, "rejected account registered a session")
					return
				}
				require.Len(t, rows, 1)
				wantAccount := firstNonEmpty(tc.flagAccount, tc.envAccount)
				assert.Equal(t, wantAccount, rows[0].Account)
				assert.DirExists(t, rows[0].WorktreePath)
				markerPath, err := os.ReadFile(marker)
				require.NoError(t, err)
				assert.Equal(t, rows[0].WorktreePath, strings.TrimSpace(string(markerPath)))
				if tc.command == "" {
					assert.Equal(t, "shell", rows[0].Tool)
				}
			})
		}
	}
}
