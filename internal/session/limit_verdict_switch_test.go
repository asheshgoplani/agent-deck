package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// limitSwitchFixture is a running Claude session on "personal" whose last
// assistant record is an org-disabled 403, with the same conversation already
// present under "spare" (as a native switch installs it).
func limitSwitchFixture(t *testing.T, id string) (*UserConfig, *Instance, *Storage) {
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
	require.NoError(t, os.MkdirAll(project, 0o755))
	const sid = "11111111-2222-3333-4444-555555555555"
	old := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	body := []byte(recUserAt(old) + "\n" + recFieldAPIErrorAt(old, 403, "oauth_org_not_allowed", fieldOrgDisabledText) + "\n")
	for _, account := range []string{"personal", "spare"} {
		path := filepath.Join(home, ".claude-"+account, "projects", ConvertToClaudeDirName(project), sid+".jsonl")
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, body, 0o600))
	}

	originalRunning, originalStop, originalStart := nativeSwitchRunning, nativeSwitchStop, nativeSwitchStart
	t.Cleanup(func() {
		nativeSwitchRunning, nativeSwitchStop, nativeSwitchStart = originalRunning, originalStop, originalStart
	})
	nativeSwitchRunning = func(*Instance) bool { return true }
	nativeSwitchStop = func(*Instance) error { return nil }
	nativeSwitchStart = func(*Instance) error { return nil }

	storage := newTestStorage(t)
	inst := &Instance{ID: id, Title: "source", ProjectPath: project, GroupPath: "test", Tool: "claude", Command: "claude", Account: "personal", ClaudeSessionID: sid, Status: StatusWaiting, CreatedAt: time.Now()}
	inst.MarkIntendedSwitch("moved here on purpose", DefaultIntendedSwitchTTL, time.Now())
	require.NoError(t, storage.Save([]*Instance{inst}))
	if kind, _ := inst.TranscriptLimit(); kind != SubstateAccountBlocked {
		t.Skipf("resolver did not find the fixture (verdict %q)", kind)
	}
	return cfg, inst, storage
}

func storedInstance(t *testing.T, storage *Storage, id string) *Instance {
	t.Helper()
	stored, err := storage.Load()
	require.NoError(t, err)
	for _, inst := range stored {
		if inst.ID == id {
			return inst
		}
	}
	t.Fatalf("instance %s not stored", id)
	return nil
}

// switch-account (CLI and TUI) moves the conversation through
// ExecuteHarnessSwitch and the scoped storage CAS, never SetField. The moved
// transcript's old 403 must not keep the session account-blocked on the
// destination, in memory or after a reload, and the old account's intent
// record must go.
func TestSwitchAccount_CommitResetsTranscriptVerdict(t *testing.T) {
	cfg, inst, storage := limitSwitchFixture(t, "switch-verdict")

	res, err := SwitchAccount(cfg, inst, "spare", AccountSwitchOptions{})
	require.NoError(t, err)
	require.NoError(t, CommitAccountSwitch(storage, inst, res))
	require.Equal(t, "spare", inst.Account)

	require.False(t, inst.LimitVerdictResetAt.IsZero(), "switch commit must record the verdict reset")
	require.Nil(t, inst.IntendedSwitch, "the old account's intent must not cover the move")
	kind, detail := inst.TranscriptLimit()
	require.Equal(t, SubstateNone, kind, "old rejection survived the account switch (detail %q)", detail)

	reloaded := storedInstance(t, storage, inst.ID)
	require.Equal(t, "spare", reloaded.Account)
	require.False(t, reloaded.LimitVerdictResetAt.IsZero(), "reset not persisted by the switch CAS")
	require.Nil(t, reloaded.IntendedSwitch, "stale intent persisted after the switch")
	kind, detail = reloaded.TranscriptLimit()
	require.Equal(t, SubstateNone, kind, "reloaded session still blocked (detail %q)", detail)
}

// `session switch` with Storage passed commits inside the executor; the
// caller's later CommitNativeHarnessSwitch is only an acknowledgement. The
// reset must be written by the in-executor commit.
func TestHarnessSwitch_StorageCommitResetsTranscriptVerdict(t *testing.T) {
	cfg, inst, storage := limitSwitchFixture(t, "harness-switch-verdict")

	result, err := ExecuteHarnessSwitch(cfg, inst, HarnessSwitchOptions{Target: SwitchPreviewTarget{Harness: "claude", Account: "spare"}, Storage: storage})
	require.NoError(t, err)
	require.True(t, result.Committed)
	require.NoError(t, storage.CommitNativeHarnessSwitch(inst, result))

	kind, detail := inst.TranscriptLimit()
	require.Equal(t, SubstateNone, kind, "old rejection survived the harness switch (detail %q)", detail)
	reloaded := storedInstance(t, storage, inst.ID)
	require.False(t, reloaded.LimitVerdictResetAt.IsZero(), "reset not persisted by the in-executor commit")
	require.Nil(t, reloaded.IntendedSwitch)
}

// A switch whose target fails to start is rolled back; the rollback must
// also restore the verdict state, or the restored source would hide its own
// account's rejection.
func TestHarnessSwitch_RollbackRestoresVerdictState(t *testing.T) {
	cfg, inst, storage := limitSwitchFixture(t, "harness-switch-rollback")
	intent := inst.IntendedSwitch

	starts := 0
	nativeSwitchStart = func(*Instance) error {
		starts++
		if starts == 1 {
			return errors.New("injected target start failure")
		}
		return nil
	}
	result, err := ExecuteHarnessSwitch(cfg, inst, HarnessSwitchOptions{Target: SwitchPreviewTarget{Harness: "claude", Account: "spare"}, Storage: storage})
	require.Error(t, err)
	require.NotNil(t, result)
	require.False(t, result.Committed)
	require.Equal(t, "personal", inst.Account)

	require.True(t, inst.LimitVerdictResetAt.IsZero(), "rollback left the verdict reset")
	require.Equal(t, intent, inst.IntendedSwitch, "rollback lost the intent record")
	kind, _ := inst.TranscriptLimit()
	require.Equal(t, SubstateAccountBlocked, kind, "restored source must still report its own rejection")
	reloaded := storedInstance(t, storage, inst.ID)
	require.Equal(t, "personal", reloaded.Account)
	require.True(t, reloaded.LimitVerdictResetAt.IsZero(), "rollback left the reset persisted")
	require.NotNil(t, reloaded.IntendedSwitch)
}
