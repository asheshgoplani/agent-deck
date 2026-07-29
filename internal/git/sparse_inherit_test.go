package git

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Issue #1708: creating a worktree from a session that lives in a SPARSE linked
// worktree used to materialize the entire repository, because agent-deck
// normalizes the invoking directory to the repository base root before calling
// `git worktree add` — and sparse-checkout state is per-worktree, so git copies
// the (usually non-sparse) MAIN worktree's state instead.
//
// The fixture below reproduces exactly that shape:
//
//	base/        main worktree on `main`, NOT sparse, contains keep/ and heavy/
//	<base>-sparse/  linked worktree on `sparse-src`, cone-sparse on keep/
//
// so every test can assert against the two observable outcomes: heavy/ present
// (full checkout) or heavy/ absent (sparsity inherited).

const sparseFixtureExcludedDir = "heavy"

// newSparseFixture returns the non-sparse base worktree and a cone-sparse
// linked worktree of the same repo.
func newSparseFixture(t *testing.T) (base, sparseWT string) {
	t.Helper()
	base = t.TempDir()
	createTestRepo(t, base)

	writeSparseFixtureTree(t, base)
	runGit(t, base, "add", ".")
	runGit(t, base, "commit", "-m", "add keep/ and heavy/")

	sparseWT = filepath.Join(t.TempDir(), "sparse-src")
	runGit(t, base, "worktree", "add", sparseWT, "-b", "sparse-src")
	runGit(t, sparseWT, "sparse-checkout", "set", "keep")

	// Guard the premise of every assertion below: the source is sparse, the
	// base worktree is not.
	if dirExists(filepath.Join(sparseWT, sparseFixtureExcludedDir)) {
		t.Fatalf("fixture: %s should be excluded from the sparse source worktree", sparseFixtureExcludedDir)
	}
	if !dirExists(filepath.Join(base, sparseFixtureExcludedDir)) {
		t.Fatalf("fixture: base worktree should be a full checkout")
	}
	return base, sparseWT
}

func writeSparseFixtureTree(t *testing.T, dir string) {
	t.Helper()
	for _, rel := range []string{
		filepath.Join("keep", "keep.txt"),
		filepath.Join(sparseFixtureExcludedDir, "heavy.txt"),
	} {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte(rel), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// assertSparseWorktree pins the full observable contract of an inherited
// worktree: excluded paths were never written, included ones were, and the
// worktree's own sparse config matches what was replayed.
func assertSparseWorktree(t *testing.T, worktreePath string, wantCone bool, wantPatterns []string) {
	t.Helper()
	if dirExists(filepath.Join(worktreePath, sparseFixtureExcludedDir)) {
		t.Errorf("%s/ was materialized: sparse checkout was not inherited", sparseFixtureExcludedDir)
	}
	if _, err := os.Stat(filepath.Join(worktreePath, "keep", "keep.txt")); err != nil {
		t.Errorf("keep/keep.txt missing from the sparse worktree: %v", err)
	}
	if !gitConfigBool(worktreePath, "core.sparseCheckout") {
		t.Error("core.sparseCheckout is not set in the new worktree")
	}
	if got := gitConfigBool(worktreePath, "core.sparseCheckoutCone"); got != wantCone {
		t.Errorf("core.sparseCheckoutCone = %v, want %v", got, wantCone)
	}
	if got := runGit(t, worktreePath, "sparse-checkout", "list"); got != strings.Join(wantPatterns, "\n") {
		t.Errorf("sparse-checkout list = %q, want %q", got, strings.Join(wantPatterns, "\n"))
	}
	// A worktree left un-materialized (patterns installed but never checked
	// out) reports every tracked path as deleted — the failure mode that makes
	// the trailing `git checkout` in ApplySparseCheckout load-bearing.
	if status := runGit(t, worktreePath, "status", "--porcelain"); status != "" {
		t.Errorf("new worktree is not clean, sparse materialization incomplete:\n%s", status)
	}
}

func TestCaptureSparseCheckout_ConeSource(t *testing.T) {
	_, sparseWT := newSparseFixture(t)

	got := CaptureSparseCheckout(sparseWT)
	if !got.Enabled {
		t.Fatal("Enabled = false for a cone-sparse worktree")
	}
	if !got.Cone {
		t.Error("Cone = false for a cone-mode worktree")
	}
	if got.SparseIndex {
		t.Error("SparseIndex = true although index.sparse is unset")
	}
	if len(got.Patterns) != 1 || got.Patterns[0] != "keep" {
		t.Errorf("Patterns = %q, want [keep]", got.Patterns)
	}
}

func TestCaptureSparseCheckout_ConeSourceWithSparseIndex(t *testing.T) {
	_, sparseWT := newSparseFixture(t)
	runGit(t, sparseWT, "sparse-checkout", "set", "--cone", "--sparse-index", "keep")

	got := CaptureSparseCheckout(sparseWT)
	if !got.Enabled || !got.Cone {
		t.Fatalf("expected an enabled cone source, got %+v", got)
	}
	if !got.SparseIndex {
		t.Error("SparseIndex = false although index.sparse is set on the source")
	}
}

// A sparse index is a cone-mode-only feature, so a non-cone source must never
// hand `--sparse-index` to the new worktree even if index.sparse is set.
func TestCaptureSparseCheckout_NonConeSourceNeverInheritsSparseIndex(t *testing.T) {
	_, sparseWT := newSparseFixture(t)
	runGit(t, sparseWT, "sparse-checkout", "set", "--no-cone", "/keep/")
	runGit(t, sparseWT, "config", "index.sparse", "true")

	got := CaptureSparseCheckout(sparseWT)
	if !got.Enabled {
		t.Fatal("Enabled = false for a non-cone sparse worktree")
	}
	if got.Cone {
		t.Error("Cone = true for a --no-cone worktree")
	}
	if got.SparseIndex {
		t.Error("SparseIndex = true for a non-cone source")
	}
	if len(got.Patterns) != 1 || got.Patterns[0] != "/keep/" {
		t.Errorf("Patterns = %q, want [/keep/]", got.Patterns)
	}
}

// Every "nothing to inherit" input must degrade to the zero value so callers
// fall back to git's normal checkout instead of failing.
func TestCaptureSparseCheckout_NonSparseAndInvalidSourcesYieldZero(t *testing.T) {
	base, _ := newSparseFixture(t)
	bare := filepath.Join(t.TempDir(), "bare.git")
	runGit(t, t.TempDir(), "init", "--bare", "-b", "main", bare)

	for name, dir := range map[string]string{
		"empty string":          "",
		"non-sparse worktree":   base,
		"not a repository":      t.TempDir(),
		"bare repo (no wtree)":  bare,
		"nonexistent directory": filepath.Join(t.TempDir(), "missing"),
	} {
		t.Run(name, func(t *testing.T) {
			if got := CaptureSparseCheckout(dir); got.Enabled {
				t.Errorf("CaptureSparseCheckout(%q) = %+v, want disabled", dir, got)
			}
		})
	}
}

// The headline behavior: creating from the base root while pointing sparse
// inheritance at the invoking (sparse) worktree must not materialize the
// excluded tree.
func TestCreateWorktreeWithOptions_InheritsConeSparseFromInvokingWorktree(t *testing.T) {
	base, sparseWT := newSparseFixture(t)
	newWT := filepath.Join(t.TempDir(), "inherited")

	if err := CreateWorktreeWithOptions(base, newWT, "feature/inherit-cone", SparseInheritOptions(true, sparseWT)); err != nil {
		t.Fatalf("CreateWorktreeWithOptions: %v", err)
	}
	assertSparseWorktree(t, newWT, true, []string{"keep"})
}

func TestCreateWorktreeWithOptions_InheritsNonConeSparse(t *testing.T) {
	base, sparseWT := newSparseFixture(t)
	runGit(t, sparseWT, "sparse-checkout", "set", "--no-cone", "/keep/")
	newWT := filepath.Join(t.TempDir(), "inherited-nocone")

	if err := CreateWorktreeWithOptions(base, newWT, "feature/inherit-nocone", SparseInheritOptions(true, sparseWT)); err != nil {
		t.Fatalf("CreateWorktreeWithOptions: %v", err)
	}
	assertSparseWorktree(t, newWT, false, []string{"/keep/"})
}

func TestCreateWorktreeWithOptions_InheritsSparseIndex(t *testing.T) {
	base, sparseWT := newSparseFixture(t)
	runGit(t, sparseWT, "sparse-checkout", "set", "--cone", "--sparse-index", "keep")
	newWT := filepath.Join(t.TempDir(), "inherited-sparse-index")

	if err := CreateWorktreeWithOptions(base, newWT, "feature/inherit-sparse-index", SparseInheritOptions(true, sparseWT)); err != nil {
		t.Fatalf("CreateWorktreeWithOptions: %v", err)
	}
	assertSparseWorktree(t, newWT, true, []string{"keep"})
	if !gitConfigBool(newWT, "index.sparse") {
		t.Error("index.sparse was not inherited by the new worktree")
	}
}

// Default behavior must be byte-for-byte unchanged: unset config (zero options)
// and an explicit off both keep git's full checkout, even from a sparse source.
func TestCreateWorktree_DefaultKeepsFullCheckoutFromSparseSource(t *testing.T) {
	base, sparseWT := newSparseFixture(t)

	t.Run("legacy CreateWorktree", func(t *testing.T) {
		newWT := filepath.Join(t.TempDir(), "legacy")
		if err := CreateWorktree(base, newWT, "feature/legacy"); err != nil {
			t.Fatalf("CreateWorktree: %v", err)
		}
		if !dirExists(filepath.Join(newWT, sparseFixtureExcludedDir)) {
			t.Errorf("%s/ missing: default worktree creation changed behavior", sparseFixtureExcludedDir)
		}
	})

	t.Run("inheritance switched off", func(t *testing.T) {
		newWT := filepath.Join(t.TempDir(), "off")
		if err := CreateWorktreeWithOptions(base, newWT, "feature/off", SparseInheritOptions(false, sparseWT)); err != nil {
			t.Fatalf("CreateWorktreeWithOptions: %v", err)
		}
		if !dirExists(filepath.Join(newWT, sparseFixtureExcludedDir)) {
			t.Errorf("%s/ missing: sparse_checkout = off must not change checkout", sparseFixtureExcludedDir)
		}
	})
}

// Inheritance on + a non-sparse source is the common case for most users; it
// must behave exactly like today.
func TestCreateWorktreeWithOptions_NonSparseSourceFallsBackToFullCheckout(t *testing.T) {
	base, _ := newSparseFixture(t)
	newWT := filepath.Join(t.TempDir(), "from-non-sparse")

	if err := CreateWorktreeWithOptions(base, newWT, "feature/non-sparse-source", SparseInheritOptions(true, base)); err != nil {
		t.Fatalf("CreateWorktreeWithOptions: %v", err)
	}
	if !dirExists(filepath.Join(newWT, sparseFixtureExcludedDir)) {
		t.Errorf("%s/ missing: a non-sparse source must produce a full checkout", sparseFixtureExcludedDir)
	}
	if gitConfigBool(newWT, "core.sparseCheckout") {
		t.Error("core.sparseCheckout set although the source is not sparse")
	}
}

// All three branch-resolution modes keep working with inheritance active:
// brand-new branch, pre-existing local branch, and remote-tracking branch.
func TestCreateWorktreeWithOptions_AllBranchResolutionModes(t *testing.T) {
	t.Run("new branch", func(t *testing.T) {
		base, sparseWT := newSparseFixture(t)
		newWT := filepath.Join(t.TempDir(), "mode-new")
		if err := CreateWorktreeWithOptions(base, newWT, "feature/mode-new", SparseInheritOptions(true, sparseWT)); err != nil {
			t.Fatalf("CreateWorktreeWithOptions: %v", err)
		}
		assertSparseWorktree(t, newWT, true, []string{"keep"})
		if got := runGit(t, newWT, "rev-parse", "--abbrev-ref", "HEAD"); got != "feature/mode-new" {
			t.Errorf("HEAD branch = %q, want feature/mode-new", got)
		}
	})

	t.Run("existing local branch", func(t *testing.T) {
		base, sparseWT := newSparseFixture(t)
		createBranch(t, base, "existing-local")
		newWT := filepath.Join(t.TempDir(), "mode-local")
		if err := CreateWorktreeWithOptions(base, newWT, "existing-local", SparseInheritOptions(true, sparseWT)); err != nil {
			t.Fatalf("CreateWorktreeWithOptions: %v", err)
		}
		assertSparseWorktree(t, newWT, true, []string{"keep"})
		if got := runGit(t, newWT, "rev-parse", "--abbrev-ref", "HEAD"); got != "existing-local" {
			t.Errorf("HEAD branch = %q, want existing-local", got)
		}
	})

	t.Run("remote-tracking branch", func(t *testing.T) {
		base, sparseWT := newSparseFixture(t)
		remote := filepath.Join(t.TempDir(), "origin.git")
		runGit(t, t.TempDir(), "init", "--bare", "-b", "main", remote)
		runGit(t, base, "remote", "add", "origin", remote)
		runGit(t, base, "push", "-u", "origin", "main")
		// A branch that exists ONLY on the remote, so resolution takes the
		// remote-tracking path.
		runGit(t, base, "push", "origin", "main:remote-only")
		runGit(t, base, "fetch", "origin")

		newWT := filepath.Join(t.TempDir(), "mode-remote")
		if err := CreateWorktreeWithOptions(base, newWT, "remote-only", SparseInheritOptions(true, sparseWT)); err != nil {
			t.Fatalf("CreateWorktreeWithOptions: %v", err)
		}
		assertSparseWorktree(t, newWT, true, []string{"keep"})
		if got := runGit(t, newWT, "rev-parse", "--abbrev-ref", "HEAD"); got != "remote-only" {
			t.Errorf("HEAD branch = %q, want remote-only", got)
		}
		if got := runGit(t, newWT, "rev-parse", "--abbrev-ref", "HEAD@{upstream}"); got != "origin/remote-only" {
			t.Errorf("upstream = %q, want origin/remote-only", got)
		}
	})
}

// Fork-with-state anchors the new worktree at an explicit start point; that
// path must inherit sparsity too.
func TestCreateWorktreeAtStartPointWithOptions_InheritsSparse(t *testing.T) {
	base, sparseWT := newSparseFixture(t)
	startPoint := runGit(t, sparseWT, "rev-parse", "HEAD")
	newWT := filepath.Join(t.TempDir(), "at-start-point")

	createdBranch, err := CreateWorktreeAtStartPointWithOptions(base, newWT, "fork/at-start-point", startPoint, SparseInheritOptions(true, sparseWT))
	if err != nil {
		t.Fatalf("CreateWorktreeAtStartPointWithOptions: %v", err)
	}
	if !createdBranch {
		t.Error("createdBranch = false although the branch was new")
	}
	assertSparseWorktree(t, newWT, true, []string{"keep"})
	if got := runGit(t, newWT, "rev-parse", "HEAD"); got != startPoint {
		t.Errorf("HEAD = %s, want the explicit start point %s", got, startPoint)
	}
}

// Bare layouts (`<project>/.bare` + sibling worktrees) have no main working
// tree at all, so the invoking linked worktree is the ONLY place the sparse
// state can come from.
func TestCreateWorktreeWithOptions_BareLayoutInheritsFromLinkedWorktree(t *testing.T) {
	projectRoot, _, worktrees := createBareRepoLayout(t, "main-wt")
	sparseWT := worktrees[0]

	// createBareRepoLayout seeds commits through a throwaway clone, so the
	// linked worktree still needs an identity of its own to commit with.
	runGit(t, sparseWT, "config", "user.email", "test@test.com")
	runGit(t, sparseWT, "config", "user.name", "Test User")
	writeSparseFixtureTree(t, sparseWT)
	runGit(t, sparseWT, "add", ".")
	runGit(t, sparseWT, "commit", "-m", "add keep/ and heavy/")
	runGit(t, sparseWT, "sparse-checkout", "set", "keep")

	newWT := filepath.Join(projectRoot, "inherited-wt")
	if err := CreateWorktreeWithOptions(projectRoot, newWT, "feature/bare-inherit", SparseInheritOptions(true, sparseWT)); err != nil {
		t.Fatalf("CreateWorktreeWithOptions: %v", err)
	}
	assertSparseWorktree(t, newWT, true, []string{"keep"})
}

// The setup script and .worktreeinclude still run after the sparse worktree is
// ready — and the script must observe a worktree where the excluded tree was
// NEVER materialized (issue #1708's whole point: no full checkout, not even a
// transient one).
func TestCreateWorktreeWithSetupOptions_SetupScriptObservesSparseWorktree(t *testing.T) {
	base, sparseWT := newSparseFixture(t)

	if err := os.MkdirAll(filepath.Join(base, ".agent-deck"), 0o755); err != nil {
		t.Fatalf("mkdir .agent-deck: %v", err)
	}
	script := "#!/bin/sh\n" +
		"if [ -d \"$AGENT_DECK_WORKTREE_PATH/" + sparseFixtureExcludedDir + "\" ]; then\n" +
		"  echo present > \"$AGENT_DECK_WORKTREE_PATH/setup-observed.txt\"\n" +
		"else\n" +
		"  echo absent > \"$AGENT_DECK_WORKTREE_PATH/setup-observed.txt\"\n" +
		"fi\n"
	if err := os.WriteFile(filepath.Join(base, ".agent-deck", "worktree-setup.sh"), []byte(script), 0o755); err != nil {
		t.Fatalf("write setup script: %v", err)
	}
	// .worktreeinclude must still be processed before the script runs. It only
	// carries GITIGNORED files, hence the committed .gitignore entry.
	if err := os.WriteFile(filepath.Join(base, ".gitignore"), []byte("included.txt\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	runGit(t, base, "add", ".gitignore")
	runGit(t, base, "commit", "-m", "ignore included.txt")
	if err := os.WriteFile(filepath.Join(base, ".worktreeinclude"), []byte("included.txt\n"), 0o644); err != nil {
		t.Fatalf("write .worktreeinclude: %v", err)
	}
	if err := os.WriteFile(filepath.Join(base, "included.txt"), []byte("carried"), 0o644); err != nil {
		t.Fatalf("write included.txt: %v", err)
	}

	newWT := filepath.Join(t.TempDir(), "with-setup")
	var stdout, stderr bytes.Buffer
	setupErr, err := CreateWorktreeWithSetupOptions(base, newWT, "feature/with-setup",
		WorktreeStateOptions{}, SparseInheritOptions(true, sparseWT), &stdout, &stderr, 0)
	if err != nil {
		t.Fatalf("CreateWorktreeWithSetupOptions: %v (stderr: %s)", err, stderr.String())
	}
	if setupErr != nil {
		t.Fatalf("setup script failed: %v (stderr: %s)", setupErr, stderr.String())
	}

	observed, readErr := os.ReadFile(filepath.Join(newWT, "setup-observed.txt"))
	if readErr != nil {
		t.Fatalf("setup script did not run: %v", readErr)
	}
	if strings.TrimSpace(string(observed)) != "absent" {
		t.Errorf("setup script observed %s/ = %q, want absent", sparseFixtureExcludedDir, strings.TrimSpace(string(observed)))
	}
	if _, err := os.Stat(filepath.Join(newWT, "included.txt")); err != nil {
		t.Errorf(".worktreeinclude was not processed: %v", err)
	}
	if dirExists(filepath.Join(newWT, sparseFixtureExcludedDir)) {
		t.Errorf("%s/ present after setup: sparsity was not inherited", sparseFixtureExcludedDir)
	}
}

// A sparse replay that fails must leave nothing behind: the worktree git just
// created (still empty, since --no-checkout skipped materialization) and the
// branch this call created are both rolled back.
func TestRunWorktreeAdd_SparseFailureRollsBackWorktreeAndBranch(t *testing.T) {
	base, _ := newSparseFixture(t)
	newWT := filepath.Join(t.TempDir(), "doomed")

	// "../escape" is rejected by `git sparse-checkout set --cone` ("could not
	// normalize path"), which fails the replay after `worktree add` succeeded.
	err := runWorktreeAdd(worktreeAddSpec{
		repoDir:       base,
		worktreePath:  newWT,
		branchName:    "feature/doomed",
		args:          []string{"-b", "feature/doomed", newWT},
		sparse:        SparseCheckoutState{Enabled: true, Cone: true, Patterns: []string{"../escape"}},
		createdBranch: true,
		failMsg:       "failed to create worktree",
	})
	if err == nil {
		t.Fatal("expected an error when the sparse replay fails")
	}
	if !strings.Contains(err.Error(), "inherit sparse checkout") {
		t.Errorf("error does not name the failing step: %v", err)
	}
	if strings.Contains(err.Error(), "cleanup failed") {
		t.Errorf("rollback itself failed: %v", err)
	}
	if _, statErr := os.Stat(newWT); !os.IsNotExist(statErr) {
		t.Errorf("half-initialized worktree still present at %s (stat err: %v)", newWT, statErr)
	}
	if BranchExists(base, "feature/doomed") {
		t.Error("branch created by the failed call was not deleted")
	}
	if list := runGit(t, base, "worktree", "list"); strings.Contains(list, newWT) {
		t.Errorf("worktree still registered with git:\n%s", list)
	}
}
