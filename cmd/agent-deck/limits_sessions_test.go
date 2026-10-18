package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func claudeAcc(name string, def bool, pct7d float64) limitAccountJSON {
	return limitAccountJSON{Harness: "claude", Name: name, Default: def,
		Windows: []limitWindowJSON{{Window: "5h", UsedPct: 10}, {Window: "7d", UsedPct: pct7d}}}
}

// Limit-guard bugs 2 and 4: `limits` tells a blocked account from a limit
// reached one, names the sessions to move, and reports Fable's own weekly cap
// as a model cap while the account still has room.
func TestAnnotateClaudeLimits(t *testing.T) {
	const blockedDetail = "organization disabled Claude subscription access"
	tests := []struct {
		name        string
		accounts    []limitAccountJSON
		rows        []limitSessionRow
		wantBlocked map[string]string // account -> blocked_reason
		wantSess    map[string][]limitSessionJSON
	}{
		{
			name: "blocked account names its sessions and moves them to the freshest free slot",
			accounts: []limitAccountJSON{
				claudeAcc("acme", false, 100),
				claudeAcc("work", false, 60),
				claudeAcc("spare", false, 20),
				claudeAcc("full", false, 100),
			},
			rows: []limitSessionRow{
				{ID: "s1", Title: "alpha", Account: "acme", Substate: "account-blocked", Detail: blockedDetail},
				{ID: "s2", Title: "beta", Account: "acme", Substate: "account-blocked", Detail: blockedDetail},
				{ID: "s3", Title: "ok", Account: "work", Substate: "waiting"},
			},
			wantBlocked: map[string]string{"acme": blockedDetail},
			wantSess: map[string][]limitSessionJSON{"acme": {
				{ID: "s1", Title: "alpha", Substate: "account-blocked", Detail: blockedDetail,
					Fix: "agent-deck session set s1 account spare --intended 'acme blocked' && agent-deck session restart s1"},
				{ID: "s2", Title: "beta", Substate: "account-blocked", Detail: blockedDetail,
					Fix: "agent-deck session set s2 account spare --intended 'acme blocked' && agent-deck session restart s2"},
			}},
		},
		{
			name: "no free slot leaves a placeholder; a blocked slot is never the target",
			accounts: []limitAccountJSON{
				claudeAcc("a", false, 10),
				claudeAcc("b", false, 5),
				claudeAcc("c", false, 100),
			},
			rows: []limitSessionRow{
				{ID: "x", Title: "tx", Account: "a", Substate: "account-blocked"},
				{ID: "y", Title: "ty", Account: "b", Substate: "account-blocked", Detail: "custom"},
			},
			wantBlocked: map[string]string{"a": blockedDetail, "b": "custom"},
			wantSess: map[string][]limitSessionJSON{
				"a": {{ID: "x", Title: "tx", Substate: "account-blocked",
					Fix: "agent-deck session set x account <other slot> --intended 'a blocked' && agent-deck session restart x"}},
				"b": {{ID: "y", Title: "ty", Substate: "account-blocked", Detail: "custom",
					Fix: "agent-deck session set y account <other slot> --intended 'b blocked' && agent-deck session restart y"}},
			},
		},
		{
			name: "stale or expired 7d never makes a slot a move target",
			accounts: []limitAccountJSON{
				claudeAcc("blk", false, 1),
				{Harness: "claude", Name: "stale", Stale: true, Windows: []limitWindowJSON{{Window: "7d", UsedPct: 1}}},
				{Harness: "claude", Name: "exp", Windows: []limitWindowJSON{{Window: "7d", UsedPct: 1, Expired: true}}},
				claudeAcc("def", true, 1),
			},
			rows:        []limitSessionRow{{ID: "z", Title: "tz", Account: "blk", Substate: "account-blocked"}},
			wantBlocked: map[string]string{"blk": blockedDetail},
			wantSess: map[string][]limitSessionJSON{"blk": {{ID: "z", Title: "tz", Substate: "account-blocked",
				Fix: "agent-deck session set z account <other slot> --intended 'blk blocked' && agent-deck session restart z"}}},
		},
		{
			name:     "model-limit gets the model switch fix",
			accounts: []limitAccountJSON{claudeAcc("work", false, 40)},
			rows:     []limitSessionRow{{ID: "m", Title: "tm", Account: "work", Substate: "model-limit", Detail: "Fable 5.1"}},
			wantSess: map[string][]limitSessionJSON{"work": {{ID: "m", Title: "tm", Substate: "model-limit", Detail: "Fable 5.1",
				Fix: "agent-deck session set m model claude-opus-5-5 --intended 'Fable 5.1 cap' && agent-deck session restart m"}}},
		},
		{
			name:     "usage-limit on Fable while the account has room is Fable's own weekly cap",
			accounts: []limitAccountJSON{claudeAcc("work", false, 40)},
			rows: []limitSessionRow{{ID: "f", Title: "tf", Account: "work", Model: "claude-fable-5-1", Substate: "usage-limit",
				Detail: "weekly limit · resets 12pm (UTC)"}},
			wantSess: map[string][]limitSessionJSON{"work": {{ID: "f", Title: "tf", Substate: "model-limit", Detail: "Fable weekly (account has room)",
				Fix: "agent-deck session set f model claude-opus-5-5 --intended 'claude-fable-5-1 cap' && agent-deck session restart f"}}},
		},
		{
			name:     "usage-limit on Fable with the account at 100% stays a usage limit",
			accounts: []limitAccountJSON{claudeAcc("work", false, 100)},
			rows: []limitSessionRow{{ID: "f", Title: "tf", Account: "work", Model: "claude-fable-5-1", Substate: "usage-limit",
				Detail: "weekly limit · resets 12pm (UTC)"}},
			wantSess: map[string][]limitSessionJSON{"work": {{ID: "f", Title: "tf", Substate: "usage-limit", Detail: "weekly limit · resets 12pm (UTC)",
				Fix: "wait for reset (weekly limit · resets 12pm (UTC)) or move account"}}},
		},
		{
			name:     "usage-limit on Fable with no fresh 7d stays a usage limit",
			accounts: []limitAccountJSON{{Harness: "claude", Name: "work", Stale: true, Windows: []limitWindowJSON{{Window: "7d", UsedPct: 10}}}},
			rows:     []limitSessionRow{{ID: "f", Title: "tf", Account: "work", Model: "fable", Substate: "usage-limit", Detail: "d"}},
			wantSess: map[string][]limitSessionJSON{"work": {{ID: "f", Title: "tf", Substate: "usage-limit", Detail: "d",
				Fix: "wait for reset (d) or move account"}}},
		},
		{
			name:     "plain usage-limit on a non-Fable model waits or moves",
			accounts: []limitAccountJSON{claudeAcc("work", false, 40)},
			rows:     []limitSessionRow{{ID: "u", Title: "tu", Account: "work", Model: "claude-opus-5-5", Substate: "usage-limit", Detail: "5h limit"}},
			wantSess: map[string][]limitSessionJSON{"work": {{ID: "u", Title: "tu", Substate: "usage-limit", Detail: "5h limit",
				Fix: "wait for reset (5h limit) or move account"}}},
		},
		{
			name:        "a session with no account belongs to the default login",
			accounts:    []limitAccountJSON{claudeAcc("default", true, 30), claudeAcc("work", false, 10)},
			rows:        []limitSessionRow{{ID: "d", Title: "td", Substate: "account-blocked"}},
			wantBlocked: map[string]string{"default": blockedDetail},
			wantSess: map[string][]limitSessionJSON{"default": {{ID: "d", Title: "td", Substate: "account-blocked",
				Fix: "agent-deck session set d account work --intended 'default blocked' && agent-deck session restart d"}}},
		},
		{
			name:     "unknown account and codex accounts are left alone",
			accounts: []limitAccountJSON{claudeAcc("work", false, 10), {Harness: "codex", Name: "default"}},
			rows:     []limitSessionRow{{ID: "g", Title: "tg", Account: "gone", Substate: "account-blocked"}, {ID: "n", Title: "tn", Substate: "account-blocked"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := append([]limitAccountJSON(nil), tt.accounts...)
			got := annotateClaudeLimits(tt.accounts, tt.rows)
			require.Equal(t, before, tt.accounts, "input accounts mutated")
			require.Len(t, got, len(tt.accounts))
			for _, a := range got {
				reason, blocked := tt.wantBlocked[a.Name]
				if a.Harness != "claude" {
					blocked, reason = false, ""
				}
				require.Equal(t, blocked, a.Blocked, "blocked for %s", a.Name)
				require.Equal(t, reason, a.BlockedReason, "blocked_reason for %s", a.Name)
				want := tt.wantSess[a.Name]
				if a.Harness != "claude" {
					want = nil
				}
				require.Equal(t, want, a.Sessions, "sessions for %s", a.Name)
			}
		})
	}
}

func TestLimitsTextNotes(t *testing.T) {
	acc := annotateClaudeLimits([]limitAccountJSON{claudeAcc("acme", false, 100), claudeAcc("work", false, 10)},
		[]limitSessionRow{
			{ID: "s1", Title: "alpha", Account: "acme", Substate: "account-blocked"},
			{ID: "s2", Title: "beta", Account: "acme", Substate: "account-blocked"},
			{ID: "s3", Title: "gamma", Account: "work", Substate: "model-limit", Detail: "Fable"},
		})
	require.Equal(t, []string{"  BLOCKED (org disabled access): move 2 sessions: alpha, beta"}, limitsTextNotes(acc[0]))
	require.Equal(t, []string{"  MODEL CAP gamma (Fable): agent-deck session set s3 model claude-opus-5-5 --intended 'Fable cap' && agent-deck session restart s3"}, limitsTextNotes(acc[1]))
}
