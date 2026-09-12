package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestProjBackendEndToEnd exercises the proj backend against a real proj
// project, which is the only way to cover the parts that matter: that proj-new
// is invoked correctly, that the branch agent-deck resolved is the branch the
// worktree ends up on, and that the result is a genuine linked git worktree
// that RemoveWorktree can tear down again.
//
// Opt-in, because it needs a populated proj project and a reflink-capable
// filesystem that no CI runner has:
//
//	AGENT_DECK_PROJ_E2E=/mnt/work/projects/core-stack go test ./internal/git/ -run ProjBackendEndToEnd -v
func TestProjBackendEndToEnd(t *testing.T) {
	projectDir := strings.TrimSpace(os.Getenv("AGENT_DECK_PROJ_E2E"))
	if projectDir == "" {
		t.Skip("set AGENT_DECK_PROJ_E2E=<proj project dir> to run")
	}
	if !projToolsAvailable() {
		t.Fatal("proj toolchain not available")
	}

	project, ok := resolveProjProject(projectDir)
	if !ok {
		t.Fatalf("%s is not a proj project (PROJ_ROOT=%s)", projectDir, projRoot())
	}
	if n := projTemplateCount(project.dir); n != 1 {
		t.Fatalf("project has %d templates, need exactly 1", n)
	}

	SetWorktreeBackend(WorktreeBackendProj)
	t.Cleanup(func() { SetWorktreeBackend(WorktreeBackendAuto) })

	name := fmt.Sprintf("adtest-%d", time.Now().UnixNano())
	branch := "agent-deck-e2e/" + name
	repoDir := filepath.Join(project.dir, ".repo")
	worktreePath := filepath.Join(project.dir, name)

	// Path generation and creation must agree, or every create silently falls
	// back to git.
	if got := GenerateWorktreePath(repoDir, branch, "subdirectory"); got != filepath.Join(project.dir, "agent-deck-e2e-"+name) {
		t.Logf("GenerateWorktreePath(%q) = %q", branch, got)
	}

	t.Cleanup(func() {
		if isDir(worktreePath) {
			if err := RemoveWorktree(repoDir, worktreePath, true); err != nil {
				t.Errorf("cleanup: RemoveWorktree: %v", err)
			}
		}
		_ = DeleteBranch(repoDir, branch, true)
	})

	start := time.Now()
	if err := CreateWorktree(repoDir, worktreePath, branch); err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	elapsed := time.Since(start)
	t.Logf("proj-backed worktree created in %s", elapsed)

	if !isDir(worktreePath) {
		t.Fatalf("worktree %s does not exist", worktreePath)
	}

	// A real linked worktree, not just a directory copy: this is what lets
	// agent-deck's existing removal and status code operate on it.
	if !IsLinkedWorktree(worktreePath) {
		t.Error("IsLinkedWorktree = false, want true")
	}

	// The branch agent-deck asked for is the branch checked out — proj's own
	// PROJ_PREFIX/<name> convention must not have overridden it.
	got, err := GetCurrentBranch(worktreePath)
	if err != nil {
		t.Fatalf("GetCurrentBranch: %v", err)
	}
	if got != branch {
		t.Errorf("branch = %q, want %q", got, branch)
	}

	// git itself must agree the worktree belongs to the repo.
	worktrees, err := ListWorktrees(repoDir)
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	found := false
	for _, wt := range worktrees {
		if resolved, _ := filepath.EvalSymlinks(wt.Path); resolved == worktreePath || wt.Path == worktreePath {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("worktree %s not in git's worktree list", worktreePath)
	}

	// The tree is actually populated (the reflink copy carried the template's
	// contents), not an empty checkout.
	entries, err := os.ReadDir(worktreePath)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) < 2 {
		t.Errorf("worktree looks empty: %d entries", len(entries))
	}

	// git status must work inside it — the index relocation proj does with
	// `git relocate` is the step most likely to leave a subtly broken worktree.
	if out, err := exec.Command("git", "-C", worktreePath, "status", "--porcelain").CombinedOutput(); err != nil {
		t.Errorf("git status in worktree failed: %s: %v", strings.TrimSpace(string(out)), err)
	}
}
