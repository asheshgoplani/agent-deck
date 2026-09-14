package session

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

const twoAccountConfig = `
[profiles.work.claude]
config_dir = "~/.claude-work"

[profiles.personal.claude]
config_dir = "~/.claude-personal"
`

// SwitchAccount is the single implementation behind the CLI's
// `session switch-account` and the TUI's account row. An unknown slot must
// abort with nothing committed, and the message must name the slots that DO
// exist — otherwise the operator has to go read config.toml to find out.
func TestSwitchAccountRejectsUnknownSlot(t *testing.T) {
	withTempAgentDeckHome(t, twoAccountConfig)
	cfg, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	inst := NewInstanceWithTool("unknown-slot", t.TempDir(), "claude")
	result, switchErr := SwitchAccount(cfg, inst, "nope", AccountSwitchOptions{})
	if result != nil {
		t.Fatal("an unknown slot must commit nothing: result must be nil so the caller does not persist")
	}
	if !errors.Is(switchErr, ErrUnknownAccount) {
		t.Fatalf("error must wrap ErrUnknownAccount, got: %v", switchErr)
	}
	for _, want := range []string{"work", "personal"} {
		if !strings.Contains(switchErr.Error(), want) {
			t.Errorf("error must list the configured accounts (missing %q): %v", want, switchErr)
		}
	}
	if inst.Account != "" {
		t.Errorf("account field must be untouched, got %q", inst.Account)
	}
}

// Tools without an account-switch executor must still be refused. Codex is
// intentionally excluded: it now has a supported configured-account path.
func TestSwitchAccountRejectsUnsupportedTool(t *testing.T) {
	withTempAgentDeckHome(t, twoAccountConfig)
	cfg, _ := LoadUserConfig()

	inst := NewInstanceWithTool("shell-session", t.TempDir(), "shell")
	result, switchErr := SwitchAccount(cfg, inst, "work", AccountSwitchOptions{})
	if result != nil {
		t.Fatal("an unsupported-tool session must commit nothing")
	}
	if !errors.Is(switchErr, ErrAccountSwitchUnsupported) {
		t.Fatalf("error must wrap ErrAccountSwitchUnsupported, got: %v", switchErr)
	}
}

// Codex named accounts are supported when the selected target has its own
// configured CODEX_HOME. This no-op path verifies the compatibility adapter
// recognizes the actual Codex target configuration instead of treating every
// non-Claude tool as unsupported.
func TestSwitchAccountSupportsConfiguredCodexTarget(t *testing.T) {
	withTempAgentDeckHome(t, `
[profiles.work.codex]
config_dir = "~/.codex-work"
`)
	cfg, _ := LoadUserConfig()
	inst := NewInstanceWithTool("codex-session", t.TempDir(), "codex")
	inst.Account = "work"

	result, switchErr := SwitchAccount(cfg, inst, "work", AccountSwitchOptions{})
	if switchErr != nil || result == nil || result.NewAccount != "work" || result.Restarted {
		t.Fatalf("configured Codex no-op = result=%#v err=%v", result, switchErr)
	}
	if result.Conversation == "" || !strings.Contains(result.Conversation, "no-op") {
		t.Fatalf("Codex no-op summary = %q", result.Conversation)
	}
}

// A session that has never held a conversation is the common case for the
// TUI picker (switch first, work later). It must commit the account field and
// report that there was nothing to migrate — not fail.
func TestSwitchAccountFreshSessionCommitsSlot(t *testing.T) {
	home := withTempAgentDeckHome(t, twoAccountConfig)
	cfg, _ := LoadUserConfig()

	inst := NewInstanceWithTool("fresh", t.TempDir(), "claude")
	result, switchErr := SwitchAccount(cfg, inst, "work", AccountSwitchOptions{})
	if switchErr != nil {
		t.Fatalf("fresh session switch must succeed, got: %v", switchErr)
	}
	if result.NewAccount != "work" || result.OldAccount != "" {
		t.Errorf("result accounts = %q -> %q, want \"\" -> \"work\"", result.OldAccount, result.NewAccount)
	}
	if inst.Account != "work" {
		t.Errorf("instance account = %q, want \"work\"", inst.Account)
	}
	if result.Restarted {
		t.Error("a session that was not running must not be started by a switch")
	}
	if !strings.Contains(result.Conversation, "no conversation") || !strings.Contains(result.Conversation, "readiness was not asserted") {
		t.Errorf("conversation summary = %q, want fresh-session/no-readiness disclosure", result.Conversation)
	}

	// The resolver must now route this session at the target account's dir.
	if got := GetClaudeConfigDirForInstance(inst); got != filepath.Join(home, ".claude-work") {
		t.Errorf("resolved config dir = %q, want the work account dir", got)
	}
}

// ConfiguredAccountNames is what both the CLI error text and the TUI pickers
// enumerate. A profile without a Claude config_dir is not a Claude account.
func TestConfiguredAccountNamesOnlyListsClaudeSlots(t *testing.T) {
	withTempAgentDeckHome(t, `
[profiles.work.claude]
config_dir = "~/.claude-work"

[profiles.personal.claude]
config_dir = "~/.claude-personal"

[profiles.codexonly.codex]
config_dir = "~/.codex-only"
`)
	cfg, _ := LoadUserConfig()

	got := ConfiguredAccountNames(cfg)
	want := []string{"personal", "work"}
	if len(got) != len(want) {
		t.Fatalf("names = %v, want %v (sorted, claude slots only)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("names = %v, want %v", got, want)
		}
	}
	if ConfiguredAccountNames(nil) != nil {
		t.Error("a nil config must yield no accounts, not panic")
	}
}
