# Fork-with-State Implementation Plan

> **⚠ DEPRECATED — superseded by [`2026-05-18-fork-with-state-followup.md`](2026-05-18-fork-with-state-followup.md)**
>
> This plan was written as a ground-up implementation roadmap before upstream
> merged #1029 (commit 6a1645eb). It remains as historical reference for the
> 21-task TDD breakdown and the FWS-001 through FWS-018 review log. The active
> plan (scoped to closing the 11 gaps identified after the upstream merge) is
> in the followup file. See also the post-merge gap analysis at
> [`../discussions/2026-05-18-post-merge-gap-analysis.md`](../discussions/2026-05-18-post-merge-gap-analysis.md).

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `--with-state` and `--with-state-and-gitignored` opt-in flags to `agent-deck session fork` (CLI and TUI) that materialize the parent session's working-tree state into a freshly-created destination branch and worktree branched off the parent's HEAD.

**Architecture:** A new `internal/git/worktree_with_state.go` exposes `MaterializeParentState`, `DetectInProgressOperation`, `HasSubmodules`, and a shared `PreflightForkWithState` gate used by both CLI and TUI. The existing `CreateWorktreeWithSetup` is split into `CreateWorktree` (already in `git.go`) + a new `RunWorktreeSetup`, and `git.go` gains a start-point-aware `CreateWorktreeAtStartPoint` helper so fork-with-state can create the new worktree from the parent session's HEAD. The fork-with-state path sequences `PreflightForkWithState → CreateWorktreeAtStartPoint → MaterializeParentState → RunWorktreeSetup`. The CLI handler in `session_cmd.go` and the TUI `ForkDialog` gain new flags/checkboxes that propagate two new transient fields on `ClaudeOptions` (`WithState`, `IncludeGitignored`).

**Tech Stack:** Go 1.24.0 (pinned via `GOTOOLCHAIN`), bubbletea/lipgloss for TUI, shelling out to `git` for diff/apply/ls-files.

**Spec:** `docs/superpowers/specs/2026-05-14-fork-worktree-with-state-design.md`

**Pre-flight (one-time, before Task 1):**

```bash
export GOTOOLCHAIN=go1.24.0
git checkout -b feature/fork-worktree-with-state
```

Per `CONTRIBUTING.md`, this branch will be pushed to your personal fork (`smorin/agent-deck`) when you open the PR. Do not push to upstream `asheshgoplani/agent-deck`.

---

## File map

| File | Action | Responsibility |
|---|---|---|
| `internal/session/tooloptions.go` | Modify | Add `WithState` + `IncludeGitignored` transient fields |
| `internal/git/worktree_with_state.go` | Create | `MaterializeParentState`, `DetectInProgressOperation`, `HasSubmodules`, `PreflightForkWithState`, internal helpers |
| `internal/git/worktree_with_state_test.go` | Create | Unit tests for the above, including the shared CLI/TUI preflight helper |
| `internal/git/worktree_with_state_integration_test.go` | Create | Git-side fork-with-state sequence integration tests |
| `internal/git/git.go` | Modify | Add `HeadCommit` and `CreateWorktreeAtStartPoint` for parent-HEAD worktree creation |
| `internal/git/setup.go` | Modify | Extract `RunWorktreeSetup`; `CreateWorktreeWithSetup` becomes a wrapper |
| `cmd/agent-deck/session_cmd.go` | Modify | New flags, implication resolution, sequence Create→Materialize→Setup with cleanup-on-error |
| `cmd/agent-deck/session_cmd_fork_state_test.go` | Create | CLI/handler contract tests for fork-with-state |
| `internal/ui/forkdialog.go` | Modify | Two new sub-checkboxes, state guards, focus-target extension, getters |
| `internal/ui/forkdialog_test.go` | Modify | TUI tests for visibility, focus order, getters |
| `internal/ui/home_fork_state_test.go` | Create | Structural guard that TUI fork-with-state uses shared `git.PreflightForkWithState` before worktree creation |
| `tests/eval/session/fork_with_state_test.go` | Create | Behavioral eval smoke test for the real CLI fork-with-state flow |
| `internal/ui/forkdialog_eval_test.go` | Create | Behavioral eval smoke test for the visible TUI ForkDialog with-state interaction |
| `CLAUDE.md` | Modify | Add fork-with-state mandatory test coverage section |
| `README.md` | Modify | Add `--with-state` example |
| `CHANGELOG.md` | Modify | Entry for the version this ships in |

---

## Task 1: Add transient fields to ClaudeOptions

**Files:**
- Modify: `internal/session/tooloptions.go:37-42`

- [ ] **Step 1: Add fields after the existing worktree transient block**

Open `internal/session/tooloptions.go` and replace lines 37-42 (the comment + four worktree fields) with:

```go
	// Transient fields for worktree fork (not persisted)
	WorkDir          string `json:"-"`
	WorktreePath     string `json:"-"`
	WorktreeRepoRoot string `json:"-"`
	WorktreeBranch   string `json:"-"`

	// Transient fields for fork-with-state (not persisted).
	// Consumed by the fork CLI/TUI handler to drive MaterializeParentState.
	WithState         bool `json:"-"`
	IncludeGitignored bool `json:"-"`
```

- [ ] **Step 2: Verify package compiles**

Run: `GOTOOLCHAIN=go1.24.0 go build ./internal/session/...`
Expected: exits 0, no output.

- [ ] **Step 3: Commit**

```bash
git add internal/session/tooloptions.go
git commit -m "feat(session): add WithState/IncludeGitignored transient options for fork-with-state"
```

---

## Task 2: DetectInProgressOperation

**Files:**
- Create: `internal/git/worktree_with_state.go`
- Create: `internal/git/worktree_with_state_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/git/worktree_with_state_test.go`:

```go
package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// runGit is a test helper that runs a git command in dir and fails the
// test if it exits non-zero. Returns combined output for assertions.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s failed: %v\noutput: %s", args, dir, err, out)
	}
	return string(out)
}

// runGitAllowFail runs a git command and returns output + error without
// failing the test. Used when we expect a conflict / non-zero exit.
func runGitAllowFail(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// initRepo creates a fresh git repo with one initial commit on main.
// Returns the repo dir.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("init\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "init")
	return dir
}

func TestDetectInProgressOperation_Clean(t *testing.T) {
	dir := initRepo(t)
	got, err := DetectInProgressOperation(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Fatalf("clean repo returned %q, want empty", got)
	}
}

func TestDetectInProgressOperation_Rebase(t *testing.T) {
	dir := initRepo(t)
	// Create two diverging commits on a side branch and main so rebase produces a conflict.
	runGit(t, dir, "checkout", "-b", "side")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("side\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "commit", "-am", "side change")
	runGit(t, dir, "checkout", "main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "commit", "-am", "main change")
	// Attempt rebase; expect conflict so rebase stays in progress.
	_, _ = runGitAllowFail(dir, "rebase", "side")

	got, err := DetectInProgressOperation(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "rebase" {
		t.Fatalf("mid-rebase returned %q, want %q", got, "rebase")
	}
}

func TestDetectInProgressOperation_Merge(t *testing.T) {
	dir := initRepo(t)
	runGit(t, dir, "checkout", "-b", "side")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("side\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "commit", "-am", "side")
	runGit(t, dir, "checkout", "main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "commit", "-am", "main")
	_, _ = runGitAllowFail(dir, "merge", "side")

	got, err := DetectInProgressOperation(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "merge" {
		t.Fatalf("mid-merge returned %q, want %q", got, "merge")
	}
}

func TestDetectInProgressOperation_CherryPick(t *testing.T) {
	dir := initRepo(t)
	runGit(t, dir, "checkout", "-b", "side")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("side\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "commit", "-am", "side")
	sideSha := runGit(t, dir, "rev-parse", "HEAD")
	runGit(t, dir, "checkout", "main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "commit", "-am", "main")
	_, _ = runGitAllowFail(dir, "cherry-pick", sideSha[:8])

	got, err := DetectInProgressOperation(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "cherry-pick" {
		t.Fatalf("mid-cherry-pick returned %q, want %q", got, "cherry-pick")
	}
}

func TestDetectInProgressOperation_Bisect(t *testing.T) {
	dir := initRepo(t)
	// Three commits to give bisect something to walk.
	for i := 0; i < 3; i++ {
		if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte{byte('a' + i)}, 0o644); err != nil {
			t.Fatal(err)
		}
		runGit(t, dir, "add", "f.txt")
		runGit(t, dir, "commit", "-m", "c")
	}
	runGit(t, dir, "bisect", "start")
	runGit(t, dir, "bisect", "bad")
	runGit(t, dir, "bisect", "good", "HEAD~2")

	got, err := DetectInProgressOperation(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "bisect" {
		t.Fatalf("active bisect returned %q, want %q", got, "bisect")
	}
}
```

- [ ] **Step 2: Run tests to confirm they fail (no implementation yet)**

Run: `GOTOOLCHAIN=go1.24.0 go test ./internal/git/ -run TestDetectInProgressOperation -v`
Expected: FAIL — `undefined: DetectInProgressOperation`

- [ ] **Step 3: Write the minimal implementation**

Create `internal/git/worktree_with_state.go`:

```go
package git

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// DetectInProgressOperation returns "rebase", "merge", "cherry-pick", "bisect",
// or "" depending on what (if any) operation is currently in progress in the
// repository at repoDir. Errors only on inability to locate the git dir.
func DetectInProgressOperation(repoDir string) (string, error) {
	gitDir, err := resolveGitDir(repoDir)
	if err != nil {
		return "", err
	}
	if pathExists(filepath.Join(gitDir, "rebase-merge")) || pathExists(filepath.Join(gitDir, "rebase-apply")) {
		return "rebase", nil
	}
	if pathExists(filepath.Join(gitDir, "CHERRY_PICK_HEAD")) {
		return "cherry-pick", nil
	}
	if pathExists(filepath.Join(gitDir, "MERGE_HEAD")) {
		return "merge", nil
	}
	if pathExists(filepath.Join(gitDir, "BISECT_LOG")) {
		return "bisect", nil
	}
	return "", nil
}

// resolveGitDir returns the absolute path to the .git directory for repoDir.
// Handles worktrees (where .git is a file containing "gitdir: ..."), bare repos,
// and plain repos uniformly via `git rev-parse --git-dir`.
func resolveGitDir(repoDir string) (string, error) {
	cmd := exec.Command("git", "-C", repoDir, "rev-parse", "--git-dir")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("rev-parse --git-dir in %s: %s: %w", repoDir, strings.TrimSpace(stderr.String()), err)
	}
	gd := strings.TrimSpace(stdout.String())
	if !filepath.IsAbs(gd) {
		gd = filepath.Join(repoDir, gd)
	}
	return gd, nil
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// HasSubmodules returns true if the repo has a .gitmodules file. Used to warn
// (not refuse) before MaterializeParentState — submodule contents are copied
// as plain files; their internal git state is not recursed into.
func HasSubmodules(repoDir string) (bool, error) {
	info, err := os.Stat(filepath.Join(repoDir, ".gitmodules"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return !info.IsDir(), nil
}
```

- [ ] **Step 4: Run tests to confirm they pass**

Run: `GOTOOLCHAIN=go1.24.0 go test ./internal/git/ -run TestDetectInProgressOperation -v`
Expected: PASS (5 subtests).

- [ ] **Step 5: Commit**

```bash
git add internal/git/worktree_with_state.go internal/git/worktree_with_state_test.go
git commit -m "feat(git): add DetectInProgressOperation for fork-with-state pre-flight"
```

---

## Task 3: HasSubmodules

**Files:**
- Modify: `internal/git/worktree_with_state_test.go`

`HasSubmodules` was implemented in Task 2's same file. This task only adds tests.

- [ ] **Step 1: Append failing tests**

Append to `internal/git/worktree_with_state_test.go`:

```go
func TestHasSubmodules_None(t *testing.T) {
	dir := initRepo(t)
	got, err := HasSubmodules(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Fatal("plain repo reported HasSubmodules=true")
	}
}

func TestHasSubmodules_Present(t *testing.T) {
	dir := initRepo(t)
	if err := os.WriteFile(filepath.Join(dir, ".gitmodules"), []byte("# fake\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := HasSubmodules(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Fatal("repo with .gitmodules reported HasSubmodules=false")
	}
}
```

- [ ] **Step 2: Run tests**

Run: `GOTOOLCHAIN=go1.24.0 go test ./internal/git/ -run TestHasSubmodules -v`
Expected: PASS (2 subtests). (Implementation was added in Task 2; tests are independent.)

- [ ] **Step 3: Commit**

```bash
git add internal/git/worktree_with_state_test.go
git commit -m "test(git): add HasSubmodules unit tests"
```

---

## Task 4: Split CreateWorktreeWithSetup → extract RunWorktreeSetup

**Files:**
- Modify: `internal/git/setup.go:89-120`
- Test: existing tests in `internal/git/setup_test.go`, `internal/git/setup_progress_test.go`, `internal/git/bare_repo_test.go`

This task is a pure refactor: behavior of `CreateWorktreeWithSetup` is unchanged, but `RunWorktreeSetup` is extracted as a separately-exported function. Existing tests must continue to pass without modification.

- [ ] **Step 1: Replace the body of CreateWorktreeWithSetup and add RunWorktreeSetup**

In `internal/git/setup.go`, replace the function `CreateWorktreeWithSetup` (lines 100-120) with:

```go
func CreateWorktreeWithSetup(repoDir, worktreePath, branchName string, stdout, stderr io.Writer, setupTimeout time.Duration) (setupErr error, err error) {
	if err = CreateWorktree(repoDir, worktreePath, branchName); err != nil {
		return nil, err
	}
	return RunWorktreeSetup(repoDir, worktreePath, stdout, stderr, setupTimeout), nil
}

// RunWorktreeSetup runs the worktree setup script (if any) against an
// existing worktree. Returns the script's exit error (non-fatal — callers
// treat it as a warning); a nil return means "no script" or "script
// succeeded."
//
// Extracted from CreateWorktreeWithSetup so the fork-with-state path can
// sequence: CreateWorktree → MaterializeParentState → RunWorktreeSetup.
// The materialization must run before the setup hook so the hook sees the
// final file contents (e.g., a parent's WIP package.json drives npm install).
func RunWorktreeSetup(repoDir, worktreePath string, stdout, stderr io.Writer, setupTimeout time.Duration) error {
	scriptPath, scriptMode := FindWorktreeSetupScript(repoDir)
	if scriptPath == "" {
		return nil
	}
	fmt.Fprintln(stderr, "Running worktree setup script...")
	start := time.Now()
	setupErr := RunWorktreeSetupScript(scriptPath, scriptMode, repoDir, worktreePath, stdout, stderr, setupTimeout)
	elapsed := time.Since(start).Round(100 * time.Millisecond)
	if setupErr != nil {
		fmt.Fprintf(stderr, "Worktree setup script failed after %s: %v\n", elapsed, setupErr)
	} else {
		fmt.Fprintf(stderr, "Worktree setup script completed in %s\n", elapsed)
	}
	return setupErr
}
```

- [ ] **Step 2: Run existing setup tests to confirm no regression**

Run: `GOTOOLCHAIN=go1.24.0 go test ./internal/git/... -run "Setup|Worktree" -race -count=1`
Expected: PASS — all existing tests still green.

- [ ] **Step 3: Commit**

```bash
git add internal/git/setup.go
git commit -m "refactor(git): extract RunWorktreeSetup from CreateWorktreeWithSetup"
```

---

## Task 4A: Add start-point-aware worktree creation for parent-HEAD forks

**Files:**
- Modify: `internal/git/git.go`
- Modify: `internal/git/git_test.go`

Fork-with-state's product contract is that the fork starts from the parent session's HEAD. This matters when the parent session already lives in a linked worktree whose HEAD differs from the main/base worktree. Existing `CreateWorktree(repoRoot, ...)` creates a new branch from the invocation repo's current HEAD and cannot express an explicit start point.

- [ ] **Step 1: Add the failing linked-worktree regression**

Append to `internal/git/git_test.go`:

```go
func TestCreateWorktreeAtStartPoint_UsesExplicitParentHead(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "base")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	createTestRepo(t, base)

	parentWT := filepath.Join(root, "parent-wt")
	if err := CreateWorktree(base, parentWT, "parent-branch"); err != nil {
		t.Fatalf("CreateWorktree parent: %v", err)
	}
	if err := os.WriteFile(filepath.Join(parentWT, "README.md"), []byte("# Parent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, parentWT, "commit", "-am", "parent change")

	baseHead := runGit(t, base, "rev-parse", "HEAD")
	parentHead := runGit(t, parentWT, "rev-parse", "HEAD")
	if baseHead == parentHead {
		t.Fatal("setup invalid: base and parent worktree HEAD should differ")
	}

	forkWT := filepath.Join(root, "fork-wt")
	createdBranch, err := CreateWorktreeAtStartPoint(base, forkWT, "fork/from-parent", parentHead)
	if err != nil {
		t.Fatalf("CreateWorktreeAtStartPoint: %v", err)
	}
	if !createdBranch {
		t.Fatal("CreateWorktreeAtStartPoint returned createdBranch=false for a new branch")
	}
	forkHead := runGit(t, forkWT, "rev-parse", "HEAD")
	if forkHead != parentHead {
		t.Fatalf("fork HEAD = %s, want parent HEAD %s (base HEAD %s)", forkHead, parentHead, baseHead)
	}
}

func TestCreateWorktreeAtStartPoint_RejectsExistingBranch(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "base")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	createTestRepo(t, base)
	parentHead := runGit(t, base, "rev-parse", "HEAD")
	runGit(t, base, "branch", "fork/existing")

	createdBranch, err := CreateWorktreeAtStartPoint(base, filepath.Join(root, "fork-wt"), "fork/existing", parentHead)
	if err == nil {
		t.Fatal("expected existing branch to be rejected")
	}
	if createdBranch {
		t.Fatal("createdBranch should be false when branch already existed")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected already-exists error, got %v", err)
	}
}
```

- [ ] **Step 2: Run test to confirm it fails**

Run: `GOTOOLCHAIN=go1.24.0 go test ./internal/git/ -run TestCreateWorktreeAtStartPoint_UsesExplicitParentHead -v`
Expected: FAIL — `undefined: CreateWorktreeAtStartPoint`.

- [ ] **Step 3: Add the minimal helper**

In `internal/git/git.go`, add near `CreateWorktree`:

```go
// HeadCommit returns the commit currently checked out at repoDir. It accepts
// normal repositories, linked worktrees, and bare-repo project roots.
func HeadCommit(repoDir string) (string, error) {
	repoDir = resolveGitInvocationDir(repoDir)
	cmd := exec.Command("git", "-C", repoDir, "rev-parse", "--verify", "HEAD^{commit}")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to resolve HEAD commit: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return strings.TrimSpace(string(output)), nil
}

// CreateWorktreeAtStartPoint creates a new branch worktree from an explicit
// start point. It returns createdBranch=true only after git successfully
// creates the branch for this call. Fork-with-state cleanup uses that proof
// before deleting a branch after materialization failure.
func CreateWorktreeAtStartPoint(repoDir, worktreePath, branchName, startPoint string) (createdBranch bool, err error) {
	if err := ValidateBranchName(branchName); err != nil {
		return false, fmt.Errorf("invalid branch name: %w", err)
	}
	if strings.TrimSpace(startPoint) == "" {
		return false, errors.New("start point cannot be empty")
	}
	repoDir = resolveGitInvocationDir(repoDir)
	if !IsGitRepo(repoDir) {
		return false, errors.New("not a git repository")
	}
	if BranchExists(repoDir, branchName) {
		return false, fmt.Errorf("branch %q already exists", branchName)
	}
	cmd := exec.Command("git", "-C", repoDir, "worktree", "add", "-b", branchName, worktreePath, startPoint)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("failed to create worktree at start point: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return true, nil
}
```

- [ ] **Step 4: Run tests**

Run: `GOTOOLCHAIN=go1.24.0 go test ./internal/git/ -run "TestCreateWorktreeAtStartPoint|TestCreateWorktree" -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/git/git.go internal/git/git_test.go
git commit -m "feat(git): create fork worktrees from explicit parent HEAD"
```

---

## Task 4B: Add shared fork-with-state preflight helper

**Files:**
- Modify: `internal/git/worktree_with_state_test.go`
- Modify: `internal/git/worktree_with_state.go`

The CLI and TUI must not each own their own fork-with-state safety checks. Keep git facts in `internal/git`: detect unsupported in-progress operations, capture the parent HEAD start point, and return submodule presence as a warning fact. CLI and TUI remain responsible for rendering those facts as surface-appropriate errors or warnings.

- [ ] **Step 1: Add failing tests for the shared helper**

In `internal/git/worktree_with_state_test.go`, add `errors` and `strings` to the imports, then append:

```go
func TestPreflightForkWithState_ReturnsParentHeadAndSubmoduleFact(t *testing.T) {
	dir := initRepo(t)
	if err := os.WriteFile(filepath.Join(dir, ".gitmodules"), []byte("[submodule \"lib\"]\n\tpath = lib\n\turl = https://example.invalid/lib.git\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := PreflightForkWithState(dir)
	if err != nil {
		t.Fatalf("PreflightForkWithState: %v", err)
	}
	wantHead := strings.TrimSpace(runGit(t, dir, "rev-parse", "--verify", "HEAD^{commit}"))
	if got.ParentHead != wantHead {
		t.Fatalf("ParentHead = %q, want %q", got.ParentHead, wantHead)
	}
	if !got.HasSubmodules {
		t.Fatal("HasSubmodules = false, want true")
	}
}

func TestPreflightForkWithState_RefusesInProgressOperation(t *testing.T) {
	dir := initRepo(t)
	runGit(t, dir, "checkout", "-b", "side")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("side\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "commit", "-am", "side change")
	runGit(t, dir, "checkout", "main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "commit", "-am", "main change")
	_, _ = runGitAllowFail(dir, "rebase", "side")

	_, err := PreflightForkWithState(dir)
	if err == nil {
		t.Fatal("expected in-progress operation error")
	}
	var opErr *InProgressOperationError
	if !errors.As(err, &opErr) {
		t.Fatalf("error = %T %v, want InProgressOperationError", err, err)
	}
	if opErr.Kind != "rebase" {
		t.Fatalf("operation kind = %q, want rebase", opErr.Kind)
	}
}
```

- [ ] **Step 2: Run tests to confirm they fail**

Run: `GOTOOLCHAIN=go1.24.0 go test ./internal/git/ -run TestPreflightForkWithState -v`
Expected: FAIL — `undefined: PreflightForkWithState` / `undefined: InProgressOperationError`.

- [ ] **Step 3: Add the shared helper**

Append to `internal/git/worktree_with_state.go`:

```go
type ForkWithStatePreflight struct {
	ParentHead    string
	HasSubmodules bool
}

// InProgressOperationError is returned when the parent repo has an unsupported
// operation in progress. Callers own the final user-facing wording.
type InProgressOperationError struct {
	Kind    string
	RepoDir string
}

func (e *InProgressOperationError) Error() string {
	return fmt.Sprintf("parent repository is mid-%s", e.Kind)
}

// PreflightForkWithState is the shared CLI/TUI preflight gate for fork-with-state.
// It returns git facts only: parent HEAD and whether submodules are present.
// Callers render warnings and errors according to their surface.
func PreflightForkWithState(parentWorktree string) (*ForkWithStatePreflight, error) {
	op, err := DetectInProgressOperation(parentWorktree)
	if err != nil {
		return nil, fmt.Errorf("detect in-progress operation: %w", err)
	}
	if op != "" {
		return nil, &InProgressOperationError{Kind: op, RepoDir: parentWorktree}
	}

	hasSubmodules, err := HasSubmodules(parentWorktree)
	if err != nil {
		return nil, fmt.Errorf("inspect submodules: %w", err)
	}

	parentHead, err := HeadCommit(parentWorktree)
	if err != nil {
		return nil, fmt.Errorf("resolve parent HEAD: %w", err)
	}

	return &ForkWithStatePreflight{
		ParentHead:    parentHead,
		HasSubmodules: hasSubmodules,
	}, nil
}
```

- [ ] **Step 4: Run tests**

Run: `GOTOOLCHAIN=go1.24.0 go test ./internal/git/ -run "TestPreflightForkWithState|TestDetectInProgressOperation|TestHasSubmodules|TestCreateWorktreeAtStartPoint" -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/git/worktree_with_state.go internal/git/worktree_with_state_test.go
git commit -m "feat(git): add shared fork-with-state preflight"
```

---

## Task 4C: Add shared fork-with-state destination collision validator

**Files:**
- Modify: `internal/git/worktree_with_state_test.go`
- Modify: `internal/git/worktree_with_state.go`

Mirrors the FWS-009 `PreflightForkWithState` pattern for destination-collision git facts. CLI and TUI both call this validator once before their respective worktree-creation paths; the inline collision checks in both surfaces' existing-worktree reuse blocks are removed in Task 13 and Task 17.

Worktree-collision is checked first so the more specific error (with path) is surfaced when both conditions are true.

- [ ] **Step 1: Add failing tests for the shared validator**

Append to `internal/git/worktree_with_state_test.go`:

```go
func TestValidateForkWithStateDestination_Clean(t *testing.T) {
	dir := initRepo(t)
	if err := ValidateForkWithStateDestination(dir, "fork/new"); err != nil {
		t.Fatalf("clean repo + fresh branch should pass, got %v", err)
	}
}

func TestValidateForkWithStateDestination_BranchExists(t *testing.T) {
	dir := initRepo(t)
	runGit(t, dir, "branch", "fork/existing")

	err := ValidateForkWithStateDestination(dir, "fork/existing")
	if err == nil {
		t.Fatal("expected DestinationCollisionError for existing branch")
	}
	var collErr *DestinationCollisionError
	if !errors.As(err, &collErr) {
		t.Fatalf("error = %T %v, want *DestinationCollisionError", err, err)
	}
	if collErr.Kind != "branch_exists" {
		t.Fatalf("Kind = %q, want branch_exists", collErr.Kind)
	}
	if collErr.Branch != "fork/existing" {
		t.Fatalf("Branch = %q, want fork/existing", collErr.Branch)
	}
}

func TestValidateForkWithStateDestination_WorktreeExists(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "base")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	createTestRepo(t, base)
	wtPath := filepath.Join(root, "fork-wt")
	if err := CreateWorktree(base, wtPath, "fork/used"); err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}

	err := ValidateForkWithStateDestination(base, "fork/used")
	if err == nil {
		t.Fatal("expected DestinationCollisionError for branch with worktree")
	}
	var collErr *DestinationCollisionError
	if !errors.As(err, &collErr) {
		t.Fatalf("error = %T %v, want *DestinationCollisionError", err, err)
	}
	if collErr.Kind != "worktree_exists" {
		t.Fatalf("Kind = %q, want worktree_exists", collErr.Kind)
	}
	if collErr.Branch != "fork/used" {
		t.Fatalf("Branch = %q, want fork/used", collErr.Branch)
	}
	if collErr.Path == "" {
		t.Fatalf("Path should be populated for worktree_exists, got empty")
	}
}
```

`errors` should already be in the imports from Task 4B. `createTestRepo` already exists in `internal/git/git_test.go` from Task 4A.

- [ ] **Step 2: Run tests to confirm they fail**

Run: `GOTOOLCHAIN=go1.24.0 go test ./internal/git/ -run TestValidateForkWithStateDestination -v`
Expected: FAIL — `undefined: ValidateForkWithStateDestination` / `undefined: DestinationCollisionError`.

- [ ] **Step 3: Add the helper and typed error**

Append to `internal/git/worktree_with_state.go`:

```go
// DestinationCollisionError is returned by ValidateForkWithStateDestination
// when the requested destination branch already has a worktree or already
// exists as a local branch. Callers own the final user-facing wording.
type DestinationCollisionError struct {
	Kind   string // "worktree_exists" or "branch_exists"
	Branch string
	Path   string // populated when Kind == "worktree_exists"
}

func (e *DestinationCollisionError) Error() string {
	switch e.Kind {
	case "worktree_exists":
		return fmt.Sprintf("branch %q already has a worktree at %s", e.Branch, e.Path)
	case "branch_exists":
		return fmt.Sprintf("branch %q already exists", e.Branch)
	default:
		return fmt.Sprintf("destination collision for branch %q", e.Branch)
	}
}

// ValidateForkWithStateDestination is the shared CLI/TUI destination-collision
// gate for fork-with-state. It refuses a destination branch that already has
// a worktree (preferred error: more specific, includes path) or that already
// exists as a local branch. Both checks are git facts; the caller decides
// how to render them.
func ValidateForkWithStateDestination(repoRoot, branch string) error {
	if path, err := GetWorktreeForBranch(repoRoot, branch); err == nil && path != "" {
		return &DestinationCollisionError{Kind: "worktree_exists", Branch: branch, Path: path}
	}
	if BranchExists(repoRoot, branch) {
		return &DestinationCollisionError{Kind: "branch_exists", Branch: branch}
	}
	return nil
}
```

- [ ] **Step 4: Run tests**

Run: `GOTOOLCHAIN=go1.24.0 go test ./internal/git/ -run "TestValidateForkWithStateDestination|TestPreflightForkWithState|TestDetectInProgressOperation|TestHasSubmodules|TestCreateWorktreeAtStartPoint" -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/git/worktree_with_state.go internal/git/worktree_with_state_test.go
git commit -m "feat(git): add shared fork-with-state destination collision validator"
```

---

## Task 5: MaterializeParentState scaffold + clean-parent case

**Files:**
- Modify: `internal/git/worktree_with_state.go`
- Modify: `internal/git/worktree_with_state_test.go`

- [ ] **Step 1: Add the failing test**

Append to `internal/git/worktree_with_state_test.go`:

```go
// makeWorktree creates a new worktree of repoDir at <repoDir>-wt with branch
// "fork/test" branched off HEAD. Returns the worktree path. Mirrors what
// the production fork code does (CreateWorktree) so unit tests exercise the
// same git invariants.
func makeWorktree(t *testing.T, repoDir string) string {
	t.Helper()
	wtPath := repoDir + "-wt"
	if err := CreateWorktree(repoDir, wtPath, "fork/test"); err != nil {
		t.Fatalf("CreateWorktree failed: %v", err)
	}
	return wtPath
}

func TestMaterializeParentState_CleanParent(t *testing.T) {
	parent := initRepo(t)
	wt := makeWorktree(t, parent)
	res, err := MaterializeParentState(parent, wt, StateCopyOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.TrackedFilesPatched != 0 || res.UntrackedFilesCopied != 0 || res.GitignoredFilesCopied != 0 {
		t.Fatalf("clean parent produced non-zero result: %+v", res)
	}
	// New worktree must remain clean.
	out := runGit(t, wt, "status", "--porcelain")
	if out != "" {
		t.Fatalf("new worktree dirty after materializing clean parent:\n%s", out)
	}
}
```

- [ ] **Step 2: Run test to confirm it fails**

Run: `GOTOOLCHAIN=go1.24.0 go test ./internal/git/ -run TestMaterializeParentState_CleanParent -v`
Expected: FAIL — `undefined: MaterializeParentState` / `undefined: StateCopyOptions`.

- [ ] **Step 3: Add the implementation scaffold**

Append to `internal/git/worktree_with_state.go`:

```go
// StateCopyOptions controls what MaterializeParentState copies. Tracked
// modifications (staged + unstaged) and untracked non-ignored files are
// always copied. IncludeGitignored opts in to copying gitignored files too.
type StateCopyOptions struct {
	IncludeGitignored bool
}

// StateCopyResult reports the count of changes applied by
// MaterializeParentState. Returned even on partial failure so callers can
// include the count in error messages.
type StateCopyResult struct {
	TrackedFilesPatched   int
	UntrackedFilesCopied  int
	GitignoredFilesCopied int
}

// MaterializeParentState copies parent's working-tree state (staged diff,
// unstaged diff, untracked files, and optionally gitignored files) into
// newWorktree. Read-only on parentWorktree — does not mutate the parent's
// index, working tree, or stash list.
//
// Caller is responsible for the worktree already existing at newWorktree
// (typically via CreateWorktree branched off parent's HEAD). Caller is also
// responsible for cleanup on error.
func MaterializeParentState(parentWorktree, newWorktree string, opts StateCopyOptions) (*StateCopyResult, error) {
	res := &StateCopyResult{}

	// 1. Apply staged changes via `git apply --index`.
	// `--index` updates both the index and working tree. A cached-only
	// apply leaves partially-staged files at their base content in the
	// working tree, causing the later unstaged patch to fail; it also
	// leaves staged deletions present as untracked files.
	stagedPatch, err := captureDiff(parentWorktree, true)
	if err != nil {
		return res, fmt.Errorf("capture staged diff: %w", err)
	}
	if len(stagedPatch) > 0 {
		if err := applyPatch(newWorktree, stagedPatch, true); err != nil {
			return res, fmt.Errorf("apply parent's staged changes: %w", err)
		}
		res.TrackedFilesPatched += countFilesInPatch(stagedPatch)
	}

	// 2. Apply unstaged changes via plain `git apply`.
	unstagedPatch, err := captureDiff(parentWorktree, false)
	if err != nil {
		return res, fmt.Errorf("capture unstaged diff: %w", err)
	}
	if len(unstagedPatch) > 0 {
		if err := applyPatch(newWorktree, unstagedPatch, false); err != nil {
			return res, fmt.Errorf("apply parent's unstaged changes: %w", err)
		}
		res.TrackedFilesPatched += countFilesInPatch(unstagedPatch)
	}

	// 3. Copy untracked non-gitignored files.
	n, err := copyUntracked(parentWorktree, newWorktree, false)
	res.UntrackedFilesCopied = n
	if err != nil {
		return res, fmt.Errorf("copy untracked files: %w", err)
	}

	// 4. Optionally copy gitignored files.
	if opts.IncludeGitignored {
		n, err := copyUntracked(parentWorktree, newWorktree, true)
		res.GitignoredFilesCopied = n
		if err != nil {
			return res, fmt.Errorf("copy gitignored files: %w", err)
		}
	}

	return res, nil
}

// captureDiff returns the binary patch for parent's staged (if staged=true)
// or unstaged changes. Empty patch means "no changes of that kind."
func captureDiff(repoDir string, staged bool) ([]byte, error) {
	args := []string{"-C", repoDir, "diff", "--binary"}
	if staged {
		args = append(args, "--cached")
	}
	cmd := exec.Command("git", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git diff (staged=%v): %s: %w", staged, strings.TrimSpace(stderr.String()), err)
	}
	return stdout.Bytes(), nil
}

// applyPatch applies a binary patch to repoDir. If staged=true, the patch is
// applied with `git apply --index`, updating both the index and working tree
// to the parent's staged state. Otherwise it applies to the working tree only.
func applyPatch(repoDir string, patch []byte, staged bool) error {
	args := []string{"-C", repoDir, "apply"}
	if staged {
		args = append(args, "--index")
	}
	cmd := exec.Command("git", args...)
	cmd.Stdin = bytes.NewReader(patch)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git apply (staged=%v): %s: %w", staged, strings.TrimSpace(stderr.String()), err)
	}
	return nil
}

// copyUntracked enumerates parent's untracked files via `git ls-files
// --others`. When gitignored=true, only files matched by .gitignore are
// listed; otherwise only non-ignored untracked files are listed. Each file
// is copied to newWorktree preserving mode bits and symlinks.
func copyUntracked(parentWorktree, newWorktree string, gitignored bool) (int, error) {
	args := []string{"-C", parentWorktree, "ls-files", "--others"}
	if gitignored {
		args = append(args, "--ignored", "--exclude-standard")
	} else {
		args = append(args, "--exclude-standard")
	}
	cmd := exec.Command("git", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("git ls-files (gitignored=%v): %s: %w", gitignored, strings.TrimSpace(stderr.String()), err)
	}

	count := 0
	for _, rel := range strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n") {
		if rel == "" {
			continue
		}
		src := filepath.Join(parentWorktree, rel)
		dst := filepath.Join(newWorktree, rel)
		if err := copyFilePreservingMode(src, dst); err != nil {
			return count, fmt.Errorf("copy %s: %w", rel, err)
		}
		count++
	}
	return count, nil
}

// copyFilePreservingMode copies a single file from src to dst. Symlinks are
// recreated as symlinks (target preserved); regular files keep their
// permission bits including the executable bit. Parent directories are
// created as needed.
func copyFilePreservingMode(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		_ = os.Remove(dst)
		return os.Symlink(target, dst)
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return os.Chmod(dst, info.Mode().Perm())
}

// countFilesInPatch counts `diff --git ` headers in a patch. Used for
// reporting only — not for correctness.
func countFilesInPatch(patch []byte) int {
	n := 0
	for _, line := range bytes.Split(patch, []byte{'\n'}) {
		if bytes.HasPrefix(line, []byte("diff --git ")) {
			n++
		}
	}
	return n
}
```

Add `"io"` to the file's import block alongside `bytes`, `errors`, `fmt`, `os`, `os/exec`, `path/filepath`, `strings`.

- [ ] **Step 4: Run the test to confirm it passes**

Run: `GOTOOLCHAIN=go1.24.0 go test ./internal/git/ -run TestMaterializeParentState_CleanParent -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/git/worktree_with_state.go internal/git/worktree_with_state_test.go
git commit -m "feat(git): MaterializeParentState scaffold + clean-parent case"
```

---

## Task 6: MaterializeParentState — staged + unstaged + partial-staged (index fidelity)

**Files:**
- Modify: `internal/git/worktree_with_state_test.go`

- [ ] **Step 1: Add failing tests**

Append to `internal/git/worktree_with_state_test.go`:

```go
// writeFile is a test helper that writes content to dir/path, creating
// parent directories as needed.
func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMaterializeParentState_StagedOnly(t *testing.T) {
	parent := initRepo(t)
	writeFile(t, parent, "a.txt", "staged\n")
	runGit(t, parent, "add", "a.txt")
	// Note: a.txt is staged-new (not yet committed) — appears only in --cached diff.

	wt := makeWorktree(t, parent)
	if _, err := MaterializeParentState(parent, wt, StateCopyOptions{}); err != nil {
		t.Fatalf("materialize failed: %v", err)
	}
	// New worktree's `git diff --cached` should match parent's.
	gotCached := runGit(t, wt, "diff", "--cached")
	wantCached := runGit(t, parent, "diff", "--cached")
	if gotCached != wantCached {
		t.Fatalf("staged diff mismatch:\nparent:\n%s\nnew:\n%s", wantCached, gotCached)
	}
}

func TestMaterializeParentState_UnstagedOnly(t *testing.T) {
	parent := initRepo(t)
	writeFile(t, parent, "README.md", "modified\n")
	// Modify a tracked file without staging.

	wt := makeWorktree(t, parent)
	if _, err := MaterializeParentState(parent, wt, StateCopyOptions{}); err != nil {
		t.Fatalf("materialize failed: %v", err)
	}
	gotUnstaged := runGit(t, wt, "diff")
	wantUnstaged := runGit(t, parent, "diff")
	if gotUnstaged != wantUnstaged {
		t.Fatalf("unstaged diff mismatch:\nparent:\n%s\nnew:\n%s", wantUnstaged, gotUnstaged)
	}
}

func TestMaterializeParentState_PartiallyStaged(t *testing.T) {
	parent := initRepo(t)
	// Add a multi-line tracked file and commit it as baseline.
	writeFile(t, parent, "data.txt", "line1\nline2\nline3\n")
	runGit(t, parent, "add", "data.txt")
	runGit(t, parent, "commit", "-m", "baseline")

	// Stage one change, then add a second unstaged change to the same file.
	writeFile(t, parent, "data.txt", "line1-staged\nline2\nline3\n")
	runGit(t, parent, "add", "data.txt")
	writeFile(t, parent, "data.txt", "line1-staged\nline2\nline3-unstaged\n")

	wt := makeWorktree(t, parent)
	if _, err := MaterializeParentState(parent, wt, StateCopyOptions{}); err != nil {
		t.Fatalf("materialize failed: %v", err)
	}

	gotCached := runGit(t, wt, "diff", "--cached")
	wantCached := runGit(t, parent, "diff", "--cached")
	if gotCached != wantCached {
		t.Fatalf("staged diff mismatch:\nparent:\n%s\nnew:\n%s", wantCached, gotCached)
	}
	gotUnstaged := runGit(t, wt, "diff")
	wantUnstaged := runGit(t, parent, "diff")
	if gotUnstaged != wantUnstaged {
		t.Fatalf("unstaged diff mismatch:\nparent:\n%s\nnew:\n%s", wantUnstaged, gotUnstaged)
	}
}
```

- [ ] **Step 2: Run tests**

Run: `GOTOOLCHAIN=go1.24.0 go test ./internal/git/ -run "TestMaterializeParentState_(StagedOnly|UnstagedOnly|PartiallyStaged)" -v`
Expected: PASS (3 subtests). Implementation from Task 5 already handles all three cases.

- [ ] **Step 3: Commit**

```bash
git add internal/git/worktree_with_state_test.go
git commit -m "test(git): cover staged + unstaged + partial-staged materialize cases"
```

---

## Task 7: MaterializeParentState — untracked + symlinks + exec bit

**Files:**
- Modify: `internal/git/worktree_with_state_test.go`

- [ ] **Step 1: Add failing tests**

Append to `internal/git/worktree_with_state_test.go`:

```go
func TestMaterializeParentState_Untracked(t *testing.T) {
	parent := initRepo(t)
	writeFile(t, parent, "new.txt", "new content\n")
	writeFile(t, parent, "sub/nested.txt", "nested\n")

	wt := makeWorktree(t, parent)
	res, err := MaterializeParentState(parent, wt, StateCopyOptions{})
	if err != nil {
		t.Fatalf("materialize failed: %v", err)
	}
	if res.UntrackedFilesCopied != 2 {
		t.Fatalf("expected 2 untracked copied, got %d", res.UntrackedFilesCopied)
	}
	for _, rel := range []string{"new.txt", "sub/nested.txt"} {
		b, err := os.ReadFile(filepath.Join(wt, rel))
		if err != nil {
			t.Fatalf("missing %s in new worktree: %v", rel, err)
		}
		want, _ := os.ReadFile(filepath.Join(parent, rel))
		if !bytes.Equal(b, want) {
			t.Fatalf("contents mismatch for %s", rel)
		}
	}
}

func TestMaterializeParentState_PreservesExecBit(t *testing.T) {
	parent := initRepo(t)
	scriptPath := filepath.Join(parent, "run.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	wt := makeWorktree(t, parent)
	if _, err := MaterializeParentState(parent, wt, StateCopyOptions{}); err != nil {
		t.Fatalf("materialize failed: %v", err)
	}
	info, err := os.Stat(filepath.Join(wt, "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("exec bit not preserved: mode=%o", info.Mode().Perm())
	}
}

func TestMaterializeParentState_Symlink(t *testing.T) {
	parent := initRepo(t)
	if err := os.Symlink("README.md", filepath.Join(parent, "link-to-readme")); err != nil {
		t.Skipf("symlinks unsupported in this filesystem: %v", err)
	}

	wt := makeWorktree(t, parent)
	if _, err := MaterializeParentState(parent, wt, StateCopyOptions{}); err != nil {
		t.Fatalf("materialize failed: %v", err)
	}
	got, err := os.Readlink(filepath.Join(wt, "link-to-readme"))
	if err != nil {
		t.Fatalf("expected symlink in new worktree: %v", err)
	}
	if got != "README.md" {
		t.Fatalf("symlink target mismatch: got %q want %q", got, "README.md")
	}
}
```

Also ensure `bytes` is in the file's import block.

- [ ] **Step 2: Run tests**

Run: `GOTOOLCHAIN=go1.24.0 go test ./internal/git/ -run "TestMaterializeParentState_(Untracked|PreservesExecBit|Symlink)" -v`
Expected: PASS (3 subtests; Symlink may SKIP on filesystems without symlink support).

- [ ] **Step 3: Commit**

```bash
git add internal/git/worktree_with_state_test.go
git commit -m "test(git): cover untracked + exec-bit + symlink materialize cases"
```

---

## Task 8: MaterializeParentState — binary file modification

**Files:**
- Modify: `internal/git/worktree_with_state_test.go`

- [ ] **Step 1: Add failing test**

Append to `internal/git/worktree_with_state_test.go`:

```go
func TestMaterializeParentState_BinaryFile(t *testing.T) {
	parent := initRepo(t)
	// Commit a baseline binary file.
	bin1 := []byte{0x00, 0x01, 0x02, 0x03, 0xff, 0xfe}
	if err := os.WriteFile(filepath.Join(parent, "img.bin"), bin1, 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, parent, "add", "img.bin")
	runGit(t, parent, "commit", "-m", "baseline binary")

	// Modify the binary.
	bin2 := []byte{0xde, 0xad, 0xbe, 0xef, 0x00, 0x01}
	if err := os.WriteFile(filepath.Join(parent, "img.bin"), bin2, 0o644); err != nil {
		t.Fatal(err)
	}

	wt := makeWorktree(t, parent)
	if _, err := MaterializeParentState(parent, wt, StateCopyOptions{}); err != nil {
		t.Fatalf("materialize failed: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(wt, "img.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, bin2) {
		t.Fatalf("binary content mismatch:\ngot:  %x\nwant: %x", got, bin2)
	}
}
```

- [ ] **Step 2: Run test**

Run: `GOTOOLCHAIN=go1.24.0 go test ./internal/git/ -run TestMaterializeParentState_BinaryFile -v`
Expected: PASS. (`git diff --binary` produces binary deltas that `git apply` reconstructs exactly.)

- [ ] **Step 3: Commit**

```bash
git add internal/git/worktree_with_state_test.go
git commit -m "test(git): cover binary file materialize"
```

---

## Task 9: MaterializeParentState — gitignored opt-in

**Files:**
- Modify: `internal/git/worktree_with_state_test.go`

- [ ] **Step 1: Add failing tests**

Append to `internal/git/worktree_with_state_test.go`:

```go
func TestMaterializeParentState_GitignoredExcludedByDefault(t *testing.T) {
	parent := initRepo(t)
	writeFile(t, parent, ".gitignore", "*.env\n")
	runGit(t, parent, "add", ".gitignore")
	runGit(t, parent, "commit", "-m", "gitignore")
	writeFile(t, parent, "secret.env", "API_KEY=xyz\n")

	wt := makeWorktree(t, parent)
	if _, err := MaterializeParentState(parent, wt, StateCopyOptions{}); err != nil {
		t.Fatalf("materialize failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt, "secret.env")); !os.IsNotExist(err) {
		t.Fatalf("gitignored file was copied by default: err=%v", err)
	}
}

func TestMaterializeParentState_GitignoredIncludedWhenOptedIn(t *testing.T) {
	parent := initRepo(t)
	writeFile(t, parent, ".gitignore", "*.env\n")
	runGit(t, parent, "add", ".gitignore")
	runGit(t, parent, "commit", "-m", "gitignore")
	writeFile(t, parent, "secret.env", "API_KEY=xyz\n")

	wt := makeWorktree(t, parent)
	res, err := MaterializeParentState(parent, wt, StateCopyOptions{IncludeGitignored: true})
	if err != nil {
		t.Fatalf("materialize failed: %v", err)
	}
	if res.GitignoredFilesCopied != 1 {
		t.Fatalf("expected 1 gitignored copied, got %d", res.GitignoredFilesCopied)
	}
	got, err := os.ReadFile(filepath.Join(wt, "secret.env"))
	if err != nil {
		t.Fatalf("expected gitignored file in new worktree: %v", err)
	}
	if string(got) != "API_KEY=xyz\n" {
		t.Fatalf("gitignored content mismatch: %q", got)
	}
}
```

- [ ] **Step 2: Run tests**

Run: `GOTOOLCHAIN=go1.24.0 go test ./internal/git/ -run "TestMaterializeParentState_Gitignored" -v`
Expected: PASS (2 subtests).

- [ ] **Step 3: Commit**

```bash
git add internal/git/worktree_with_state_test.go
git commit -m "test(git): cover gitignored exclude-by-default + opt-in"
```

---

## Task 10: MaterializeParentState — parent-untouched invariant + staged deletion

**Files:**
- Modify: `internal/git/worktree_with_state_test.go`

- [ ] **Step 1: Add failing tests**

Append to `internal/git/worktree_with_state_test.go`:

```go
func TestMaterializeParentState_ParentUntouched(t *testing.T) {
	parent := initRepo(t)
	writeFile(t, parent, "tracked.txt", "tracked\n")
	runGit(t, parent, "add", "tracked.txt")
	runGit(t, parent, "commit", "-m", "tracked")
	// Make a complex parent state: staged, unstaged, untracked.
	writeFile(t, parent, "tracked.txt", "staged-edit\n")
	runGit(t, parent, "add", "tracked.txt")
	writeFile(t, parent, "tracked.txt", "staged-edit\nunstaged-more\n")
	writeFile(t, parent, "new-untracked.txt", "untracked\n")

	statusBefore := runGit(t, parent, "status", "--porcelain")
	diffCachedBefore := runGit(t, parent, "diff", "--cached")
	diffBefore := runGit(t, parent, "diff")
	stashBefore := runGit(t, parent, "stash", "list")

	wt := makeWorktree(t, parent)
	if _, err := MaterializeParentState(parent, wt, StateCopyOptions{}); err != nil {
		t.Fatalf("materialize failed: %v", err)
	}

	if got := runGit(t, parent, "status", "--porcelain"); got != statusBefore {
		t.Fatalf("parent status changed:\nbefore:\n%s\nafter:\n%s", statusBefore, got)
	}
	if got := runGit(t, parent, "diff", "--cached"); got != diffCachedBefore {
		t.Fatalf("parent staged diff changed")
	}
	if got := runGit(t, parent, "diff"); got != diffBefore {
		t.Fatalf("parent unstaged diff changed")
	}
	if got := runGit(t, parent, "stash", "list"); got != stashBefore {
		t.Fatalf("parent stash list changed:\nbefore:\n%s\nafter:\n%s", stashBefore, got)
	}
}

func TestMaterializeParentState_StagedDeletion(t *testing.T) {
	parent := initRepo(t)
	writeFile(t, parent, "doomed.txt", "delete me\n")
	runGit(t, parent, "add", "doomed.txt")
	runGit(t, parent, "commit", "-m", "add doomed")
	runGit(t, parent, "rm", "doomed.txt")
	// doomed.txt is now staged-for-deletion in parent.

	wt := makeWorktree(t, parent)
	if _, err := MaterializeParentState(parent, wt, StateCopyOptions{}); err != nil {
		t.Fatalf("materialize failed: %v", err)
	}
	gotCached := runGit(t, wt, "diff", "--cached")
	wantCached := runGit(t, parent, "diff", "--cached")
	if gotCached != wantCached {
		t.Fatalf("staged deletion mismatch:\nparent:\n%s\nnew:\n%s", wantCached, gotCached)
	}
	if _, err := os.Stat(filepath.Join(wt, "doomed.txt")); !os.IsNotExist(err) {
		t.Fatalf("doomed.txt should not exist in new worktree's working tree: err=%v", err)
	}
}
```

- [ ] **Step 2: Run tests**

Run: `GOTOOLCHAIN=go1.24.0 go test ./internal/git/ -run "TestMaterializeParentState_(ParentUntouched|StagedDeletion)" -v`
Expected: PASS (2 subtests).

- [ ] **Step 3: Commit**

```bash
git add internal/git/worktree_with_state_test.go
git commit -m "test(git): cover parent-untouched invariant + staged deletion"
```

---

## Task 11: Run full git test suite

- [ ] **Step 1: Race-checked full run**

Run: `GOTOOLCHAIN=go1.24.0 go test ./internal/git/... -race -count=1`
Expected: PASS — all existing and new tests green.

- [ ] **Step 2: Commit (no-op unless something needs fixing)**

If failures appear, fix them inline and re-run. No commit unless fixes were made.

---

## Task 12: CLI flag parsing + implication helper

**Files:**
- Modify: `cmd/agent-deck/session_cmd.go` (the `handleSessionFork` function and a new helper near it)
- Create: `cmd/agent-deck/session_cmd_fork_state_test.go`

The implication chain is unit-testable in isolation. We add a small pure-function helper and test it before wiring it into the handler.

- [ ] **Step 1: Add failing tests**

Create `cmd/agent-deck/session_cmd_fork_state_test.go`:

```go
package main

import "testing"

func TestResolveForkStateFlags_AllOff(t *testing.T) {
	r := resolveForkStateFlags(false, false, "", false)
	if r.WorktreeEnabled || r.WithState || r.IncludeGitignored {
		t.Fatalf("all-off should yield all-false, got %+v", r)
	}
}

func TestResolveForkStateFlags_GitignoredImpliesWithStateOnly(t *testing.T) {
	r := resolveForkStateFlags(false, true, "", false)
	if !r.WithState || !r.IncludeGitignored {
		t.Fatalf("--with-state-and-gitignored should imply --with-state, got %+v", r)
	}
	if r.WorktreeEnabled || r.Branch != "" {
		t.Fatalf("CLI should not auto-enable or auto-name worktree for gitignored-only flag, got %+v", r)
	}
}

func TestResolveForkStateFlags_WithStateDoesNotAutoNameOrEnableWorktree(t *testing.T) {
	r := resolveForkStateFlags(true, false, "", false)
	if !r.WithState {
		t.Fatalf("--with-state should be preserved, got %+v", r)
	}
	if r.WorktreeEnabled || r.Branch != "" {
		t.Fatalf("CLI should not auto-enable or auto-name worktree for --with-state, got %+v", r)
	}
	if r.IncludeGitignored {
		t.Fatalf("--with-state alone should not enable gitignored, got %+v", r)
	}
}

func TestResolveForkStateFlags_ExplicitBranchPreserved(t *testing.T) {
	r := resolveForkStateFlags(true, false, "my-branch", true)
	if !r.WorktreeEnabled {
		t.Fatalf("explicit -w should enable worktree, got %+v", r)
	}
	if r.Branch != "my-branch" {
		t.Fatalf("explicit branch lost: %+v", r)
	}
}

func TestResolveForkStateFlags_WorktreeOnlyNoStateNoGitignored(t *testing.T) {
	r := resolveForkStateFlags(false, false, "feat", true)
	if !r.WorktreeEnabled {
		t.Fatalf("explicit -w should enable worktree, got %+v", r)
	}
	if r.WithState || r.IncludeGitignored {
		t.Fatalf("-w alone should not enable state/gitignored, got %+v", r)
	}
}
```

- [ ] **Step 2: Run tests to confirm they fail**

Run: `GOTOOLCHAIN=go1.24.0 go test ./cmd/agent-deck/ -run TestResolveForkStateFlags -v`
Expected: FAIL — `undefined: resolveForkStateFlags`.

- [ ] **Step 3: Add the helper in session_cmd.go**

In `cmd/agent-deck/session_cmd.go`, add this helper just before `handleSessionFork` (around line 587):

```go
// forkStateFlags is the resolved fork-with-state configuration after
// applying the CLI implication chain: --with-state-and-gitignored implies
// --with-state. The CLI does not auto-enable or auto-name worktrees; callers
// must reject --with-state unless -w/--worktree supplied an explicit branch.
type forkStateFlags struct {
	WorktreeEnabled   bool
	WithState         bool
	IncludeGitignored bool
	Branch            string
}

// resolveForkStateFlags applies the implication chain. The branch name is
// propagated unchanged. CLI callers must not auto-name; --with-state without
// an explicit worktree branch is rejected in handleSessionFork.
func resolveForkStateFlags(withState, gitignored bool, branch string, worktreeExplicit bool) forkStateFlags {
	r := forkStateFlags{Branch: branch, WorktreeEnabled: worktreeExplicit}
	if gitignored {
		r.IncludeGitignored = true
		r.WithState = true
		return r
	}
	if withState {
		r.WithState = true
		return r
	}
	return r
}
```

- [ ] **Step 4: Run tests**

Run: `GOTOOLCHAIN=go1.24.0 go test ./cmd/agent-deck/ -run TestResolveForkStateFlags -v`
Expected: PASS (5 subtests).

- [ ] **Step 5: Commit**

```bash
git add cmd/agent-deck/session_cmd.go cmd/agent-deck/session_cmd_fork_state_test.go
git commit -m "feat(cli): resolveForkStateFlags helper for --with-state implication chain"
```

---

## Task 12A: CLI contract tests for fork-with-state handler

**Files:**
- Modify: `cmd/agent-deck/session_cmd.go`
- Modify: `cmd/agent-deck/session_cmd_fork_state_test.go`

Task 14 below is git-side integration coverage. This task adds the missing real CLI/handler contract coverage so flag registration, refusal messages, destination validation, and option propagation cannot regress while helper tests stay green.

- [ ] **Step 1: Add a test-only before-start hook**

In `cmd/agent-deck/session_cmd.go`, near `handleSessionFork`, add:

```go
// sessionForkBeforeStartHook is nil in production. Tests use it to inspect the
// fully prepared fork and options after CLI parsing/worktree preparation, but
// before tmux Start() mutates the environment.
var sessionForkBeforeStartHook func(parent *session.Instance, forked *session.Instance, opts *session.ClaudeOptions)
```

Then in `handleSessionFork`, after sandbox config is applied and immediately before `forkedInst.Start()`, add:

```go
	if sessionForkBeforeStartHook != nil {
		sessionForkBeforeStartHook(inst, forkedInst, opts)
		return
	}
```

- [ ] **Step 2: Add CLI-contract helpers and tests**

In `cmd/agent-deck/session_cmd_fork_state_test.go`, replace the single `testing` import with:

```go
import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)
```

Then append:

```go
const forkStateCLIProfile = "ch_support_test"

func seedForkableCLISession(t *testing.T, home, title string) string {
	t.Helper()
	t.Setenv("HOME", home)

	repo := filepath.Join(home, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runGit("init", "-b", "main")
	runGit("config", "user.email", "t@t")
	runGit("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "README.md")
	runGit("commit", "-m", "init")

	storage, err := session.NewStorageWithProfile(forkStateCLIProfile)
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })

	inst := session.NewInstance(title, repo)
	inst.Tool = "claude"
	inst.ClaudeSessionID = "parent-session-id"
	inst.ClaudeDetectedAt = time.Now()
	if err := storage.SaveWithGroups([]*session.Instance{inst}, nil); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}
	return repo
}

func TestSessionFork_WithStateRequiresExplicitDestinationBranch(t *testing.T) {
	home := t.TempDir()
	seedForkableCLISession(t, home, "parent")

	stdout, stderr, code := runAgentDeck(t, home, "session", "fork", "parent", "--with-state")
	if code == 0 {
		t.Fatalf("expected non-zero exit\nstdout: %s\nstderr: %s", stdout, stderr)
	}
	if !strings.Contains(stderr+stdout, "--with-state requires --worktree <new-branch>") {
		t.Fatalf("missing explicit-branch error\nstdout: %s\nstderr: %s", stdout, stderr)
	}
}

func TestSessionFork_WithStateAndGitignoredRequiresExplicitDestinationBranch(t *testing.T) {
	home := t.TempDir()
	seedForkableCLISession(t, home, "parent")

	stdout, stderr, code := runAgentDeck(t, home, "session", "fork", "parent", "--with-state-and-gitignored")
	if code == 0 {
		t.Fatalf("expected non-zero exit\nstdout: %s\nstderr: %s", stdout, stderr)
	}
	if !strings.Contains(stderr+stdout, "--with-state requires --worktree <new-branch>") {
		t.Fatalf("missing explicit-branch error\nstdout: %s\nstderr: %s", stdout, stderr)
	}
}

func TestSessionFork_WithState_RejectsExistingDestinationBranch(t *testing.T) {
	home := t.TempDir()
	repo := seedForkableCLISession(t, home, "parent")
	if out, err := exec.Command("git", "-C", repo, "branch", "fork/existing").CombinedOutput(); err != nil {
		t.Fatalf("create branch: %v\n%s", err, out)
	}

	stdout, stderr, code := runAgentDeck(t, home, "session", "fork", "parent", "--with-state", "-w", "fork/existing")
	if code == 0 {
		t.Fatalf("expected non-zero exit\nstdout: %s\nstderr: %s", stdout, stderr)
	}
	if !strings.Contains(stderr+stdout, "branch 'fork/existing' already exists") {
		t.Fatalf("missing existing-branch error\nstdout: %s\nstderr: %s", stdout, stderr)
	}
}

func TestSessionFork_WithState_RejectsExistingDestinationWorktree(t *testing.T) {
	home := t.TempDir()
	repo := seedForkableCLISession(t, home, "parent")
	existingWT := filepath.Join(home, "existing-wt")
	if out, err := exec.Command("git", "-C", repo, "worktree", "add", "-b", "fork/used", existingWT).CombinedOutput(); err != nil {
		t.Fatalf("create worktree: %v\n%s", err, out)
	}

	stdout, stderr, code := runAgentDeck(t, home, "session", "fork", "parent", "--with-state", "-w", "fork/used")
	if code == 0 {
		t.Fatalf("expected non-zero exit\nstdout: %s\nstderr: %s", stdout, stderr)
	}
	if !strings.Contains(stderr+stdout, "branch 'fork/used' already has a worktree") {
		t.Fatalf("missing existing-worktree error\nstdout: %s\nstderr: %s", stdout, stderr)
	}
}

func TestSessionFork_WithStateAndGitignored_PropagatesOptionsBeforeStart(t *testing.T) {
	home := t.TempDir()
	seedForkableCLISession(t, home, "parent")

	var captured *session.ClaudeOptions
	oldHook := sessionForkBeforeStartHook
	sessionForkBeforeStartHook = func(_ *session.Instance, _ *session.Instance, opts *session.ClaudeOptions) {
		copied := *opts
		captured = &copied
	}
	t.Cleanup(func() { sessionForkBeforeStartHook = oldHook })

	handleSessionFork(forkStateCLIProfile, []string{"parent", "--with-state-and-gitignored", "-w", "fork/with-env"})

	if captured == nil {
		t.Fatal("before-start hook did not capture options")
	}
	if !captured.WithState || !captured.IncludeGitignored {
		t.Fatalf("state flags not propagated: %+v", captured)
	}
	if captured.WorktreeBranch != "fork/with-env" || captured.WorktreePath == "" {
		t.Fatalf("worktree destination not propagated: %+v", captured)
	}
}
```

- [ ] **Step 3: Run the CLI refusal tests and confirm they fail before Task 13**

Run:

```bash
GOTOOLCHAIN=go1.24.0 go test ./cmd/agent-deck/ -run "TestSessionFork_WithState(RequiresExplicitDestinationBranch|AndGitignoredRequiresExplicitDestinationBranch|_RejectsExistingDestination(Branch|Worktree))" -v
```

Expected before Task 13: FAIL, proving the current CLI does not yet provide the new contract. After Task 13, the same command must pass.

---

## Task 13: CLI fork handler — wire new flags, sequence Create→Materialize→Setup with cleanup

**Files:**
- Modify: `cmd/agent-deck/session_cmd.go:587-799` (the `handleSessionFork` function)

- [ ] **Step 1: Add the new flags to the flagset**

In `handleSessionFork`, after the existing flag declarations (around line 602, just after `sandboxImage`), add:

```go
	withState := fs.Bool("with-state", false, "Carry parent's tracked + staged + untracked state into a new worktree. Requires --worktree <new-branch>.")
	withStateAndGitignored := fs.Bool("with-state-and-gitignored", false, "Also copy gitignored files. Implies --with-state.")
```

Also extend the `fs.Usage` example block (around line 612-617) by adding two lines after the existing examples:

```go
		fmt.Println("  agent-deck session fork my-project --with-state -w fork/my-project-wip")
		fmt.Println("  agent-deck session fork my-project --with-state-and-gitignored -w fork/my-fork-with-env -t \"my-fork-with-env\"")
```

- [ ] **Step 2: Apply the implication chain after parsing**

Inside `handleSessionFork`, just after the existing `wtBranch` and `createNewBranch` resolution block (around line 684-688), insert:

```go
	// Apply implication chain: --with-state-and-gitignored → --with-state.
	// The CLI does not auto-name the destination branch. Users must provide
	// -w/--worktree <new-branch>; the TUI is the surface that suggests names.
	stateFlags := resolveForkStateFlags(*withState, *withStateAndGitignored, wtBranch, wtBranch != "")
	if stateFlags.WithState && wtBranch == "" {
		out.Error("--with-state requires --worktree <new-branch>", ErrCodeInvalidOperation)
		os.Exit(1)
	}
	// Fork-with-state uses CreateWorktreeAtStartPoint, which always creates the
	// branch and returns createdBranch as proof for cleanup. The legacy
	// createNewBranch intent flag is not consulted on the with-state path; the
	// branch-existence guard below is unreachable from this branch.
```

No new import is required for this step.

- [ ] **Step 3: Add destination validation and shared pre-flight refusal of in-progress operations**

Add `errors` to the imports in `cmd/agent-deck/session_cmd.go`. Inside the `if wtBranch != "" { ... }` block, after `repoRoot` is resolved and after `wtBranch = wtSettings.ApplyBranchPrefix(wtBranch)`, replace the existing branch-existence validation with:

```go
		parentHead := ""
		if stateFlags.WithState {
			if err := git.ValidateForkWithStateDestination(repoRoot, wtBranch); err != nil {
				var collErr *git.DestinationCollisionError
				if errors.As(err, &collErr) {
					switch collErr.Kind {
					case "worktree_exists":
						out.Error(fmt.Sprintf("branch '%s' already has a worktree at %s; choose a new destination branch for --with-state", collErr.Branch, collErr.Path), ErrCodeInvalidOperation)
					case "branch_exists":
						out.Error(fmt.Sprintf("branch '%s' already exists; choose a new destination branch for --with-state", collErr.Branch), ErrCodeInvalidOperation)
					default:
						out.Error(collErr.Error(), ErrCodeInvalidOperation)
					}
					os.Exit(1)
				}
				out.Error(fmt.Sprintf("failed to validate destination: %v", err), ErrCodeInvalidOperation)
				os.Exit(1)
			}
			preflight, err := git.PreflightForkWithState(inst.ProjectPath)
			if err != nil {
				var opErr *git.InProgressOperationError
				if errors.As(err, &opErr) {
					hint := ""
					switch opErr.Kind {
					case "rebase":
						hint = fmt.Sprintf("finish or abort the rebase before forking with state (cd %s && git rebase --abort)", inst.ProjectPath)
					case "merge":
						hint = "resolve or abort the merge before forking with state"
					case "cherry-pick":
						hint = "finish or abort the cherry-pick before forking with state"
					case "bisect":
						hint = fmt.Sprintf("run 'git bisect reset' in %s before forking with state", inst.ProjectPath)
					}
					out.Error(fmt.Sprintf("parent session is mid-%s; %s", opErr.Kind, hint), ErrCodeInvalidOperation)
					os.Exit(1)
				}
				out.Error(fmt.Sprintf("failed to inspect parent's git state: %v", err), ErrCodeInvalidOperation)
				os.Exit(1)
			}
			if preflight.HasSubmodules {
				fmt.Fprintln(os.Stderr, "Warning: submodules detected — copied as files, not recursed (parent's submodule states preserved)")
			}
			parentHead = preflight.ParentHead
		} else if !createNewBranch && !git.BranchExists(repoRoot, wtBranch) {
			out.Error(fmt.Sprintf("branch '%s' does not exist (use -b to create)", wtBranch), ErrCodeInvalidOperation)
			os.Exit(1)
		}
```

Leave the existing-worktree reuse block (today's `session_cmd.go:720-743`) **unchanged**. With-state cannot reach it because `ValidateForkWithStateDestination` already refused above; for non-with-state, today's reuse behavior is preserved exactly. No edits to that block in this step.

- [ ] **Step 4: Replace the CreateWorktreeWithSetup call with Create → Materialize → Setup**

In the same `if wtBranch != "" { ... }` block, find the existing call to `git.CreateWorktreeWithSetup` (around line 735) and the warning print on line 740-742. Replace those lines with:

```go
			var createErr error
			createdBranch := false
			if stateFlags.WithState {
				createdBranch, createErr = git.CreateWorktreeAtStartPoint(repoRoot, worktreePath, wtBranch, parentHead)
			} else {
				createErr = git.CreateWorktree(repoRoot, worktreePath, wtBranch)
			}
			if createErr != nil {
				out.Error(fmt.Sprintf("worktree creation failed: %v", createErr), ErrCodeInvalidOperation)
				os.Exit(1)
			}

			if stateFlags.WithState {
				_, materializeErr := git.MaterializeParentState(
					inst.ProjectPath,
					worktreePath,
					git.StateCopyOptions{IncludeGitignored: stateFlags.IncludeGitignored},
				)
				if materializeErr != nil {
					// Cleanup: remove the half-baked worktree. Delete the branch only
					// when creation returned proof that this operation created it.
					_ = exec.Command("git", "-C", repoRoot, "worktree", "remove", "--force", worktreePath).Run()
					if createdBranch {
						_ = exec.Command("git", "-C", repoRoot, "branch", "-D", wtBranch).Run()
					}
					out.Error(fmt.Sprintf("failed to materialize parent state: %v; new worktree cleaned up", materializeErr), ErrCodeInvalidOperation)
					os.Exit(1)
				}
			}

			setupErr := git.RunWorktreeSetup(repoRoot, worktreePath, os.Stdout, os.Stderr, session.GetWorktreeSettings().SetupTimeout())
			if setupErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: worktree setup script failed: %v\n", setupErr)
			}
```

Make sure `"os/exec"` is in the imports at the top of `cmd/agent-deck/session_cmd.go`. If not, add it.

- [ ] **Step 5: Propagate WithState and IncludeGitignored to ClaudeOptions**

In the same block, find where `opts` is set (around line 745-750) and add the two new fields at the end:

```go
			opts = session.NewClaudeOptions(userConfig)
			opts.WorkDir = worktreePath
			opts.WorktreePath = worktreePath
			opts.WorktreeRepoRoot = repoRoot
			opts.WorktreeBranch = wtBranch
			opts.WithState = stateFlags.WithState
			opts.IncludeGitignored = stateFlags.IncludeGitignored
```

- [ ] **Step 6: Verify the package compiles**

Run: `GOTOOLCHAIN=go1.24.0 go build ./cmd/agent-deck/...`
Expected: exits 0, no output.

- [ ] **Step 7: Run CLI contract tests and existing fork tests**

Run:

```bash
GOTOOLCHAIN=go1.24.0 go test ./cmd/agent-deck/ -run "TestSessionFork_WithState(RequiresExplicitDestinationBranch|AndGitignoredRequiresExplicitDestinationBranch|_RejectsExistingDestination(Branch|Worktree)|AndGitignored_PropagatesOptionsBeforeStart)" -race -count=1
GOTOOLCHAIN=go1.24.0 go test ./cmd/agent-deck/ -run "Fork|fork" -race -count=1
```

Expected: PASS — Task 12A's CLI contract tests now pass and existing fork tests remain green.

- [ ] **Step 8: Commit**

```bash
git add cmd/agent-deck/session_cmd.go cmd/agent-deck/session_cmd_fork_state_test.go
git commit -m "feat(cli): wire --with-state[-and-gitignored] flags through fork handler with cleanup"
```

---

## Task 14: Git-side integration tests — materialization sequence for fork-with-state

**Files:**
- Create: `internal/git/worktree_with_state_integration_test.go`

The real CLI/handler contract is covered in Task 12A and Task 13. This task covers the *git-side* end-to-end behavior: simulate the sequence of git calls the handler makes (`CreateWorktreeAtStartPoint → MaterializeParentState → RunWorktreeSetup`) against a real repo and verify the resulting worktree contents.

This follows the existing `internal/git/setup_progress_test.go`, `internal/git/setup_test.go`, and `internal/git/bare_repo_test.go` pattern: direct git-helper integration lives in `internal/git`, while `cmd/agent-deck/session_cmd_fork_state_test.go` stays focused on CLI/handler contract tests.

- [ ] **Step 1: Add the failing git-side integration tests**

Create `internal/git/worktree_with_state_integration_test.go`:

```go
package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// integrationFork sets up a parent repo with mixed state, then runs the
// same git sequence the CLI handler runs: CreateWorktreeAtStartPoint →
// MaterializeParentState → RunWorktreeSetup. Returns the worktree path so
// the test can assert on it.
func integrationFork(t *testing.T, gitignored bool) (parent, worktree string) {
	t.Helper()
	parent = t.TempDir()
	runShell := func(args ...string) {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = parent
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	runShell("git", "init", "-b", "main")
	runShell("git", "config", "user.email", "t@t")
	runShell("git", "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(parent, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runShell("git", "add", "README.md")
	runShell("git", "commit", "-m", "init")
	// Dirty state: tracked edit + untracked + gitignored.
	if err := os.WriteFile(filepath.Join(parent, "README.md"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "new.txt"), []byte("untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, ".gitignore"), []byte("*.env\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runShell("git", "add", ".gitignore")
	runShell("git", "commit", "-m", "gi")
	if err := os.WriteFile(filepath.Join(parent, "secret.env"), []byte("KEY=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	worktree = parent + "-wt"
	parentHead, err := HeadCommit(parent)
	if err != nil {
		t.Fatalf("HeadCommit: %v", err)
	}
	if createdBranch, err := CreateWorktreeAtStartPoint(parent, worktree, "fork/test", parentHead); err != nil {
		t.Fatalf("CreateWorktreeAtStartPoint: %v", err)
	} else if !createdBranch {
		t.Fatal("CreateWorktreeAtStartPoint returned createdBranch=false")
	}
	if _, err := MaterializeParentState(parent, worktree, StateCopyOptions{IncludeGitignored: gitignored}); err != nil {
		t.Fatalf("MaterializeParentState: %v", err)
	}
	if err := RunWorktreeSetup(parent, worktree, os.Stdout, os.Stderr, 0); err != nil {
		t.Fatalf("RunWorktreeSetup: %v", err)
	}
	return parent, worktree
}

func TestForkWithStateGitSequence_DirtyParent(t *testing.T) {
	_, wt := integrationFork(t, false)

	got, err := os.ReadFile(filepath.Join(wt, "README.md"))
	if err != nil || string(got) != "edited\n" {
		t.Fatalf("tracked edit not materialized: %q err=%v", got, err)
	}
	got, err = os.ReadFile(filepath.Join(wt, "new.txt"))
	if err != nil || string(got) != "untracked\n" {
		t.Fatalf("untracked not materialized: %q err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(wt, "secret.env")); !os.IsNotExist(err) {
		t.Fatalf("gitignored file leaked without opt-in: err=%v", err)
	}
}

func TestForkWithStateGitSequence_CleanParent(t *testing.T) {
	parent := t.TempDir()
	runShell := func(args ...string) {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = parent
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	runShell("git", "init", "-b", "main")
	runShell("git", "config", "user.email", "t@t")
	runShell("git", "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(parent, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runShell("git", "add", "README.md")
	runShell("git", "commit", "-m", "init")

	worktree := parent + "-clean-wt"
	parentHead, err := HeadCommit(parent)
	if err != nil {
		t.Fatalf("HeadCommit: %v", err)
	}
	if _, err := CreateWorktreeAtStartPoint(parent, worktree, "fork/clean", parentHead); err != nil {
		t.Fatalf("CreateWorktreeAtStartPoint: %v", err)
	}
	if _, err := MaterializeParentState(parent, worktree, StateCopyOptions{}); err != nil {
		t.Fatalf("MaterializeParentState: %v", err)
	}
	out, err := exec.Command("git", "-C", worktree, "status", "--short").CombinedOutput()
	if err != nil {
		t.Fatalf("git status: %v\n%s", err, out)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("clean parent produced dirty fork:\n%s", out)
	}
}

func TestForkWithStateGitSequence_WithGitignored(t *testing.T) {
	_, wt := integrationFork(t, true)
	got, err := os.ReadFile(filepath.Join(wt, "secret.env"))
	if err != nil {
		t.Fatalf("gitignored file missing: %v", err)
	}
	if string(got) != "KEY=1\n" {
		t.Fatalf("gitignored content mismatch: %q", got)
	}
}

func TestForkWithStateGitSequence_CleansUpOnMaterializeFailure(t *testing.T) {
	parent := t.TempDir()
	runShell := func(args ...string) {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = parent
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	runShell("git", "init", "-b", "main")
	runShell("git", "config", "user.email", "t@t")
	runShell("git", "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(parent, "f.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runShell("git", "add", "f.txt")
	runShell("git", "commit", "-m", "init")

	worktree := parent + "-wt"
	parentHead, err := HeadCommit(parent)
	if err != nil {
		t.Fatalf("HeadCommit: %v", err)
	}
	createdBranch, err := CreateWorktreeAtStartPoint(parent, worktree, "fork/test", parentHead)
	if err != nil {
		t.Fatalf("CreateWorktreeAtStartPoint: %v", err)
	}
	// Force a materialize failure: corrupt parent's git dir so `git diff` errors.
	// Replace the parent's HEAD ref with garbage.
	if err := os.WriteFile(filepath.Join(parent, ".git", "HEAD"), []byte("garbage\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := MaterializeParentState(parent, worktree, StateCopyOptions{}); err == nil {
		t.Fatal("expected MaterializeParentState to fail with corrupt parent HEAD")
	}
	// Simulate the handler's cleanup: remove the worktree and branch.
	_ = exec.Command("git", "-C", parent, "worktree", "remove", "--force", worktree).Run()
	if createdBranch {
		_ = exec.Command("git", "-C", parent, "branch", "-D", "fork/test").Run()
	}

	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Fatalf("worktree directory not cleaned up: err=%v", err)
	}
	branches := func() string {
		out, _ := exec.Command("git", "-C", parent, "branch").CombinedOutput()
		return string(out)
	}()
	if strings.Contains(branches, "fork/test") {
		t.Fatalf("branch fork/test not cleaned up:\n%s", branches)
	}
}

func TestForkWithStateGitSequence_FailsWhenMidRebase(t *testing.T) {
	parent := t.TempDir()
	runShell := func(args ...string) {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = parent
		_, _ = cmd.CombinedOutput()
	}
	mustShell := func(args ...string) {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = parent
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	mustShell("git", "init", "-b", "main")
	mustShell("git", "config", "user.email", "t@t")
	mustShell("git", "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(parent, "f.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustShell("git", "add", "f.txt")
	mustShell("git", "commit", "-m", "init")
	mustShell("git", "checkout", "-b", "side")
	if err := os.WriteFile(filepath.Join(parent, "f.txt"), []byte("side\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustShell("git", "commit", "-am", "s")
	mustShell("git", "checkout", "main")
	if err := os.WriteFile(filepath.Join(parent, "f.txt"), []byte("main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustShell("git", "commit", "-am", "m")
	runShell("git", "rebase", "side") // expected to conflict

	op, err := DetectInProgressOperation(parent)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if op != "rebase" {
		t.Fatalf("expected mid-rebase, got %q", op)
	}
	worktree := parent + "-rebase-wt"
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Fatalf("mid-rebase path should not create worktree, stat err=%v", err)
	}
}

func TestForkWithStateGitSequence_UsesParentWorktreeHead(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "base")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	runIn := func(dir string, args ...string) {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v\n%s", dir, args, err, out)
		}
	}
	outputIn := func(dir string, args ...string) string {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v\n%s", dir, args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	runIn(base, "git", "init", "-b", "main")
	runIn(base, "git", "config", "user.email", "t@t")
	runIn(base, "git", "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(base, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runIn(base, "git", "add", "README.md")
	runIn(base, "git", "commit", "-m", "base")

	parentWT := filepath.Join(root, "parent-wt")
	if err := CreateWorktree(base, parentWT, "parent-branch"); err != nil {
		t.Fatalf("CreateWorktree parent: %v", err)
	}
	if err := os.WriteFile(filepath.Join(parentWT, "README.md"), []byte("parent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runIn(parentWT, "git", "commit", "-am", "parent")

	baseHead := outputIn(base, "git", "rev-parse", "HEAD")
	parentHead, err := HeadCommit(parentWT)
	if err != nil {
		t.Fatalf("HeadCommit: %v", err)
	}
	if parentHead == baseHead {
		t.Fatal("setup invalid: parent and base HEAD should differ")
	}

	forkWT := filepath.Join(root, "fork-wt")
	if createdBranch, err := CreateWorktreeAtStartPoint(base, forkWT, "fork/from-parent", parentHead); err != nil {
		t.Fatalf("CreateWorktreeAtStartPoint: %v", err)
	} else if !createdBranch {
		t.Fatal("CreateWorktreeAtStartPoint returned createdBranch=false")
	}
	forkHead := outputIn(forkWT, "git", "rev-parse", "HEAD")
	if forkHead != parentHead {
		t.Fatalf("fork HEAD = %s, want parent HEAD %s (base HEAD %s)", forkHead, parentHead, baseHead)
	}
}

func TestForkWithStateGitSequence_BareRepoLayoutLinkedParentWorktree(t *testing.T) {
	root := t.TempDir()
	bareDir := filepath.Join(root, ".bare")
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "--bare", "-b", "main", bareDir)

	seed := filepath.Join(root, "seed")
	run("clone", bareDir, seed)
	runIn := func(dir string, args ...string) {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v\n%s", dir, args, err, out)
		}
	}
	runIn(seed, "git", "config", "user.email", "t@t")
	runIn(seed, "git", "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runIn(seed, "git", "add", "README.md")
	runIn(seed, "git", "commit", "-m", "init")
	runIn(seed, "git", "push", "origin", "main")

	// The bare repo/project root is metadata only. Fork-with-state still
	// materializes from a checked-out parent worktree because that is where
	// staged, unstaged, and untracked state exists.
	parentWT := filepath.Join(root, "parent-wt")
	run("-C", bareDir, "worktree", "add", parentWT, "main")
	if err := os.WriteFile(filepath.Join(parentWT, "wip.txt"), []byte("bare-layout-wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repoRoot, err := GetWorktreeBaseRoot(root)
	if err != nil {
		t.Fatalf("GetWorktreeBaseRoot: %v", err)
	}
	parentHead, err := HeadCommit(parentWT)
	if err != nil {
		t.Fatalf("HeadCommit: %v", err)
	}
	forkWT := filepath.Join(root, "fork-wt")
	if _, err := CreateWorktreeAtStartPoint(repoRoot, forkWT, "fork/bare", parentHead); err != nil {
		t.Fatalf("CreateWorktreeAtStartPoint: %v", err)
	}
	if _, err := MaterializeParentState(parentWT, forkWT, StateCopyOptions{}); err != nil {
		t.Fatalf("MaterializeParentState: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(forkWT, "wip.txt"))
	if err != nil || string(got) != "bare-layout-wip\n" {
		t.Fatalf("bare-layout WIP not materialized: %q err=%v", got, err)
	}
}

func TestForkWithStateGitSequence_MaterializesBeforeSetupHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell setup hook test uses /bin/sh")
	}
	parent := t.TempDir()
	runShell := func(args ...string) {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = parent
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	runShell("git", "init", "-b", "main")
	runShell("git", "config", "user.email", "t@t")
	runShell("git", "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(parent, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runShell("git", "add", "README.md")
	runShell("git", "commit", "-m", "init")

	scriptDir := filepath.Join(parent, ".agent-deck")
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	observed := filepath.Join(parent, "setup-observed.txt")
	script := fmt.Sprintf("#!/bin/sh\npwd > %q\ncat wip.txt >> %q\n", observed, observed)
	if err := os.WriteFile(filepath.Join(scriptDir, "worktree-setup.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "wip.txt"), []byte("from-parent\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	worktree := parent + "-setup-wt"
	parentHead, err := HeadCommit(parent)
	if err != nil {
		t.Fatalf("HeadCommit: %v", err)
	}
	if _, err := CreateWorktreeAtStartPoint(parent, worktree, "fork/setup", parentHead); err != nil {
		t.Fatalf("CreateWorktreeAtStartPoint: %v", err)
	}
	if _, err := MaterializeParentState(parent, worktree, StateCopyOptions{}); err != nil {
		t.Fatalf("MaterializeParentState: %v", err)
	}
	if err := RunWorktreeSetup(parent, worktree, os.Stdout, os.Stderr, 0); err != nil {
		t.Fatalf("RunWorktreeSetup: %v", err)
	}
	got, err := os.ReadFile(observed)
	if err != nil {
		t.Fatalf("setup output missing: %v", err)
	}
	if !strings.HasPrefix(string(got), worktree+"\n") || !strings.Contains(string(got), "from-parent\n") {
		t.Fatalf("setup did not run in materialized worktree, got:\n%s", got)
	}
}

```

- [ ] **Step 2: Run tests**

Run: `GOTOOLCHAIN=go1.24.0 go test ./internal/git/ -run "TestForkWithStateGitSequence" -race -count=1 -v`
Expected: PASS (8 tests).

- [ ] **Step 3: Commit**

```bash
git add internal/git/worktree_with_state_integration_test.go
git commit -m "test(git): cover fork-with-state materialization sequence"
```

---

## Task 15: TUI — add WithState and IncludeGitignored fields + checkbox rendering

**Files:**
- Modify: `internal/ui/forkdialog.go`

- [ ] **Step 1: Add fields to the ForkDialog struct**

In `internal/ui/forkdialog.go`, find the `ForkDialog` struct definition (around line 17-40) and add two fields just after `sandboxEnabled bool`:

```go
	// State-carrying support for fork-with-state
	withStateEnabled       bool
	withStateAndGitignored bool
```

- [ ] **Step 2: Add exported getters**

First update the existing `ToggleWorktree` method so turning worktree mode off also clears the nested fork-with-state flags:

```go
// ToggleWorktree toggles the worktree checkbox.
func (d *ForkDialog) ToggleWorktree() {
	d.worktreeEnabled = !d.worktreeEnabled
	if !d.worktreeEnabled {
		d.withStateEnabled = false
		d.withStateAndGitignored = false
	}
}
```

After the existing `IsSandboxEnabled` and `ToggleSandbox` methods (around line 202-209), add:

```go
// IsWithStateEnabled returns whether fork-with-state mode is enabled.
func (d *ForkDialog) IsWithStateEnabled() bool {
	return d.withStateEnabled
}

// ToggleWithState toggles fork-with-state mode. Has no effect unless the
// worktree checkbox is on (the surface only exposes this when worktree is on).
func (d *ForkDialog) ToggleWithState() {
	if !d.worktreeEnabled {
		return
	}
	d.withStateEnabled = !d.withStateEnabled
	if !d.withStateEnabled {
		// Turning off with-state also turns off its nested gitignored opt-in.
		d.withStateAndGitignored = false
	}
}

// IsWithStateAndGitignoredEnabled returns whether the gitignored opt-in
// is enabled.
func (d *ForkDialog) IsWithStateAndGitignoredEnabled() bool {
	return d.withStateAndGitignored
}

// ToggleWithStateAndGitignored toggles the gitignored sub-option. Has no
// effect unless with-state is already on.
func (d *ForkDialog) ToggleWithStateAndGitignored() {
	if !d.withStateEnabled {
		return
	}
	d.withStateAndGitignored = !d.withStateAndGitignored
}
```

- [ ] **Step 3: Reset fields in Show() and Hide()**

In the `Show` method (around line 97), after the existing `d.sandboxEnabled = false` line, add:

```go
	d.withStateEnabled = false
	d.withStateAndGitignored = false
```

- [ ] **Step 4: Render the new checkboxes inside the worktree section**

In the `View` method, find the worktree-section rendering block (around line 568-582). After the existing branch input rendering, before the closing brace of `if d.worktreeEnabled {`, add:

```go
			// With-state nested checkbox.
			withStateCb := "[ ]"
			if d.withStateEnabled {
				withStateCb = "[x]"
			}
			worktreeSection += "\n  " + checkboxStyle.Render(fmt.Sprintf("%s Carry parent state (press y)", withStateCb)) + "\n"

			// Gitignored sub-checkbox, only when with-state is on.
			if d.withStateEnabled {
				gitignoredCb := "[ ]"
				if d.withStateAndGitignored {
					gitignoredCb = "[x]"
				}
				worktreeSection += "    " + checkboxStyle.Render(fmt.Sprintf("%s Include gitignored files (press i)", gitignoredCb)) + "\n"
			}
```

- [ ] **Step 5: Wire the `y` and `i` key handlers**

In the `Update` method's `switch msg.String()` block (around line 397, after the existing `"s"` case), add:

```go
		case "y":
			// Toggle with-state when worktree is on.
			if d.worktreeEnabled {
				d.ToggleWithState()
				return d, nil
			}

		case "i":
			// Toggle gitignored sub-option when with-state is on.
			if d.worktreeEnabled && d.withStateEnabled {
				d.ToggleWithStateAndGitignored()
				return d, nil
			}
```

- [ ] **Step 6: Verify the package compiles**

Run: `GOTOOLCHAIN=go1.24.0 go build ./internal/ui/...`
Expected: exits 0.

- [ ] **Step 7: Commit**

```bash
git add internal/ui/forkdialog.go
git commit -m "feat(tui): add fork-with-state sub-checkboxes to ForkDialog"
```

---

## Task 15A: TUI — make fork-with-state controls real focus targets

**Files:**
- Modify: `internal/ui/forkdialog.go`

`NewDialog` already uses an explicit ordered `focusTargets` slice for dynamic focusable UI. Apply that same pattern to `ForkDialog` instead of extending the current numeric `focusIndex` special cases. The resulting focus order must match the spec:

- Git repo, worktree off: name -> group -> worktree -> conductor -> options
- Git repo, worktree on, with-state off: name -> group -> worktree -> branch -> with-state -> conductor -> options
- Git repo, worktree on, with-state on: name -> group -> worktree -> branch -> with-state -> gitignored -> conductor -> options
- Non-git repo: name -> group -> conductor -> options

- [ ] **Step 1: Add explicit fork focus targets**

Near the `ForkDialog` struct, add:

```go
type forkFocusTarget int

const (
	forkFocusName forkFocusTarget = iota
	forkFocusGroup
	forkFocusWorktree
	forkFocusBranch
	forkFocusWithState
	forkFocusGitignored
	forkFocusConductor
	forkFocusOptions
)
```

Then update the focus fields on `ForkDialog`:

```go
	focusTargets []forkFocusTarget
	focusIndex   int // Index into focusTargets.
```

- [ ] **Step 2: Replace numeric focus helpers with target helpers**

Remove `conductorFocusIndex()` and `optionsStartIndex()`. Add these helpers, modeled after `NewDialog`:

```go
func (d *ForkDialog) currentTarget() forkFocusTarget {
	if d.focusIndex < 0 || d.focusIndex >= len(d.focusTargets) {
		return forkFocusName
	}
	return d.focusTargets[d.focusIndex]
}

func (d *ForkDialog) indexOf(target forkFocusTarget) int {
	for i, t := range d.focusTargets {
		if t == target {
			return i
		}
	}
	return -1
}

func (d *ForkDialog) rebuildFocusTargets() {
	targets := []forkFocusTarget{forkFocusName, forkFocusGroup}
	if d.isGitRepo {
		targets = append(targets, forkFocusWorktree)
		if d.worktreeEnabled {
			targets = append(targets, forkFocusBranch, forkFocusWithState)
			if d.withStateEnabled {
				targets = append(targets, forkFocusGitignored)
			}
		}
	}
	if d.hasConductors() {
		targets = append(targets, forkFocusConductor)
	}
	targets = append(targets, forkFocusOptions)
	d.focusTargets = targets
	if d.focusIndex >= len(d.focusTargets) {
		d.focusIndex = len(d.focusTargets) - 1
	}
	if d.focusIndex < 0 {
		d.focusIndex = 0
	}
}
```

- [ ] **Step 3: Rebuild focus targets when conditional controls change**

At the end of `Show`, after config defaults and conductor setup are applied, rebuild focus targets and refocus the name input:

```go
	d.rebuildFocusTargets()
	d.focusIndex = d.indexOf(forkFocusName)
	d.updateFocus()
```

Update the state toggles from Task 15 so they rebuild dynamic targets:

```go
func (d *ForkDialog) ToggleWorktree() {
	d.worktreeEnabled = !d.worktreeEnabled
	if !d.worktreeEnabled {
		d.withStateEnabled = false
		d.withStateAndGitignored = false
	}
	d.rebuildFocusTargets()
}

func (d *ForkDialog) ToggleWithState() {
	if !d.worktreeEnabled {
		return
	}
	d.withStateEnabled = !d.withStateEnabled
	if !d.withStateEnabled {
		d.withStateAndGitignored = false
	}
	d.rebuildFocusTargets()
}
```

- [ ] **Step 4: Update navigation and focused-input dispatch**

In `Update`, replace `optStart := d.optionsStartIndex()` and the numeric tab/up/down branch-skipping logic with target-based navigation:

```go
	cur := d.currentTarget()
	switch msg.String() {
	case "tab", "down":
		if msg.String() == "down" && cur == forkFocusConductor {
			if d.conductorCursor < len(d.conductorSessions) {
				d.conductorCursor++
				return d, nil
			}
		}
		if cur == forkFocusOptions {
			return d, d.optionsPanel.Update(msg)
		}
		if d.focusIndex < len(d.focusTargets)-1 {
			d.focusIndex++
			d.updateFocus()
		}
		return d, nil

	case "shift+tab", "up":
		if msg.String() == "up" && cur == forkFocusConductor {
			if d.conductorCursor > 0 {
				d.conductorCursor--
				return d, nil
			}
		}
		if cur == forkFocusOptions && !d.optionsPanel.AtTop() {
			return d, d.optionsPanel.Update(msg)
		}
		if d.focusIndex > 0 {
			d.focusIndex--
			d.updateFocus()
		}
		return d, nil
```

Update the control handlers so text fields keep receiving normal letters:

```go
	case "w":
		if cur == forkFocusWorktree {
			d.ToggleWorktree()
			if d.worktreeEnabled {
				d.focusIndex = d.indexOf(forkFocusBranch)
			}
			d.updateFocus()
			return d, nil
		}

	case "ctrl+f":
		if cur == forkFocusBranch && d.worktreeEnabled {
			// Existing branch picker code stays here.
		}

	case "y":
		if cur == forkFocusWithState {
			d.ToggleWithState()
			d.updateFocus()
			return d, nil
		}

	case "i":
		if cur == forkFocusGitignored {
			d.ToggleWithStateAndGitignored()
			return d, nil
		}

	case " ", "left", "right":
		switch cur {
		case forkFocusWorktree:
			d.ToggleWorktree()
			if d.worktreeEnabled {
				d.focusIndex = d.indexOf(forkFocusBranch)
			}
			d.updateFocus()
			return d, nil
		case forkFocusWithState:
			d.ToggleWithState()
			d.updateFocus()
			return d, nil
		case forkFocusGitignored:
			d.ToggleWithStateAndGitignored()
			return d, nil
		case forkFocusOptions:
			return d, d.optionsPanel.Update(msg)
		}
```

Finally, update the focused input dispatch:

```go
	switch d.currentTarget() {
	case forkFocusName:
		d.nameInput, cmd = d.nameInput.Update(msg)
	case forkFocusGroup:
		d.groupInput, cmd = d.groupInput.Update(msg)
	case forkFocusBranch:
		oldBranch := d.branchInput.Value()
		d.branchInput, cmd = d.branchInput.Update(msg)
		if d.branchInput.Value() != oldBranch && d.branchPicker != nil && d.branchPicker.IsVisible() {
			d.branchPicker.SetQuery(d.branchInput.Value())
		}
	case forkFocusOptions:
		cmd = d.optionsPanel.Update(msg)
	}
```

- [ ] **Step 5: Update focus application and active rendering**

Replace `updateFocus()` with target-based focus:

```go
func (d *ForkDialog) updateFocus() {
	d.nameInput.Blur()
	d.groupInput.Blur()
	d.branchInput.Blur()
	d.optionsPanel.Blur()

	switch d.currentTarget() {
	case forkFocusName:
		d.nameInput.Focus()
	case forkFocusGroup:
		d.groupInput.Focus()
	case forkFocusBranch:
		d.branchInput.Focus()
	case forkFocusOptions:
		d.optionsPanel.Focus()
	}
}
```

In `View`, drive active checkbox styling from `currentTarget()`:

```go
	cur := d.currentTarget()
...
	if cur == forkFocusWorktree {
		worktreeSection += checkboxActiveStyle.Render(fmt.Sprintf("  %s Create in worktree (press w)", checkbox))
	} else {
		worktreeSection += checkboxStyle.Render(fmt.Sprintf("  %s Create in worktree", checkbox))
	}
...
	if cur == forkFocusBranch {
		worktreeSection += activeLabelStyle.Render("▶ Branch:")
	} else {
		worktreeSection += labelStyle.Render("  Branch:")
	}
...
	withStateStyle := checkboxStyle
	withStateLabel := "Carry parent state"
	if cur == forkFocusWithState {
		withStateStyle = checkboxActiveStyle
		withStateLabel += " (press y)"
	}
	worktreeSection += "\n  " + withStateStyle.Render(fmt.Sprintf("%s %s", withStateCb, withStateLabel)) + "\n"
```

Use the same pattern for `forkFocusGitignored`, appending `(press i)` only when that target is focused.

- [ ] **Step 6: Verify and commit**

Run: `GOTOOLCHAIN=go1.24.0 go build ./internal/ui/...`
Expected: exits 0.

```bash
git add internal/ui/forkdialog.go
git commit -m "refactor(tui): make ForkDialog focus order target-based"
```

---

## Task 16: TUI test — checkbox visibility, toggling, and focus order

**Files:**
- Modify: `internal/ui/forkdialog_test.go`

- [ ] **Step 1: Add failing tests**

Add `reflect` to the imports in `internal/ui/forkdialog_test.go`, then append:

```go
func TestForkDialog_WithStateCheckbox_DefaultsOff(t *testing.T) {
	d := NewForkDialog()
	d.Show("parent-session", t.TempDir(), "", nil, "")
	if d.IsWithStateEnabled() {
		t.Fatal("with-state should default to off")
	}
	if d.IsWithStateAndGitignoredEnabled() {
		t.Fatal("with-state-gitignored should default to off")
	}
}

func TestForkDialog_ToggleWithState(t *testing.T) {
	d := NewForkDialog()
	d.Show("parent-session", t.TempDir(), "", nil, "")
	d.ToggleWorktree()
	d.ToggleWithState()
	if !d.IsWithStateEnabled() {
		t.Fatal("ToggleWithState did not enable")
	}
	d.ToggleWithState()
	if d.IsWithStateEnabled() {
		t.Fatal("ToggleWithState did not disable on second call")
	}
}

func TestForkDialog_ToggleWithStateRequiresWorktree(t *testing.T) {
	d := NewForkDialog()
	d.Show("parent-session", t.TempDir(), "", nil, "")
	d.ToggleWithState()
	if d.IsWithStateEnabled() {
		t.Fatal("ToggleWithState should be a no-op while worktree is off")
	}
}

func TestForkDialog_GitignoredRequiresWithState(t *testing.T) {
	d := NewForkDialog()
	d.Show("parent-session", t.TempDir(), "", nil, "")
	// Without with-state enabled, gitignored toggle is a no-op.
	d.ToggleWithStateAndGitignored()
	if d.IsWithStateAndGitignoredEnabled() {
		t.Fatal("gitignored toggled without with-state on")
	}
	// With with-state on, it toggles.
	d.ToggleWorktree()
	d.ToggleWithState()
	d.ToggleWithStateAndGitignored()
	if !d.IsWithStateAndGitignoredEnabled() {
		t.Fatal("gitignored did not toggle with with-state on")
	}
}

func TestForkDialog_TogglingWithStateOffClearsGitignored(t *testing.T) {
	d := NewForkDialog()
	d.Show("parent-session", t.TempDir(), "", nil, "")
	d.ToggleWorktree()
	d.ToggleWithState()
	d.ToggleWithStateAndGitignored()
	if !d.IsWithStateAndGitignoredEnabled() {
		t.Fatal("setup: gitignored should be on")
	}
	d.ToggleWithState() // turn off
	if d.IsWithStateAndGitignoredEnabled() {
		t.Fatal("turning off with-state should also clear gitignored")
	}
}

func TestForkDialog_TogglingWorktreeOffClearsWithState(t *testing.T) {
	d := NewForkDialog()
	d.Show("parent-session", t.TempDir(), "", nil, "")
	d.ToggleWorktree()
	d.ToggleWithState()
	d.ToggleWithStateAndGitignored()

	d.ToggleWorktree() // turn worktree off
	if d.IsWithStateEnabled() {
		t.Fatal("turning off worktree should clear with-state")
	}
	if d.IsWithStateAndGitignoredEnabled() {
		t.Fatal("turning off worktree should clear gitignored")
	}
}

func TestForkDialog_WithStateSuggestsEditableDestinationBranch(t *testing.T) {
	d := NewForkDialog()
	d.Show("Parent Session", t.TempDir(), "", nil, "")
	_, _, branch, _ := d.GetValuesWithWorktree()
	if branch != "fork/parent-session" {
		t.Fatalf("suggested destination branch = %q, want fork/parent-session", branch)
	}
}

func TestForkDialog_FocusOrder(t *testing.T) {
	conductors := []*session.Instance{{
		ID:          "conductor-1",
		Title:       "conductor-main",
		ProjectPath: "/tmp/main",
	}}

	d := NewForkDialog()
	d.Show("parent-session", t.TempDir(), "", conductors, "")

	// Avoid a filesystem git fixture here; this is a structural focus-target
	// test, and Show already covers git repository detection separately.
	d.isGitRepo = true
	d.worktreeEnabled = false
	d.rebuildFocusTargets()
	assertForkFocusTargets(t, d, []forkFocusTarget{
		forkFocusName,
		forkFocusGroup,
		forkFocusWorktree,
		forkFocusConductor,
		forkFocusOptions,
	})

	d.ToggleWorktree()
	assertForkFocusTargets(t, d, []forkFocusTarget{
		forkFocusName,
		forkFocusGroup,
		forkFocusWorktree,
		forkFocusBranch,
		forkFocusWithState,
		forkFocusConductor,
		forkFocusOptions,
	})

	d.ToggleWithState()
	assertForkFocusTargets(t, d, []forkFocusTarget{
		forkFocusName,
		forkFocusGroup,
		forkFocusWorktree,
		forkFocusBranch,
		forkFocusWithState,
		forkFocusGitignored,
		forkFocusConductor,
		forkFocusOptions,
	})

	d.isGitRepo = false
	d.rebuildFocusTargets()
	assertForkFocusTargets(t, d, []forkFocusTarget{
		forkFocusName,
		forkFocusGroup,
		forkFocusConductor,
		forkFocusOptions,
	})
}

func assertForkFocusTargets(t *testing.T, d *ForkDialog, want []forkFocusTarget) {
	t.Helper()
	if !reflect.DeepEqual(d.focusTargets, want) {
		t.Fatalf("focusTargets = %#v, want %#v", d.focusTargets, want)
	}
}
```

- [ ] **Step 2: Run tests**

Run: `GOTOOLCHAIN=go1.24.0 go test ./internal/ui/ -run "TestForkDialog_(WithState|ToggleWithState|GitignoredRequires|Toggling|FocusOrder)" -race -count=1 -v`
Expected: PASS (8 tests; method names match Task 15 and Task 15A helpers).

- [ ] **Step 3: Commit**

```bash
git add internal/ui/forkdialog_test.go
git commit -m "test(tui): cover with-state checkbox visibility + toggling"
```

---

## Task 17: TUI — submit handler propagates to ClaudeOptions

**Files:**
- Modify: `internal/ui/home.go` (the place where the fork dialog's submit creates `opts`)
- Create: `internal/ui/home_fork_state_test.go` (structural guard for the submit path)

The TUI submit path is in `internal/ui/home.go`. We need to copy the dialog's new bool getters into the `ClaudeOptions` it builds.

The fork dialog already suggests an editable destination branch (`fork/<sanitized-session>`) in the visible branch input. That suggestion is TUI-only; the CLI must not auto-name. Regardless of whether the user accepts or edits the suggestion, fork-with-state treats the submitted branch as a new destination branch from the parent session's HEAD.

- [ ] **Step 1: Locate the fork submit path**

Run: `grep -n "ForkDialog\|forkDialog\|GetOptions\|WorktreePath = " internal/ui/home.go | head -30`

Find the block where, after the fork dialog returns Enter, the code constructs a `*session.ClaudeOptions` for the new instance. Look for the surrounding context that sets `opts.WorktreePath`, `opts.WorktreeBranch`, and similar — the same place needs `opts.WithState` and `opts.IncludeGitignored`.

- [ ] **Step 2: Add the two assignments where the fork-submit handler builds `opts`**

Run: `grep -n "forkDialog\|ForkDialog\|opts.WorktreePath\s*=" internal/ui/home.go | head -20`

Find the spot where the fork-submit path constructs `opts *session.ClaudeOptions` and sets `opts.WorktreePath = ...` / `opts.WorktreeBranch = ...`. Immediately after those assignments, append:

```go
opts.WithState = m.forkDialog.IsWithStateEnabled()
opts.IncludeGitignored = m.forkDialog.IsWithStateAndGitignoredEnabled()
```

If the surrounding code uses a different receiver name for the dialog (e.g., `m.fork` or just `forkDialog`), use that — grep output from Step 2 tells you which.

- [ ] **Step 3: In the same submit handler, use the shared preflight helper and sequence Create→Materialize→Setup**

In `internal/ui/home.go`, add `errors` to the imports. Before the existing-worktree reuse block, gate fork-with-state with the shared destination validator:

```go
						if opts.WithState {
							if err := git.ValidateForkWithStateDestination(opts.WorktreeRepoRoot, opts.WorktreeBranch); err != nil {
								var collErr *git.DestinationCollisionError
								if errors.As(err, &collErr) {
									switch collErr.Kind {
									case "worktree_exists":
										return sessionForkedMsg{err: fmt.Errorf("branch %q already has a worktree at %s; choose a new destination branch for --with-state", collErr.Branch, collErr.Path), sourceID: sourceID}
									case "branch_exists":
										return sessionForkedMsg{err: fmt.Errorf("branch %q already exists; choose a new destination branch for --with-state", collErr.Branch), sourceID: sourceID}
									default:
										return sessionForkedMsg{err: collErr, sourceID: sourceID}
									}
								}
								return sessionForkedMsg{err: fmt.Errorf("failed to validate destination: %w", err), sourceID: sourceID}
							}
						}
```

Leave the existing-worktree reuse block (today's `home.go:8499-8514`) **unchanged**. With-state cannot reach it because `ValidateForkWithStateDestination` already refused above; for non-with-state, today's reuse behavior is preserved exactly.

Then add this local presentation helper near `forkSessionCmdWithOptions`:

```go
func forkWithStateOperationHint(kind, repoPath string) string {
	switch kind {
	case "rebase":
		return fmt.Sprintf("finish or abort the rebase before forking with state (cd %s && git rebase --abort)", repoPath)
	case "merge":
		return "resolve or abort the merge before forking with state"
	case "cherry-pick":
		return "finish or abort the cherry-pick before forking with state"
	case "bisect":
		return fmt.Sprintf("run 'git bisect reset' in %s before forking with state", repoPath)
	default:
		return "finish or abort the in-progress git operation before forking with state"
	}
}
```

Then replace lines 8506-8513 (the `var setupBuf bytes.Buffer ... if setupErr != nil { ... }` block). The surrounding code uses `source` for the parent `*session.Instance`, `opts` for the new `*session.ClaudeOptions`, and returns `sessionForkedMsg{err: ..., sourceID: sourceID}` on failure.

```go
				var setupBuf bytes.Buffer
				var createErr error
				createdBranch := false
				if opts.WithState {
					preflight, preflightErr := git.PreflightForkWithState(source.ProjectPath)
					if preflightErr != nil {
						var opErr *git.InProgressOperationError
						if errors.As(preflightErr, &opErr) {
							return sessionForkedMsg{err: fmt.Errorf("parent session is mid-%s; %s", opErr.Kind, forkWithStateOperationHint(opErr.Kind, source.ProjectPath)), sourceID: sourceID}
						}
						return sessionForkedMsg{err: fmt.Errorf("failed to inspect parent's git state: %w", preflightErr), sourceID: sourceID}
					}
					if preflight.HasSubmodules {
						uiLog.Warn("fork_with_state_submodules_detected", slog.String("source_path", source.ProjectPath), slog.String("message", "submodules detected; copied as files, not recursed"))
					}
					createdBranch, createErr = git.CreateWorktreeAtStartPoint(opts.WorktreeRepoRoot, opts.WorktreePath, opts.WorktreeBranch, preflight.ParentHead)
				} else {
					createErr = git.CreateWorktree(opts.WorktreeRepoRoot, opts.WorktreePath, opts.WorktreeBranch)
				}
				if createErr != nil {
					return sessionForkedMsg{err: fmt.Errorf("worktree creation failed: %w", createErr), sourceID: sourceID}
				}
				if opts.WithState {
					if _, mErr := git.MaterializeParentState(
						source.ProjectPath,
						opts.WorktreePath,
						git.StateCopyOptions{IncludeGitignored: opts.IncludeGitignored},
					); mErr != nil {
						_ = exec.Command("git", "-C", opts.WorktreeRepoRoot, "worktree", "remove", "--force", opts.WorktreePath).Run()
						if createdBranch {
							_ = exec.Command("git", "-C", opts.WorktreeRepoRoot, "branch", "-D", opts.WorktreeBranch).Run()
						}
						return sessionForkedMsg{err: fmt.Errorf("failed to materialize parent state: %w; new worktree cleaned up", mErr), sourceID: sourceID}
					}
				}
				setupErr := git.RunWorktreeSetup(opts.WorktreeRepoRoot, opts.WorktreePath, &setupBuf, &setupBuf, session.GetWorktreeSettings().SetupTimeout())
				if setupErr != nil {
					uiLog.Warn("worktree_setup_script_failed", slog.String("error", setupErr.Error()), slog.String("output", setupBuf.String()))
				}
```

Also ensure `"os/exec"` is in the imports at the top of `internal/ui/home.go`. The other imports (`fmt`, `bytes`, `slog`, etc.) are already present.

- [ ] **Step 4: Add a structural guard for the TUI submit path**

Create `internal/ui/home_fork_state_test.go`:

```go
package ui

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func extractHomeFuncBody(src, fnName string) string {
	re := regexp.MustCompile(`(?m)^func\s+\(h \*Home\)\s+` + regexp.QuoteMeta(fnName) + `\s*\(`)
	loc := re.FindStringIndex(src)
	if loc == nil {
		return ""
	}
	i := loc[1]
	for i < len(src) && src[i] != '{' {
		i++
	}
	if i == len(src) {
		return ""
	}
	start := i + 1
	depth := 1
	for j := start; j < len(src); j++ {
		switch src[j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[start:j]
			}
		}
	}
	return ""
}

func TestForkSessionCmdWithOptions_WithStateUsesSharedPreflight(t *testing.T) {
	data, err := os.ReadFile("home.go")
	if err != nil {
		t.Fatalf("read home.go: %v", err)
	}
	body := extractHomeFuncBody(string(data), "forkSessionCmdWithOptions")
	if body == "" {
		t.Fatal("forkSessionCmdWithOptions body not found")
	}

	preflight := strings.Index(body, "git.PreflightForkWithState(source.ProjectPath)")
	create := strings.Index(body, "git.CreateWorktreeAtStartPoint(")
	if preflight == -1 {
		t.Fatal("TUI fork-with-state path must call git.PreflightForkWithState(source.ProjectPath)")
	}
	if create == -1 {
		t.Fatal("TUI fork-with-state path must call git.CreateWorktreeAtStartPoint")
	}
	if preflight > create {
		t.Fatal("TUI fork-with-state preflight must run before CreateWorktreeAtStartPoint")
	}
	if strings.Contains(body, "git.DetectInProgressOperation(source.ProjectPath)") ||
		strings.Contains(body, "git.HasSubmodules(source.ProjectPath)") {
		t.Fatal("TUI path should use shared PreflightForkWithState, not duplicate lower-level preflight calls")
	}
}
```

- [ ] **Step 5: Verify the package compiles**

Run: `GOTOOLCHAIN=go1.24.0 go build ./internal/ui/...`
Expected: exits 0.

- [ ] **Step 6: Run TUI tests to confirm no regression**

Run: `GOTOOLCHAIN=go1.24.0 go test ./internal/ui/... -run "ForkDialog_(WithState|ToggleWithState|GitignoredRequires|Toggling|FocusOrder)|ForkSessionCmdWithOptions_WithStateUsesSharedPreflight" -race -count=1`
Expected: PASS — no test regressions.

- [ ] **Step 7: Commit**

```bash
git add internal/ui/home.go internal/ui/home_fork_state_test.go
git commit -m "feat(tui): wire fork-with-state through submit handler with cleanup"
```

---

## Task 17A: Behavioral eval smoke coverage for fork-with-state

**Files:**
- Create: `tests/eval/session/fork_with_state_test.go`
- Create: `internal/ui/forkdialog_eval_test.go`

This task satisfies the existing evaluator-harness mandate for user-observable behavior. The unit, handler, and structural tests above are still required, but they do not prove the real binary and rendered TUI interaction behave the way a user sees them.

- [ ] **Step 1: Add a real-binary CLI eval**

Create `tests/eval/session/fork_with_state_test.go`:

```go
//go:build eval_smoke

package session_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	decksession "github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/tests/eval/harness"
)

func TestEval_SessionForkWithState_CreatesMaterializedWorktree(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH")
	}

	sb := harness.NewSandbox(t)
	sb.InstallTmuxShim(t)
	installEvalClaudeShim(t, sb)

	parent := initForkStateEvalRepo(t, sb.Home)
	seedForkableClaudeSession(t, sb, "parent", parent)

	cwdFile := filepath.Join(sb.Home, "claude-cwd.txt")
	out, err := runBinWithEnv(sb, []string{"AGENTDECK_EVAL_CLAUDE_CWD=" + cwdFile},
		"session", "fork", "parent",
		"--with-state-and-gitignored",
		"-w", "fork/eval-state",
		"-t", "forked",
	)
	if err != nil {
		t.Fatalf("session fork --with-state-and-gitignored failed: %v\n%s", err, out)
	}

	dest := worktreeForBranch(t, parent, "fork/eval-state")
	assertFile(t, filepath.Join(dest, "untracked.txt"), "untracked parent\n")
	assertFile(t, filepath.Join(dest, "ignored.txt"), "ignored parent\n")

	if got := runGitOut(t, dest, "diff", "--cached", "--name-only"); !strings.Contains(got, "staged.txt") {
		t.Fatalf("destination missing staged diff for staged.txt; git diff --cached --name-only:\n%s", got)
	}
	if got := runGitOut(t, dest, "diff", "--name-only"); !strings.Contains(got, "unstaged.txt") {
		t.Fatalf("destination missing unstaged diff for unstaged.txt; git diff --name-only:\n%s", got)
	}
	if got := waitForFile(t, cwdFile, 5*time.Second); strings.TrimSpace(got) != dest {
		t.Fatalf("forked claude started in cwd %q, want destination worktree %q", strings.TrimSpace(got), dest)
	}
}

func initForkStateEvalRepo(t *testing.T, root string) string {
	t.Helper()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-b", "main")
	runGit(t, repo, "config", "user.email", "eval@example.com")
	runGit(t, repo, "config", "user.name", "Eval")
	writeFile(t, filepath.Join(repo, ".gitignore"), "ignored.txt\n")
	writeFile(t, filepath.Join(repo, "staged.txt"), "base staged\n")
	writeFile(t, filepath.Join(repo, "unstaged.txt"), "base unstaged\n")
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-m", "base")

	writeFile(t, filepath.Join(repo, "staged.txt"), "staged parent\n")
	runGit(t, repo, "add", "staged.txt")
	writeFile(t, filepath.Join(repo, "unstaged.txt"), "unstaged parent\n")
	writeFile(t, filepath.Join(repo, "untracked.txt"), "untracked parent\n")
	writeFile(t, filepath.Join(repo, "ignored.txt"), "ignored parent\n")
	return repo
}

func seedForkableClaudeSession(t *testing.T, sb *harness.Sandbox, title, projectPath string) {
	t.Helper()
	t.Setenv("HOME", sb.Home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(sb.Home, ".config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(sb.Home, ".local", "state"))

	storage, err := decksession.NewStorageWithProfile("")
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })

	inst := decksession.NewInstanceWithGroupAndTool(title, projectPath, decksession.DefaultGroupPath, "claude")
	inst.Command = "claude"
	inst.ClaudeSessionID = "parent-eval-session"
	inst.Status = decksession.StatusStopped
	instances := []*decksession.Instance{inst}
	if err := storage.SaveWithGroups(instances, decksession.NewGroupTree(instances)); err != nil {
		t.Fatalf("seed storage: %v", err)
	}
}

func installEvalClaudeShim(t *testing.T, sb *harness.Sandbox) {
	t.Helper()
	shim := filepath.Join(sb.ShimDir, "claude")
	script := `#!/usr/bin/env bash
printf '%s\n' "$PWD" > "$AGENTDECK_EVAL_CLAUDE_CWD"
sleep 30
`
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatalf("write claude shim: %v", err)
	}
}

func runBinWithEnv(sb *harness.Sandbox, extraEnv []string, args ...string) (string, error) {
	cmd := exec.Command(sb.BinPath, args...)
	cmd.Env = append(sb.Env(), extraEnv...)
	cmd.Dir = sb.Home
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s failed: %v\n%s", args, dir, err, out)
	}
}

func runGitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s failed: %v\n%s", args, dir, err, out)
	}
	return string(out)
}

func worktreeForBranch(t *testing.T, repo, branch string) string {
	t.Helper()
	out := runGitOut(t, repo, "worktree", "list", "--porcelain")
	var current string
	for _, line := range strings.Split(out, "\n") {
		if path, ok := strings.CutPrefix(line, "worktree "); ok {
			current = path
		}
		if b, ok := strings.CutPrefix(line, "branch refs/heads/"); ok && b == branch {
			return current
		}
	}
	t.Fatalf("branch %q not found in git worktree list:\n%s", branch, out)
	return ""
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}

func waitForFile(t *testing.T, path string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			return string(data)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
	return ""
}
```

- [ ] **Step 2: Add a colocated TUI eval**

Create `internal/ui/forkdialog_eval_test.go`:

```go
//go:build eval_smoke

package ui

import (
	"os/exec"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestEval_ForkDialog_WithStateVisibleInteraction(t *testing.T) {
	repo := t.TempDir()
	runForkDialogEvalGit(t, repo, "init", "-b", "main")

	d := NewForkDialog()
	d.SetSize(80, 24)
	d.Show("parent session", repo, "", nil, "")

	if got := stripForkDialogEvalANSI(d.View()); strings.Contains(got, "Carry parent state") {
		t.Fatalf("with-state control visible before worktree toggle:\n%s", got)
	}

	sendForkDialogEvalKey(t, d, 'w')
	if !d.IsWorktreeEnabled() {
		t.Fatal("pressing w should enable worktree mode")
	}
	if got := stripForkDialogEvalANSI(d.View()); !strings.Contains(got, "Carry parent state") {
		t.Fatalf("with-state control missing after worktree toggle:\n%s", got)
	}

	sendForkDialogEvalKey(t, d, 'y')
	if !d.IsWithStateEnabled() {
		t.Fatal("pressing y should enable carry-parent-state")
	}
	if got := stripForkDialogEvalANSI(d.View()); !strings.Contains(got, "Include gitignored files") {
		t.Fatalf("gitignored control missing after with-state toggle:\n%s", got)
	}

	sendForkDialogEvalKey(t, d, 'i')
	if !d.IsWithStateAndGitignoredEnabled() {
		t.Fatal("pressing i should enable include-gitignored")
	}
}

func sendForkDialogEvalKey(t *testing.T, d *ForkDialog, r rune) {
	t.Helper()
	_, _ = d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
}

func runForkDialogEvalGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

func stripForkDialogEvalANSI(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) {
			switch s[i+1] {
			case '[':
				j := i + 2
				for j < len(s) && !((s[j] >= 'A' && s[j] <= 'Z') || (s[j] >= 'a' && s[j] <= 'z')) {
					j++
				}
				i = j
				continue
			case ']':
				j := i + 2
				for j < len(s) && s[j] != 0x07 && !(s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\') {
					j++
				}
				if j < len(s) && s[j] == 0x1b {
					j++
				}
				i = j
				continue
			}
		}
		out.WriteByte(s[i])
	}
	return out.String()
}
```

- [ ] **Step 3: Run the targeted eval smoke tests**

Run:

```bash
GOTOOLCHAIN=go1.24.0 go test -tags eval_smoke ./tests/eval/session/... ./internal/ui/... -run "TestEval_SessionForkWithState|TestEval_ForkDialog_WithState" -race -count=1 -v
```

Expected: PASS. The CLI eval may take a few seconds because it builds the real binary and starts an isolated tmux-backed fork with a fake `claude` shim.

- [ ] **Step 4: Commit**

```bash
git add tests/eval/session/fork_with_state_test.go internal/ui/forkdialog_eval_test.go
git commit -m "test(eval): cover fork-with-state user-visible flows"
```

---

## Task 18: Verify the mandate lives in the tracked spec (no CLAUDE.md edit)

**Files:**
- Verify: `docs/superpowers/specs/2026-05-14-fork-worktree-with-state-design.md` (`## Mandatory test coverage` section)

Per CONTRIBUTING.md (and PR #1002), `CLAUDE.md` is intentionally untracked and per-developer. The mandate is housed in the tracked spec instead — `## Mandatory test coverage` section. This task is a verification step that the section is current and the regexes haven't drifted from the actual test names.

- [ ] **Step 1: Verify the spec section exists and is current**

Run:

```bash
grep -A2 "^## Mandatory test coverage" docs/superpowers/specs/2026-05-14-fork-worktree-with-state-design.md | head
```

Expected: shows the heading and the first lines of the section.

- [ ] **Step 2: Verify the regexes match the actual test names**

Run (after Tasks 2-17 have landed their tests):

```bash
go test ./internal/git/... -run "Materialize|DetectInProgress|HasSubmodules|PreflightForkWithState|ValidateForkWithStateDestination|CreateWorktreeAtStartPoint|HeadCommit|ForkWithStateGitSequence" -race -count=1
go test ./cmd/agent-deck/... -run "SessionFork_WithState|ResolveForkStateFlags" -race -count=1
go test ./internal/ui/... -run "ForkDialog_(WithState|ToggleWithState|GitignoredRequires|Toggling|FocusOrder)|ForkSessionCmdWithOptions_WithStateUsesSharedPreflight" -race -count=1
go test -tags eval_smoke ./tests/eval/session/... ./internal/ui/... -run "TestEval_SessionForkWithState|TestEval_ForkDialog_WithState" -race -count=1
```

Expected: every command matches at least one test (no "warning: no tests to run" output). If a regex matches zero tests, the spec's regex is stale — fix the spec, not the test names.

- [ ] **Step 3: No commit unless Step 2 surfaced a regex fix**

If Step 2 was clean, no action. If the spec needed a regex update to match actual test names, commit just the spec change:

```bash
git add docs/superpowers/specs/2026-05-14-fork-worktree-with-state-design.md
git commit -m "docs(spec): align mandate regex with actual test names"
```

### Why this changed from "edit CLAUDE.md"

The original plan added a section to `CLAUDE.md`. After CONTRIBUTING.md was re-read during the post-plan review (Item 4 of the review pass), the team confirmed that PR #1002 intentionally made `CLAUDE.md` per-developer / untracked. The mandate therefore must live in a tracked file. The tracked spec already houses the full test inventory in its `## Testing` section; promoting the mandate text alongside it (in `## Mandatory test coverage`) keeps the mandate enforceable by CI without depending on a per-developer file. See spec review log entry FWS-018.

---

## Task 19: README + CHANGELOG entries

**Files:**
- Modify: `README.md`
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Expand the README Fork Sessions section**

Run: `grep -n "### Fork Sessions\|### Git Worktrees" README.md`

In `README.md`, find `### Fork Sessions` near the top-level feature list. Replace that short section with:

````markdown
### Fork Sessions

Try different approaches without losing context. Fork any Claude conversation instantly. Each fork inherits the full conversation history.

- Press `f` for quick fork, `F` to customize name/group
- Fork your forks to explore as many branches as you need
- Use `--with-state` when you want the fork to start in a new worktree that includes the parent's current working-tree edits

```bash
agent-deck session fork my-project
agent-deck session fork my-project -t "my-project-fork"

# Carry parent's tracked + staged + untracked edits into a new destination branch/worktree.
agent-deck session fork my-project --with-state -w fork/my-project-wip

# Also copy gitignored files (e.g., .env, build caches).
agent-deck session fork my-project --with-state-and-gitignored -w fork/my-project-with-env
```

On the CLI, `--with-state` requires `-w/--worktree <new-branch>` and does not auto-name the destination branch. `--with-state-and-gitignored` implies `--with-state`. Parent's index, working tree, and stash list are left byte-identical after the fork. The fork captures parent state at the moment of fork; later parent edits are not reflected. Mid-rebase/merge/cherry-pick/bisect parents are refused with an actionable error.
````

- [ ] **Step 2: Cross-reference fork-with-state from Git Worktrees**

In `README.md`, find `### Git Worktrees`. After the existing initial worktree bullets and before `Configure the default worktree location...`, insert:

````markdown
Forking can also create a new worktree from a parent Claude session:

```bash
agent-deck session fork my-project --with-state -w fork/my-project-wip
```

Use this when the parent has useful in-progress edits that should move into a parallel branch without mutating the parent checkout.
````

- [ ] **Step 3: Add a CHANGELOG entry**

Add the following `### Added` entry under `## [Unreleased]` in `CHANGELOG.md`. If `## [Unreleased]` has no subsections yet, create `### Added` directly beneath it. Use the repo's Keep a Changelog prose style:

```markdown
### Added
- **Forked Claude sessions can now carry the parent's working-tree state into a new worktree.** `agent-deck session fork --with-state -w <new-branch>` and `--with-state-and-gitignored -w <new-branch>` materialize the parent session's staged, unstaged, untracked, and optionally gitignored files into a freshly-created destination branch/worktree branched off the parent's HEAD. The parent checkout stays read-only; existing destination branches/worktrees and mid-rebase/merge/cherry-pick/bisect parents are refused. The TUI fork dialog gains matching sub-checkboxes (`y` for with-state, `i` for gitignored) plus an editable suggested destination branch.

### Changed
- **Internal worktree setup now has an explicit materialization point.** `internal/git/CreateWorktreeWithSetup` is split into `CreateWorktree` + `RunWorktreeSetup`, with the original function preserved as a wrapper so existing direct callers remain unchanged.
```

- [ ] **Step 4: Commit**

```bash
git add README.md CHANGELOG.md
git commit -m "docs: add --with-state[-and-gitignored] fork examples to README and CHANGELOG"
```

---

## Pre-execution verification scenarios

These probes are run before implementation to validate tricky git semantics in isolated scratch repositories. Create all scratch repositories under `/private/tmp` or `/tmp`; do not use repo-local temporary directories.

### PEV-001: staged materialization uses `git apply --index`

Purpose: prove the implementation sequence preserves partially-staged files and staged deletions before writing production code.

Manual probe:

1. Create a temp repo with one committed multi-line tracked file.
2. Modify one line and stage it, then modify another line in the same file without staging.
3. Create a new worktree from the same commit.
4. Capture `git diff --binary --cached` and `git diff --binary` from the parent.
5. Apply the staged patch to the new worktree with `git apply --index`, then apply the unstaged patch with plain `git apply`.
6. Verify `git diff --cached` and `git diff` match between parent and new worktree.
7. Repeat with a committed file removed via `git rm`; verify the new worktree reports `D  <file>` and the file is absent from disk.

Failure mode this prevents: `git apply --cached` updates only the index, leaving partially-staged working-tree content at the base version and leaving staged deletions on disk as untracked files.

### PEV-002: linked parent worktree forks from parent HEAD

Purpose: prove the fork-with-state start point is the parent session's HEAD, not the main/base worktree's HEAD.

Manual probe:

1. Create a temp repo under `/private/tmp` or `/tmp` with an initial commit on `main`.
2. Create a linked worktree on `parent-branch` and add one commit there.
3. Verify `git -C <base> rev-parse HEAD` differs from `git -C <parent-wt> rev-parse HEAD`.
4. Create a new fork worktree using the proposed helper shape: `git -C <base> worktree add -b fork/from-parent <fork-wt> <parent-head>`.
5. Verify `git -C <fork-wt> rev-parse HEAD` equals the parent worktree HEAD and does not equal the base worktree HEAD.

Failure mode this prevents: creating the fork with `git -C <base> worktree add -b <branch> <path>` silently starts the fork at the base worktree HEAD when the parent session already lives in a linked worktree.

### PEV-003: source shape matrix always creates a new destination

Purpose: prove source branch/worktree shape is independent from the destination contract. Fork-with-state may read from several source shapes, but it always creates a new destination branch and worktree with the requested destination name.

Manual probe:

1. Create a temp repo under `/private/tmp` or `/tmp` with an initial commit on `main`.
2. Source shape A: create a linked source worktree on `source/a`, add a commit, dirty the source worktree, then create `fork/a-new` with `git worktree add -b fork/a-new <dest-a> <source-a-head>` and materialize source state. Verify `fork/a-new` is a separate branch/worktree and source worktree state remains unchanged.
3. Source shape B: use the main worktree on an existing branch with no separate source worktree, dirty it, then create `fork/b-new` from that source `HEAD`. Verify the destination branch/worktree is new.
4. Source shape C: create a linked source worktree in detached HEAD state, dirty it, then create `fork/c-new` from that detached `HEAD`. Verify the destination branch/worktree is new.
5. Destination collision: pre-create branch/worktree `fork/existing`, then attempt fork-with-state to `fork/existing`. Verify the planned handler refuses before materialization instead of reusing the existing worktree.

Failure mode this prevents: treating the source branch/worktree as the destination, silently reusing an existing destination worktree, or making CLI hidden auto-names that differ from the user's requested destination.

### PEV-004: CLI contract tests exercise the actual command path

Purpose: prove the plan's CLI claims are covered through the real CLI/handler path, not only through direct git helper calls.

Manual probe:

1. Build the `agent-deck` binary in a temp output path under `/private/tmp` or `/tmp`.
2. Create an isolated temp `HOME` and seed a forkable Claude session in that profile with a temp git repo as its project path.
3. Run `agent-deck session fork <session> --with-state`; verify it exits non-zero and prints `--with-state requires --worktree <new-branch>`.
4. Run `agent-deck session fork <session> --with-state-and-gitignored`; verify the same explicit-destination refusal.
5. Pre-create destination branch `fork/existing`, then run `agent-deck session fork <session> --with-state -w fork/existing`; verify it refuses the existing branch before materialization.
6. Pre-create destination branch/worktree `fork/used`, then run `agent-deck session fork <session> --with-state -w fork/used`; verify it refuses existing worktree reuse.
7. Run the handler-level before-start hook test for `--with-state-and-gitignored -w fork/with-env`; verify `ClaudeOptions.WithState` and `IncludeGitignored` are true before `Start()`.

Failure mode this prevents: green git helper tests masking broken CLI flag registration, missing refusal paths, wrong error text, or dropped `ClaudeOptions` propagation.

### PEV-005: setup hook discovery uses repo root and execution uses worktree

Purpose: prove setup-hook discovery and execution use different paths. The setup script is found under the repo root, but it runs with the new worktree as cwd after parent state is materialized.

Manual probe:

1. Create a temp repo under `/private/tmp` or `/tmp` with `.agent-deck/worktree-setup.sh` committed or present in the repo root.
2. Make the setup script write `pwd` and the contents of a parent-WIP file to a temp output file.
3. Dirty the parent by adding the parent-WIP file without committing it.
4. Create the destination worktree, materialize parent state, then run `RunWorktreeSetup(<repo-root>, <dest-worktree>, ...)`.
5. Verify the setup script was discovered from `<repo-root>/.agent-deck/worktree-setup.sh`, ran with `<dest-worktree>` as cwd, and observed the materialized parent-WIP file.

Failure mode this prevents: implementing the spec with a worktree-only setup API that looks for `.agent-deck/worktree-setup.sh` in the new worktree or fails to expose `AGENT_DECK_REPO_ROOT` correctly.

### PEV-006: shared preflight keeps CLI and TUI parity

Purpose: prove the safety checks are centralized before implementation and that both user surfaces call the same fork-with-state preflight contract.

Manual probe:

1. Create a temp repo under `/private/tmp` or `/tmp` with an initial commit.
2. Force a rebase conflict in that repo and verify `DetectInProgressOperation(<repo>)` returns `rebase`.
3. Verify the planned `PreflightForkWithState(<repo>)` shape returns `InProgressOperationError{Kind: "rebase"}` before any destination worktree is created.
4. Abort the rebase, add a `.gitmodules` file, and verify `PreflightForkWithState(<repo>)` returns the current `HEAD` plus `HasSubmodules=true`.
5. After implementation, inspect the CLI and TUI fork-with-state paths and verify both call `git.PreflightForkWithState(...)` before `CreateWorktreeAtStartPoint`.
6. Verify neither path duplicates `DetectInProgressOperation(...)` and `HasSubmodules(...)` inline.

Failure mode this prevents: CLI refusing mid-operation parents while the TUI creates a fork anyway, or one surface warning about submodules while the other silently proceeds.

---

## Task 20: Full repo verification

- [ ] **Step 1: Run formatter, linter, and full test suite**

Run:
```bash
GOTOOLCHAIN=go1.24.0 make fmt
GOTOOLCHAIN=go1.24.0 make lint
GOTOOLCHAIN=go1.24.0 make test
```
Expected: all three succeed.

- [ ] **Step 2: Run the new mandate suite**

Run:
```bash
GOTOOLCHAIN=go1.24.0 go test ./internal/git/... -run "Materialize|DetectInProgress|HasSubmodules|PreflightForkWithState|ValidateForkWithStateDestination|CreateWorktreeAtStartPoint|HeadCommit|ForkWithStateGitSequence" -race -count=1
GOTOOLCHAIN=go1.24.0 go test ./cmd/agent-deck/... -run "SessionFork_WithState|ResolveForkStateFlags" -race -count=1
GOTOOLCHAIN=go1.24.0 go test ./internal/ui/... -run "ForkDialog_(WithState|ToggleWithState|GitignoredRequires|Toggling|FocusOrder)|ForkSessionCmdWithOptions_WithStateUsesSharedPreflight" -race -count=1
GOTOOLCHAIN=go1.24.0 go test -tags eval_smoke ./tests/eval/session/... ./internal/ui/... -run "TestEval_SessionForkWithState|TestEval_ForkDialog_WithState" -race -count=1
```
Expected: all PASS.

- [ ] **Step 3: Re-run existing mandate suites to confirm no regression**

Run:
```bash
GOTOOLCHAIN=go1.24.0 go test -run TestPersistence_ ./internal/session/... -race -count=1
GOTOOLCHAIN=go1.24.0 go test ./internal/feedback/... ./internal/ui/... ./cmd/agent-deck/... -run "Feedback|Sender_" -race -count=1
GOTOOLCHAIN=go1.24.0 go test ./internal/watcher/... -race -count=1 -timeout 120s
```
Expected: all PASS — none of these touch fork code so they should be unaffected.

- [ ] **Step 4: Commit (no-op unless fixes were needed)**

If any of the above turned up fixes, commit them with a tight scope:

```bash
git add -A
git commit -m "fix: address <specific issue surfaced by verification>"
```

---

## Task 21: Open PR against upstream

- [ ] **Step 1: Push to your fork**

```bash
# One-time setup if not already done:
gh repo fork asheshgoplani/agent-deck --remote=true
git remote rename origin upstream    # if origin still points at asheshgoplani
git remote add origin git@github.com:smorin/agent-deck.git  # only if rename was needed

git push -u origin feature/fork-worktree-with-state
```

- [ ] **Step 2: Open the PR**

```bash
gh pr create --title "feat: fork --with-state[-and-gitignored] — carry parent working tree into new worktree" --body "$(cat <<'EOF'
## Summary
- Adds opt-in `--with-state` and `--with-state-and-gitignored` flags to `agent-deck session fork` (and matching TUI sub-checkboxes) that materialize the parent session's working-tree state into a freshly-created destination branch/worktree branched off parent's HEAD.
- Read-only on parent: parent's index, working tree, and stash list are byte-identical after the fork.
- Refuses mid-rebase / mid-merge / mid-cherry-pick / mid-bisect with an actionable error through a shared CLI/TUI preflight helper.
- Materialization runs *before* the setup hook so user setup scripts see parent's WIP.

## Design and plan
- Spec: `docs/superpowers/specs/2026-05-14-fork-worktree-with-state-design.md`
- Plan: `docs/superpowers/plans/2026-05-14-fork-worktree-with-state.md`

## Test plan
- [ ] `go test ./internal/git/... -run "Materialize|DetectInProgress|HasSubmodules|PreflightForkWithState|ValidateForkWithStateDestination|CreateWorktreeAtStartPoint|HeadCommit|ForkWithStateGitSequence" -race`
- [ ] `go test ./cmd/agent-deck/... -run "SessionFork_WithState|ResolveForkStateFlags" -race`
- [ ] `go test ./internal/ui/... -run "ForkDialog_(WithState|ToggleWithState|GitignoredRequires|Toggling|FocusOrder)|ForkSessionCmdWithOptions_WithStateUsesSharedPreflight" -race`
- [ ] `go test -tags eval_smoke ./tests/eval/session/... ./internal/ui/... -run "TestEval_SessionForkWithState|TestEval_ForkDialog_WithState" -race`
- [ ] Existing session-persistence, feedback, watcher mandate suites all pass
- [ ] Manual TUI walkthrough: open fork dialog on a dirty parent, toggle `w` → `y` → `i`, submit, verify new worktree contains parent's WIP including gitignored files
- [ ] Manual CLI walkthrough: `agent-deck session fork <dirty-session> --with-state-and-gitignored -w fork/test-fork -t test-fork`
EOF
)"
```

- [ ] **Step 3: Report PR URL**

The previous command prints a PR URL on success. Copy it into the conversation for review.

---

## Post-plan author review PR proposal draft

Before opening or finalizing the PR, send the author a short proposal that highlights the design decisions most likely to matter during review. This is a review note, not a substitute for tests.

Draft:

```markdown
## Proposal: fork-with-state destination, start point, materialization, and cleanup design

This PR adds opt-in `agent-deck session fork --with-state` and `--with-state-and-gitignored`.

### Design decision: source is flexible, destination is always new

Fork-with-state reads from the selected parent session regardless of whether that source is a normal branch checkout, a linked worktree, or detached HEAD. What it creates is always a new destination branch and a new destination worktree.

On CLI, the destination branch must be explicit:

```bash
agent-deck session fork <session> --with-state -w fork/my-session-wip
```

The CLI does not auto-suggest or auto-name hidden branches. In the TUI, the visible branch field may be prefilled with `fork/<sanitized-session>` because the user can inspect and edit it before submit.

If you prefer CLI parity with the TUI, the alternative is to allow CLI auto-naming when `--with-state` is supplied without `-w`. I did not choose that in this plan because hidden destination names are easier to miss in scripts and logs.

### Design decision: fork from the parent session's HEAD

For fork-with-state, the new worktree is born from the parent session's current `HEAD`, then the parent's staged, unstaged, untracked, and optionally gitignored state is materialized into it.

This is intentional for parent sessions that already live in linked worktrees. If the main/base worktree is at commit `M` and the parent session worktree is at commit `P`, fork-with-state starts from `P`, not `M`.

Recommended implementation in this plan:

- capture `parentHead` with `git -C <parent-worktree> rev-parse --verify HEAD^{commit}`
- create the new worktree with an explicit start point via `CreateWorktreeAtStartPoint(repoRoot, worktreePath, branch, parentHead)`
- keep existing `CreateWorktree(...)` behavior unchanged for normal worktree creation paths

Alternative if you prefer a broader API:

- replace the narrow helper with an options-style API, for example `CreateWorktreeWithOptions(CreateWorktreeOptions{RepoDir, WorktreePath, BranchName, StartPoint})`
- route both existing and fork-with-state callers through that API
- preserve the same behavioral contract: fork-with-state must still start from parent session `HEAD`

I chose the narrow helper in the plan to minimize blast radius. The options-style API is reasonable if you want this to become the general worktree creation surface now.

### Design decision: shared fork-with-state preflight

Both CLI and TUI call `git.PreflightForkWithState(parentWorktree)` before creating the destination worktree.

That helper owns git facts only:

- refuse in-progress rebase, merge, cherry-pick, or bisect with `InProgressOperationError`
- capture the parent session's `HEAD`
- return `HasSubmodules=true` as a warning fact

The CLI and TUI still own presentation. CLI prints the refusal or warning through the CLI output path; TUI returns `sessionForkedMsg` errors or logs the submodule warning through the TUI logging path.

The alternative is to duplicate `DetectInProgressOperation`, `HasSubmodules`, and `HeadCommit` calls in both surfaces, or to add a TUI-only helper. I did not choose that because it makes future CLI/TUI drift more likely. The shared helper follows the existing `internal/git` worktree-helper pattern while keeping UI wording out of the git package.

### Testing decision: CLI contract coverage is separate from git-side integration

The plan now separates two kinds of tests:

- CLI/handler contract tests run the compiled command or the actual `handleSessionFork` path with a before-start hook. These cover flag registration, refusal messages, destination validation, and `ClaudeOptions` propagation.
- Git-side integration tests live in `internal/git/worktree_with_state_integration_test.go` and call the git helpers directly. These remain useful for materialization semantics, but they do not count as proof that the CLI surface works.

This split keeps the heavy tmux start path out of unit tests while still preventing helper-only tests from masking a broken user-facing command.

### Testing decision: bounded behavioral eval coverage

The repo already requires evaluator-harness coverage for user-observable behavior that pure Go tests cannot structurally express. This PR therefore includes a small eval layer in addition to unit, handler, and git-helper integration tests:

- `tests/eval/session/fork_with_state_test.go` drives the real `agent-deck` binary with a scratch HOME, fake `claude`, isolated tmux socket, and real git repo. It proves the visible CLI command creates a materialized destination worktree and starts the forked session in that worktree.
- `internal/ui/forkdialog_eval_test.go` is colocated with `internal/ui` because of Go's `internal/` import rule. It drives the rendered `ForkDialog` through `w -> y -> i` and proves the nested controls are visible only through the user-facing interaction path.

The alternative is to rely only on handler and structural tests. I did not choose that because this feature changes a real CLI command and visible TUI controls, and the repo has had prior regressions where those exact classes of behavior passed ordinary Go tests but failed for users.

### Documentation decision: dedicated fork docs plus worktree cross-reference

The README change expands the existing `### Fork Sessions` feature section with the new `--with-state` examples and CLI contract. The `### Git Worktrees` section also gets a short cross-reference because the feature creates a worktree, but the full explanation stays in the fork section where users looking for fork behavior are most likely to read first.

The alternative is to put the main docs only under `### Git Worktrees`. I did not choose that for this plan because fork-with-state is invoked through `agent-deck session fork`, and README already introduces forking as a first-class feature near the top of the page. The changelog entry follows the existing Keep a Changelog prose style under `## [Unreleased]`, not conventional-commit wording.

### Design decision: branch cleanup requires creation proof

Materialization failure cleans up the half-created worktree. The branch is deleted only if the worktree creation helper returned proof that this operation created the branch.

This is defensive hardening for the new cleanup path. The current `CreateWorktreeAtStartPoint(... -b <branch> <startPoint>)` shape rejects an already-existing branch before materialization can run, but cleanup still should not key branch deletion off intent flags like `createNewBranch`. If this helper is refactored later, existing branches must remain protected.

### Design decision: setup hook discovery root and execution cwd are separate

The split setup API keeps both `repoDir` and `worktreePath`:

- `repoDir` is where `.agent-deck/worktree-setup.sh` is discovered and what the script receives as `AGENT_DECK_REPO_ROOT`
- `worktreePath` is the cwd where the script runs and what the script receives as `AGENT_DECK_WORKTREE_PATH`

This preserves current setup semantics while allowing fork-with-state to materialize parent WIP before the hook runs. The alternative is a worktree-only setup API, but that risks discovering setup scripts from the wrong location in linked-worktree and bare-layout projects.

### Design decision: bare repositories are metadata, not state sources

Fork-with-state materializes from a checked-out parent working tree. That source can be:

- a normal repo checkout on the default branch
- a normal repo checkout on any other branch
- a linked worktree
- a detached-HEAD checkout with a working tree

A bare repository directory, or a bare-layout project root that only contains `.bare/`, is not itself a valid source for `--with-state` because there are no checked-out files, unstaged edits, or untracked files to copy. The bare-layout integration test therefore uses a linked parent worktree as the source while using the bare project root for repository-level operations.

```

---

## Spec coverage check

Each spec requirement maps to at least one task:

| Spec requirement | Task(s) |
|---|---|
| CLI `--with-state` and `--with-state-and-gitignored` flags | 12, 12A, 13 |
| CLI explicit destination branch, no auto-name | 12, 12A, 13, 19 |
| CLI refusal messages and destination collision behavior | 12A, 13, PEV-004 |
| Implication chain | 12, 13 |
| TUI sub-checkboxes with `y`/`i` toggles | 15, 16 |
| TUI nested state invariant: with-state requires worktree and clears when worktree turns off | 15, 16 |
| TUI editable destination-branch suggestion | 15, 16, 17 |
| TUI focus order extension | 15A, 16 |
| `ClaudeOptions.WithState` / `IncludeGitignored` | 1, 12A, 13 |
| Parent session HEAD used as fork start point | 4A, 4B, 13, 14, 17, PEV-002 |
| Proof-based branch cleanup on materialization failure | 4A, 13, 17 |
| Fresh destination branch/worktree; no with-state reuse | 4A, 4C, 12A, 13, 17, PEV-003 |
| Shared fork-with-state destination collision validator | 4C, 13, 17 |
| `MaterializeParentState` covering staged + unstaged + untracked + gitignored | 5, 6, 7, 8, 9 |
| Parent-untouched invariant | 10 |
| Staged-deletion preserved | 10 |
| Binary/symlink/exec-bit | 7, 8 |
| `DetectInProgressOperation` | 2, 4B |
| Shared fork-with-state preflight helper | 4B, 13, 17, PEV-006 |
| `HasSubmodules` warning | 3, 4B, 13, 17, PEV-006 |
| Split `CreateWorktreeWithSetup` → `RunWorktreeSetup` | 4 |
| Materialize-before-setup-hook ordering | 13, 17 |
| Cleanup-on-error (worktree remove + branch delete) | 13, 14, 17 |
| Refuse mid-rebase/merge/cherry-pick/bisect | 4B, 13, 14, 17, PEV-006 |
| CLAUDE.md mandate section | 18 |
| Behavioral eval smoke coverage | 17A, 18, 20 |
| README + CHANGELOG | 19 |
| Pre-execution verification probes | PEV-001, PEV-002, PEV-003, PEV-004, PEV-005, PEV-006 |
| Post-plan author PR proposal draft | Post-plan author review PR proposal draft |
| Full verification | 20 |
| PR opened against upstream | 21 |

No spec requirements without a task; no orphan tasks.

## Review change log

- 2026-05-15: Accepted FWS-001 after clean-context verification. Changed `MaterializeParentState` staged patch application from `git apply --cached` to `git apply --index`, and added PEV-001 to pre-execution verification so partially-staged edits and staged deletions are probed in temp repositories before implementation.
- 2026-05-15: Accepted FWS-002 after clean-context verification. Added the parent-HEAD start-point contract to the plan with `HeadCommit`, `CreateWorktreeAtStartPoint`, linked-worktree regression coverage, TUI/CLI parent-head creation paths, PEV-002, and a post-plan author PR proposal draft that offers the narrower helper or a broader options-style API while preserving the same behavior.
- 2026-05-15: Accepted FWS-003 after clean-context verification as new-code hardening. The plan now has `CreateWorktreeAtStartPoint` return `createdBranch`, rejects pre-existing branches on the fork-with-state start-point path, and gates branch deletion during materialization cleanup on that proof. The author PR proposal draft now calls out the cleanup design decision.
- 2026-05-15: Accepted FWS-004 after clean-context verification. The plan now states that source sessions may be normal branch checkouts, linked worktrees, or detached HEAD, but fork-with-state always creates a new destination branch/worktree. CLI requires explicit `-w/--worktree <new-branch>` with no hidden suggestion; TUI keeps an editable suggested destination name. Added PEV-003 and author-review notes for the destination contract.
- 2026-05-15: Accepted FWS-005 after clean-context verification. Renamed Task 14 to git-side integration coverage, added Task 12A for real CLI/handler contract tests, added a before-start test hook for option propagation, added PEV-004, and documented the testing split in the author-review proposal.
- 2026-05-16: Accepted FWS-006 after clean-context verification. Aligned setup-hook wording with the existing `RunWorktreeSetup(repoDir, worktreePath, ...)` contract, added PEV-005 to verify script discovery from repo root and execution in the new worktree, and documented the design decision for author review.
- 2026-05-16: Accepted FWS-007 after clean-context verification. Task 14 now includes the spec-promised clean-parent, bare-repo-layout, and materialize-before-setup-hook automated git-side integration tests, strengthens the mid-rebase test with a destination-absence assertion, and raises the expected fork-with-state integration count from 5 to 8.
- 2026-05-16: Accepted FWS-008 after clean-context verification. Updated the CLAUDE.md mandate draft so it preserves the valid `--with-state-and-gitignored` to `--with-state` implication while explicitly forbidding CLI with-state flags from auto-creating or auto-naming worktrees.
- 2026-05-17: Accepted FWS-009 after clean-context verification and option comparison. Added shared `git.PreflightForkWithState` coverage in Task 4B, rewired the planned CLI and TUI paths through that helper, added PEV-006, extended the TUI structural guard, and documented the shared-preflight design decision for author review.
- 2026-05-17: Accepted FWS-010 after clean-context verification. Hardened the ForkDialog plan so `ToggleWithState` is a no-op unless worktree mode is enabled, `ToggleWorktree` clears nested state when turning worktree off, and Task 16 tests lock that invariant in.
- 2026-05-17: Accepted FWS-011 after clean-context verification and codebase-pattern comparison. Added Task 15A so ForkDialog adopts the existing `NewDialog` focus-target architecture for dynamic focus order, added `TestForkDialog_FocusOrder`, and updated the fork-with-state TUI mandate regexes to include that regression.
- 2026-05-17: Accepted FWS-012 after clean-context verification and terminology review. Renamed the bare-layout integration case to `TestForkWithStateGitSequence_BareRepoLayoutLinkedParentWorktree`, clarified that a bare repo/project root is metadata rather than a fork-with-state source, and added an author-review design note preserving support for normal checkouts, linked worktrees, and detached-HEAD working trees.
- 2026-05-17: Accepted FWS-013 after clean-context verification. Fixed the plan file map to point at the dedicated `cmd/agent-deck/session_cmd_fork_state_test.go` file used by the spec and detailed tasks for fork-with-state CLI/handler coverage.
- 2026-05-17: Accepted ADP-007 codebase-pattern audit recommendation. Moved Task 14's direct git-helper sequence tests into `internal/git/worktree_with_state_integration_test.go`, leaving `cmd/agent-deck/session_cmd_fork_state_test.go` focused on CLI/handler contracts and updating the mandate/test-plan regexes to include `ForkWithStateGitSequence`.
- 2026-05-17: Accepted ADP-008 codebase-pattern audit recommendation. Reworked the planned TUI structural guard to extract `forkSessionCmdWithOptions` with a brace-counting helper, matching the existing structural-test pattern instead of depending on a neighboring `deleteSession` boundary.
- 2026-05-17: Accepted ADP-010 codebase-pattern audit recommendation. Added Task 17A with bounded behavioral eval smoke coverage: a real-binary CLI fork-with-state eval under `tests/eval/session` and a colocated `internal/ui` ForkDialog eval for the visible `w -> y -> i` interaction path. Added the eval command and paths to the mandate, full verification, PR test plan, spec coverage check, and author-review proposal.
- 2026-05-17: Accepted ADP-011 codebase-pattern audit recommendation with Option 2. Task 19 now expands the README's dedicated `### Fork Sessions` section with fork-with-state examples and contract text, adds a short `### Git Worktrees` cross-reference, and changes the CHANGELOG instructions to the repo's Keep a Changelog prose style under `## [Unreleased]`.
- 2026-05-17: Accepted FWS-016 after the Item 4 discussion document and option comparison. Added new Task 4C to extract a shared `git.ValidateForkWithStateDestination(repoRoot, branch)` helper with a typed `DestinationCollisionError` (worktree_exists / branch_exists). Rewrote Task 13 Step 3 (CLI) to call the helper before `PreflightForkWithState` and leave the existing-worktree reuse block exactly as today's code. Rewrote Task 17 Step 3 (TUI) to call the helper before the reuse block and leave the reuse block exactly as today's code. Updated mandate regexes (Task 18, Task 20, Task 21 test plan) to include `ValidateForkWithStateDestination`. Added structural-change RFC requirement for the new helper. Added three helper unit tests in Task 4C. Eliminates the CLI redundancy without imposing symmetry on TUI; mirrors the FWS-009 architectural pattern.
- 2026-05-18: Accepted FWS-018 plan-side. Rewrote Task 18 to verify the spec's `## Mandatory test coverage` section is current with the actual test names (regex sanity check), instead of editing the now-untracked `CLAUDE.md`. Per CONTRIBUTING.md and PR #1002, `CLAUDE.md` is intentionally per-developer; the mandate must live in a tracked file. The spec is that file. Task 18 no longer creates a commit unless the regex drifted.
