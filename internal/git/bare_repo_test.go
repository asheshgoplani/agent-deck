package git

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// createBareRepoLayout builds the bare-repository layout described in issue #715:
//
//	projectRoot/
//	├── .bare/              (bare git dir, no working tree)
//	├── worktree-<n>/       (linked worktrees, all equal)
//	└── .agent-deck/        (optional — tests opt in when needed)
//
// Returns (projectRoot, bareDir, worktreePaths). Each worktree is on its own branch
// (main for the first, feature-<n> for subsequent ones) and has a real commit
// so `git worktree add` is accepted.
func createBareRepoLayout(t *testing.T, worktreeNames ...string) (projectRoot, bareDir string, worktrees []string) {
	t.Helper()

	projectRoot = t.TempDir()
	bareDir = filepath.Join(projectRoot, ".bare")

	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("git %s failed: %v (stderr: %s)", strings.Join(args, " "), err, stderr.String())
		}
	}

	run("init", "--bare", "-b", "main", bareDir)

	// Seed the bare repo with a real commit so worktree add can check out main.
	seedDir := t.TempDir()
	run("clone", bareDir, seedDir)
	runIn := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("git %s in %s failed: %v (stderr: %s)", strings.Join(args, " "), dir, err, stderr.String())
		}
	}
	runIn(seedDir, "config", "user.email", "test@test.com")
	runIn(seedDir, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(seedDir, "README.md"), []byte("# Bare test repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runIn(seedDir, "add", ".")
	runIn(seedDir, "commit", "-m", "initial")
	runIn(seedDir, "push", "origin", "main")
	// The seed clone is no longer needed.
	if err := os.RemoveAll(seedDir); err != nil {
		t.Logf("cleanup warning: %v", err)
	}

	if len(worktreeNames) == 0 {
		worktreeNames = []string{"worktree1"}
	}

	for i, name := range worktreeNames {
		wtPath := filepath.Join(projectRoot, name)
		if i == 0 {
			// First worktree checks out main.
			runIn(bareDir, "worktree", "add", wtPath, "main")
		} else {
			// Subsequent worktrees get a fresh branch.
			branch := "feature-" + name
			runIn(bareDir, "worktree", "add", "-b", branch, wtPath, "main")
		}
		worktrees = append(worktrees, wtPath)
	}

	return projectRoot, bareDir, worktrees
}

func TestIsBareRepo_TrueForBareDir(t *testing.T) {
	_, bareDir, _ := createBareRepoLayout(t, "worktree1")

	if !IsBareRepo(bareDir) {
		t.Errorf("IsBareRepo(%q) = false, want true", bareDir)
	}
}

func TestIsBareRepo_FalseForLinkedWorktree(t *testing.T) {
	_, _, worktrees := createBareRepoLayout(t, "worktree1")

	// A linked worktree is NOT itself bare — it has a working tree.
	if IsBareRepo(worktrees[0]) {
		t.Errorf("IsBareRepo(linked worktree %q) = true, want false", worktrees[0])
	}
}

func TestIsBareRepo_FalseForNormalRepo(t *testing.T) {
	dir := t.TempDir()
	createTestRepo(t, dir)

	if IsBareRepo(dir) {
		t.Errorf("IsBareRepo(normal repo %q) = true, want false", dir)
	}
}

func TestIsBareRepoWorktree_TrueForLinkedWorktree(t *testing.T) {
	_, _, worktrees := createBareRepoLayout(t, "worktree1", "worktree2", "worktree3")

	for _, wt := range worktrees {
		if !IsBareRepoWorktree(wt) {
			t.Errorf("IsBareRepoWorktree(%q) = false, want true", wt)
		}
	}
}

func TestIsBareRepoWorktree_FalseForNormalWorktree(t *testing.T) {
	dir := t.TempDir()
	createTestRepo(t, dir)

	wtPath := filepath.Join(t.TempDir(), "regular-wt")
	if err := CreateWorktree(dir, wtPath, "feature-regular"); err != nil {
		t.Fatalf("failed to create worktree: %v", err)
	}

	if IsBareRepoWorktree(wtPath) {
		t.Errorf("IsBareRepoWorktree(normal worktree %q) = true, want false", wtPath)
	}
}

// TestGetMainWorktreePath_BareRepo: in a bare-repo layout there is no "main"
// worktree — all linked worktrees are equal. The function must return the
// project root (parent of .bare/), which is where .agent-deck/ and other
// shared config live. Previously this returned the linked worktree's own path
// because the TrimSuffix-against-".git" branch never matched.
func TestGetMainWorktreePath_BareRepo(t *testing.T) {
	projectRoot, _, worktrees := createBareRepoLayout(t, "worktree1", "worktree2", "worktree3")

	expected, _ := filepath.EvalSymlinks(projectRoot)

	for _, wt := range worktrees {
		got, err := GetMainWorktreePath(wt)
		if err != nil {
			t.Fatalf("GetMainWorktreePath(%q) error: %v", wt, err)
		}
		resolved, _ := filepath.EvalSymlinks(got)
		if resolved != expected {
			t.Errorf("GetMainWorktreePath(%q) = %q, want %q (project root, parent of .bare)",
				wt, resolved, expected)
		}
	}
}

// TestGetWorktreeBaseRoot_BareRepo mirrors the above: callers use this to
// locate .agent-deck/ when spinning up sessions.
func TestGetWorktreeBaseRoot_BareRepo(t *testing.T) {
	projectRoot, _, worktrees := createBareRepoLayout(t, "worktree1", "worktree2", "worktree3")

	expected, _ := filepath.EvalSymlinks(projectRoot)

	for _, wt := range worktrees {
		got, err := GetWorktreeBaseRoot(wt)
		if err != nil {
			t.Fatalf("GetWorktreeBaseRoot(%q) error: %v", wt, err)
		}
		resolved, _ := filepath.EvalSymlinks(got)
		if resolved != expected {
			t.Errorf("GetWorktreeBaseRoot(%q) = %q, want %q",
				wt, resolved, expected)
		}
	}
}

// TestGetWorktreeBaseRoot_BareRepoFromProjectRoot: the user's `agent-deck launch`
// path may point at the project root itself (where .bare lives). The function
// must still resolve to the project root rather than erroring.
func TestGetWorktreeBaseRoot_BareRepoFromProjectRoot(t *testing.T) {
	projectRoot, _, _ := createBareRepoLayout(t, "worktree1")

	got, err := GetWorktreeBaseRoot(projectRoot)
	if err != nil {
		t.Fatalf("GetWorktreeBaseRoot(projectRoot %q) error: %v", projectRoot, err)
	}

	expected, _ := filepath.EvalSymlinks(projectRoot)
	resolved, _ := filepath.EvalSymlinks(got)

	if resolved != expected {
		t.Errorf("GetWorktreeBaseRoot(%q) = %q, want %q", projectRoot, resolved, expected)
	}
}

// TestFindWorktreeSetupScript_BareRepo: .agent-deck lives at the project root
// (alongside .bare/), not inside any individual linked worktree.
func TestFindWorktreeSetupScript_BareRepo(t *testing.T) {
	projectRoot, _, _ := createBareRepoLayout(t, "worktree1")

	scriptDir := filepath.Join(projectRoot, ".agent-deck")
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(scriptDir, "worktree-setup.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\necho hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := FindWorktreeSetupScript(projectRoot)
	if got != scriptPath {
		t.Errorf("FindWorktreeSetupScript(%q) = %q, want %q", projectRoot, got, scriptPath)
	}
}

// TestCreateWorktree_FromBareProjectRoot: the launch-cmd path passes the
// project root (as returned by GetWorktreeBaseRoot) to CreateWorktree. The
// project root has no .git — but .bare/ inside does. CreateWorktree must
// resolve transparently.
func TestCreateWorktree_FromBareProjectRoot(t *testing.T) {
	projectRoot, _, _ := createBareRepoLayout(t, "worktree1")

	newWorktreePath := filepath.Join(projectRoot, "worktree-new")
	if err := CreateWorktree(projectRoot, newWorktreePath, "feature-new"); err != nil {
		t.Fatalf("CreateWorktree(projectRoot=%q) failed: %v", projectRoot, err)
	}

	if _, err := os.Stat(filepath.Join(newWorktreePath, "README.md")); err != nil {
		t.Errorf("new worktree missing README.md: %v", err)
	}
}

// TestCreateWorktreeWithSetup_BareRepo: end-to-end check that ties together
// project-root resolution + setup-script discovery + worktree creation on a
// bare layout. Mirrors how launch_cmd.go invokes this.
func TestCreateWorktreeWithSetup_BareRepo(t *testing.T) {
	projectRoot, _, _ := createBareRepoLayout(t, "worktree1")

	// Stage a config file and setup script at project root.
	if err := os.WriteFile(filepath.Join(projectRoot, ".env.local"), []byte("SECRET=shh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	scriptDir := filepath.Join(projectRoot, ".agent-deck")
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
cp "$AGENT_DECK_REPO_ROOT/.env.local" "$AGENT_DECK_WORKTREE_PATH/.env.local"
echo "bare-setup done"
`
	if err := os.WriteFile(filepath.Join(scriptDir, "worktree-setup.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	newWorktreePath := filepath.Join(projectRoot, "worktree-feat")
	var stdout, stderr bytes.Buffer
	setupErr, err := CreateWorktreeWithSetup(projectRoot, newWorktreePath, "feature-bare-e2e", &stdout, &stderr, 0)
	if err != nil {
		t.Fatalf("CreateWorktreeWithSetup failed: %v (stderr: %s)", err, stderr.String())
	}
	if setupErr != nil {
		t.Fatalf("setup script errored: %v (stderr: %s)", setupErr, stderr.String())
	}

	if !strings.Contains(stdout.String(), "bare-setup done") {
		t.Errorf("expected stdout to contain 'bare-setup done', got %q", stdout.String())
	}

	data, err := os.ReadFile(filepath.Join(newWorktreePath, ".env.local"))
	if err != nil {
		t.Fatalf(".env.local not copied into new worktree: %v", err)
	}
	if string(data) != "SECRET=shh\n" {
		t.Errorf("unexpected .env.local content: %q", data)
	}
}

// TestListWorktrees_BareRepo: enumeration must work from the project root, not
// just from inside .bare/. This catches any path where the "default worktree"
// assumption leaks into listing.
func TestListWorktrees_BareRepo(t *testing.T) {
	projectRoot, _, _ := createBareRepoLayout(t, "worktree1", "worktree2", "worktree3")

	wts, err := ListWorktrees(projectRoot)
	if err != nil {
		t.Fatalf("ListWorktrees(projectRoot) failed: %v", err)
	}

	// Should enumerate the bare repo itself (marked Bare:true) plus 3 linked worktrees.
	var bareCount, linkedCount int
	for _, w := range wts {
		if w.Bare {
			bareCount++
		} else {
			linkedCount++
		}
	}
	if bareCount != 1 {
		t.Errorf("expected exactly 1 bare entry, got %d (%v)", bareCount, wts)
	}
	if linkedCount != 3 {
		t.Errorf("expected exactly 3 linked worktrees, got %d (%v)", linkedCount, wts)
	}
}

// TestBranchExists_FromBareProjectRoot: launch_cmd.go → main.go passes the
// project root (from GetWorktreeBaseRoot) to BranchExists. For bare layouts
// that's the parent of .bare/, which is not itself a git dir; the call must
// still resolve branches correctly.
func TestBranchExists_FromBareProjectRoot(t *testing.T) {
	projectRoot, _, _ := createBareRepoLayout(t, "worktree1", "worktree2")

	// main branch (from the seed commit) should exist.
	if !BranchExists(projectRoot, "main") {
		t.Errorf("BranchExists(%q, main) = false; want true", projectRoot)
	}
	// feature-worktree2 branch was auto-created by the fixture.
	if !BranchExists(projectRoot, "feature-worktree2") {
		t.Errorf("BranchExists(%q, feature-worktree2) = false; want true", projectRoot)
	}
	// Non-existent branches should report false, not error out.
	if BranchExists(projectRoot, "never-existed") {
		t.Errorf("BranchExists(%q, never-existed) = true; want false", projectRoot)
	}
}

// TestBareRepo_AllWorktreesEqual_NoDefaultAssumption: GetMainWorktreePath must
// return the same project root regardless of which linked worktree is queried.
// There is no "default" or "main" worktree in a bare layout.
func TestBareRepo_AllWorktreesEqual_NoDefaultAssumption(t *testing.T) {
	_, _, worktrees := createBareRepoLayout(t, "alpha", "bravo", "charlie")

	var first string
	for i, wt := range worktrees {
		got, err := GetMainWorktreePath(wt)
		if err != nil {
			t.Fatalf("GetMainWorktreePath(%q) error: %v", wt, err)
		}
		resolved, _ := filepath.EvalSymlinks(got)
		if i == 0 {
			first = resolved
			continue
		}
		if resolved != first {
			t.Errorf("worktree %d resolved to %q, but worktree 0 resolved to %q — must be identical", i, resolved, first)
		}
	}
}
