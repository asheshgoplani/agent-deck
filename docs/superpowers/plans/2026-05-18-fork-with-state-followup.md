# Fork-with-State Followup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the 11 gaps identified after upstream merged #1030 (commit `6a1645eb`) into the fork-with-state feature, split across two PRs.

**Architecture:** Layer on top of upstream's merged API (`MaterializeWipFromParent`, `CreateWorktreeWithStateAndSetup`). Add new helpers in new files; modify the CLI and TUI handlers to wrap upstream's wrapper with our pre-checks and cleanup. Do not refactor or replace upstream's just-merged code.

**Tech Stack:** Go 1.24.0 (pinned via `GOTOOLCHAIN`), bubbletea/lipgloss for TUI, shelling out to `git` for diff/apply/ls-files/worktree/branch ops.

**Spec:** [`docs/superpowers/specs/2026-05-18-fork-with-state-followup-design.md`](../specs/2026-05-18-fork-with-state-followup-design.md)
**Gap analysis:** [`docs/superpowers/discussions/2026-05-18-post-merge-gap-analysis.md`](../discussions/2026-05-18-post-merge-gap-analysis.md)
**Deprecated original plan:** [`2026-05-14-fork-worktree-with-state.md`](2026-05-14-fork-worktree-with-state.md)

## Pre-flight (one-time, before Task 1)

```bash
export GOTOOLCHAIN=go1.24.0
# Verify local main is current with upstream's #1030 merge
git fetch upstream
git log --oneline upstream/main | grep -E "1029|1030" | head
# Expected: 6a1645eb feat(fork): --with-state and --with-state-and-gitignored ...

# Sanity-check upstream's tests pass on local clone
GOTOOLCHAIN=go1.24.0 go test ./internal/git/... -run "RegressionFor1029|WithState" -race -count=1
```

Per `CONTRIBUTING.md`, PR-A branches from `main` and is pushed to `smorin/agent-deck` (origin); PR-B does the same but rebases onto `main` after PR-A merges.

---

## File map

| File | Action | PR |
|---|---|---|
| `internal/git/git.go` | Modify — add `HeadCommit`, `CreateWorktreeAtStartPoint` | A |
| `internal/git/git_test.go` | Modify — add `TestCreateWorktreeAtStartPoint_*` tests | A |
| `internal/git/fork_with_state_destination.go` | Create — `ValidateForkWithStateDestination`, `DestinationCollisionError` | A |
| `internal/git/fork_with_state_destination_test.go` | Create — validator tests | A |
| `internal/git/materialize_wip_invariant_test.go` | Create — parent-untouched invariant test | A |
| `internal/git/fork_with_state_integration_test.go` | Create — bare-repo + setup-hook observation tests | A |
| `internal/git/issue1029_edge_test.go` | Modify — add 4 missing mid-op refusal tests | A |
| `cmd/agent-deck/session_cmd.go` | Modify — parent-HEAD + destination validation + cleanup-on-error + before-start hook | A |
| `cmd/agent-deck/session_cmd_fork_state_test.go` | Create — CLI contract tests | A |
| `tests/eval/session/fork_with_state_test.go` | Create — eval smoke for CLI | A |
| `internal/ui/forkdialog.go` | Modify — sub-checkboxes, focus order, getters | B |
| `internal/ui/forkdialog_test.go` | Modify — state-machine tests | B |
| `internal/ui/forkdialog_eval_test.go` | Create — TUI behavioral eval | B |
| `internal/ui/home.go` | Modify — TUI submit wires collision check + cleanup-on-error | B |

---

# PR-A — Correctness fixes + test hardening (CLI surface)

Closes gaps 2, 3, 4 (CLI portion), 5, 6, 7, 8, 9, 10 (CLI portion).

## Task A1 — `HeadCommit` + `CreateWorktreeAtStartPoint` helpers (gap 2)

**Files:**
- Modify: `internal/git/git.go`
- Modify: `internal/git/git_test.go`

Upstream's `CreateWorktree(repoDir, ...)` creates from invocation dir's HEAD, which is wrong when the parent session lives in a linked worktree. Add two helpers: `HeadCommit(repoDir)` returns the resolved commit at `repoDir`'s HEAD (works for normal repos, linked worktrees, and bare-repo project roots via `resolveGitInvocationDir`); `CreateWorktreeAtStartPoint(repoDir, worktreePath, branch, startPoint)` creates a new branch worktree from an explicit commit, and returns `createdBranch=true` only when git actually created the branch (so cleanup can be proof-based, not intent-based).

- [ ] **Step 1: Write failing tests in `internal/git/git_test.go`**

```go
func TestCreateWorktreeAtStartPoint_UsesExplicitParentHead(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "base")
	if err := os.MkdirAll(base, 0o755); err != nil { t.Fatal(err) }
	createTestRepo(t, base)

	parentWT := filepath.Join(root, "parent-wt")
	if err := CreateWorktree(base, parentWT, "parent-branch"); err != nil {
		t.Fatalf("CreateWorktree parent: %v", err)
	}
	if err := os.WriteFile(filepath.Join(parentWT, "README.md"), []byte("parent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, parentWT, "commit", "-am", "parent change")

	baseHead := strings.TrimSpace(runGit(t, base, "rev-parse", "HEAD"))
	parentHead, err := HeadCommit(parentWT)
	if err != nil { t.Fatalf("HeadCommit: %v", err) }
	if baseHead == parentHead {
		t.Fatal("setup invalid: base and parent HEAD should differ")
	}

	forkWT := filepath.Join(root, "fork-wt")
	createdBranch, err := CreateWorktreeAtStartPoint(base, forkWT, "fork/from-parent", parentHead)
	if err != nil { t.Fatalf("CreateWorktreeAtStartPoint: %v", err) }
	if !createdBranch {
		t.Fatal("CreateWorktreeAtStartPoint returned createdBranch=false for a new branch")
	}
	forkHead := strings.TrimSpace(runGit(t, forkWT, "rev-parse", "HEAD"))
	if forkHead != parentHead {
		t.Fatalf("fork HEAD = %s, want parent HEAD %s (base HEAD %s)", forkHead, parentHead, baseHead)
	}
}

func TestCreateWorktreeAtStartPoint_RejectsExistingBranch(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "base")
	if err := os.MkdirAll(base, 0o755); err != nil { t.Fatal(err) }
	createTestRepo(t, base)
	parentHead, _ := HeadCommit(base)
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

- [ ] **Step 2: Run, confirm FAIL** — `GOTOOLCHAIN=go1.24.0 go test ./internal/git/ -run TestCreateWorktreeAtStartPoint -v` should fail with `undefined: HeadCommit`, `undefined: CreateWorktreeAtStartPoint`.

- [ ] **Step 3: Add helpers in `internal/git/git.go`** (near `CreateWorktree`):

```go
// HeadCommit returns the commit currently checked out at repoDir. Works for
// normal repos, linked worktrees, and bare-repo project roots.
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
// start point. Returns createdBranch=true only after git successfully creates
// the branch for this call. Used by fork-with-state to anchor the new worktree
// at the parent session's HEAD instead of the invocation repo's HEAD.
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

- [ ] **Step 4: Run, confirm PASS** — `GOTOOLCHAIN=go1.24.0 go test ./internal/git/ -run TestCreateWorktreeAtStartPoint -v`

- [ ] **Step 5: Commit**

```bash
git add internal/git/git.go internal/git/git_test.go
git commit -m "feat(git): HeadCommit + CreateWorktreeAtStartPoint for fork-with-state parent HEAD anchoring"
```

---

## Task A2 — `ValidateForkWithStateDestination` + `DestinationCollisionError` (gap 3)

**Files:**
- Create: `internal/git/fork_with_state_destination.go`
- Create: `internal/git/fork_with_state_destination_test.go`

Shared `internal/git` helper that returns typed collision errors. Both CLI and TUI handlers call this before invoking upstream's `CreateWorktreeWithStateAndSetup`. Worktree-existence is checked first (more specific error, includes path).

- [ ] **Step 1: Write failing tests in `internal/git/fork_with_state_destination_test.go`**

```go
package git

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateForkWithStateDestination_Clean(t *testing.T) {
	dir := t.TempDir()
	createTestRepo(t, dir)
	if err := ValidateForkWithStateDestination(dir, "fork/new"); err != nil {
		t.Fatalf("clean repo + fresh branch should pass, got %v", err)
	}
}

func TestValidateForkWithStateDestination_BranchExists(t *testing.T) {
	dir := t.TempDir()
	createTestRepo(t, dir)
	runGit(t, dir, "branch", "fork/existing")

	err := ValidateForkWithStateDestination(dir, "fork/existing")
	if err == nil { t.Fatal("expected DestinationCollisionError") }
	var collErr *DestinationCollisionError
	if !errors.As(err, &collErr) {
		t.Fatalf("error = %T %v, want *DestinationCollisionError", err, err)
	}
	if collErr.Kind != "branch_exists" || collErr.Branch != "fork/existing" {
		t.Fatalf("unexpected error: %+v", collErr)
	}
}

func TestValidateForkWithStateDestination_WorktreeExists(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "base")
	if err := os.MkdirAll(base, 0o755); err != nil { t.Fatal(err) }
	createTestRepo(t, base)
	wtPath := filepath.Join(root, "fork-wt")
	if err := CreateWorktree(base, wtPath, "fork/used"); err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}

	err := ValidateForkWithStateDestination(base, "fork/used")
	if err == nil { t.Fatal("expected DestinationCollisionError") }
	var collErr *DestinationCollisionError
	if !errors.As(err, &collErr) {
		t.Fatalf("error = %T %v, want *DestinationCollisionError", err, err)
	}
	if collErr.Kind != "worktree_exists" || collErr.Path == "" {
		t.Fatalf("unexpected error: %+v", collErr)
	}
}
```

- [ ] **Step 2: Run, confirm FAIL** — `undefined: ValidateForkWithStateDestination`.

- [ ] **Step 3: Write `internal/git/fork_with_state_destination.go`**

```go
package git

import "fmt"

// DestinationCollisionError is returned by ValidateForkWithStateDestination
// when the requested destination branch already has a worktree or already
// exists as a local branch. Callers own user-facing wording.
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
// gate for fork-with-state. Worktree-collision is checked first so the more
// specific error (with path) is surfaced when both conditions are true.
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

- [ ] **Step 4: Run, confirm PASS**

- [ ] **Step 5: Commit**

```bash
git add internal/git/fork_with_state_destination.go internal/git/fork_with_state_destination_test.go
git commit -m "feat(git): shared ValidateForkWithStateDestination + typed DestinationCollisionError"
```

---

## Task A3 — Wire parent-HEAD + collision check + cleanup-on-error into `handleSessionFork` (gaps 2, 3, 4-CLI)

**Files:**
- Modify: `cmd/agent-deck/session_cmd.go`

Upstream's CLI handler currently calls `CreateWorktreeWithStateAndSetup` directly. We pre-empt with validation, anchor the worktree at parent's HEAD via `CreateWorktreeAtStartPoint`, then drive the rest of upstream's wrapper steps manually (materialize + worktreeinclude + setup) so we can guard each with cleanup. This keeps upstream's `MaterializeWipFromParent`, `ProcessWorktreeInclude`, and the setup-script logic untouched.

Strategy: when `wantState` is true, take a custom flow; when false, delegate to upstream's existing wrapper for backward compatibility.

- [ ] **Step 1: Add imports** — ensure `"errors"` is imported.

- [ ] **Step 2: Inside `handleSessionFork`, after the existing `if wantState && wtBranch == "" { ... }` validation upstream added, insert the with-state custom path**

Replace the existing call:

```go
setupErr, err := git.CreateWorktreeWithStateAndSetup(
    repoRoot, worktreePath, wtBranch,
    git.WorktreeStateOptions{WithState: wantState, WithIgnored: *withStateGitignored},
    os.Stdout, os.Stderr, session.GetWorktreeSettings().SetupTimeout())
```

with:

```go
var setupErr error
if wantState {
    // Pre-flight: destination collision check using the shared validator.
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

    // Capture parent's HEAD so linked-worktree parents anchor correctly.
    parentHead, hcErr := git.HeadCommit(inst.ProjectPath)
    if hcErr != nil {
        out.Error(fmt.Sprintf("failed to resolve parent session HEAD: %v", hcErr), ErrCodeInvalidOperation)
        os.Exit(1)
    }

    createdBranch, cwErr := git.CreateWorktreeAtStartPoint(repoRoot, worktreePath, wtBranch, parentHead)
    if cwErr != nil {
        out.Error(fmt.Sprintf("worktree creation failed: %v", cwErr), ErrCodeInvalidOperation)
        os.Exit(1)
    }

    // Materialize parent state, with cleanup-on-error.
    if matErr := git.MaterializeWipFromParent(inst.ProjectPath, worktreePath, *withStateGitignored); matErr != nil {
        _ = exec.Command("git", "-C", repoRoot, "worktree", "remove", "--force", worktreePath).Run()
        if createdBranch {
            _ = exec.Command("git", "-C", repoRoot, "branch", "-D", wtBranch).Run()
        }
        out.Error(fmt.Sprintf("failed to materialize parent state: %v; new worktree cleaned up", matErr), ErrCodeInvalidOperation)
        os.Exit(1)
    }

    // Continue the upstream wrapper's tail: worktreeinclude + setup hook.
    if inclErr := git.ProcessWorktreeInclude(repoRoot, worktreePath, os.Stderr); inclErr != nil {
        fmt.Fprintf(os.Stderr, "worktreeinclude: %v\n", inclErr)
    }
    setupErr = git.RunWorktreeSetupAfterCreate(repoRoot, worktreePath, os.Stdout, os.Stderr, session.GetWorktreeSettings().SetupTimeout())
} else {
    // Legacy path: no with-state. Delegate to upstream's wrapper unchanged.
    setupErr, err = git.CreateWorktreeWithStateAndSetup(
        repoRoot, worktreePath, wtBranch,
        git.WorktreeStateOptions{},
        os.Stdout, os.Stderr, session.GetWorktreeSettings().SetupTimeout())
    if err != nil {
        out.Error(fmt.Sprintf("worktree creation failed: %v", err), ErrCodeInvalidOperation)
        os.Exit(1)
    }
}
```

**Note:** `git.RunWorktreeSetupAfterCreate` may need to be a small new exported helper that runs only the setup-hook portion of `CreateWorktreeWithStateAndSetup`. If it doesn't exist, define it in this task as a 10-line wrapper around the existing setup-hook code in `internal/git/setup.go`. Alternatively, the with-state path can call `CreateWorktreeWithStateAndSetup` AFTER `CreateWorktreeAtStartPoint` removed the worktree it already created — but that's awkward. Defining `RunWorktreeSetupAfterCreate` is cleaner.

- [ ] **Step 3: If needed, add `RunWorktreeSetupAfterCreate` to `internal/git/setup.go`**

```go
// RunWorktreeSetupAfterCreate runs the worktree setup script for an
// already-created worktree. Extracted from CreateWorktreeWithStateAndSetup so
// the fork-with-state path can sequence Create → Materialize → Setup with
// per-step error handling. Returns the script's exit error; nil if no script.
func RunWorktreeSetupAfterCreate(repoDir, worktreePath string, stdout, stderr io.Writer, setupTimeout time.Duration) error {
	scriptPath, scriptMode := FindWorktreeSetupScript(repoDir)
	if scriptPath == "" { return nil }
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

- [ ] **Step 4: Verify the package compiles** — `GOTOOLCHAIN=go1.24.0 go build ./cmd/agent-deck/...`

- [ ] **Step 5: Run upstream's existing fork tests** — `GOTOOLCHAIN=go1.24.0 go test ./cmd/agent-deck/... ./internal/git/... -run "Fork|WithState|RegressionFor1029" -race -count=1`. Should still pass.

- [ ] **Step 6: Commit**

```bash
git add cmd/agent-deck/session_cmd.go internal/git/setup.go
git commit -m "feat(cli): parent-HEAD + destination collision + cleanup-on-error for fork --with-state"
```

---

## Task A4 — CLI before-start hook + contract tests (gaps 8, 9)

**Files:**
- Modify: `cmd/agent-deck/session_cmd.go` (add `sessionForkBeforeStartHook` test seam)
- Create: `cmd/agent-deck/session_cmd_fork_state_test.go`

Add a test-only `sessionForkBeforeStartHook` variable that lets contract tests inspect the prepared `Instance` and the resolved `git.WorktreeStateOptions` before `Start()` is called. (Upstream's #1030 did **not** add with-state fields to `session.ClaudeOptions`; the flags flow through the git layer, so the hook surfaces them directly.) Then write contract tests for the explicit-destination refusal, collision refusal, and option propagation.

- [ ] **Step 1: Add the hook variable in `cmd/agent-deck/session_cmd.go`**

```go
// sessionForkBeforeStartHook is nil in production. Tests assign it to inspect
// the fully-prepared fork before tmux Start() mutates the environment. The
// with-state flag values are captured separately because upstream's #1030
// wired them through git.WorktreeStateOptions, not session.ClaudeOptions.
var sessionForkBeforeStartHook func(parent *session.Instance, forked *session.Instance, state git.WorktreeStateOptions)
```

In `handleSessionFork`, immediately before `forkedInst.Start()`, add:

```go
if sessionForkBeforeStartHook != nil {
    sessionForkBeforeStartHook(inst, forkedInst, git.WorktreeStateOptions{WithState: wantState, WithIgnored: *withStateGitignored})
    return
}
```

- [ ] **Step 2: Write `cmd/agent-deck/session_cmd_fork_state_test.go`**

(Full test file — uses `runAgentDeck` test helper from existing tests if available; otherwise inline shell-out to the built binary. Covers:)
- `TestSessionFork_WithStateRequiresExplicitDestinationBranch` — `--with-state` without `-w` → exit non-zero, error message
- `TestSessionFork_WithStateAndGitignoredRequiresExplicitDestinationBranch` — same for `--with-state-and-gitignored`
- `TestSessionFork_WithState_RejectsExistingDestinationBranch` — `-w fork/existing --with-state` → error mentions "already exists"
- `TestSessionFork_WithState_RejectsExistingDestinationWorktree` — pre-create worktree, then `-w fork/used --with-state` → error mentions "already has a worktree"
- `TestSessionFork_WithStateOptionsPropagatedBeforeStart` — uses `sessionForkBeforeStartHook` to capture the resolved `git.WorktreeStateOptions` plus the forked `*session.Instance`, asserts `state.WithState && state.WithIgnored` and that the forked instance was created on the requested worktree branch (e.g. `fork/with-env`). The flags do **not** live on `session.ClaudeOptions` — upstream's #1030 routes them through the git layer.

- [ ] **Step 3: Add 4 missing mid-op refusal tests in `internal/git/issue1029_edge_test.go`**

Upstream has `TestRefuseUnsafeParentState_Merge` (or similar). Add `_Rebase`, `_CherryPick`, `_Revert`, `_Bisect` — each forces the corresponding mid-op state then asserts `MaterializeWipFromParent` returns an error mentioning the kind.

- [ ] **Step 4: Run** — `GOTOOLCHAIN=go1.24.0 go test ./cmd/agent-deck/... ./internal/git/... -run "SessionFork_WithState|RefuseUnsafeParentState" -race -count=1`

- [ ] **Step 5: Commit**

```bash
git add cmd/agent-deck/session_cmd.go cmd/agent-deck/session_cmd_fork_state_test.go internal/git/issue1029_edge_test.go
git commit -m "test(cli): contract tests for --with-state + 4 missing mid-op refusal regressions"
```

---

## Task A5 — Parent-untouched invariant test (gap 5)

**Files:**
- Create: `internal/git/materialize_wip_invariant_test.go`

```go
package git

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMaterializeWipFromParent_ParentUntouched(t *testing.T) {
	parent := t.TempDir()
	createTestRepo(t, parent)
	// Build a complex WIP state on parent: staged + unstaged + untracked.
	writeFile(t, parent, "tracked.txt", "tracked\n")
	runGit(t, parent, "add", "tracked.txt")
	runGit(t, parent, "commit", "-m", "tracked")
	writeFile(t, parent, "tracked.txt", "staged\n")
	runGit(t, parent, "add", "tracked.txt")
	writeFile(t, parent, "tracked.txt", "staged\nunstaged\n")
	writeFile(t, parent, "new-untracked.txt", "untracked\n")

	statusBefore := runGit(t, parent, "status", "--porcelain")
	diffCachedBefore := runGit(t, parent, "diff", "--cached")
	diffBefore := runGit(t, parent, "diff")
	stashBefore := runGit(t, parent, "stash", "list")

	parentHead, err := HeadCommit(parent)
	if err != nil { t.Fatal(err) }
	child := parent + "-fork"
	if _, err := CreateWorktreeAtStartPoint(parent, child, "fork/inv", parentHead); err != nil { t.Fatal(err) }
	if err := MaterializeWipFromParent(parent, child, false); err != nil { t.Fatal(err) }

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
```

`writeFile` and `runGit` are shared test helpers from the existing `internal/git/*_test.go` files.

- [ ] **Step 1: Add the test, run, expect PASS** (upstream's implementation already satisfies this invariant; this is a regression test against future changes)

- [ ] **Step 2: Commit** — `git commit -m "test(git): assert MaterializeWipFromParent leaves parent byte-identical"`

---

## Task A6 — Bare-repo + linked parent worktree test (gap 6)

**Files:**
- Create: `internal/git/fork_with_state_integration_test.go`

Test that fork-with-state works when:
- The repository is a bare-layout project (`.bare/` directory inside the project root)
- The parent session lives in a linked worktree (not in the bare dir itself)
- The fork is anchored at the parent worktree's HEAD via `CreateWorktreeAtStartPoint`

- [ ] **Step 1: Write `TestForkWithState_BareRepoLayoutLinkedParentWorktree`** — initialize bare repo, create seed clone, push initial commit, create parent linked worktree from bare, dirty parent (WIP), capture parent HEAD, create fork worktree via `CreateWorktreeAtStartPoint(GetWorktreeBaseRoot(root), fork-path, "fork/bare", parentHead)`, materialize, assert fork's WIP matches parent's.

- [ ] **Step 2: Run, commit**

---

## Task A7 — Setup-hook observation test (gap 7)

**Files:**
- Modify: `internal/git/fork_with_state_integration_test.go`

Setup script writes the SHA of a parent-WIP file into a marker. Test asserts the marker contains the parent-WIP content's SHA, proving setup ran AFTER materialization.

- [ ] **Step 1: Add `TestForkWithState_SetupHookObservesMaterializedState`** — places `.agent-deck/worktree-setup.sh` in parent that does `sha256sum wip.txt > /tmp/marker.txt`; dirty parent with `wip.txt`; run the full A3 sequence; assert `/tmp/marker.txt` contains the SHA of "wip-content".

- [ ] **Step 2: Commit**

---

## Task A8 — CLI behavioral eval (gap 10 CLI)

**Files:**
- Create: `tests/eval/session/fork_with_state_test.go`

Eval-tagged (`//go:build eval_smoke`) test that runs the compiled `agent-deck` binary against a scratch HOME, a fake `claude` script, and a real git repo. Asserts that `agent-deck session fork <parent> --with-state-and-gitignored -w fork/eval` creates a new destination worktree at the correct path with parent's WIP materialized.

- [ ] **Step 1: Write the eval test** — model after existing evals in `tests/eval/session/`
- [ ] **Step 2: Run with `-tags eval_smoke`, commit**

---

## Task A9 — PR-A verification

- [ ] **Step 1: Run formatter + linter + tests**

```bash
GOTOOLCHAIN=go1.24.0 make fmt
GOTOOLCHAIN=go1.24.0 make lint
GOTOOLCHAIN=go1.24.0 make test
```

- [ ] **Step 2: Run the mandate suite** — from the followup spec's `## Mandatory test coverage` section.

- [ ] **Step 3: Re-run upstream's existing tests to confirm no regression**

```bash
GOTOOLCHAIN=go1.24.0 go test ./internal/git/... -run "Issue1029|RegressionFor1029" -race -count=1
```

- [ ] **Step 4: Commit fixes if any**

---

## Task A10 — Open PR-A

- [ ] **Step 1: Push** — `git push -u origin feature/fork-worktree-with-state` (or a sub-branch named `feature/fork-with-state-pr-a` if PR-A is on a different branch from PR-B)
- [ ] **Step 2: Open PR-A** via `gh pr create` against `upstream/main`. Reference issue #1029 and PR #1030. Body cites the followup spec, the gap analysis, and lists the gaps PR-A closes.
- [ ] **Step 3: Report PR-A URL**

---

# PR-B — TUI integration (depends on PR-A merge)

Closes gap 1, plus the TUI portions of gaps 3, 4, and 10.

## Task B1 — `ForkDialog` state + getters (gap 1)

**Files:**
- Modify: `internal/ui/forkdialog.go`

Add `withStateEnabled bool` and `withStateAndGitignored bool` fields. Exported getters: `IsWithStateEnabled()`, `IsWithStateAndGitignoredEnabled()`. Toggle methods: `ToggleWithState()`, `ToggleWithStateAndGitignored()`. Nested-state invariants: `ToggleWithState` is a no-op unless worktree is on; `ToggleWorktree` clears with-state if turning off; `ToggleWithStateAndGitignored` is a no-op unless with-state is on; `ToggleWithState` clears gitignored if turning off.

- [ ] **Step 1: Add fields, exported getters, and toggle methods**
- [ ] **Step 2: Reset fields in `Show()` and `Hide()`**
- [ ] **Step 3: Verify compile** — `GOTOOLCHAIN=go1.24.0 go build ./internal/ui/...`
- [ ] **Step 4: Commit** — `git commit -m "feat(tui): ForkDialog state + getters for fork-with-state"`

## Task B2 — `ForkDialog` focus targets (gap 1)

**Files:**
- Modify: `internal/ui/forkdialog.go`

Refactor to use the existing `NewDialog` focus-target pattern: declare `forkFocusTarget` enum, ordered `focusTargets` slice rebuilt on conditional toggles. Replace numeric `focusIndex` arithmetic.

- [ ] **Step 1-5: Apply focus-target refactor as documented in the deprecated plan's Task 15A**
- [ ] **Step 6: Commit**

## Task B3 — `ForkDialog` rendering + key handlers + tests (gap 1)

**Files:**
- Modify: `internal/ui/forkdialog.go`
- Modify: `internal/ui/forkdialog_test.go`

Render the two new checkboxes when worktree is on; render the gitignored checkbox nested when with-state is on. Wire `y` and `i` key handlers. Add state-machine tests (toggle requires worktree, toggling worktree off clears with-state, etc.).

- [ ] **Step 1: Add checkbox rendering in `View()`**
- [ ] **Step 2: Add `y`/`i` key handlers in `Update()`**
- [ ] **Step 3: Add state-machine tests in `forkdialog_test.go`**
- [ ] **Step 4: Run, commit**

## Task B4 — TUI submit wires collision check + cleanup-on-error (gaps 1, 3-TUI, 4-TUI)

**Files:**
- Modify: `internal/ui/home.go`

In `forkSessionCmdWithOptions`, read the with-state booleans from the dialog getters (`IsWithStateEnabled`, `IsWithStateAndGitignoredEnabled`) and build a local `stateOpts := git.WorktreeStateOptions{WithState: ..., WithIgnored: ...}`. (Named `stateOpts` rather than `opts` to avoid collision with the `opts *session.ClaudeOptions` convention used throughout this package; these flags are **not** carried on `session.ClaudeOptions` — upstream wired them through the git layer in #1030.) Replace the existing `CreateWorktreeWithSetup` call with a flow that:
1. Calls `ValidateForkWithStateDestination` first (if `stateOpts.WithState`)
2. Calls `HeadCommit(source.ProjectPath)` for parent-HEAD anchoring (if `stateOpts.WithState`)
3. Calls `CreateWorktreeAtStartPoint` (with-state) or `CreateWorktree` (legacy)
4. Calls `MaterializeWipFromParent(source.ProjectPath, worktreePath, stateOpts.WithIgnored)` (with-state) with cleanup-on-error
5. Calls `ProcessWorktreeInclude` + setup hook

Returns `sessionForkedMsg{err: ..., sourceID: ...}` on error.

- [ ] **Step 1-5: Implement, test, commit**

## Task B5 — TUI behavioral eval (gap 10 TUI)

**Files:**
- Create: `internal/ui/forkdialog_eval_test.go`

Eval-tagged test that renders `ForkDialog`, drives `w → y → i` keystrokes, asserts visible checkbox text appears via substring checks on `View()` output, and asserts the getters report submitted values.

- [ ] **Step 1: Write `TestEval_ForkDialog_WithStateVisibleInteraction`**
- [ ] **Step 2: Commit**

## Task B6 — PR-B verification

- [ ] Same as A9: fmt, lint, test, mandate suite, regression check.

## Task B7 — Open PR-B

- [ ] **Step 1: Rebase onto upstream/main** (which should now include PR-A after merge)
- [ ] **Step 2: Push + open PR-B** referencing PR-A and the followup spec.

---

## Mandate verification (post-implementation)

After Tasks A1-A10 and B1-B7 are all complete, run the followup spec's mandate suite:

```bash
go test ./internal/git/... -run "Materialize|RefuseUnsafeParentState|ValidateForkWithStateDestination|CreateWorktreeAtStartPoint|HeadCommit|ForkWithState|Issue1029" -race -count=1
go test ./cmd/agent-deck/... -run "SessionFork_WithState" -race -count=1
go test ./internal/ui/... -run "ForkDialog_(WithState|ToggleWithState|GitignoredRequires|Toggling|FocusOrder)" -race -count=1
go test -tags eval_smoke ./tests/eval/session/... ./internal/ui/... -run "TestEval_SessionForkWithState|TestEval_ForkDialog_WithState" -race -count=1
```

Each command must match at least one test. If any returns "no tests to run," update the spec's regex to match the actual test names.

---

## Spec coverage check

| Gap | Spec section | Plan task(s) |
|---|---|---|
| 1. TUI integration | G1 | B1, B2, B3, B4 |
| 2. Parent-HEAD start point | G2 | A1, A3 |
| 3. Destination collision validation | G3 | A2, A3, B4 |
| 4. Cleanup-on-error (CLI) | G4 | A3 |
| 4. Cleanup-on-error (TUI) | G4 | B4 |
| 5. Parent-untouched invariant | G5 | A5 |
| 6. Bare-repo + linked parent worktree | G6 | A6 |
| 7. Setup-hook observation | G7 | A7 |
| 8. 4 missing mid-op refusal tests | G8 | A4 (step 3) |
| 9. CLI before-start hook contract test | G9 | A4 (steps 1-2) |
| 10. Behavioral eval smoke (CLI) | G10 CLI | A8 |
| 10. Behavioral eval smoke (TUI) | G10 TUI | B5 |
| 11. Shared `PreflightForkWithState` extraction | Out of scope | Deferred PR-C |

No gap without a task (except gap 11 by explicit design).

---

## Out of scope (deferred PR-C)

**Gap 11 — Shared `PreflightForkWithState` extraction.** Upstream's `refuseUnsafeParentState` is internal/lowercase and returns a plain formatted `error`. Promoting it to an exported helper that returns typed `InProgressOperationError` is a structural change to upstream's just-merged code; it deserves an RFC discussion with @asheshgoplani before implementation. PR-A and PR-B leave `refuseUnsafeParentState` alone — they call into `MaterializeWipFromParent` which has the refusal baked in. The price is that the CLI and TUI can't share a single explicit preflight gate with surface-specific error rendering; the refusal happens inside materialize. Acceptable for the user-visible behavior of PR-A and PR-B.

---

## Review change log

- 2026-05-19: FUS-002 — Removed stale references to ClaudeOptions.WithState/IncludeGitignored fields. Upstream's #1030 chose a different architecture (flags flow through git.WorktreeStateOptions, not ClaudeOptions). Plan corrected to reflect upstream's actual wiring; A4's CLI contract tests already adapted to the real shape. Dropped the `internal/session/tooloptions.go` file-map row and rewrote the A4 before-start hook signature + the propagation assertion to use `git.WorktreeStateOptions` instead of `*session.ClaudeOptions`.
