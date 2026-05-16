package git

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runIn/revParse helpers live in mergeback_bare_repo_test.go in the same package.

// createBareAtRootLayout builds the bare-at-root layout:
//
//	projectRoot/        (← the bare repo itself, e.g. "kslifeinc.git")
//	├── HEAD, config, objects/, refs/, packed-refs, worktrees/, ...
//	├── worktree-<n>/   (linked worktrees, all equal, as direct children)
//	└── .agent-deck/    (optional — tests opt in when needed)
//
// Distinct from createBareRepoLayout, which nests the bare dir as
// projectRoot/.bare/. Here the bare dir IS the project root, named with the
// conventional ".git" suffix to mirror real-world `git clone --bare` output.
//
// Returns (projectRoot=bareDir, worktreePaths). Each worktree gets its own
// branch (main for the first, feature-<name> for subsequent ones).
func createBareAtRootLayout(t *testing.T, worktreeNames ...string) (bareDir string, worktrees []string) {
	t.Helper()

	parent := t.TempDir()
	bareDir = filepath.Join(parent, "kslifeinc.git")

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
	if err := os.WriteFile(filepath.Join(seedDir, "README.md"), []byte("# Bare at root test repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runIn(seedDir, "add", ".")
	runIn(seedDir, "commit", "-m", "initial")
	runIn(seedDir, "push", "origin", "main")
	if err := os.RemoveAll(seedDir); err != nil {
		t.Logf("cleanup warning: %v", err)
	}

	if len(worktreeNames) == 0 {
		worktreeNames = []string{"worktree1"}
	}

	for i, name := range worktreeNames {
		wtPath := filepath.Join(bareDir, name)
		if i == 0 {
			runIn(bareDir, "worktree", "add", wtPath, "main")
		} else {
			branch := "feature-" + name
			runIn(bareDir, "worktree", "add", "-b", branch, wtPath, "main")
		}
		worktrees = append(worktrees, wtPath)
	}

	return bareDir, worktrees
}

func TestIsBareRepoAtRoot_TrueForAtRoot(t *testing.T) {
	bareDir, _ := createBareAtRootLayout(t, "worktree1")
	if !IsBareRepoAtRoot(bareDir) {
		t.Errorf("IsBareRepoAtRoot(%q) = false, want true", bareDir)
	}
}

func TestIsBareRepoAtRoot_FalseForDotBareLayout(t *testing.T) {
	_, bareDir, _ := createBareRepoLayout(t, "worktree1")
	if IsBareRepoAtRoot(bareDir) {
		t.Errorf("IsBareRepoAtRoot(.bare layout %q) = true, want false", bareDir)
	}
}

func TestIsBareRepoAtRoot_FalseForLinkedWorktree(t *testing.T) {
	_, worktrees := createBareAtRootLayout(t, "worktree1")
	if IsBareRepoAtRoot(worktrees[0]) {
		t.Errorf("IsBareRepoAtRoot(linked worktree %q) = true, want false", worktrees[0])
	}
}

func TestIsBareRepoAtRoot_FalseForNormalRepo(t *testing.T) {
	dir := t.TempDir()
	createTestRepo(t, dir)
	if IsBareRepoAtRoot(dir) {
		t.Errorf("IsBareRepoAtRoot(normal repo %q) = true, want false", dir)
	}
}

// IsBareRepoAtRoot must filter the same false-positive class that
// findNestedBareRepo addresses. `git rev-parse --is-bare-repository` walks
// up the tree, so any descendant of a bare repo (hooks/, objects/, refs/...)
// reports true. Pre-fix, IsBareRepoAtRoot used IsBareRepo and would have
// returned true for "/repo.git/hooks" (basename "hooks" ≠ ".bare", and
// IsBareRepo says it's bare). Post-fix it uses isBareRepoSelf and rejects.
func TestIsBareRepoAtRoot_FalseForInternalSubdirs(t *testing.T) {
	bareDir, _ := createBareAtRootLayout(t, "worktree1")
	for _, internal := range []string{"hooks", "objects", "refs", "info"} {
		sub := filepath.Join(bareDir, internal)
		if IsBareRepoAtRoot(sub) {
			t.Errorf("IsBareRepoAtRoot(%q) = true, want false (internal subdir of bare)", sub)
		}
	}
}

func TestIsBareRepoWorktree_TrueForAtRootLinkedWorktree(t *testing.T) {
	_, worktrees := createBareAtRootLayout(t, "worktree1", "worktree2")
	for _, wt := range worktrees {
		if !IsBareRepoWorktree(wt) {
			t.Errorf("IsBareRepoWorktree(%q) = false, want true", wt)
		}
	}
}

// In the at-root layout the bare repo dir is itself the project root.
// GetMainWorktreePath must return the bare dir, NOT its parent (the parent is
// just a generic dir that may hold many unrelated projects).
func TestGetMainWorktreePath_BareAtRoot(t *testing.T) {
	bareDir, worktrees := createBareAtRootLayout(t, "worktree1", "worktree2", "worktree3")
	expected, _ := filepath.EvalSymlinks(bareDir)

	for _, wt := range worktrees {
		got, err := GetMainWorktreePath(wt)
		if err != nil {
			t.Fatalf("GetMainWorktreePath(%q) error: %v", wt, err)
		}
		resolved, _ := filepath.EvalSymlinks(got)
		if resolved != expected {
			t.Errorf("GetMainWorktreePath(%q) = %q, want %q (bare dir itself)",
				wt, resolved, expected)
		}
	}
}

func TestGetWorktreeBaseRoot_BareAtRoot_FromLinkedWorktree(t *testing.T) {
	bareDir, worktrees := createBareAtRootLayout(t, "worktree1", "worktree2")
	expected, _ := filepath.EvalSymlinks(bareDir)

	for _, wt := range worktrees {
		got, err := GetWorktreeBaseRoot(wt)
		if err != nil {
			t.Fatalf("GetWorktreeBaseRoot(%q) error: %v", wt, err)
		}
		resolved, _ := filepath.EvalSymlinks(got)
		if resolved != expected {
			t.Errorf("GetWorktreeBaseRoot(%q) = %q, want %q", wt, resolved, expected)
		}
	}
}

// Called from the bare dir itself (no linked-worktree context). Previously
// fell through to `git rev-parse --show-toplevel`, which errors on bare repos.
func TestGetWorktreeBaseRoot_BareAtRoot_FromProjectRoot(t *testing.T) {
	bareDir, _ := createBareAtRootLayout(t, "worktree1")
	got, err := GetWorktreeBaseRoot(bareDir)
	if err != nil {
		t.Fatalf("GetWorktreeBaseRoot(bareDir %q) error: %v", bareDir, err)
	}
	expected, _ := filepath.EvalSymlinks(bareDir)
	resolved, _ := filepath.EvalSymlinks(got)
	if resolved != expected {
		t.Errorf("GetWorktreeBaseRoot(%q) = %q, want %q", bareDir, resolved, expected)
	}
}

// GetMainWorktreePath must return the same project root regardless of which
// linked worktree is queried — no worktree is "main" in this layout.
func TestBareAtRoot_AllWorktreesEqual(t *testing.T) {
	_, worktrees := createBareAtRootLayout(t, "alpha", "bravo", "charlie")

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

// In the at-root layout, sibling and subdirectory must both resolve to direct
// children of the bare dir — neither default makes sense when the project
// root IS the bare repo.
func TestGenerateWorktreePath_BareAtRoot(t *testing.T) {
	bareDir, _ := createBareAtRootLayout(t, "worktree1")

	want := filepath.Join(bareDir, "feat-x")

	for _, location := range []string{"", "sibling", "subdirectory"} {
		got := GenerateWorktreePath(bareDir, "feat-x", location)
		if got != want {
			t.Errorf("GenerateWorktreePath(bareDir, feat-x, %q) = %q, want %q", location, got, want)
		}
	}
}

// Sanity check: the at-root override is conditional, so the existing nested
// .bare/ layout must still honor sibling/subdirectory defaults.
func TestGenerateWorktreePath_NestedBare_UnchangedDefaults(t *testing.T) {
	_, bareDir, _ := createBareRepoLayout(t, "worktree1")
	parent := filepath.Dir(bareDir)

	gotSibling := GenerateWorktreePath(parent, "feat-x", "sibling")
	wantSibling := parent + "-feat-x"
	if gotSibling != wantSibling {
		t.Errorf("sibling: got %q, want %q", gotSibling, wantSibling)
	}

	gotSubdir := GenerateWorktreePath(parent, "feat-x", "subdirectory")
	wantSubdir := filepath.Join(parent, ".worktrees", "feat-x")
	if gotSubdir != wantSubdir {
		t.Errorf("subdirectory: got %q, want %q", gotSubdir, wantSubdir)
	}
}

func TestCreateWorktree_BareAtRoot(t *testing.T) {
	bareDir, _ := createBareAtRootLayout(t, "worktree1")
	newWT := filepath.Join(bareDir, "feature-new")

	if err := CreateWorktree(bareDir, newWT, "feature-new"); err != nil {
		t.Fatalf("CreateWorktree(bareDir=%q) failed: %v", bareDir, err)
	}
	if _, err := os.Stat(filepath.Join(newWT, "README.md")); err != nil {
		t.Errorf("new worktree missing README.md: %v", err)
	}
}

// End-to-end: project-root resolution + setup-script discovery + worktree
// creation in an at-root bare layout. Mirrors how launch_cmd.go drives it.
func TestCreateWorktreeWithSetup_BareAtRoot(t *testing.T) {
	bareDir, _ := createBareAtRootLayout(t, "worktree1")

	if err := os.WriteFile(filepath.Join(bareDir, ".env.local"), []byte("SECRET=shh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	scriptDir := filepath.Join(bareDir, ".agent-deck")
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
cp "$AGENT_DECK_REPO_ROOT/.env.local" "$AGENT_DECK_WORKTREE_PATH/.env.local"
echo "at-root-setup done"
`
	if err := os.WriteFile(filepath.Join(scriptDir, "worktree-setup.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	newWT := filepath.Join(bareDir, "worktree-feat")
	var stdout, stderr bytes.Buffer
	setupErr, err := CreateWorktreeWithSetup(bareDir, newWT, "feature-at-root-e2e", &stdout, &stderr, 0)
	if err != nil {
		t.Fatalf("CreateWorktreeWithSetup failed: %v (stderr: %s)", err, stderr.String())
	}
	if setupErr != nil {
		t.Fatalf("setup script errored: %v (stderr: %s)", setupErr, stderr.String())
	}

	if !strings.Contains(stdout.String(), "at-root-setup done") {
		t.Errorf("expected stdout to contain 'at-root-setup done', got %q", stdout.String())
	}
	data, err := os.ReadFile(filepath.Join(newWT, ".env.local"))
	if err != nil {
		t.Fatalf(".env.local not copied into new worktree: %v", err)
	}
	if string(data) != "SECRET=shh\n" {
		t.Errorf("unexpected .env.local content: %q", data)
	}
}

func TestFindWorktreeSetupScript_BareAtRoot(t *testing.T) {
	bareDir, _ := createBareAtRootLayout(t, "worktree1")
	scriptDir := filepath.Join(bareDir, ".agent-deck")
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(scriptDir, "worktree-setup.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\necho hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Discovery must work both from the bare dir itself and via the public
	// resolver path that callers reach (GetWorktreeBaseRoot → FindWorktree…).
	got, _ := FindWorktreeSetupScript(bareDir)
	if got != scriptPath {
		t.Errorf("FindWorktreeSetupScript(%q) = %q, want %q", bareDir, got, scriptPath)
	}
}

func TestListWorktrees_BareAtRoot(t *testing.T) {
	bareDir, _ := createBareAtRootLayout(t, "worktree1", "worktree2", "worktree3")

	wts, err := ListWorktrees(bareDir)
	if err != nil {
		t.Fatalf("ListWorktrees(bareDir) failed: %v", err)
	}

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

func TestBranchExists_BareAtRoot_FromProjectRoot(t *testing.T) {
	bareDir, _ := createBareAtRootLayout(t, "worktree1", "worktree2")

	if !BranchExists(bareDir, "main") {
		t.Errorf("BranchExists(%q, main) = false; want true", bareDir)
	}
	if !BranchExists(bareDir, "feature-worktree2") {
		t.Errorf("BranchExists(%q, feature-worktree2) = false; want true", bareDir)
	}
	if BranchExists(bareDir, "never-existed") {
		t.Errorf("BranchExists(%q, never-existed) = true; want false", bareDir)
	}
}

// WorktreePath is the launch_cmd.go entry point. These tests pin the
// interactions between the user-configurable Template/Location knobs and
// the new at-root override.

// A non-empty Template must take precedence over the at-root override.
// This is the documented escape hatch for users who want a fully custom path.
func TestWorktreePath_BareAtRoot_TemplateWins(t *testing.T) {
	bareDir, _ := createBareAtRootLayout(t, "worktree1")

	got := WorktreePath(WorktreePathOptions{
		Branch:    "feat-x",
		Location:  "sibling",
		RepoDir:   bareDir,
		SessionID: "deadbeef",
		Template:  "{repo-root}/custom/{branch}-{session-id}",
	})
	want := filepath.Join(bareDir, "custom", "feat-x-deadbeef")
	if got != want {
		t.Errorf("WorktreePath with template: got %q, want %q", got, want)
	}
}

// Empty Template (the common case) must hit the at-root override regardless
// of which Location value the user has configured.
func TestWorktreePath_BareAtRoot_LocationOverrides(t *testing.T) {
	bareDir, _ := createBareAtRootLayout(t, "worktree1")

	for _, location := range []string{"", "sibling", "subdirectory"} {
		got := WorktreePath(WorktreePathOptions{
			Branch:   "feat-x",
			Location: location,
			RepoDir:  bareDir,
		})
		want := filepath.Join(bareDir, "feat-x")
		if got != want {
			t.Errorf("WorktreePath Location=%q: got %q, want %q", location, got, want)
		}
	}
}

// A custom path Location (contains "/" or starts with "~") must take
// precedence over the at-root override — same as it does for normal repos.
func TestWorktreePath_BareAtRoot_CustomPathWins(t *testing.T) {
	bareDir, _ := createBareAtRootLayout(t, "worktree1")
	repoName := filepath.Base(bareDir)
	target := t.TempDir()

	got := WorktreePath(WorktreePathOptions{
		Branch:   "feat-x",
		Location: target,
		RepoDir:  bareDir,
	})
	want := filepath.Join(target, repoName, "feat-x")
	if got != want {
		t.Errorf("WorktreePath custom path: got %q, want %q", got, want)
	}
}

// Branch sanitization (slashes, spaces) must still apply in the at-root
// override branch.
func TestWorktreePath_BareAtRoot_BranchSanitization(t *testing.T) {
	bareDir, _ := createBareAtRootLayout(t, "worktree1")

	got := WorktreePath(WorktreePathOptions{
		Branch:   "feature/with spaces",
		Location: "sibling",
		RepoDir:  bareDir,
	})
	want := filepath.Join(bareDir, "feature-with-spaces")
	if got != want {
		t.Errorf("branch sanitization: got %q, want %q", got, want)
	}
}

// MergeBack must work in the at-root layout: fast-forward (update-ref on
// the bare dir) and non-FF (throwaway worktree merge). Mirrors
// TestWorktree_MergeBack_BareRepo_RegressionFor891 for the .bare/ layout.

func TestMergeBack_BareAtRoot_FastForward(t *testing.T) {
	bareDir, worktrees := createBareAtRootLayout(t, "main-wt", "feature-wt")
	featureWT := worktrees[1]
	featureBranch := "feature-feature-wt"

	runIn(t, featureWT, "config", "user.email", "test@test.com")
	runIn(t, featureWT, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(featureWT, "feature.txt"), []byte("from feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runIn(t, featureWT, "add", "feature.txt")
	runIn(t, featureWT, "commit", "-m", "add feature.txt")

	featureSHA := revParse(t, featureWT, "HEAD")
	mainSHABefore := revParse(t, bareDir, "main")
	if featureSHA == mainSHABefore {
		t.Fatalf("test setup: feature SHA == main SHA")
	}

	// Pass the bare dir itself as the project root — that's what
	// GetWorktreeBaseRoot now returns for at-root.
	if err := MergeBack(bareDir, featureBranch, "main"); err != nil {
		t.Fatalf("MergeBack failed in at-root layout: %v", err)
	}
	if got := revParse(t, bareDir, "main"); got != featureSHA {
		t.Errorf("after MergeBack, main = %s, want %s", got, featureSHA)
	}
}

// Note: a non-FF MergeBack in a bare layout where main is already checked
// out in a linked worktree is a pre-existing limitation of mergeBackInBareRepo
// — the throwaway-worktree fallback can't add another checkout of main. This
// is shared with the .bare/ layout (the existing #891 regression test also
// only covers the FF case). Out of scope for this change.

// findNestedBareRepo must NOT report a bare-at-root dir as nesting a bare
// repo inside itself (git's internal `worktrees/` dir would otherwise trip
// IsBareRepo via parent discovery and be misreported).
func TestFindNestedBareRepo_BareAtRoot_ReturnsEmpty(t *testing.T) {
	bareDir, _ := createBareAtRootLayout(t, "worktree1")

	if got := findNestedBareRepo(bareDir); got != "" {
		t.Errorf("findNestedBareRepo(%q) = %q, want \"\" (bare dir is itself the project root, not nesting a bare repo)", bareDir, got)
	}
}

// A parent dir containing a bare-at-root project should still resolve via
// the nested-bare detection path. Verifies the at-root project is reachable
// from one level up (the "agent-deck launch /parent/" use case).
func TestFindNestedBareRepo_ParentOfBareAtRoot(t *testing.T) {
	bareDir, _ := createBareAtRootLayout(t, "worktree1")
	parent := filepath.Dir(bareDir)

	got := findNestedBareRepo(parent)
	if got == "" {
		t.Fatalf("findNestedBareRepo(parent %q) = \"\"; want %q", parent, bareDir)
	}
	resolvedGot, _ := filepath.EvalSymlinks(got)
	resolvedWant, _ := filepath.EvalSymlinks(bareDir)
	if resolvedGot != resolvedWant {
		t.Errorf("findNestedBareRepo(parent) = %q, want %q", resolvedGot, resolvedWant)
	}
}

// RemoveWorktree must work in the at-root layout. Without the at-root fix,
// this path would have routed through callers that resolved the project
// root to a stranger dir; we lock in correct behavior here.
func TestRemoveWorktree_BareAtRoot(t *testing.T) {
	bareDir, worktrees := createBareAtRootLayout(t, "main-wt", "feature-wt")
	featureWT := worktrees[1]

	if err := RemoveWorktree(bareDir, featureWT, false); err != nil {
		t.Fatalf("RemoveWorktree(force=false) failed: %v", err)
	}
	if _, err := os.Stat(featureWT); !os.IsNotExist(err) {
		t.Errorf("worktree dir still exists after RemoveWorktree: err=%v", err)
	}

	// The other worktree must remain untouched.
	if _, err := os.Stat(worktrees[0]); err != nil {
		t.Errorf("untouched worktree disappeared: %v", err)
	}

	// Bare repo internals must still be intact.
	for _, internal := range []string{"HEAD", "objects", "refs"} {
		if _, err := os.Stat(filepath.Join(bareDir, internal)); err != nil {
			t.Errorf("bare repo internal %q damaged: %v", internal, err)
		}
	}
}

// RemoveWorktree with force=true exercises the os.RemoveAll fallback.
// In at-root the worktree dir is inside the bare repo, so the isGitDir
// safety guard must not mistakenly refuse — the linked worktree's dir
// is not itself a git dir (worktree gitdir lives in bareDir/worktrees/<name>).
func TestRemoveWorktree_BareAtRoot_Force(t *testing.T) {
	bareDir, worktrees := createBareAtRootLayout(t, "main-wt", "feature-wt")
	featureWT := worktrees[1]

	// Drop an untracked file so `git worktree remove` would normally refuse.
	if err := os.WriteFile(filepath.Join(featureWT, "untracked.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := RemoveWorktree(bareDir, featureWT, true); err != nil {
		t.Fatalf("RemoveWorktree(force=true) failed: %v", err)
	}
	if _, err := os.Stat(featureWT); !os.IsNotExist(err) {
		t.Errorf("worktree dir still exists after force RemoveWorktree: err=%v", err)
	}
	// Bare repo must survive.
	if !IsBareRepo(bareDir) {
		t.Errorf("bare repo no longer recognized after force removal")
	}
}

// IsBareRepoAtRoot must not crash on a non-existent path or a path that
// isn't a git repo at all. Used as a guard before generating paths in
// GenerateWorktreePath, which is invoked with arbitrary user paths.
func TestIsBareRepoAtRoot_SafeOnNonExistentPaths(t *testing.T) {
	if IsBareRepoAtRoot("/this/path/does/not/exist/agent-deck") {
		t.Errorf("IsBareRepoAtRoot(nonexistent) returned true")
	}
	if IsBareRepoAtRoot(t.TempDir()) {
		t.Errorf("IsBareRepoAtRoot(empty tempdir) returned true")
	}
}

// isBareRepoSelf must distinguish "the bare repo itself" from "any descendant
// of a bare repo." This is the strict-self contract that findNestedBareRepo
// relies on so it doesn't misidentify internal git subdirs (hooks/, objects/,
// refs/) as the nested bare repo.
func TestIsBareRepoSelf(t *testing.T) {
	bareDir, worktrees := createBareAtRootLayout(t, "worktree1")

	// True: the bare repo dir itself.
	if !isBareRepoSelf(bareDir) {
		t.Errorf("isBareRepoSelf(bareDir %q) = false, want true", bareDir)
	}

	// False for git's internal subdirs of a bare repo. IsBareRepo returns
	// true for these via parent discovery — the whole point of this helper
	// is to filter them out.
	for _, internal := range []string{"hooks", "objects", "refs", "info"} {
		sub := filepath.Join(bareDir, internal)
		if isBareRepoSelf(sub) {
			t.Errorf("isBareRepoSelf(%q) = true, want false (internal subdir of bare)", sub)
		}
	}

	// False for a linked worktree (it's a working tree, not a bare repo).
	if isBareRepoSelf(worktrees[0]) {
		t.Errorf("isBareRepoSelf(worktree %q) = true, want false", worktrees[0])
	}

	// False for non-git paths.
	if isBareRepoSelf(t.TempDir()) {
		t.Errorf("isBareRepoSelf(empty tempdir) = true, want false")
	}

	// False for the conventional nested layout's bare dir as well — it IS
	// a bare repo, so should also return true. Sanity check.
	_, dotBareDir, _ := createBareRepoLayout(t, "worktree1")
	if !isBareRepoSelf(dotBareDir) {
		t.Errorf("isBareRepoSelf(.bare dir %q) = false, want true", dotBareDir)
	}
}

// IsGitRepoOrBareProjectRoot is the entry-point guard used by launch_cmd.go
// and worktree_cmd.go to reject "not a git repo" inputs. Must accept all
// three valid handles into an at-root layout: the bare dir, a linked worktree
// inside it, and the parent dir (via findNestedBareRepo fallback).
func TestIsGitRepoOrBareProjectRoot_BareAtRoot(t *testing.T) {
	bareDir, worktrees := createBareAtRootLayout(t, "worktree1")
	parent := filepath.Dir(bareDir)

	for _, p := range []string{bareDir, worktrees[0], parent} {
		if !IsGitRepoOrBareProjectRoot(p) {
			t.Errorf("IsGitRepoOrBareProjectRoot(%q) = false, want true", p)
		}
	}

	// Negative case: an unrelated empty dir must NOT be treated as a project root.
	other := t.TempDir()
	if IsGitRepoOrBareProjectRoot(other) {
		t.Errorf("IsGitRepoOrBareProjectRoot(empty %q) = true, want false", other)
	}
}
