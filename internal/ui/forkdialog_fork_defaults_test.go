package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/stretchr/testify/assert"
)

func forkDefaultsGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)

	repo := filepath.Join(home, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	cmd := exec.Command("git", "init", "-q", "-b", "main")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	return repo
}

// With no [fork] config present, the dialog opens reflecting the built-in
// defaults: worktree + with-state ON, gitignored OFF (opt-in).
func TestForkDialog_Show_SeedsComprehensiveWithStateDefault(t *testing.T) {
	repo := forkDefaultsGitRepo(t)

	d := NewForkDialog()
	d.ShowWithParentSandboxed("My Session", repo, "grp", nil, "", false)

	assert.True(t, d.IsWorktreeEnabled(), "worktree seeded ON in a git repo")
	assert.True(t, d.IsWithStateEnabled(), "with_state seeded ON from [fork] comprehensive default")
	assert.False(t, d.IsWithStateAndGitignoredEnabled(), "with_ignored seeded OFF by default (opt-in)")
}

// A jujutsu repo is state-capable as of #1305, so Shift+F must present a
// coherent worktree + with-state option there too — not just on git. This is
// the acceptance guard for issue criterion #2: the dialog seeds with-state on a
// jj repo via explicit jj detection, and submit no longer hits the old git-only
// rejection now that forkSessionCmdWithOptions routes jj to
// forkWithStateWorkspaceJJ. Uses a non-colocated jj repo.
func TestForkDialog_Show_SeedsWithStateOnJujutsuRepo(t *testing.T) {
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj not on PATH")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := filepath.Join(t.TempDir(), "jjconfig.toml")
	if err := os.WriteFile(cfg, []byte("[user]\nname = \"Test User\"\nemail = \"test@example.com\"\n"), 0o644); err != nil {
		t.Fatalf("write jj config: %v", err)
	}
	t.Setenv("JJ_CONFIG", cfg)
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)

	repo := filepath.Join(home, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	cmd := exec.Command("jj", "git", "init")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("jj git init failed in this env: %v\n%s", err, out)
	}

	d := NewForkDialog()
	d.ShowWithParentSandboxed("My Session", repo, "grp", nil, "", false)

	assert.True(t, d.IsWorktreeEnabled(), "worktree (workspace) seeded ON in a jj repo (#1305 dialog parity)")
	assert.True(t, d.IsWithStateEnabled(), "with_state seeded ON for a jj repo — no git-only gate")
}

func TestForkDialog_Show_DockerAutoMatchesSandboxedParent(t *testing.T) {
	repo := forkDefaultsGitRepo(t)

	d := NewForkDialog()
	d.ShowWithParentSandboxed("My Session", repo, "grp", nil, "", true)

	assert.True(t, d.IsSandboxEnabled(), "docker=auto should seed ON for sandboxed parent")
}

func TestForkDialog_Show_UsesForkBranchPrefix(t *testing.T) {
	repo := forkDefaultsGitRepo(t)
	cfg, err := session.LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	cfg.Fork.BranchPrefix = "wip/"
	if err := session.SaveUserConfig(cfg); err != nil {
		t.Fatalf("SaveUserConfig: %v", err)
	}
	session.ClearUserConfigCache()

	d := NewForkDialog()
	d.ShowWithParentSandboxed("Fix Bug", repo, "grp", nil, "", false)

	assert.Equal(t, "wip/fix-bug", d.branchInput.Value())
}
