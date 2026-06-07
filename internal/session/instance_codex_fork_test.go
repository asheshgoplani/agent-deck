package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"al.essio.dev/pkg/shellescape"
)

func seedCodexRollout(t *testing.T, codexHome, sid string) {
	t.Helper()
	dir := filepath.Join(codexHome, "sessions", "2026", "06", "06")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir rollout dir: %v", err)
	}
	p := filepath.Join(dir, "rollout-20260606T000000-"+sid+".jsonl")
	if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write rollout: %v", err)
	}
}

func TestCanForkCodex(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	sid := "11111111-2222-3333-4444-555555555555"
	seedCodexRollout(t, home, sid)

	inst := NewInstanceWithTool("cx", "/tmp/p", "codex")
	inst.CodexSessionID = sid
	inst.CodexDetectedAt = time.Now()
	if !inst.CanForkCodex() {
		t.Fatal("codex session with an on-disk rollout must be forkable")
	}

	inst.CodexSessionID = "no-rollout-uuid"
	if inst.CanForkCodex() {
		t.Fatal("codex session without a rollout must NOT be forkable")
	}
}

func TestCreateForkedCodexInstance_UsesWorktreeAndForkCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	sid := "11111111-2222-3333-4444-555555555555"
	seedCodexRollout(t, home, sid)

	parent := NewInstanceWithTool("cx parent", "/tmp/original", "codex")
	parent.CodexSessionID = sid
	parent.CodexDetectedAt = time.Now()

	opts := &ClaudeOptions{
		WorkDir:          "/tmp/original-wt",
		WorktreePath:     "/tmp/original-wt",
		WorktreeRepoRoot: "/tmp/original",
		WorktreeBranch:   "fork/cx-parent",
	}
	forked, cmd, err := parent.CreateForkedCodexInstanceWithOptions("cx parent (fork)", "", opts)
	if err != nil {
		t.Fatalf("CreateForkedCodexInstanceWithOptions: %v", err)
	}
	if forked.ProjectPath != "/tmp/original-wt" {
		t.Fatalf("forked ProjectPath = %q, want worktree dir", forked.ProjectPath)
	}
	if forked.WorktreePath != "/tmp/original-wt" || forked.WorktreeBranch != "fork/cx-parent" {
		t.Fatalf("worktree metadata not copied: %+v", forked)
	}
	if !forked.IsForkAwaitingStart || forked.ForkStartCommand == "" {
		t.Fatal("codex fork must defer launch via ForkStartCommand/IsForkAwaitingStart (Pi pattern)")
	}
	if !strings.Contains(cmd, "fork "+sid) {
		t.Fatalf("fork command must run `codex fork <parent-sid>`; got: %s", cmd)
	}
}

func TestCreateForkedCodexInstance_UsesConfiguredCodexHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CODEX_HOME", "")
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	codexHome := filepath.Join(home, "codex work")
	cfg := &UserConfig{Codex: CodexSettings{Command: `CODEX_HOME="` + codexHome + `" codex`}}
	if err := SaveUserConfig(cfg); err != nil {
		t.Fatalf("SaveUserConfig: %v", err)
	}
	ClearUserConfigCache()

	sid := "aaaaaaaa-2222-3333-4444-555555555555"
	seedCodexRollout(t, codexHome, sid)

	parent := NewInstanceWithTool("cx parent", "/tmp/original", "codex")
	parent.CodexSessionID = sid
	parent.CodexDetectedAt = time.Now()

	_, cmd, err := parent.CreateForkedCodexInstanceWithOptions("cx parent (fork)", "", nil)
	if err != nil {
		t.Fatalf("CreateForkedCodexInstanceWithOptions: %v", err)
	}
	want := `CODEX_HOME="` + codexHome + `" codex fork ` + sid
	if !strings.Contains(cmd, want) {
		t.Fatalf("configured CODEX_HOME command must be preserved for fork; want %q in %q", want, cmd)
	}
}

func TestCreateForkedCodexInstance_QuotesConfiguredCodexConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CODEX_HOME", "")
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	codexHome := filepath.Join(home, "codex config dir")
	cfg := &UserConfig{Codex: CodexSettings{ConfigDir: codexHome}}
	if err := SaveUserConfig(cfg); err != nil {
		t.Fatalf("SaveUserConfig: %v", err)
	}
	ClearUserConfigCache()

	sid := "bbbbbbbb-2222-3333-4444-555555555555"
	seedCodexRollout(t, codexHome, sid)

	parent := NewInstanceWithTool("cx parent", "/tmp/original", "codex")
	parent.CodexSessionID = sid
	parent.CodexDetectedAt = time.Now()

	_, cmd, err := parent.CreateForkedCodexInstanceWithOptions("cx parent (fork)", "", nil)
	if err != nil {
		t.Fatalf("CreateForkedCodexInstanceWithOptions: %v", err)
	}
	want := "CODEX_HOME=" + shellescape.Quote(codexHome) + " "
	if !strings.Contains(cmd, want) {
		t.Fatalf("configured [codex].config_dir must be shell-quoted for fork; want %q in %q", want, cmd)
	}
}

func TestCreateForkedCodexInstance_PreservesCompatibleToolIdentity(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(home, ".codex")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CODEX_HOME", codexHome)
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	cfg := &UserConfig{
		Tools: map[string]ToolDef{
			"my-codex": {
				Command:        "codex-wrapper",
				CompatibleWith: "codex",
			},
		},
	}
	if err := SaveUserConfig(cfg); err != nil {
		t.Fatalf("SaveUserConfig: %v", err)
	}
	ClearUserConfigCache()

	sid := "cccccccc-2222-3333-4444-555555555555"
	seedCodexRollout(t, codexHome, sid)

	parent := NewInstanceWithTool("cx parent", "/tmp/original", "my-codex")
	parent.Command = "codex-wrapper"
	parent.CodexSessionID = sid
	parent.CodexDetectedAt = time.Now()

	forked, cmd, err := parent.CreateForkedCodexInstanceWithOptions("cx parent (fork)", "", nil)
	if err != nil {
		t.Fatalf("CreateForkedCodexInstanceWithOptions: %v", err)
	}
	if forked.Tool != "my-codex" {
		t.Fatalf("forked Tool = %q, want custom Codex-compatible tool identity", forked.Tool)
	}
	if !strings.Contains(cmd, "AGENTDECK_TOOL=my-codex") {
		t.Fatalf("fork command must preserve AGENTDECK_TOOL identity; got %q", cmd)
	}
	if !strings.Contains(cmd, "codex-wrapper fork "+sid) {
		t.Fatalf("fork command must use the compatible tool command; got %q", cmd)
	}
}

// TestCreateForkedCodexInstance_ShellQuotesEnvPrefix guards the PR #1299 review
// finding: a user-editable session title containing shell metacharacters must be
// shell-quoted in the generated fork launch command, not Go-%q-quoted (which would
// still allow $(...) / backtick expansion under a shell).
func TestCreateForkedCodexInstance_ShellQuotesEnvPrefix(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	sid := "dddddddd-2222-3333-4444-555555555555"
	seedCodexRollout(t, home, sid)

	parent := NewInstanceWithTool("cx parent", "/tmp/original", "codex")
	parent.CodexSessionID = sid
	parent.CodexDetectedAt = time.Now()

	evil := "pwn $(touch /tmp/agentdeck-pwn)"
	_, cmd, err := parent.CreateForkedCodexInstanceWithOptions(evil, "", nil)
	if err != nil {
		t.Fatalf("CreateForkedCodexInstanceWithOptions: %v", err)
	}
	if !strings.Contains(cmd, "AGENTDECK_TITLE="+shellescape.Quote(evil)) {
		t.Fatalf("AGENTDECK_TITLE must be shell-quoted via shellescape.Quote; got: %s", cmd)
	}
}
