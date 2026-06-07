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

// With no [fork] config present, the dialog opens reflecting the comprehensive
// built-in defaults: with-state ON and (in a git repo) gitignored ON.
func TestForkDialog_Show_SeedsComprehensiveWithStateDefault(t *testing.T) {
	repo := forkDefaultsGitRepo(t)

	d := NewForkDialog()
	d.ShowWithParentSandboxed("My Session", repo, "grp", nil, "", false)

	assert.True(t, d.IsWorktreeEnabled(), "worktree seeded ON in a git repo")
	assert.True(t, d.IsWithStateEnabled(), "with_state seeded ON from [fork] comprehensive default")
	assert.True(t, d.IsWithStateAndGitignoredEnabled(), "with_ignored seeded ON from [fork] comprehensive default")
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
