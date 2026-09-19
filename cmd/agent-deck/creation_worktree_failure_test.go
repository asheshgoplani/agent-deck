package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func creationGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}
func creationRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	creationGit(t, repo, "init")
	creationGit(t, repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "initial")
	return repo
}
func TestCreationWorktreeFailureRefusesFallbackAndCleansPartial(t *testing.T) {
	t.Run("branch occupied", func(t *testing.T) { testCreationWorktreeFailure(t, false) })
	t.Run("post-create error", func(t *testing.T) { testCreationWorktreeFailure(t, true) })
}

func testCreationWorktreeFailure(t *testing.T, postCreate bool) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	repoA, repoB := creationRepo(t), creationRepo(t)
	settings := session.GetWorktreeSettings()
	branch := settings.ApplyBranchPrefix("r1-isolation")
	if postCreate {
		realGit, err := exec.LookPath("git")
		if err != nil {
			t.Fatal(err)
		}
		bin := t.TempDir()
		script := "#!/bin/sh\ncase \"$*\" in *\"worktree add\"*) \"" + realGit + "\" \"$@\"; exit 1;; esac\nexec \"" + realGit + "\" \"$@\"\n"
		if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	} else {
		creationGit(t, repoB, "checkout", "-b", branch)
	}
	beforeA := creationGit(t, repoA, "worktree", "list", "--porcelain")
	beforeB := creationGit(t, repoB, "worktree", "list", "--porcelain")
	inst := &session.Instance{ID: "r1-worktree-failure", Tool: "shell", ProjectPath: repoA}
	err := applyCreationExtras(inst, "", []string{repoB}, "r1-isolation", false, "")
	if err == nil {
		t.Fatal("requested isolation silently fell back to original repository")
	}
	if !strings.Contains(err.Error(), "worktree") {
		t.Fatalf("missing worktree diagnostic: %v", err)
	}
	if got := creationGit(t, repoA, "worktree", "list", "--porcelain"); got != beforeA {
		t.Fatalf("partial worktree remains: %s", got)
	}
	if got := creationGit(t, repoB, "worktree", "list", "--porcelain"); got != beforeB {
		t.Fatalf("original worktree changed: %s", got)
	}
	if _, err := os.Stat(inst.MultiRepoTempDir); !os.IsNotExist(err) {
		t.Fatalf("owned workspace remains: %v", err)
	}
	for _, repo := range []string{repoA, repoB} {
		if _, err := os.Stat(filepath.Join(repo, ".git")); err != nil {
			t.Fatalf("original repository damaged: %v", err)
		}
	}
}
