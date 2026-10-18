package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Limit-guard bug 2 end to end: a live session on slot "acme" whose last
// assistant turn is the org-disabled rejection makes `limits --json` mark
// acme blocked and name the session with a fix that moves it to the
// other, free slot. Isolated HOME; the store is read, never written.
func TestLimitsJSONNamesBlockedAccountSessions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("AGENTDECK_PROFILE", "")
	acme := filepath.Join(home, "claude-acme")
	work := filepath.Join(home, "claude-work")
	project := filepath.Join(home, "proj")
	for _, d := range []string{acme, work, project} {
		require.NoError(t, os.MkdirAll(d, 0o700))
	}
	writeMacappConfig(t, home, "[macapp]\nplugins = true\n"+
		"[profiles.acme.claude]\nconfig_dir = '"+acme+"'\n"+
		"[profiles.work.claude]\nconfig_dir = '"+work+"'\n")

	// The transcript: one user turn, then the field 403 record.
	const sid = "11111111-2222-3333-4444-555555555555"
	tdir := filepath.Join(acme, "projects", session.ConvertToClaudeDirName(project))
	require.NoError(t, os.MkdirAll(tdir, 0o700))
	ts := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	user := fmt.Sprintf(`{"type":"user","sessionId":%q,"message":{"role":"user","content":"hi"},"timestamp":%q}`, sid, ts)
	blocked := fmt.Sprintf(`{"type":"assistant","sessionId":%q,"isApiErrorMessage":true,"apiErrorStatus":403,"error":"oauth_org_not_allowed","message":{"role":"assistant","model":"<synthetic>","content":[{"type":"text","text":"Your organization has disabled Claude subscription access for Claude Code · Use an Anthropic API key instead, or ask your admin to enable access"}]},"timestamp":%q}`, sid, ts)
	require.NoError(t, os.WriteFile(filepath.Join(tdir, sid+".jsonl"), []byte(user+"\n"+blocked+"\n"), 0o600))

	storage, err := session.NewStorageWithProfile("default")
	require.NoError(t, err)
	inst := &session.Instance{ID: "blk-1", Title: "alpha", Tool: "claude", ProjectPath: project,
		Account: "acme", ClaudeSessionID: sid, Status: session.StatusWaiting, CreatedAt: time.Now()}
	instances := []*session.Instance{inst}
	require.NoError(t, storage.SaveWithGroups(instances, session.NewGroupTreeWithGroups(instances, nil)))
	storage.Close()

	// Fresh 7d on both slots; work has room, so it is the move target.
	for name, pct := range map[string]float64{"acme": 100, "work": 30} {
		payload := fmt.Sprintf(`{"session_id":"x","rate_limits":{"five_hour":{"used_percentage":10,"resets_at":%d},"seven_day":{"used_percentage":%v,"resets_at":%d}}}`,
			time.Now().Add(3*time.Hour).Unix(), pct, time.Now().Add(72*time.Hour).Unix())
		ingest := runUsageCLI(t, home, payload, "-p", name, "usage", "ingest", "claude")
		require.Equal(t, 0, ingest.exitCode, ingest.stderr)
	}

	env := []string{"CODEX_HOME=" + filepath.Join(home, "absent-codex")}
	stdout, stderr, code := runAgentDeckEnv(t, home, "", env, "-p", "default", "limits", "--json")
	require.Equal(t, 0, code, "stdout=%s stderr=%s", stdout, stderr)
	var report struct {
		Accounts []limitAccountJSON `json:"accounts"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &report), stdout)
	byName := map[string]limitAccountJSON{}
	for _, a := range report.Accounts {
		byName[a.Name] = a
	}
	require.True(t, byName["acme"].Blocked, stdout)
	require.Equal(t, "organization disabled Claude subscription access", byName["acme"].BlockedReason, stdout)
	require.Equal(t, []limitSessionJSON{{ID: "blk-1", Title: "alpha", Substate: "account-blocked",
		Detail: "organization disabled Claude subscription access",
		Fix:    "agent-deck session set blk-1 account work --intended 'acme blocked' && agent-deck session restart blk-1"}},
		byName["acme"].Sessions, stdout)
	require.False(t, byName["work"].Blocked, stdout)
	require.Empty(t, byName["work"].Sessions, stdout)

	text, stderr, code := runAgentDeckEnv(t, home, "", env, "-p", "default", "limits")
	require.Equal(t, 0, code, stderr)
	require.True(t, strings.Contains(text, "  BLOCKED (org disabled access): move 1 sessions: alpha"), text)
}
