package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// A conductor titled "conductor-team-ops" running in conductor/ops
// (the shared-box layout) whose telegram plugin has no state anywhere: both
// `conductor status` and `session show` must find it by its dir and warn
// loudly, in text and JSON. With the state in conductor/ops/telegram the
// warning goes away.
func TestConductorStatusAndSessionShow_WarnOnMissingChannelState(t *testing.T) {
	sandboxConductorHome(t)
	home := os.Getenv("HOME")
	if err := session.SaveConductorMeta(&session.ConductorMeta{Name: "ops", Profile: "default"}); err != nil {
		t.Fatal(err)
	}
	dir, err := session.ConductorNameDir("ops")
	if err != nil {
		t.Fatal(err)
	}
	cond := session.NewInstanceWithGroupAndTool("conductor-team-ops", dir, "team/ops", "claude")
	cond.IsConductor = true
	cond.Channels = []string{"plugin:telegram@claude-plugins-official"}
	saveConductorFixture(t, "ops", cond).Close()

	out, stderr, code := runAgentDeck(t, home, "conductor", "status", "ops", "--json")
	if code != 0 {
		t.Fatalf("status --json exited %d: %s %s", code, out, stderr)
	}
	var status struct {
		Conductors []struct {
			SessionID       string   `json:"session_id"`
			ChannelWarnings []string `json:"channel_warnings"`
		} `json:"conductors"`
	}
	if err := json.Unmarshal([]byte(out), &status); err != nil {
		t.Fatalf("decode status: %v: %s", err, out)
	}
	if len(status.Conductors) != 1 || status.Conductors[0].SessionID != cond.ID {
		t.Fatalf("status did not find the conductor by its dir: %s", out)
	}
	if w := status.Conductors[0].ChannelWarnings; len(w) != 1 || !strings.Contains(w[0], "telegram") {
		t.Fatalf("status channel_warnings = %v, want one telegram warning", w)
	}
	out, _, _ = runAgentDeck(t, home, "conductor", "status", "ops")
	if !strings.Contains(out, "WARNING: channel plugin telegram has no state") {
		t.Fatalf("status text missing the channel warning:\n%s", out)
	}

	env := []string{"AGENTDECK_PROFILE=default"}
	out, stderr, code = runAgentDeckEnv(t, home, "", env, "session", "show", cond.ID, "--json")
	if code != 0 {
		t.Fatalf("session show --json exited %d: %s %s", code, out, stderr)
	}
	if !strings.Contains(out, `"channel_warnings"`) || !strings.Contains(out, "telegram has no state") {
		t.Fatalf("session show JSON missing channel_warnings:\n%s", out)
	}
	out, _, _ = runAgentDeckEnv(t, home, "", env, "session", "show", cond.ID)
	if !strings.Contains(out, "WARNING: channel plugin telegram has no state") {
		t.Fatalf("session show text missing the channel warning:\n%s", out)
	}

	stateDir := filepath.Join(dir, "telegram")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, ".env"), []byte("TELEGRAM_BOT_TOKEN=test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _, _ = runAgentDeckEnv(t, home, "", env, "session", "show", cond.ID, "--json")
	if strings.Contains(out, "channel_warnings") {
		t.Fatalf("state present in conductor/ops/telegram, yet session show warns:\n%s", out)
	}
}

// `session set <id> account <slot>` changes the account outside the switch
// executor; the conductor's telegram state from the old account dir must be
// carried there too (anchored as a link in conductor/<name>/telegram).
func TestSessionSetAccount_CarriesConductorChannelState(t *testing.T) {
	sandboxConductorHome(t)
	home := os.Getenv("HOME")
	cfgPath, err := session.GetUserConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := "[profiles.alpha.claude]\nconfig_dir = \"~/.claude-alpha\"\n\n[profiles.beta.claude]\nconfig_dir = \"~/.claude-beta\"\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)
	if err := session.SaveConductorMeta(&session.ConductorMeta{Name: "ops", Profile: "default"}); err != nil {
		t.Fatal(err)
	}
	dir, err := session.ConductorNameDir("ops")
	if err != nil {
		t.Fatal(err)
	}
	oldState := filepath.Join(home, ".claude-alpha", "channels", "telegram")
	if err := os.MkdirAll(oldState, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldState, ".env"), []byte("TELEGRAM_BOT_TOKEN=test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cond := session.NewInstanceWithGroupAndTool("conductor-team-ops", dir, "team/ops", "claude")
	cond.IsConductor = true
	cond.Account = "alpha"
	cond.Channels = []string{"plugin:telegram@claude-plugins-official"}
	saveConductorFixture(t, "ops", cond).Close()

	out, stderr, code := runAgentDeckEnv(t, home, "", []string{"AGENTDECK_PROFILE=default"}, "session", "set", cond.ID, "account", "beta")
	if code != 0 {
		t.Fatalf("session set account exited %d: %s %s", code, out, stderr)
	}
	anchor := filepath.Join(dir, "telegram")
	wantTarget, err := filepath.EvalSymlinks(oldState)
	if err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(anchor); err != nil || target != wantTarget {
		t.Fatalf("conductor/ops/telegram = %q, %v; want link -> %s (stderr: %s)", target, err, oldState, stderr)
	}
	if _, err := os.Lstat(filepath.Join(home, ".claude-beta", "channels", "telegram")); !os.IsNotExist(err) {
		t.Errorf("state must not be linked into the shared account dir; lstat err = %v", err)
	}
}

// An old flagged row in conductor/ops plus the row a setup re-run created
// both resolve as conductor ops: two bot owners. Status must say so.
func TestConductorStatus_WarnsOnDuplicateConductorSessions(t *testing.T) {
	sandboxConductorHome(t)
	home := os.Getenv("HOME")
	if err := session.SaveConductorMeta(&session.ConductorMeta{Name: "ops", Profile: "default"}); err != nil {
		t.Fatal(err)
	}
	dir, err := session.ConductorNameDir("ops")
	if err != nil {
		t.Fatal(err)
	}
	old := session.NewInstanceWithGroupAndTool("conductor-team-ops", dir, "team/ops", "claude")
	old.IsConductor = true
	dup := session.NewInstanceWithGroupAndTool("conductor-ops", dir, "conductor", "claude")
	dup.IsConductor = true
	dup.ConductorName = "ops"
	saveConductorFixture(t, "ops", old, dup).Close()

	out, stderr, code := runAgentDeck(t, home, "conductor", "status", "ops", "--json")
	if code != 0 {
		t.Fatalf("status --json exited %d: %s %s", code, out, stderr)
	}
	if !strings.Contains(out, old.ID) || !strings.Contains(out, dup.ID) || !strings.Contains(out, "duplicate_session_ids") {
		t.Fatalf("status JSON does not list both conductor sessions:\n%s", out)
	}
	out, _, _ = runAgentDeck(t, home, "conductor", "status", "ops")
	if !strings.Contains(out, `2 sessions resolve as conductor "ops"`) {
		t.Fatalf("status text missing the duplicate warning:\n%s", out)
	}
}
