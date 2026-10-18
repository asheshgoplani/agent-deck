package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// An unknown default login must not mask the configured slots' verdict: when
// every slot is at its limit, the harness is limited even though the
// default login has no feed.
func TestLimitReachedIgnoresUnknownDefaultAccount(t *testing.T) {
	full := []limitWindowJSON{{Window: "5h", UsedPct: 100}}
	got := limitReachedFrom([][]limitAccountJSON{{
		{Harness: "claude", Name: "work", Windows: full},
		{Harness: "claude", Name: "default", Default: true, Windows: []limitWindowJSON{}, Error: "no feed: run agent-deck hooks install"},
	}})
	require.True(t, got["claude"])
	got = limitReachedFrom([][]limitAccountJSON{{
		{Harness: "claude", Name: "work", Windows: full},
		{Harness: "claude", Name: "default", Default: true, Windows: []limitWindowJSON{{Window: "5h", UsedPct: 10}}},
	}})
	require.False(t, got["claude"], "a known default account with headroom keeps the harness usable")
}

// A window whose resets_at has passed no longer applies: a stale 100% from
// the last statusLine payload must not make the account limit-reached.
func TestLimitReachedIgnoresExpiredWindow(t *testing.T) {
	got := limitReachedFrom([][]limitAccountJSON{{
		{Harness: "claude", Name: "personal", Windows: []limitWindowJSON{
			{Window: "5h", UsedPct: 12},
			{Window: "7d", UsedPct: 100, Expired: true},
		}},
	}})
	require.False(t, got["claude"], "an expired 100% window must not mark the account limit-reached")
	got = limitReachedFrom([][]limitAccountJSON{{
		{Harness: "claude", Name: "personal", Windows: []limitWindowJSON{
			{Window: "5h", UsedPct: 100, Expired: true},
			{Window: "7d", UsedPct: 100},
		}},
	}})
	require.True(t, got["claude"], "a live 100% window still limits the account")
}

func TestLimitWindowJSONExpiredField(t *testing.T) {
	cases := []struct {
		name string
		w    limitWindowJSON
		want string
	}{
		{"live window omits expired", limitWindowJSON{Window: "5h", UsedPct: 40}, `{"window":"5h","used_pct":40}`},
		{"expired window is marked", limitWindowJSON{Window: "7d", UsedPct: 100, ResetsAt: "2026-10-01T00:00:00Z", Expired: true}, `{"window":"7d","used_pct":100,"resets_at":"2026-10-01T00:00:00Z","expired":true}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.w)
			require.NoError(t, err)
			require.JSONEq(t, tc.want, string(b))
			require.Equal(t, tc.want, string(b))
		})
	}
}

func TestLimitWindowText(t *testing.T) {
	require.Equal(t, "7d 52%", limitWindowText(limitWindowJSON{Window: "7d", UsedPct: 52}))
	require.Equal(t, "7d expired (was 100%)", limitWindowText(limitWindowJSON{Window: "7d", UsedPct: 100, Expired: true}))
}

// limits marks each window whose resets_at has passed, for both the Claude
// quota cache and the Codex rollout frame, in JSON and in the text output.
func TestLimitsMarksExpiredWindows(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, ".claude-work")
	_ = os.MkdirAll(work, 0o755)
	writeMacappConfig(t, home, "[macapp]\nplugins = true\n\n[profiles.work.claude]\nconfig_dir = \""+work+"\"\n")
	cache := filepath.Join(home, ".cache")
	quotaDir := filepath.Join(cache, "agent-deck", "quota", "work")
	_ = os.MkdirAll(quotaDir, 0o700)
	now := time.Now()
	past, future := now.Add(-time.Hour).Unix(), now.Add(3*time.Hour).Unix()
	snap := fmt.Sprintf(`{"id":"claude","label":"Claude","windows":[{"kind":"five_hour","label":"5h","used_percentage":40,"resets_at":%d},{"kind":"seven_day","label":"7d","used_percentage":100,"resets_at":%d}],"updated_at":%d}`, future, past, now.Unix())
	require.NoError(t, os.WriteFile(filepath.Join(quotaDir, "claude.json"), []byte(snap), 0o644))
	day := filepath.Join(home, ".codex", "sessions", "2026", "10", "18")
	_ = os.MkdirAll(day, 0o755)
	frame := fmt.Sprintf(`{"type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":100.0,"window_minutes":10080,"resets_at":%d},"secondary":{"used_percent":7.0,"window_minutes":300,"resets_at":%d}}}}`, past, future) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(day, "rollout-2026-10-18T09-00-00-abc.jsonl"), []byte(frame), 0o644))
	env := []string{"XDG_CACHE_HOME=" + cache, "CODEX_HOME=" + filepath.Join(home, ".codex")}
	stdout, stderr, code := runAgentDeckEnv(t, home, "", env, "limits", "--json")
	require.Equal(t, 0, code, stderr)
	var res struct {
		Accounts []limitAccountJSON `json:"accounts"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &res), stdout)
	got := map[string]bool{}
	byHarness := map[string][]limitAccountJSON{}
	for _, a := range res.Accounts {
		byHarness[a.Harness] = append(byHarness[a.Harness], a)
		for _, w := range a.Windows {
			got[a.Harness+"/"+a.Name+"/"+w.Window] = w.Expired
		}
	}
	require.Equal(t, map[string]bool{
		"claude/work/5h": false, "claude/work/7d": true,
		"codex/default/weekly": true, "codex/default/5h": false,
	}, got)
	require.Equal(t, map[string]bool{"claude": false, "codex": false}, limitReachedFrom([][]limitAccountJSON{byHarness["claude"], byHarness["codex"]}))
	stdout, stderr, code = runAgentDeckEnv(t, home, "", env, "limits")
	require.Equal(t, 0, code, stderr)
	require.Contains(t, stdout, "7d expired (was 100%)")
	require.Contains(t, stdout, "weekly expired (was 100%)")
}
