package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/vcs"
)

func cleanupFixture(t *testing.T) (main, unpushed, busy, empty string, worktrees []vcs.Worktree) {
	t.Helper()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	main = filepath.Join(root, "main")
	runFixtureGit(t, root, "init", "--bare", remote)
	runFixtureGit(t, root, "clone", remote, main)
	runFixtureGit(t, main, "config", "user.email", "fixture@example.invalid")
	runFixtureGit(t, main, "config", "user.name", "Fixture")
	if err := os.WriteFile(filepath.Join(main, "README"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, main, "add", "README")
	runFixtureGit(t, main, "commit", "-m", "base")
	runFixtureGit(t, main, "push", "-u", "origin", "HEAD")
	unpushed, busy, empty = filepath.Join(root, "unpushed"), filepath.Join(root, "busy"), filepath.Join(root, "empty")
	runFixtureGit(t, main, "worktree", "add", "-b", "unpushed", unpushed)
	runFixtureGit(t, main, "worktree", "add", "-b", "busy", busy)
	runFixtureGit(t, main, "worktree", "add", "-b", "empty", empty)
	if err := os.WriteFile(filepath.Join(unpushed, "work"), []byte("precious\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, unpushed, "add", "work")
	runFixtureGit(t, unpushed, "commit", "-m", "unpushed work")
	worktrees = []vcs.Worktree{{Path: main, Branch: "main"}, {Path: unpushed, Branch: "unpushed"}, {Path: busy, Branch: "busy"}, {Path: empty, Branch: "empty"}}
	return
}

func runFixtureGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func factsByPath(facts []worktreeCleanupFacts) map[string]worktreeCleanupFacts {
	m := make(map[string]worktreeCleanupFacts, len(facts))
	for _, f := range facts {
		m[f.Worktree.Path] = f
	}
	return m
}

func pathsContain(worktrees []vcs.Worktree, path string) bool {
	for _, wt := range worktrees {
		if wt.Path == path {
			return true
		}
	}
	return false
}

func waitForFixtureCWD(t *testing.T, pid int, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		got, err := processWithCWDInside(path)
		if err != nil {
			t.Fatal(err)
		}
		if got == pid {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("process %d cwd was not observed", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCleanupExcludesUnpushedCommit(t *testing.T) {
	_, unpushed, _, empty, worktrees := cleanupFixture(t)
	orphans, protected := classifyUnregisteredWorktrees(worktrees, nil)
	if pathsContain(orphans, unpushed) || !pathsContain(orphans, empty) {
		t.Fatalf("orphans = %+v, must exclude %s and include %s", orphans, unpushed, empty)
	}
	if got := factsByPath(protected)[unpushed].Unpushed; got != 1 {
		t.Fatalf("unpushed count = %d, want 1", got)
	}
}

func TestCleanupExcludesUncommittedChanges(t *testing.T) {
	_, _, _, empty, worktrees := cleanupFixture(t)
	dirty := worktrees[2].Path
	if err := os.WriteFile(filepath.Join(dirty, "uncommitted"), []byte("precious\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	orphans, protected := classifyUnregisteredWorktrees(worktrees, nil)
	if pathsContain(orphans, dirty) || !pathContainsFacts(protected, dirty) {
		t.Fatalf("dirty worktree was not protected: orphans=%+v protected=%+v", orphans, protected)
	}
	if !factsByPath(protected)[dirty].Dirty {
		t.Fatal("dirty fact was not surfaced")
	}
	if !pathsContain(orphans, empty) {
		t.Fatalf("empty worktree %s was not an orphan", empty)
	}
}

func pathContainsFacts(facts []worktreeCleanupFacts, path string) bool {
	_, ok := factsByPath(facts)[path]
	return ok
}

func TestCleanupExcludesLiveProcessCWDInside(t *testing.T) {
	_, _, busy, empty, worktrees := cleanupFixture(t)
	cmd := exec.Command("sh", "-c", fmt.Sprintf("cd %q && exec sleep 30", busy))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	waitForFixtureCWD(t, cmd.Process.Pid, busy)
	orphans, protected := classifyUnregisteredWorktrees(worktrees, nil)
	if len(orphans) != 1 || orphans[0].Path != empty {
		t.Fatalf("orphans = %+v, want only %s", orphans, empty)
	}
	if got := factsByPath(protected)[busy].LivePID; got != cmd.Process.Pid {
		t.Fatalf("live pid = %d, want %d", got, cmd.Process.Pid)
	}
}

func TestCleanupForceCannotOverrideRealityExclusions(t *testing.T) {
	_, unpushed, busy, empty, worktrees := cleanupFixture(t)
	cmd := exec.Command("sh", "-c", fmt.Sprintf("cd %q && exec sleep 30", busy))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	waitForFixtureCWD(t, cmd.Process.Pid, busy)
	// --force is intentionally absent from classification: it acts only after
	// this immutable deletion set has been constructed.
	orphans, protected := classifyUnregisteredWorktrees(worktrees, nil)
	if len(orphans) != 1 || orphans[0].Path != empty {
		t.Fatalf("force deletion set = %+v, want only %s", orphans, empty)
	}
	got := factsByPath(protected)
	if _, ok := got[unpushed]; !ok {
		t.Fatal("--force could reach unpushed worktree")
	}
	if _, ok := got[busy]; !ok {
		t.Fatal("--force could reach live-process worktree")
	}
}
