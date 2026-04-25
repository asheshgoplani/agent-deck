// Package session — gate tests for worker-scratch CLAUDE_CONFIG_DIR
// (issue #759).
//
// v1.7.68 (PR #732, internal/session/worker_scratch.go) adds an
// ephemeral CLAUDE_CONFIG_DIR for every non-conductor claude worker
// to disable the Telegram plugin per-spawn. The fix is only
// load-bearing on hosts that actually run a Telegram conductor — on
// every other host, the indirection breaks per-group `config_dir`
// account isolation: macOS Claude Code keys OAuth credentials by the
// literal CLAUDE_CONFIG_DIR path, and the scratch path is opaque, so
// Claude falls back to the default `~/.claude` account.
//
// These tests pin the predicate behaviour:
//   - No Telegram conductor configured ⇒ NeedsWorkerScratchConfigDir == false
//   - Telegram conductor configured     ⇒ NeedsWorkerScratchConfigDir == true
//   - Conductor / channel-owner / non-claude short-circuits still win
//     even when a Telegram conductor IS configured.

package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeUserConfigForTest writes ~/.agent-deck/config.toml inside the
// supplied home dir and clears the user-config cache so the next
// LoadUserConfig() call sees the fresh contents. The HOME env var
// MUST already point at home for the helper to take effect.
func writeUserConfigForTest(t *testing.T, home, body string) {
	t.Helper()
	dir := filepath.Join(home, ".agent-deck")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir .agent-deck: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)
}

// TestNeedsWorkerScratchConfigDir_NoTelegramConductor_ReturnsFalse
// pins the gate: a non-conductor claude worker on a host with no
// Telegram conductor configured MUST NOT receive a scratch dir.
// Without this gate the indirection silently rewrites
// CLAUDE_CONFIG_DIR for every worker, defeating per-group config_dir
// account isolation (issue #759).
func TestNeedsWorkerScratchConfigDir_NoTelegramConductor_ReturnsFalse(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Per-group dir present, but [conductor.telegram] token is empty —
	// the v1.7.68 indirection has nothing to protect against here.
	writeUserConfigForTest(t, home, `
[groups."personal".claude]
config_dir = "~/.claude-personal"
`)

	inst := &Instance{
		ID:    "00000000-0000-0000-0000-0000000000a1",
		Tool:  "claude",
		Title: "my-worker",
	}

	if got := inst.NeedsWorkerScratchConfigDir(); got {
		t.Errorf("no Telegram conductor configured — NeedsWorkerScratchConfigDir must return false; got true")
	}
}

// TestNeedsWorkerScratchConfigDir_WithTelegramConductor_ReturnsTrue
// pins the inverse: when a Telegram conductor IS configured, the
// predicate keeps firing for non-conductor claude workers — the
// #732 fix continues to protect the conductor's bot token from a
// duplicate poller.
func TestNeedsWorkerScratchConfigDir_WithTelegramConductor_ReturnsTrue(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeUserConfigForTest(t, home, `
[conductor.telegram]
token = "fake-bot-token"
user_id = 1
`)

	inst := &Instance{
		ID:    "00000000-0000-0000-0000-0000000000a2",
		Tool:  "claude",
		Title: "my-worker",
	}

	if got := inst.NeedsWorkerScratchConfigDir(); !got {
		t.Errorf("Telegram conductor configured — NeedsWorkerScratchConfigDir must return true; got false")
	}
}

// TestNeedsWorkerScratchConfigDir_ConductorShortCircuits_EvenWithTelegramToken
// guards a regression vector: even when a Telegram conductor IS
// configured (so the gate is OPEN), the conductor session itself
// must NEVER be scratched — it is the legitimate bot owner.
func TestNeedsWorkerScratchConfigDir_ConductorShortCircuits_EvenWithTelegramToken(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeUserConfigForTest(t, home, `
[conductor.telegram]
token = "fake-bot-token"
user_id = 1
`)

	inst := &Instance{
		ID:    "00000000-0000-0000-0000-0000000000a3",
		Tool:  "claude",
		Title: "conductor-personal",
	}

	if got := inst.NeedsWorkerScratchConfigDir(); got {
		t.Errorf("conductor session must keep ambient profile even when telegram conductor configured; got scratch=true")
	}
}

// TestPerGroupConfig_NoScratchWhenNoTelegramConductor is the
// integration-level mirror of #759: end-to-end on a host with
// per-group config_dir and no Telegram conductor, the spawn command
// builder must export the per-group dir verbatim, not a scratch
// path. Locked under the TestPerGroupConfig_* mandate in CLAUDE.md.
func TestPerGroupConfig_NoScratchWhenNoTelegramConductor(t *testing.T) {
	home := t.TempDir()
	origHome := os.Getenv("HOME")
	origClaudeDir := os.Getenv("CLAUDE_CONFIG_DIR")
	t.Cleanup(func() {
		_ = os.Setenv("HOME", origHome)
		if origClaudeDir != "" {
			_ = os.Setenv("CLAUDE_CONFIG_DIR", origClaudeDir)
		} else {
			_ = os.Unsetenv("CLAUDE_CONFIG_DIR")
		}
		ClearUserConfigCache()
	})
	_ = os.Setenv("HOME", home)
	_ = os.Unsetenv("CLAUDE_CONFIG_DIR")

	writeUserConfigForTest(t, home, `
[groups."personal".claude]
config_dir = "~/.claude-personal"
`)

	// Per-group dir must exist so the source-profile mirror has a
	// base — matches real-world setups where the user has actually
	// logged in to a Claude account under this dir.
	if err := os.MkdirAll(filepath.Join(home, ".claude-personal"), 0o755); err != nil {
		t.Fatalf("mkdir personal: %v", err)
	}

	inst := NewInstanceWithGroupAndTool("worker", "/tmp/p", "personal", "claude")
	inst.prepareWorkerScratchConfigDirForSpawn()

	if inst.WorkerScratchConfigDir != "" {
		t.Errorf("no telegram conductor configured — scratch dir must not be created; got %q", inst.WorkerScratchConfigDir)
	}

	cmd := inst.buildClaudeCommand("claude")
	wantDir := filepath.Join(home, ".claude-personal")
	wantInline := "CLAUDE_CONFIG_DIR=" + wantDir
	if !strings.Contains(cmd, wantInline) {
		t.Errorf("spawn must export per-group config_dir verbatim; want contains %q\n  got: %s", wantInline, cmd)
	}
	if strings.Contains(cmd, "worker-scratch") {
		t.Errorf("spawn must not reference worker-scratch path when no telegram conductor; got: %s", cmd)
	}
}
