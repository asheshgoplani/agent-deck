package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Review item 1: Fable's own weekly cap is only claimed when the 429 names the
// weekly window AND no fresh window on the account (5h included) is at 100%.
func TestAnnotateClaudeLimits_FableRelabelNeedsWeeklyAndAccountRoom(t *testing.T) {
	fable := func(detail string) []limitSessionRow {
		return []limitSessionRow{{ID: "f", Title: "tf", Account: "work", Model: "claude-fable-5-1", Substate: "usage-limit", Detail: detail}}
	}
	t.Run("5h window on Fable stays a usage limit", func(t *testing.T) {
		got := annotateClaudeLimits([]limitAccountJSON{claudeAcc("work", false, 40)}, fable("session limit · resets 3pm (UTC)"))
		require.Equal(t, []limitSessionJSON{{ID: "f", Title: "tf", Substate: "usage-limit", Detail: "session limit · resets 3pm (UTC)",
			Fix: "wait for reset (session limit · resets 3pm (UTC)) or move account"}}, got[0].Sessions)
	})
	t.Run("weekly on Fable with the 5h window at 100 stays a usage limit", func(t *testing.T) {
		acc := limitAccountJSON{Harness: "claude", Name: "work", Windows: []limitWindowJSON{{Window: "5h", UsedPct: 100}, {Window: "7d", UsedPct: 40}}}
		got := annotateClaudeLimits([]limitAccountJSON{acc}, fable("weekly limit · resets 12pm (UTC)"))
		require.Equal(t, "usage-limit", got[0].Sessions[0].Substate)
		require.Equal(t, "weekly limit · resets 12pm (UTC)", got[0].Sessions[0].Detail)
	})
	t.Run("weekly on Fable with room everywhere is Fable's cap", func(t *testing.T) {
		got := annotateClaudeLimits([]limitAccountJSON{claudeAcc("work", false, 40)}, fable("weekly limit · resets 12pm (UTC)"))
		require.Equal(t, "model-limit", got[0].Sessions[0].Substate)
		require.Equal(t, "Fable weekly (account has room)", got[0].Sessions[0].Detail)
	})
}

// Review item 3: a session belongs to the slot that owns its resolved Claude
// config dir, whatever set it (account, conductor, group, env); the default
// login only gets sessions no slot owns.
func TestAnnotateClaudeLimits_MembershipByResolvedConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	def := claudeAcc("default", true, 30)
	def.ConfigDir = filepath.Join(home, ".claude")
	work := claudeAcc("work", false, 10)
	work.ConfigDir = filepath.Join(home, "claude-work")
	spare := claudeAcc("spare", false, 5)
	spare.ConfigDir = filepath.Join(home, "claude-spare")
	accounts := []limitAccountJSON{def, work, spare}

	got := annotateClaudeLimits(accounts, []limitSessionRow{
		{ID: "g", Title: "group-routed", Substate: "account-blocked", ConfigDir: filepath.Join(home, "claude-work") + "/"},
		{ID: "t", Title: "tilde-routed", Substate: "account-blocked", ConfigDir: "~/claude-work"},
		{ID: "d", Title: "default-login", Substate: "account-blocked", ConfigDir: filepath.Join(home, ".claude")},
		{ID: "u", Title: "unowned-dir", Substate: "account-blocked", ConfigDir: filepath.Join(home, "elsewhere")},
	})
	titles := func(a limitAccountJSON) []string {
		var out []string
		for _, s := range a.Sessions {
			out = append(out, s.Title)
		}
		return out
	}
	require.Equal(t, []string{"default-login", "unowned-dir"}, titles(got[0]))
	require.Equal(t, []string{"group-routed", "tilde-routed"}, titles(got[1]))
	require.True(t, got[1].Blocked)
	require.Empty(t, got[2].Sessions)
	require.False(t, got[0].Blocked && got[0].Name == "work")
}

// Review item 5: a model cap on the fallback model (or on Opus) has no model
// to switch to; the fix waits or moves account instead of a no-op switch.
func TestAnnotateClaudeLimits_ModelCapOnFallbackMovesAccount(t *testing.T) {
	accounts := []limitAccountJSON{claudeAcc("work", false, 40), claudeAcc("spare", false, 10)}
	got := annotateClaudeLimits(accounts, []limitSessionRow{
		{ID: "a", Title: "ta", Account: "work", Model: limitsFallbackModel, Substate: "model-limit", Detail: "Opus 5.5"},
		{ID: "b", Title: "tb", Account: "work", Substate: "model-limit", Detail: "Opus"},
		{ID: "c", Title: "tc", Account: "work", Model: "claude-fable-5-1", Substate: "model-limit", Detail: "Fable 5.1"},
	})
	require.Equal(t, []limitSessionJSON{
		{ID: "a", Title: "ta", Substate: "model-limit", Detail: "Opus 5.5",
			Fix: "wait for reset (Opus 5.5) or move account: agent-deck session set a account spare --intended 'claude-opus-5-5 cap' && agent-deck session restart a"},
		{ID: "b", Title: "tb", Substate: "model-limit", Detail: "Opus",
			Fix: "wait for reset (Opus) or move account: agent-deck session set b account spare --intended 'Opus cap' && agent-deck session restart b"},
		{ID: "c", Title: "tc", Substate: "model-limit", Detail: "Fable 5.1",
			Fix: "agent-deck session set c model claude-opus-5-5 --intended 'claude-fable-5-1 cap' && agent-deck session restart c"},
	}, got[0].Sessions)
}

// Review item 4: `limits` is read-only and cheap, so it reads the transcript
// verdict only — never Substate (pane captures, hook-lag writes).
func TestLiveClaudeSessionRows_ReadsTranscriptOnly(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "limits_sessions.go", nil, 0)
	require.NoError(t, err)
	var fn *ast.FuncDecl
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "liveClaudeSessionRows" {
			fn = fd
		}
	}
	require.NotNil(t, fn)
	var calls []string
	ast.Inspect(fn, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			calls = append(calls, sel.Sel.Name)
		}
		return true
	})
	require.NotContains(t, calls, "Substate")
	require.NotContains(t, calls, "SubstateDetail")
	require.Contains(t, calls, "TranscriptLimit")
}

// Review item 3 end to end: a session with no stored account whose group
// routes it to slot acme's config dir is listed under acme, not under
// the default login.
func TestLimitsJSONAssignsSessionByGroupConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("AGENTDECK_PROFILE", "")
	acme := filepath.Join(home, "claude-acme")
	work := filepath.Join(home, "claude-work")
	project := filepath.Join(home, "proj")
	for _, d := range []string{acme, work, project, filepath.Join(home, ".claude")} {
		require.NoError(t, os.MkdirAll(d, 0o700))
	}
	writeMacappConfig(t, home, "[macapp]\nplugins = true\n"+
		"[profiles.acme.claude]\nconfig_dir = '~/claude-acme'\n"+
		"[profiles.work.claude]\nconfig_dir = '"+work+"'\n"+
		"[groups.\"routed\".claude]\nconfig_dir = '"+acme+"'\n")

	const sid = "22222222-3333-4444-5555-666666666666"
	tdir := filepath.Join(acme, "projects", session.ConvertToClaudeDirName(project))
	require.NoError(t, os.MkdirAll(tdir, 0o700))
	ts := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	user := fmt.Sprintf(`{"type":"user","sessionId":%q,"message":{"role":"user","content":"hi"},"timestamp":%q}`, sid, ts)
	blocked := fmt.Sprintf(`{"type":"assistant","sessionId":%q,"isApiErrorMessage":true,"apiErrorStatus":403,"error":"oauth_org_not_allowed","message":{"role":"assistant","model":"<synthetic>","content":[{"type":"text","text":"Your organization has disabled Claude subscription access for Claude Code"}]},"timestamp":%q}`, sid, ts)
	require.NoError(t, os.WriteFile(filepath.Join(tdir, sid+".jsonl"), []byte(user+"\n"+blocked+"\n"), 0o600))

	storage, err := session.NewStorageWithProfile("default")
	require.NoError(t, err)
	inst := &session.Instance{ID: "grp-1", Title: "routed-one", Tool: "claude", ProjectPath: project,
		GroupPath: "routed", ClaudeSessionID: sid, Status: session.StatusWaiting, CreatedAt: time.Now()}
	instances := []*session.Instance{inst}
	require.NoError(t, storage.SaveWithGroups(instances, session.NewGroupTreeWithGroups(instances, nil)))
	storage.Close()

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
	require.Len(t, byName["acme"].Sessions, 1, stdout)
	require.Equal(t, "grp-1", byName["acme"].Sessions[0].ID, stdout)
	require.False(t, byName["default"].Blocked, stdout)
	require.Empty(t, byName["default"].Sessions, stdout)
}

// A session running Fable as the configured default (no per-session model
// override) hits Fable's weekly cap just like one with the override.
func TestAnnotateClaudeLimits_FableWeeklyOnDefaultModel(t *testing.T) {
	got := annotateClaudeLimits([]limitAccountJSON{claudeAcc("work", false, 40)}, []limitSessionRow{
		{ID: "f", Title: "tf", Account: "work", EffectiveModel: "claude-fable-5-1", Substate: "usage-limit", Detail: "weekly limit · resets 12pm (UTC)"},
	})
	require.Equal(t, []limitSessionJSON{{ID: "f", Title: "tf", Substate: "model-limit", Detail: "Fable weekly (account has room)",
		Fix: "agent-deck session set f model claude-opus-5-5 --intended 'claude-fable-5-1 cap' && agent-deck session restart f"}}, got[0].Sessions)
}

// Model names and rejection text are untrusted: the printed fix must stay a
// single shell-safe argument however they are spelled.
func TestAnnotateClaudeLimits_FixCommandsAreShellQuoted(t *testing.T) {
	got := annotateClaudeLimits([]limitAccountJSON{claudeAcc("work", false, 40)}, []limitSessionRow{
		{ID: "m", Title: "tm", Account: "work", Substate: "model-limit", Detail: "Fable's cap; $(touch x)"},
	})
	require.Equal(t, `agent-deck session set m model claude-opus-5-5 --intended 'Fable'"'"'s cap; $(touch x) cap' && agent-deck session restart m`,
		got[0].Sessions[0].Fix)
}
