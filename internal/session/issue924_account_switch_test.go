// Issue #924 — per-session account switch behavior.
//
// Locks the resolver wiring: when Instance.Account names a profile that
// has a [profiles.<name>.claude].config_dir block, the spawn env's
// CLAUDE_CONFIG_DIR and the AGENTDECK_RESOLVED_* hint vars (#925) both
// reflect the account-level dir. Clearing the field falls back through
// the existing chain. The source label "account" appears in CFG-07
// observability.
//
// Bug reporter: @bautrey. Companion: issue924_account_field_test.go.
package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withTempAgentDeckHome sets up an isolated $HOME with a freshly-rendered
// ~/.agent-deck/config.toml and clears the user-config cache so the next
// LoadUserConfig picks up the new file. Returns the resolved
// ~/.claude-* directories caller-named via `wantedAccounts`.
//
// The caller usually wants to assert both that the named account
// resolves *and* that a different account resolves to its own dir, so
// the helper writes both blocks.
func withTempAgentDeckHome(t *testing.T, tomlBody string) (tmpHome string) {
	t.Helper()
	tmpHome = t.TempDir()
	origHome := os.Getenv("HOME")
	origProfile := os.Getenv("AGENTDECK_PROFILE")
	origEnvDir := os.Getenv("CLAUDE_CONFIG_DIR")
	t.Cleanup(func() {
		_ = os.Setenv("HOME", origHome)
		if origProfile != "" {
			_ = os.Setenv("AGENTDECK_PROFILE", origProfile)
		} else {
			_ = os.Unsetenv("AGENTDECK_PROFILE")
		}
		if origEnvDir != "" {
			_ = os.Setenv("CLAUDE_CONFIG_DIR", origEnvDir)
		} else {
			_ = os.Unsetenv("CLAUDE_CONFIG_DIR")
		}
		ClearUserConfigCache()
	})
	_ = os.Setenv("HOME", tmpHome)
	_ = os.Unsetenv("CLAUDE_CONFIG_DIR")
	_ = os.Unsetenv("AGENTDECK_PROFILE")
	// Keep XDG_CONFIG_HOME inside this temp HOME too. Empty XDG dir means reads
	// fall back to the legacy config.toml written below.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpHome, ".config"))

	agentDeckDir := filepath.Join(tmpHome, ".agent-deck")
	if err := os.MkdirAll(agentDeckDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentDeckDir, "config.toml"), []byte(tomlBody), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	ClearUserConfigCache()
	return tmpHome
}

// TestAccount_ResolvesToProfileConfigDir locks the resolver:
// Instance.Account="work" must select [profiles.work.claude].config_dir.
func TestAccount_ResolvesToProfileConfigDir(t *testing.T) {
	tmpHome := withTempAgentDeckHome(t, `
[profiles.work.claude]
config_dir = "~/.claude-work"

[profiles.personal.claude]
config_dir = "~/.claude-personal"
`)

	inst := &Instance{
		ID:          "switch-1",
		Tool:        "claude",
		Title:       "switch-test",
		ProjectPath: "/tmp/p",
		GroupPath:   "default",
		Account:     "work",
	}
	got, source := GetClaudeConfigDirSourceForInstance(inst)
	wantPath := filepath.Join(tmpHome, ".claude-work")
	if got != wantPath {
		t.Errorf("Account=work: got dir=%q, want %q", got, wantPath)
	}
	if source != "account" {
		t.Errorf("Account=work: source label = %q, want %q (CFG-07 observability)", source, "account")
	}

	// Switch to the other account — same resolver, different output.
	inst.Account = "personal"
	got, source = GetClaudeConfigDirSourceForInstance(inst)
	wantPath = filepath.Join(tmpHome, ".claude-personal")
	if got != wantPath {
		t.Errorf("Account=personal: got dir=%q, want %q", got, wantPath)
	}
	if source != "account" {
		t.Errorf("Account=personal: source label = %q, want %q", source, "account")
	}
}

// TestAccount_BeatsConductor_AndGroup locks the priority: Account is
// the most-specific level. With a conductor name AND a group block
// both configured, the per-session Account must still win.
func TestAccount_BeatsConductor_AndGroup(t *testing.T) {
	tmpHome := withTempAgentDeckHome(t, `
[profiles.work.claude]
config_dir = "~/.claude-account-wins"

[conductors."mybot".claude]
config_dir = "~/.claude-conductor-loses"

[groups."mygroup".claude]
config_dir = "~/.claude-group-loses"
`)

	inst := &Instance{
		ID:          "priority-1",
		Tool:        "claude",
		Title:       "conductor-mybot",
		ProjectPath: "/tmp/p",
		GroupPath:   "mygroup",
		Account:     "work",
	}
	got, source := GetClaudeConfigDirSourceForInstance(inst)
	wantPath := filepath.Join(tmpHome, ".claude-account-wins")
	if got != wantPath {
		t.Errorf("Account must beat conductor+group: got=%q want=%q (source=%q)", got, wantPath, source)
	}
	if source != "account" {
		t.Errorf("source label = %q, want %q", source, "account")
	}

	// Clear Account — chain should fall through to conductor (its level
	// beats group, per the existing #881 fix).
	inst.Account = ""
	got, source = GetClaudeConfigDirSourceForInstance(inst)
	wantPath = filepath.Join(tmpHome, ".claude-conductor-loses")
	if got != wantPath {
		t.Errorf("After clearing Account, conductor must take over: got=%q want=%q (source=%q)", got, wantPath, source)
	}
	if source != "conductor" {
		t.Errorf("after Account cleared: source = %q, want %q", source, "conductor")
	}
}

// TestAccount_UnknownNameFallsThrough locks the permissive behaviour:
// an Account name with no matching [profiles.<name>] block silently
// falls through to the existing chain, never erroring.
func TestAccount_UnknownNameFallsThrough(t *testing.T) {
	tmpHome := withTempAgentDeckHome(t, `
[profiles.work.claude]
config_dir = "~/.claude-work"

[groups."default".claude]
config_dir = "~/.claude-group"
`)

	inst := &Instance{
		ID:          "unknown-1",
		Tool:        "claude",
		Title:       "unknown-account",
		ProjectPath: "/tmp/p",
		GroupPath:   "default",
		Account:     "does-not-exist",
	}
	got, source := GetClaudeConfigDirSourceForInstance(inst)
	wantPath := filepath.Join(tmpHome, ".claude-group")
	if got != wantPath {
		t.Errorf("unknown Account must fall through to group: got=%q want=%q (source=%q)", got, wantPath, source)
	}
	if source != "group" {
		t.Errorf("source label = %q, want %q", source, "group")
	}
}

// TestAccount_SwitchChangesSpawnEnv locks the end-to-end MVP behaviour:
// flipping Instance.Account and calling buildClaudeCommand emits a
// different CLAUDE_CONFIG_DIR in the spawn env. This is exactly what
// "stop + restart with new account" gets after persistence — proving
// the resolver wires through the actual spawn path.
func TestAccount_SwitchChangesSpawnEnv(t *testing.T) {
	withTelegramConductorPresent(t)
	tmpHome := withTempAgentDeckHome(t, `
[profiles.work.claude]
config_dir = "~/.claude-work"

[profiles.personal.claude]
config_dir = "~/.claude-personal"
`)

	// Pre-seed both account dirs so EnsureWorkerScratchConfigDir doesn't
	// trip on missing source profiles when the spawn path runs.
	for _, name := range []string{".claude-work", ".claude-personal"} {
		dir := filepath.Join(tmpHome, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{}`), 0o644); err != nil {
			t.Fatalf("seed settings.json: %v", err)
		}
	}

	inst := &Instance{
		ID:          "00000000-0000-0000-0000-000000000924",
		Tool:        "claude",
		Title:       "issue-924",
		ProjectPath: filepath.Join(tmpHome, "proj"),
		GroupPath:   "default",
		Account:     "work",
	}

	cmdWork := inst.buildClaudeCommand("claude")
	wantWorkHint := "AGENTDECK_RESOLVED_CONFIG_DIR=" + filepath.Join(tmpHome, ".claude-work")
	if !strings.Contains(cmdWork, wantWorkHint) {
		t.Errorf("Account=work spawn must contain %q;\ngot: %s", wantWorkHint, cmdWork)
	}
	wantWorkSource := "AGENTDECK_RESOLVED_SOURCE=account"
	if !strings.Contains(cmdWork, wantWorkSource) {
		t.Errorf("Account=work spawn must label source via %q;\ngot: %s", wantWorkSource, cmdWork)
	}

	// Switch to the other account — same instance, new spawn env.
	inst.Account = "personal"
	// Drop the prior worker-scratch dir so the next spawn re-derives it
	// from the new source profile (otherwise EnsureWorkerScratchConfigDir
	// would skip and we'd be testing stale state, not the switch).
	inst.WorkerScratchConfigDir = ""

	cmdPersonal := inst.buildClaudeCommand("claude")
	wantPersonalHint := "AGENTDECK_RESOLVED_CONFIG_DIR=" + filepath.Join(tmpHome, ".claude-personal")
	if !strings.Contains(cmdPersonal, wantPersonalHint) {
		t.Errorf("Account=personal spawn must contain %q;\ngot: %s", wantPersonalHint, cmdPersonal)
	}
	if strings.Contains(cmdPersonal, wantWorkHint) {
		t.Errorf("Account=personal spawn must NOT carry the old work hint;\ngot cmd containing %q: %s", wantWorkHint, cmdPersonal)
	}
}
