package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	if got := factsByPath(protected)[unpushed].Unpushed; got == nil || *got != 1 {
		t.Fatalf("unpushed count = %v, want 1", got)
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
	if got := factsByPath(protected)[dirty].Dirty; got == nil || !*got {
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
	if got := factsByPath(protected)[busy].LivePID; got == nil || *got != cmd.Process.Pid {
		t.Fatalf("live pid = %v, want %d", got, cmd.Process.Pid)
	}
}

func TestCleanupSelfProbeHelper(t *testing.T) {
	path := os.Getenv("AGENTDECK_TEST_SELF_CWD")
	if path == "" {
		return
	}
	if err := os.Chdir(path); err != nil {
		t.Fatal(err)
	}
	pid, err := processWithCWDInside(path)
	if err != nil {
		t.Fatal(err)
	}
	if pid != os.Getpid() {
		t.Fatalf("self cwd pid = %d, want %d", pid, os.Getpid())
	}
}

// Regression for E1: cleanup itself is a live cwd-inside process and must veto
// deleting the worktree from which it is directly executed.
func TestCleanupExcludesOwnProcessCWD(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestCleanupSelfProbeHelper$")
	cmd.Env = append(os.Environ(), "AGENTDECK_TEST_SELF_CWD="+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("self-cwd helper: %v\n%s", err, out)
	}
}

// Regression for E2: a candidate that becomes busy during confirmation is
// re-inspected and rejected at the removal boundary.
func TestCleanupRevalidatesRealityBeforeRemoval(t *testing.T) {
	_, _, busy, _, worktrees := cleanupFixture(t)
	initial := inspectWorktreeForCleanup(worktrees[2])
	if !initial.safeToRemove() {
		t.Fatalf("initial candidate unexpectedly protected: %s", initial.summary())
	}
	cmd := exec.Command("sh", "-c", fmt.Sprintf("cd %q && exec sleep 30", busy))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	waitForFixtureCWD(t, cmd.Process.Pid, busy)
	_, reason := revalidateCleanupCandidate(worktrees[2], worktrees, nil)
	if !strings.Contains(reason, fmt.Sprintf("pid %d", cmd.Process.Pid)) {
		t.Fatalf("revalidation reason = %q, want live pid", reason)
	}
}

// Regression for E3: registry occupancy through a symlink is the same path as
// Git's canonical worktree path.
func TestCleanupCanonicalizesSymlinkOccupancy(t *testing.T) {
	_, _, _, empty, worktrees := cleanupFixture(t)
	link := filepath.Join(filepath.Dir(empty), "empty-link")
	if err := os.Symlink(empty, link); err != nil {
		t.Fatal(err)
	}
	orphans, _ := classifyUnregisteredWorktrees(worktrees, map[string]bool{link: true})
	if pathsContain(orphans, empty) {
		t.Fatalf("symlink-occupied worktree %s entered orphan set", empty)
	}
}

// Regression for E4: fields after a failed probe were never observed and must
// not be serialized or described as empty.
func TestCleanupUnknownFactsAreNotEmpty(t *testing.T) {
	facts := inspectWorktreeForCleanup(vcs.Worktree{Path: filepath.Join(t.TempDir(), "missing"), Branch: "gone"})
	data := facts.jsonData()
	for _, key := range []string{"unpushed", "dirty", "live_pid"} {
		if _, exists := data[key]; exists {
			t.Fatalf("unknown field %q serialized as known: %#v", key, data)
		}
	}
	for _, want := range []string{"unpushed unknown", "dirty=unknown", "pid unknown"} {
		if !strings.Contains(facts.summary(), want) {
			t.Fatalf("summary %q missing %q", facts.summary(), want)
		}
	}
}

// Regression for E5: an inspection failure alone is a deletion veto. This
// fails if the InspectErr predicate is removed from safeToRemove.
func TestCleanupInspectionErrorFailsClosed(t *testing.T) {
	zero := 0
	clean := false
	facts := worktreeCleanupFacts{Unpushed: &zero, Dirty: &clean, LivePID: &zero, InspectErr: fmt.Errorf("probe failed")}
	if facts.safeToRemove() {
		t.Fatal("inspection error was treated as permission to remove")
	}
}

// Regression for E6: after another cleanup wins, the absent worktree is a
// skipped candidate rather than a second successful removal.
func TestCleanupConcurrentLoserSkipsMissingWorktree(t *testing.T) {
	main, _, _, empty, worktrees := cleanupFixture(t)
	backend, err := detectAndCreateBackend(main)
	if err != nil {
		t.Fatal(err)
	}
	candidate := worktrees[3]
	type result struct {
		removed bool
		reason  string
		err     error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for range 2 {
		go func() {
			<-start
			removed, reason, err := removeCleanupCandidate(backend, candidate, func() (map[string]bool, error) { return nil, nil })
			results <- result{removed: removed, reason: reason, err: err}
		}()
	}
	close(start)
	first, second := <-results, <-results
	removedCount := 0
	loserReason := ""
	for _, got := range []result{first, second} {
		if got.err != nil {
			t.Fatalf("concurrent cleanup error: %v (%s)", got.err, got.reason)
		}
		if got.removed {
			removedCount++
		} else {
			loserReason = got.reason
		}
	}
	if removedCount != 1 || loserReason != "no longer registered as a worktree" {
		t.Fatalf("removed=%d loser reason=%q; both calls must not report success for %s", removedCount, loserReason, empty)
	}
}

type cleanupRemovalSpyBackend struct {
	vcs.Backend
	removeCalls int
}

func (b *cleanupRemovalSpyBackend) RemoveWorktree(path string, force bool) error {
	b.removeCalls++
	return b.Backend.RemoveWorktree(path, force)
}

// Regression for E7: a session can claim a candidate while cleanup waits for
// confirmation. Fresh canonical occupancy must veto the destructive backend
// call even though the worktree itself is still clean and present.
func TestCleanupDeletionTimeSessionOwnershipVeto(t *testing.T) {
	main, _, _, _, worktrees := cleanupFixture(t)
	backend, err := detectAndCreateBackend(main)
	if err != nil {
		t.Fatal(err)
	}
	spy := &cleanupRemovalSpyBackend{Backend: backend}
	candidate := worktrees[3]
	removed, reason, err := removeCleanupCandidate(spy, candidate, func() (map[string]bool, error) {
		return map[string]bool{canonicalPathKey(candidate.Path): true}, nil
	})
	if err != nil {
		t.Fatalf("deletion-time ownership check: %v", err)
	}
	if removed {
		t.Fatal("session-owned worktree reported removed")
	}
	if reason != "now associated with a session" {
		t.Fatalf("reason = %q, want %q", reason, "now associated with a session")
	}
	if spy.removeCalls != 0 {
		t.Fatalf("backend removal called %d time(s), want 0", spy.removeCalls)
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
