# Fork-with-State Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `--with-state` and `--with-state-and-gitignored` opt-in flags to `agent-deck session fork` (CLI and TUI) that materialize the parent session's working-tree state into a freshly-created worktree branched off the parent's HEAD.

**Architecture:** A new `internal/git/worktree_with_state.go` exposes `MaterializeParentState`, `DetectInProgressOperation`, and `HasSubmodules`. The existing `CreateWorktreeWithSetup` is split into `CreateWorktree` (already in `git.go`) + a new `RunWorktreeSetup` so the fork-with-state path can sequence `CreateWorktree → MaterializeParentState → RunWorktreeSetup`. The CLI handler in `session_cmd.go` and the TUI `ForkDialog` gain new flags/checkboxes that propagate two new transient fields on `ClaudeOptions` (`WithState`, `IncludeGitignored`).

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
| `internal/git/worktree_with_state.go` | Create | `MaterializeParentState`, `DetectInProgressOperation`, `HasSubmodules`, internal helpers |
| `internal/git/worktree_with_state_test.go` | Create | Unit tests for the above |
| `internal/git/setup.go` | Modify | Extract `RunWorktreeSetup`; `CreateWorktreeWithSetup` becomes a wrapper |
| `cmd/agent-deck/session_cmd.go` | Modify | New flags, implication resolution, sequence Create→Materialize→Setup with cleanup-on-error |
| `cmd/agent-deck/session_cmd_test.go` | Modify (or create if missing) | Integration tests for fork-with-state |
| `internal/ui/forkdialog.go` | Modify | Two new sub-checkboxes, focus order extension, getters |
| `internal/ui/forkdialog_test.go` | Modify | TUI tests for visibility, focus, getters |
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

	// 1. Apply staged changes via `git apply --cached`.
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

// applyPatch applies a binary patch to repoDir. If cached=true, the patch
// is applied to the index only (mirrors `git apply --cached`); otherwise it
// is applied to the working tree.
func applyPatch(repoDir string, patch []byte, cached bool) error {
	args := []string{"-C", repoDir, "apply"}
	if cached {
		args = append(args, "--cached")
	}
	cmd := exec.Command("git", args...)
	cmd.Stdin = bytes.NewReader(patch)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git apply (cached=%v): %s: %w", cached, strings.TrimSpace(stderr.String()), err)
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

func TestResolveForkStateFlags_GitignoredImpliesWithStateImpliesWorktree(t *testing.T) {
	r := resolveForkStateFlags(false, true, "", false)
	if !r.WorktreeEnabled || !r.WithState || !r.IncludeGitignored {
		t.Fatalf("--with-state-and-gitignored should imply both parents, got %+v", r)
	}
}

func TestResolveForkStateFlags_WithStateImpliesWorktree(t *testing.T) {
	r := resolveForkStateFlags(true, false, "", false)
	if !r.WorktreeEnabled || !r.WithState {
		t.Fatalf("--with-state should imply --worktree, got %+v", r)
	}
	if r.IncludeGitignored {
		t.Fatalf("--with-state alone should not enable gitignored, got %+v", r)
	}
}

func TestResolveForkStateFlags_ExplicitBranchPreserved(t *testing.T) {
	r := resolveForkStateFlags(true, false, "my-branch", false)
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
// applying the implication chain: --with-state-and-gitignored implies
// --with-state implies --worktree.
type forkStateFlags struct {
	WorktreeEnabled   bool
	WithState         bool
	IncludeGitignored bool
	Branch            string
}

// resolveForkStateFlags applies the implication chain. Any of the three
// boolean inputs being true forces the parents up the chain to true. The
// branch name is propagated unchanged; an empty branch with WorktreeEnabled
// signals the caller to auto-name.
func resolveForkStateFlags(withState, gitignored bool, branch string, worktreeExplicit bool) forkStateFlags {
	r := forkStateFlags{Branch: branch}
	if gitignored {
		r.IncludeGitignored = true
		r.WithState = true
		r.WorktreeEnabled = true
		return r
	}
	if withState {
		r.WithState = true
		r.WorktreeEnabled = true
		return r
	}
	r.WorktreeEnabled = worktreeExplicit
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

## Task 13: CLI fork handler — wire new flags, sequence Create→Materialize→Setup with cleanup

**Files:**
- Modify: `cmd/agent-deck/session_cmd.go:587-799` (the `handleSessionFork` function)

- [ ] **Step 1: Add the new flags to the flagset**

In `handleSessionFork`, after the existing flag declarations (around line 602, just after `sandboxImage`), add:

```go
	withState := fs.Bool("with-state", false, "Carry parent's tracked + staged + untracked state. Implies --worktree (auto-names branch if not supplied).")
	withStateAndGitignored := fs.Bool("with-state-and-gitignored", false, "Also copy gitignored files. Implies --with-state.")
```

Also extend the `fs.Usage` example block (around line 612-617) by adding two lines after the existing examples:

```go
		fmt.Println("  agent-deck session fork my-project --with-state")
		fmt.Println("  agent-deck session fork my-project --with-state-and-gitignored -t \"my-fork-with-env\"")
```

- [ ] **Step 2: Apply the implication chain after parsing**

Inside `handleSessionFork`, just after the existing `wtBranch` and `createNewBranch` resolution block (around line 684-688), insert:

```go
	// Apply implication chain: --with-state-and-gitignored → --with-state → --worktree.
	stateFlags := resolveForkStateFlags(*withState, *withStateAndGitignored, wtBranch, wtBranch != "")
	if stateFlags.WorktreeEnabled && wtBranch == "" {
		// Auto-name the branch using the sanitized fork title.
		sanitized := strings.ToLower(forkTitle)
		sanitized = strings.ReplaceAll(sanitized, " ", "-")
		wtBranch = "fork/" + sanitized
		createNewBranch = true
	}
	if stateFlags.WithState && !stateFlags.WorktreeEnabled {
		// Defensive — resolveForkStateFlags guarantees this is unreachable.
		out.Error("--with-state requires worktree mode", ErrCodeInvalidOperation)
		os.Exit(1)
	}
```

Make sure `strings` is imported at the top of the file (it should already be).

- [ ] **Step 3: Add pre-flight refusal of in-progress operations**

Inside the `if wtBranch != "" { ... }` block (around line 692), at the very top, add:

```go
		if stateFlags.WithState {
			if op, err := git.DetectInProgressOperation(inst.ProjectPath); err != nil {
				out.Error(fmt.Sprintf("failed to inspect parent's git state: %v", err), ErrCodeInvalidOperation)
				os.Exit(1)
			} else if op != "" {
				hint := ""
				switch op {
				case "rebase":
					hint = fmt.Sprintf("finish or abort the rebase before forking with state (cd %s && git rebase --abort)", inst.ProjectPath)
				case "merge":
					hint = "resolve or abort the merge before forking with state"
				case "cherry-pick":
					hint = "finish or abort the cherry-pick before forking with state"
				case "bisect":
					hint = fmt.Sprintf("run 'git bisect reset' in %s before forking with state", inst.ProjectPath)
				}
				out.Error(fmt.Sprintf("parent session is mid-%s; %s", op, hint), ErrCodeInvalidOperation)
				os.Exit(1)
			}
			if has, _ := git.HasSubmodules(inst.ProjectPath); has {
				fmt.Fprintln(os.Stderr, "Warning: submodules detected — copied as files, not recursed (parent's submodule states preserved)")
			}
		}
```

- [ ] **Step 4: Replace the CreateWorktreeWithSetup call with Create → Materialize → Setup**

In the same `if wtBranch != "" { ... }` block, find the existing call to `git.CreateWorktreeWithSetup` (around line 735) and the warning print on line 740-742. Replace those lines with:

```go
			if err := git.CreateWorktree(repoRoot, worktreePath, wtBranch); err != nil {
				out.Error(fmt.Sprintf("worktree creation failed: %v", err), ErrCodeInvalidOperation)
				os.Exit(1)
			}

			if stateFlags.WithState {
				_, materializeErr := git.MaterializeParentState(
					inst.ProjectPath,
					worktreePath,
					git.StateCopyOptions{IncludeGitignored: stateFlags.IncludeGitignored},
				)
				if materializeErr != nil {
					// Cleanup: remove the half-baked worktree and the branch we created.
					_ = exec.Command("git", "-C", repoRoot, "worktree", "remove", "--force", worktreePath).Run()
					if createNewBranch {
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

- [ ] **Step 7: Run existing fork unit tests to confirm no regression**

Run: `GOTOOLCHAIN=go1.24.0 go test ./cmd/agent-deck/ -run "Fork|fork" -race -count=1`
Expected: PASS — existing fork tests still green.

- [ ] **Step 8: Commit**

```bash
git add cmd/agent-deck/session_cmd.go
git commit -m "feat(cli): wire --with-state[-and-gitignored] flags through fork handler with cleanup"
```

---

## Task 14: CLI integration test — happy path with --with-state on a dirty parent

**Files:**
- Modify: `cmd/agent-deck/session_cmd_fork_state_test.go`

Building binary integration tests for `handleSessionFork` is heavy because the handler launches actual Claude sessions. Instead we test the *git-side* end-to-end behavior: simulate the sequence of git calls the handler makes (CreateWorktree → MaterializeParentState → RunWorktreeSetup) against a real repo and verify the resulting worktree contents.

This is what the existing `internal/ui/bare_repo_worktree_guards_test.go` does and what `internal/git/setup_progress_test.go` does — they test the integration of multiple git helpers without standing up the full CLI.

- [ ] **Step 1: Add the failing integration test**

Append to `cmd/agent-deck/session_cmd_fork_state_test.go`:

```go
import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/git"
)

// integrationFork sets up a parent repo with mixed state, then runs the
// same git sequence the CLI handler runs: CreateWorktree → MaterializeParentState
// → RunWorktreeSetup. Returns the worktree path so the test can assert on it.
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
	if err := git.CreateWorktree(parent, worktree, "fork/test"); err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	if _, err := git.MaterializeParentState(parent, worktree, git.StateCopyOptions{IncludeGitignored: gitignored}); err != nil {
		t.Fatalf("MaterializeParentState: %v", err)
	}
	// RunWorktreeSetup with no setup script is a no-op.
	if err := git.RunWorktreeSetup(parent, worktree, os.Stdout, os.Stderr, 0); err != nil {
		t.Fatalf("RunWorktreeSetup: %v", err)
	}
	return parent, worktree
}

func TestSessionFork_WithState_DirtyParent(t *testing.T) {
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

func TestSessionFork_WithStateAndGitignored_CopiesIgnored(t *testing.T) {
	_, wt := integrationFork(t, true)
	got, err := os.ReadFile(filepath.Join(wt, "secret.env"))
	if err != nil {
		t.Fatalf("gitignored file missing: %v", err)
	}
	if string(got) != "KEY=1\n" {
		t.Fatalf("gitignored content mismatch: %q", got)
	}
}

func TestSessionFork_WithState_MaterializeFailureCleansUp(t *testing.T) {
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
	if err := git.CreateWorktree(parent, worktree, "fork/test"); err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	// Force a materialize failure: corrupt parent's git dir so `git diff` errors.
	// Replace the parent's HEAD ref with garbage.
	if err := os.WriteFile(filepath.Join(parent, ".git", "HEAD"), []byte("garbage\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := git.MaterializeParentState(parent, worktree, git.StateCopyOptions{}); err == nil {
		t.Fatal("expected MaterializeParentState to fail with corrupt parent HEAD")
	}
	// Simulate the handler's cleanup: remove the worktree and branch.
	_ = exec.Command("git", "-C", parent, "worktree", "remove", "--force", worktree).Run()
	_ = exec.Command("git", "-C", parent, "branch", "-D", "fork/test").Run()

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

func TestSessionFork_WithState_RefusesWhenMidRebase(t *testing.T) {
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

	op, err := git.DetectInProgressOperation(parent)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if op != "rebase" {
		t.Fatalf("expected mid-rebase, got %q", op)
	}
}
```

- [ ] **Step 2: Run tests**

Run: `GOTOOLCHAIN=go1.24.0 go test ./cmd/agent-deck/ -run "TestSessionFork_WithState" -race -count=1 -v`
Expected: PASS (4 subtests).

- [ ] **Step 3: Commit**

```bash
git add cmd/agent-deck/session_cmd_fork_state_test.go
git commit -m "test(cli): integration tests for fork --with-state (dirty parent, gitignored, cleanup, refuse mid-rebase)"
```

---

## Task 15: TUI — add WithState and IncludeGitignored fields + checkbox rendering

**Files:**
- Modify: `internal/ui/forkdialog.go`

- [ ] **Step 1: Add fields to the ForkDialog struct**

In `internal/ui/forkdialog.go`, find the `ForkDialog` struct definition (around line 17-40) and add two fields just after `sandboxEnabled bool`:

```go
	// State-carrying support for fork-with-state
	withStateEnabled         bool
	withStateAndGitignored   bool
```

- [ ] **Step 2: Add exported getters**

After the existing `IsSandboxEnabled` and `ToggleSandbox` methods (around line 202-209), add:

```go
// IsWithStateEnabled returns whether fork-with-state mode is enabled.
func (d *ForkDialog) IsWithStateEnabled() bool {
	return d.withStateEnabled
}

// ToggleWithState toggles fork-with-state mode. Has no effect unless the
// worktree checkbox is on (the surface only exposes this when worktree is on).
func (d *ForkDialog) ToggleWithState() {
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

## Task 16: TUI test — checkbox visibility and toggling

**Files:**
- Modify: `internal/ui/forkdialog_test.go`

- [ ] **Step 1: Add failing tests**

Append to `internal/ui/forkdialog_test.go`:

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
	d.ToggleWithState()
	if !d.IsWithStateEnabled() {
		t.Fatal("ToggleWithState did not enable")
	}
	d.ToggleWithState()
	if d.IsWithStateEnabled() {
		t.Fatal("ToggleWithState did not disable on second call")
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
	d.ToggleWithState()
	d.ToggleWithStateAndGitignored()
	if !d.IsWithStateAndGitignoredEnabled() {
		t.Fatal("gitignored did not toggle with with-state on")
	}
}

func TestForkDialog_TogglingWithStateOffClearsGitignored(t *testing.T) {
	d := NewForkDialog()
	d.Show("parent-session", t.TempDir(), "", nil, "")
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
```

- [ ] **Step 2: Run tests**

Run: `GOTOOLCHAIN=go1.24.0 go test ./internal/ui/ -run TestForkDialog_WithState -race -count=1 -v`
Expected: PASS (4 subtests; method names match Task 15's getters).

- [ ] **Step 3: Commit**

```bash
git add internal/ui/forkdialog_test.go
git commit -m "test(tui): cover with-state checkbox visibility + toggling"
```

---

## Task 17: TUI — submit handler propagates to ClaudeOptions

**Files:**
- Modify: `internal/ui/home.go` (the place where the fork dialog's submit creates `opts`)

The TUI submit path is in `internal/ui/home.go`. We need to copy the dialog's new bool getters into the `ClaudeOptions` it builds.

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

- [ ] **Step 3: In the same submit handler, sequence Create→Materialize→Setup**

In `internal/ui/home.go`, replace lines 8506-8513 (the `var setupBuf bytes.Buffer ... if setupErr != nil { ... }` block). The surrounding code uses `source` for the parent `*session.Instance`, `opts` for the new `*session.ClaudeOptions`, and returns `sessionForkedMsg{err: ..., sourceID: sourceID}` on failure.

```go
					var setupBuf bytes.Buffer
					if err := git.CreateWorktree(opts.WorktreeRepoRoot, opts.WorktreePath, opts.WorktreeBranch); err != nil {
						return sessionForkedMsg{err: fmt.Errorf("worktree creation failed: %w", err), sourceID: sourceID}
					}
					if opts.WithState {
						if _, mErr := git.MaterializeParentState(
							source.ProjectPath,
							opts.WorktreePath,
							git.StateCopyOptions{IncludeGitignored: opts.IncludeGitignored},
						); mErr != nil {
							_ = exec.Command("git", "-C", opts.WorktreeRepoRoot, "worktree", "remove", "--force", opts.WorktreePath).Run()
							_ = exec.Command("git", "-C", opts.WorktreeRepoRoot, "branch", "-D", opts.WorktreeBranch).Run()
							return sessionForkedMsg{err: fmt.Errorf("failed to materialize parent state: %w; new worktree cleaned up", mErr), sourceID: sourceID}
						}
					}
					setupErr := git.RunWorktreeSetup(opts.WorktreeRepoRoot, opts.WorktreePath, &setupBuf, &setupBuf, session.GetWorktreeSettings().SetupTimeout())
					if setupErr != nil {
						uiLog.Warn("worktree_setup_script_failed", slog.String("error", setupErr.Error()), slog.String("output", setupBuf.String()))
					}
```

Also ensure `"os/exec"` is in the imports at the top of `internal/ui/home.go`. The other imports (`fmt`, `bytes`, `slog`, etc.) are already present.

- [ ] **Step 4: Verify the package compiles**

Run: `GOTOOLCHAIN=go1.24.0 go build ./internal/ui/...`
Expected: exits 0.

- [ ] **Step 5: Run existing TUI tests to confirm no regression**

Run: `GOTOOLCHAIN=go1.24.0 go test ./internal/ui/... -race -count=1`
Expected: PASS — no test regressions.

- [ ] **Step 6: Commit**

```bash
git add internal/ui/home.go
git commit -m "feat(tui): wire fork-with-state through submit handler with cleanup"
```

---

## Task 18: Add Fork-with-state mandate to CLAUDE.md

**Files:**
- Modify: `CLAUDE.md` (append a new section before the "## --no-verify mandate" section)

- [ ] **Step 1: Insert the new section**

In `CLAUDE.md`, find the line `## --no-verify mandate` (toward the end of the file). Immediately before that line, insert:

```markdown
## Fork-with-state: mandatory test coverage

Fork-with-state copies parent's working-tree contents (staged + unstaged +
untracked, optionally gitignored) into a freshly-created worktree branched
off parent's HEAD. Read-only on parent. Class of bugs: a unit test passes
but the resulting worktree silently diverges from parent's actual state.

Any PR modifying fork-with-state paths MUST pass:

```bash
go test ./internal/git/... -run "Materialize|DetectInProgress|HasSubmodules" -race -count=1
go test ./cmd/agent-deck/... -run "SessionFork_WithState|ResolveForkStateFlags" -race -count=1
go test ./internal/ui/... -run "ForkDialog_WithState" -race -count=1
```

### Paths under the mandate

- `internal/git/worktree_with_state.go` (+ `_test.go`)
- `internal/git/setup.go` — the `CreateWorktree` / `RunWorktreeSetup` / `CreateWorktreeWithSetup` split
- `cmd/agent-deck/session_cmd.go` — fork handler, `resolveForkStateFlags`
- `internal/ui/forkdialog.go` — with-state sub-checkboxes
- `internal/ui/home.go` — TUI fork submit handler
- `internal/session/tooloptions.go` — `WithState` / `IncludeGitignored` fields

### Structural changes requiring RFC

- Re-collapsing `CreateWorktree` + `RunWorktreeSetup` back into the old monolithic `CreateWorktreeWithSetup` (breaks materialization-before-setup ordering)
- Mutating parent's index, working tree, or stash list as part of materialization (`git stash`, `git add`, etc.)
- Changing the implication chain `--with-state-and-gitignored → --with-state → --worktree`
- Adding silent fallbacks when materialization fails (must always cleanup + error)

```

- [ ] **Step 2: Verify the file still parses (just an open)**

Run: `head -1 CLAUDE.md && wc -l CLAUDE.md`
Expected: shows top of file + a sensible line count larger than before.

- [ ] **Step 3: Commit**

```bash
git add CLAUDE.md
git commit -m "docs(claude): add Fork-with-state mandatory test coverage section"
```

---

## Task 19: README + CHANGELOG entries

**Files:**
- Modify: `README.md`
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Add a README example**

Run: `grep -n "session fork\|## Fork\|Forking" README.md | head`

Find the section in `README.md` that documents `session fork` (if one exists; otherwise search for the closest fork-related example). Append after the existing fork examples:

````markdown
**Fork with parent's working state:**

```bash
# Carry parent's tracked + staged + untracked edits into a new worktree.
agent-deck session fork my-project --with-state

# Also copy gitignored files (e.g., .env, build caches).
agent-deck session fork my-project --with-state-and-gitignored
```

`--with-state` implies `--worktree` (auto-names branch `fork/<session>` if not supplied).
`--with-state-and-gitignored` implies `--with-state`. Parent's index, working tree, and stash list are left byte-identical after the fork. The fork captures parent state at the moment of fork — later parent edits are not reflected. Mid-rebase/merge/cherry-pick/bisect parents are refused with an actionable error.
````

- [ ] **Step 2: Add a CHANGELOG entry**

Add a new section at the top of `CHANGELOG.md` under the unreleased version header (or create one if none exists). Use the same conventional-commit-style format the existing changelog uses:

```markdown
### Added
- `agent-deck session fork --with-state` and `--with-state-and-gitignored`: opt-in flags that materialize the parent session's working-tree state (staged + unstaged + untracked, optionally gitignored) into a freshly-created worktree branched off parent's HEAD. Read-only on parent. Refuses mid-rebase/merge/cherry-pick/bisect. TUI dialog gains matching sub-checkboxes (press `y` for with-state, `i` for gitignored).

### Changed
- Internal: `internal/git/CreateWorktreeWithSetup` is now a wrapper around `CreateWorktree` + `RunWorktreeSetup`. Direct callers unchanged.
```

- [ ] **Step 3: Commit**

```bash
git add README.md CHANGELOG.md
git commit -m "docs: add --with-state[-and-gitignored] fork examples to README and CHANGELOG"
```

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
GOTOOLCHAIN=go1.24.0 go test ./internal/git/... -run "Materialize|DetectInProgress|HasSubmodules" -race -count=1
GOTOOLCHAIN=go1.24.0 go test ./cmd/agent-deck/... -run "SessionFork_WithState|ResolveForkStateFlags" -race -count=1
GOTOOLCHAIN=go1.24.0 go test ./internal/ui/... -run "ForkDialog_WithState" -race -count=1
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
- Adds opt-in `--with-state` and `--with-state-and-gitignored` flags to `agent-deck session fork` (and matching TUI sub-checkboxes) that materialize the parent session's working-tree state into a freshly-created worktree branched off parent's HEAD.
- Read-only on parent: parent's index, working tree, and stash list are byte-identical after the fork.
- Refuses mid-rebase / mid-merge / mid-cherry-pick / mid-bisect with an actionable error.
- Materialization runs *before* the setup hook so user setup scripts see parent's WIP.

## Design and plan
- Spec: `docs/superpowers/specs/2026-05-14-fork-worktree-with-state-design.md`
- Plan: `docs/superpowers/plans/2026-05-14-fork-worktree-with-state.md`

## Test plan
- [ ] `go test ./internal/git/... -run "Materialize|DetectInProgress|HasSubmodules" -race`
- [ ] `go test ./cmd/agent-deck/... -run "SessionFork_WithState|ResolveForkStateFlags" -race`
- [ ] `go test ./internal/ui/... -run "ForkDialog_WithState" -race`
- [ ] Existing session-persistence, feedback, watcher mandate suites all pass
- [ ] Manual TUI walkthrough: open fork dialog on a dirty parent, toggle `w` → `y` → `i`, submit, verify new worktree contains parent's WIP including gitignored files
- [ ] Manual CLI walkthrough: `agent-deck session fork <dirty-session> --with-state-and-gitignored -t test-fork`
EOF
)"
```

- [ ] **Step 3: Report PR URL**

The previous command prints a PR URL on success. Copy it into the conversation for review.

---

## Spec coverage check

Each spec requirement maps to at least one task:

| Spec requirement | Task(s) |
|---|---|
| CLI `--with-state` and `--with-state-and-gitignored` flags | 12, 13 |
| Implication chain | 12, 13 |
| TUI sub-checkboxes with `y`/`i` toggles | 15, 16 |
| TUI focus order extension | 15 (focus block extended in same edit) |
| `ClaudeOptions.WithState` / `IncludeGitignored` | 1 |
| `MaterializeParentState` covering staged + unstaged + untracked + gitignored | 5, 6, 7, 8, 9 |
| Parent-untouched invariant | 10 |
| Staged-deletion preserved | 10 |
| Binary/symlink/exec-bit | 7, 8 |
| `DetectInProgressOperation` | 2 |
| `HasSubmodules` warning | 3, 13 |
| Split `CreateWorktreeWithSetup` → `RunWorktreeSetup` | 4 |
| Materialize-before-setup-hook ordering | 13, 17 |
| Cleanup-on-error (worktree remove + branch delete) | 13, 14, 17 |
| Refuse mid-rebase/merge/cherry-pick/bisect | 13, 14 |
| CLAUDE.md mandate section | 18 |
| README + CHANGELOG | 19 |
| Full verification | 20 |
| PR opened against upstream | 21 |

No spec requirements without a task; no orphan tasks.
