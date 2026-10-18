package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// Review item 2: after `session set <id> account|model ...` the resumed
// conversation still ends in the old rejection. That rejection must not keep
// the session account-blocked; only a turn newer than the switch decides.
func TestSetField_AccountSwitchResetsTranscriptVerdict(t *testing.T) {
	home := accountReviewConfig(t, "[profiles.work.claude]\nconfig_dir = '~/claude-work'\n")
	project := filepath.Join(home, "proj")
	require.NoError(t, os.MkdirAll(project, 0o755))
	const sid = "verdict-reset-session"
	inst := NewInstanceWithTool("verdict-reset", project, "claude")
	inst.ClaudeSessionID = sid

	old := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	body := recUserAt(old) + "\n" + recFieldAPIErrorAt(old, 403, "oauth_org_not_allowed", fieldOrgDisabledText) + "\n"
	// The conversation lives in the old dir and, migrated, in the new one.
	var paths []string
	for _, cfg := range []string{GetClaudeConfigDirForInstance(inst), filepath.Join(home, "claude-work")} {
		dir := filepath.Join(cfg, "projects", ConvertToClaudeDirName(project))
		require.NoError(t, os.MkdirAll(dir, 0o755))
		p := filepath.Join(dir, sid+".jsonl")
		require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
		paths = append(paths, p)
	}
	if got := locateHandoffTranscript(inst); got != paths[0] {
		t.Skipf("resolver did not find the fixture (got %q)", got)
	}
	kind, _ := inst.TranscriptLimit()
	require.Equal(t, SubstateAccountBlocked, kind, "precondition: the 403 is the verdict")

	_, _, err := SetField(inst, FieldAccount, "work", nil)
	require.NoError(t, err)
	require.False(t, inst.LimitVerdictResetAt.IsZero(), "switch must record the reset time")

	kind, detail := inst.TranscriptLimit()
	require.Equal(t, SubstateNone, kind, "old rejection survived the account switch (detail %q)", detail)

	// A rejection newer than the switch still counts.
	time.Sleep(5 * time.Millisecond)
	newer := time.Now().UTC().Format(time.RFC3339Nano)
	f, err := os.OpenFile(paths[1], os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString(recUserAt(newer) + "\n" + recFieldAPIErrorAt(newer, 403, "oauth_org_not_allowed", fieldOrgDisabledText) + "\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	inst.mu.Lock()
	inst.lastUsageLimitScanAt = time.Time{}
	inst.mu.Unlock()
	kind, _ = inst.TranscriptLimit()
	require.Equal(t, SubstateAccountBlocked, kind, "a rejection after the switch must still count")
}

// A model switch resets the verdict the same way and clears the memo, and a
// no-op set (same value) does not.
func TestSetField_ModelSwitchResetsVerdictMemo(t *testing.T) {
	accountReviewConfig(t, "")
	inst := NewInstanceWithTool("verdict-reset-model", t.TempDir(), "claude")
	inst.ClaudeSessionID = "bound"
	inst.mu.Lock()
	inst.usageLimitSessionID = "bound"
	inst.usageLimitedCached = true
	inst.usageLimitVerdict = limitVerdict{Kind: SubstateModelLimit, Detail: "Fable 5.1"}
	inst.lastUsageLimitScanAt = time.Now()
	inst.mu.Unlock()

	_, _, err := SetField(inst, FieldModel, "claude-opus-5-5", nil)
	require.NoError(t, err)
	require.False(t, inst.LimitVerdictResetAt.IsZero())
	inst.mu.RLock()
	require.False(t, inst.usageLimitedCached, "memo survived the model switch")
	require.Equal(t, SubstateNone, inst.usageLimitVerdict.Kind)
	inst.mu.RUnlock()

	first := inst.LimitVerdictResetAt
	_, _, err = SetField(inst, FieldModel, "claude-opus-5-5", nil)
	require.NoError(t, err)
	require.Equal(t, first, inst.LimitVerdictResetAt, "a no-op set must not move the reset")
}

func TestLatestAssistantTurnLimitSince_IgnoresRejectionAtOrBeforeReset(t *testing.T) {
	ts := time.Now().Add(-time.Minute).UTC()
	path := writeUsageLimitTranscript(t, recUserAt(ts.Format(time.RFC3339Nano)),
		recFieldAPIErrorAt(ts.Format(time.RFC3339Nano), 429, "rate_limit", fieldWeeklyText))
	v, ok := latestAssistantTurnLimitSince(path, time.Now(), ts)
	require.True(t, ok)
	require.Equal(t, SubstateNone, v.Kind, "rejection stamped at the reset must be ignored")
	v, ok = latestAssistantTurnLimitSince(path, time.Now(), ts.Add(-time.Second))
	require.True(t, ok)
	require.Equal(t, SubstateUsageLimit, v.Kind, "rejection after the reset must count")
}

// The reset time survives save/load in tool_data and is a typed statedb key.
func TestLimitVerdictReset_ToolDataRoundTrip(t *testing.T) {
	at := time.Date(2026, 10, 18, 9, 0, 0, 123456789, time.UTC)
	td := WriteLimitVerdictResetToToolData(json.RawMessage(`{"context_level":"primer"}`), at)
	require.True(t, at.Equal(ReadLimitVerdictResetFromToolData(td)), "round trip: %s", td)
	require.Equal(t, "primer", ReadContextLevelFromToolData(td))

	cleared := WriteLimitVerdictResetToToolData(td, time.Time{})
	require.True(t, ReadLimitVerdictResetFromToolData(statedb.MergeToolDataExtras(td, cleared)).IsZero(),
		"cleared reset resurrected by MergeToolDataExtras")

	row, err := instanceToRow(&Instance{ID: "x", Tool: "claude", LimitVerdictResetAt: at})
	require.NoError(t, err)
	require.True(t, at.Equal(ReadLimitVerdictResetFromToolData(row.ToolData)))
}

// A no-op `session set account|model` (same value) changes nothing, so it
// must not drop an intent record either.
func TestSetField_NoOpSwitchKeepsIntendedSwitch(t *testing.T) {
	accountReviewConfig(t, "[profiles.work.claude]\nconfig_dir = '~/claude-work'\n")
	inst := NewInstanceWithTool("noop-intent", t.TempDir(), "claude")
	_, _, err := SetField(inst, FieldAccount, "work", nil)
	require.NoError(t, err)
	_, _, err = SetField(inst, FieldModel, "claude-opus-5-5", nil)
	require.NoError(t, err)
	inst.MarkIntendedSwitch("conductor move", DefaultIntendedSwitchTTL, time.Now())
	rec := inst.IntendedSwitch

	_, _, err = SetField(inst, FieldAccount, "work", nil)
	require.NoError(t, err)
	require.Equal(t, rec, inst.IntendedSwitch, "no-op account set cleared the intent")
	_, _, err = SetField(inst, FieldModel, "claude-opus-5-5", nil)
	require.NoError(t, err)
	require.Equal(t, rec, inst.IntendedSwitch, "no-op model set cleared the intent")
}
