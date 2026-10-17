package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/stretchr/testify/require"
)

func TestHomeSwitchAccountSharedProjectsStore(t *testing.T) {
	for _, sharedStore := range []bool{true, false} {
		t.Run(map[bool]string{true: "shared", false: "different"}[sharedStore], func(t *testing.T) {
			withAccountsConfig(t, "personal", "work")
			homeDir := os.Getenv("HOME")
			t.Setenv("XDG_DATA_HOME", filepath.Join(homeDir, ".local", "share"))
			t.Setenv("AGENTDECK_ACCOUNT", "")
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			shared := filepath.Join(homeDir, "shared")
			require.NoError(t, os.MkdirAll(shared, 0o700))
			for _, account := range []string{"personal", "work"} {
				root := filepath.Join(homeDir, ".claude-"+account)
				require.NoError(t, os.MkdirAll(root, 0o700))
				target := shared
				if account == "work" && !sharedStore {
					target = filepath.Join(homeDir, "different")
					require.NoError(t, os.MkdirAll(target, 0o700))
				}
				require.NoError(t, os.Symlink(target, filepath.Join(root, "projects")))
			}
			project := filepath.Join(homeDir, "project")
			require.NoError(t, os.MkdirAll(project, 0o700))
			inst := session.NewInstanceWithTool("shared-tui", project, "claude")
			inst.Account, inst.Status = "personal", session.StatusStopped
			inst.ClaudeSessionID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
			path := filepath.Join(shared, session.ConvertToClaudeDirName(project), inst.ClaudeSessionID+".jsonl")
			body := []byte(fmt.Sprintf("{\"sessionId\":%q,\"type\":\"user\",\"timestamp\":\"2026-10-10T00:00:00Z\",\"message\":\"shared\"}\n", inst.ClaudeSessionID))
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
			require.NoError(t, os.WriteFile(path, body, 0o600))
			before, err := os.Stat(path)
			require.NoError(t, err)
			storage, err := session.NewStorageWithProfile("shared-store-tui")
			require.NoError(t, err)
			t.Cleanup(func() { _ = storage.Close() })
			require.NoError(t, storage.Save([]*session.Instance{inst}))
			home := &Home{storage: storage, instances: []*session.Instance{inst},
				instanceByID: map[string]*session.Instance{inst.ID: inst}, switchGenerations: map[string]uint64{}}
			cmd := home.switchSessionAccount(inst.ID, "work")
			require.NotNil(t, cmd)
			msg, ok := cmd().(accountSwitchedMsg)
			require.True(t, ok)
			if sharedStore {
				require.NoError(t, msg.err)
				require.True(t, msg.committed)
				require.Equal(t, "work", inst.Account)
				require.NotNil(t, msg.nativeResult)
				require.NotNil(t, msg.nativeResult.Transcript)
				require.Empty(t, msg.nativeResult.Transcript.Installed)
				require.Empty(t, msg.nativeResult.Transcript.BackedUp)
				require.Contains(t, msg.nativeResult.Conversation, "already present")
			} else {
				require.ErrorContains(t, msg.err, "symlink")
				require.False(t, msg.committed)
				require.Equal(t, "personal", inst.Account)
			}
			rows, err := storage.Load()
			require.NoError(t, err)
			require.Len(t, rows, 1)
			require.Equal(t, inst.Account, rows[0].Account)
			got, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, body, got)
			after, err := os.Stat(path)
			require.NoError(t, err)
			require.True(t, os.SameFile(before, after))
			require.Equal(t, before.ModTime(), after.ModTime())
		})
	}
}
