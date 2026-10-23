package session

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Field report (Germany Immigration, v1.16.30): switch-account exited 1 after
// copying the conversation, the record went back to the source account, and
// yet Claude kept running on the target account (the destination transcript
// held the newest event). The target start reported an error although the
// session's pane did come up on the target; the rollback then recorded the
// source. A start error while the session is live on the target keeps the
// switch; only a session not running on the target is rolled back.
func startRaceFixture(t *testing.T) (*UserConfig, *Storage, *Instance) {
	t.Helper()
	home := withTempAgentDeckHome(t, `
[profiles.personal.claude]
config_dir = "~/.claude-personal"
[profiles.spare.claude]
config_dir = "~/.claude-spare"
`)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	cfg, err := LoadUserConfig()
	require.NoError(t, err)
	project := filepath.Join(home, "project")
	const sid = "11111111-2222-3333-4444-555555555555"
	path := filepath.Join(home, ".claude-personal", "projects", ConvertToClaudeDirName(project), sid+".jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(`{"sessionId":"`+sid+`","type":"user","timestamp":"2026-10-19T08:00:00Z","message":"hi"}`+"\n"), 0o600))
	storage := newTestStorage(t)
	inst := &Instance{ID: "start-race", Title: "Germany Immigration", ProjectPath: project, GroupPath: "test", Tool: "claude", Command: "claude", Account: "personal", ClaudeSessionID: sid, Status: StatusError, CreatedAt: time.Now()}
	require.NoError(t, storage.Save([]*Instance{inst}))
	return cfg, storage, inst
}

func stubStartRace(t *testing.T, live bool) *int {
	t.Helper()
	origRunning, origStop, origStart, origLive := nativeSwitchRunning, nativeSwitchStop, nativeSwitchStart, nativeSwitchLiveOnAccount
	t.Cleanup(func() {
		nativeSwitchRunning, nativeSwitchStop, nativeSwitchStart, nativeSwitchLiveOnAccount = origRunning, origStop, origStart, origLive
	})
	starts := 0
	nativeSwitchRunning = func(*Instance) bool { return true }
	nativeSwitchStop = func(*Instance) error { return nil }
	nativeSwitchStart = func(*Instance) error {
		starts++
		return errors.New("failed to start tmux session: failed to create tmux session: exit status 1 (output: duplicate session: agentdeck_Germany-Immigration_2e8e1169)")
	}
	nativeSwitchLiveOnAccount = func(_ *Instance, dir string) bool { return live && strings.HasSuffix(dir, ".claude-spare") }
	return &starts
}

func TestSwitchAccount_StartErrorWhileLiveOnTargetKeepsTheSwitch(t *testing.T) {
	cfg, storage, inst := startRaceFixture(t)
	starts := stubStartRace(t, true)

	result, err := SwitchAccount(cfg, inst, "spare", AccountSwitchOptions{Storage: storage})
	require.NoError(t, err, "a session live on the target account is a switch that happened")
	require.True(t, result.Restarted)
	require.Contains(t, strings.Join(result.Warnings, "\n"), "switch kept")
	require.NoError(t, CommitAccountSwitch(storage, inst, result))
	stored, err := storage.Load()
	require.NoError(t, err)
	require.Equal(t, "spare", stored[0].Account, "the record must name the account Claude runs on")
	require.Equal(t, 1, *starts, "no source restart over the live target")
}

func TestSwitchAccount_StartErrorNotLiveStillRollsBack(t *testing.T) {
	cfg, storage, inst := startRaceFixture(t)
	stubStartRace(t, false)

	result, err := SwitchAccount(cfg, inst, "spare", AccountSwitchOptions{Storage: storage})
	require.Error(t, err)
	require.Nil(t, result)
	require.Contains(t, err.Error(), "duplicate session", "the reason reaches the caller")
	stored, err := storage.Load()
	require.NoError(t, err)
	require.Equal(t, "personal", stored[0].Account)
}
