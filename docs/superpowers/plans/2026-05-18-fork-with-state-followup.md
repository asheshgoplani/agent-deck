# Fork-with-State Followup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the 11 gaps identified after upstream merged #1030 (commit `6a1645eb`) into the fork-with-state feature, split across two PRs.

**Architecture:** Layer on top of upstream's merged API (`MaterializeWipFromParent`, `CreateWorktreeWithStateAndSetup`). The with-state CLI path is already decomposed by PR #1263, and PR-B mirrors that on the TUI side (`CreateWorktreeAtStartPoint` → `MaterializeWipFromParent` → `RunWorktreeSetupAfterCreate`) behind a git-backend guard; non-state creation stays backend-routed and can continue using the upstream wrapper. Do not refactor or replace upstream's just-merged code.

**Tech Stack:** Go 1.25.11 (pinned via `GOTOOLCHAIN`), bubbletea/lipgloss for TUI, shelling out to `git` for diff/apply/ls-files/worktree/branch ops.

**Spec:** [`docs/superpowers/specs/2026-05-18-fork-with-state-followup-design.md`](../specs/2026-05-18-fork-with-state-followup-design.md)
**Gap analysis:** [`docs/superpowers/discussions/2026-05-18-post-merge-gap-analysis.md`](../discussions/2026-05-18-post-merge-gap-analysis.md)
**Deprecated original plan:** [`2026-05-14-fork-worktree-with-state.md`](2026-05-14-fork-worktree-with-state.md)

## Pre-flight (one-time, before Task 1)

```bash
export GOTOOLCHAIN=go1.25.11
# Verify local main is current: PR-A has LANDED (#1263) and the materialize-from-repo-root
# followup (#1277) followed it. The original #1030 merge is the older baseline.
git fetch upstream
git log --oneline upstream/main | grep -E "1263|1277|1030" | head
# Expected (newest first): ... #1277 (materialize fork state from repo root),
#   5dc3e912 PR #1263 (--with-state CLI: A1–A10), 6a1645eb #1030 (original --with-state)

# Sanity-check upstream's tests pass on local clone
GOTOOLCHAIN=go1.25.11 go test ./internal/git/... -run "RegressionFor1029|WithState" -race -count=1
```

Per `CONTRIBUTING.md`, PR-A branched from `main` and was pushed to `smorin/agent-deck` (origin). PR-B now continues in `worktree/fork-state-tui-followup` on current main after PR-A merged.

---

## File map

| File | Action | PR |
|---|---|---|
| `internal/git/git.go` | Modify — add `HeadCommit`, `CreateWorktreeAtStartPoint` | A |
| `internal/git/git_test.go` | Modify — add `TestCreateWorktreeAtStartPoint_*` and `TestHeadCommit_IgnoresGitWarningsOnStderr` tests | A |
| `internal/git/fork_with_state_destination.go` | Create — `ValidateForkWithStateDestination`, `DestinationCollisionError`, collision kind constants, `HasSubmodules`, `DetectInProgressOperation` | A |
| `internal/git/fork_with_state_destination_test.go` | Create — validator tests | A |
| `internal/git/materialize_wip_invariant_test.go` | Create — `TestMaterializeWipFromParent_ParentUntouched` | A |
| `internal/git/fork_with_state_integration_test.go` | Create — bare-repo + setup-hook observation tests | A |
| `internal/git/issue1029_edge_test.go` | Modify — add 4 missing mid-op refusal tests | A |
| `cmd/agent-deck/session_cmd.go` | Modify — parent-HEAD + destination validation + cleanup-on-error + before-start hook | A |
| `cmd/agent-deck/session_cmd_fork_state_test.go` | Create — CLI contract tests | A |
| `tests/eval/session/fork_with_state_test.go` | Create — CLI eval suite for real-binary with-state behavior and refusal/warning paths | A |
| `internal/ui/forkdialog.go` | Modify — sub-checkboxes, focus order, getters | B |
| `internal/ui/forkdialog_test.go` | Modify — state-machine tests | B |
| `internal/ui/forkdialog_eval_test.go` | Create — TUI behavioral eval | B |
| `internal/ui/home.go` | Modify — TUI submit wires the git-only decomposed with-state path with collision check + cleanup, while keeping non-state creation backend-routed | B |

---

# PR-A — Correctness fixes + test hardening (CLI surface)

> ✅ **LANDED — merged as PR #1263 (2026-06-03).** Tasks A1–A10 are complete. The task bodies below are historical, with as-merged reconciliation notes added where the checked-in code differs from the draft snippets. Do not re-implement.

Closes gaps 2, 3, 4 (CLI portion), 5, 6, 7, 8, 9, 10 (CLI portion).

## Task A1 — `HeadCommit` + `CreateWorktreeAtStartPoint` helpers (gap 2)

**Files:**
- Modify: `internal/git/git.go`
- Modify: `internal/git/git_test.go`

Upstream's `CreateWorktree(repoDir, ...)` creates from invocation dir's HEAD, which is wrong when the parent session lives in a linked worktree. Add two helpers: `HeadCommit(repoDir)` returns the resolved commit at `repoDir`'s HEAD (works for normal repos, linked worktrees, and bare-repo project roots via `resolveGitInvocationDir`); `CreateWorktreeAtStartPoint(repoDir, worktreePath, branch, startPoint)` creates a new branch worktree from an explicit commit, and returns `createdBranch=true` only when git actually created the branch (so cleanup can be proof-based, not intent-based).

> **AS-MERGED NOTE (PR #1263):** `HeadCommit` uses `cmd.Output()` with stderr captured separately, not `CombinedOutput()`, so git warnings on stderr cannot be trimmed into the commit hash. The checked-in tests also include `TestHeadCommit_IgnoresGitWarningsOnStderr`.

- [x] **Step 1: Write failing tests in `internal/git/git_test.go`**

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

- [x] **Step 2: Run, confirm FAIL** — `GOTOOLCHAIN=go1.25.11 go test ./internal/git/ -run TestCreateWorktreeAtStartPoint -v` should fail with `undefined: HeadCommit`, `undefined: CreateWorktreeAtStartPoint`.

- [x] **Step 3: Add helpers in `internal/git/git.go`** (near `CreateWorktree`):

```go
// HeadCommit returns the commit currently checked out at repoDir. Works for
// normal repos, linked worktrees, and bare-repo project roots.
func HeadCommit(repoDir string) (string, error) {
	repoDir = resolveGitInvocationDir(repoDir)
	cmd := exec.Command("git", "-C", repoDir, "rev-parse", "--verify", "HEAD^{commit}")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to resolve HEAD commit: %s: %w", strings.TrimSpace(stderr.String()), err)
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

- [x] **Step 4: Run, confirm PASS** — `GOTOOLCHAIN=go1.25.11 go test ./internal/git/ -run TestCreateWorktreeAtStartPoint -v`

- [x] **Step 5: Commit**

```bash
git add internal/git/git.go internal/git/git_test.go
git commit -m "feat(git): HeadCommit + CreateWorktreeAtStartPoint for fork-with-state parent HEAD anchoring"
```

---

## Task A2 — `ValidateForkWithStateDestination` + `DestinationCollisionError` (gap 3)

**Files:**
- Create: `internal/git/fork_with_state_destination.go`
- Create: `internal/git/fork_with_state_destination_test.go`

Shared `internal/git` helper that returns typed collision errors. The CLI calls this before its decomposed with-state path, and the TUI must do the same before `CreateWorktreeAtStartPoint`. Worktree-existence is checked first (more specific error, includes path).

> **AS-MERGED NOTE (PR #1263):** The checked-in validator uses exported collision-kind constants (`CollisionWorktreeExists`, `CollisionBranchExists`) and propagates `GetWorktreeForBranch` failures as `checking existing worktrees: ...`. This task's original snippet ignored that error and used string literals.

- [x] **Step 1: Write failing tests in `internal/git/fork_with_state_destination_test.go`**

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
	if collErr.Kind != CollisionBranchExists || collErr.Branch != "fork/existing" {
		t.Fatalf("unexpected error: %+v", collErr)
	}
}

func TestValidateForkWithStateDestination_WorktreeExists_TakesPrecedence(t *testing.T) {
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
	if collErr.Kind != CollisionWorktreeExists || collErr.Path == "" {
		t.Fatalf("unexpected error: %+v", collErr)
	}
}
```

- [x] **Step 2: Run, confirm FAIL** — `undefined: ValidateForkWithStateDestination`.

- [x] **Step 3: Write `internal/git/fork_with_state_destination.go`**

```go
package git

import "fmt"

// Recognized values for DestinationCollisionError.Kind.
const (
	CollisionWorktreeExists = "worktree_exists"
	CollisionBranchExists   = "branch_exists"
)

// DestinationCollisionError is returned by ValidateForkWithStateDestination
// when the requested destination branch already has a worktree or already
// exists as a local branch. Callers own user-facing wording.
type DestinationCollisionError struct {
	Kind   string // CollisionWorktreeExists or CollisionBranchExists
	Branch string
	Path   string // populated when Kind == CollisionWorktreeExists
}

func (e *DestinationCollisionError) Error() string {
	switch e.Kind {
	case CollisionWorktreeExists:
		return fmt.Sprintf("branch %q already has a worktree at %s", e.Branch, e.Path)
	case CollisionBranchExists:
		return fmt.Sprintf("branch %q already exists", e.Branch)
	default:
		return fmt.Sprintf("destination collision for branch %q", e.Branch)
	}
}

// ValidateForkWithStateDestination is the shared CLI/TUI destination-collision
// gate for fork-with-state. Worktree-collision is checked first so the more
// specific error (with path) is surfaced when both conditions are true.
func ValidateForkWithStateDestination(repoRoot, branch string) error {
	path, err := GetWorktreeForBranch(repoRoot, branch)
	if err != nil {
		return fmt.Errorf("checking existing worktrees: %w", err)
	}
	if path != "" {
		return &DestinationCollisionError{Kind: CollisionWorktreeExists, Branch: branch, Path: path}
	}
	if BranchExists(repoRoot, branch) {
		return &DestinationCollisionError{Kind: CollisionBranchExists, Branch: branch}
	}
	return nil
}
```

- [x] **Step 4: Run, confirm PASS**

- [x] **Step 5: Commit**

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

> ⚠️ **AS-MERGED NOTE (PR #1263):** The code snippet in this task is **superseded by the merged `cmd/agent-deck/session_cmd.go`**. As merged, the with-state path is **DECOMPOSED and routed through the VCS backend abstraction** (`internal/vcs`, `internal/vcsbackend`, `cmd/agent-deck/vcs_helper.go`'s `detectAndCreateBackend`), not the single literal flow below. Deltas from this snippet, all of which landed:
> - **Early non-git guard placed BEFORE the git-direct collision gate:** `if wantState && backend.Type() != vcs.TypeGit { reject }`. This is what makes the subsequent git-direct calls jujutsu-safe — with-state is git-only and vcsbackend has no with-state methods.
> - **Mutually-exclusive collision gate:** `if wantState { ValidateForkWithStateDestination(...) } else if !createNewBranch && !backend.BranchExists(...) { "branch does not exist (use -b)" }`.
> - **Branch prefix and path guard:** the configured branch prefix is applied before validation, and an already-existing computed worktree path is refused before creation.
> - **Reuse routed through the backend** via `backend.GetWorktreeForBranch(...)`, gated on `!wantState`.
> - **Mid-op refusal with actionable abort commands** in the error text; **submodule warning**; **`HeadCommit` written to stdout only**; **cleanup-on-error** carrying a `branchCleanupHint`.
>
> Treat the snippet below as the design intent; consult the merged `session_cmd.go` for the authoritative shape. PR-B's Task B4 mirrors this same backend-routed reconciliation on the TUI side.

- [x] **Step 1: Add imports** — ensure `"errors"` is imported.

- [x] **Step 2: Inside `handleSessionFork`, after the existing `if wantState && wtBranch == "" { ... }` validation upstream added, insert the with-state custom path**

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

- [x] **Step 3: If needed, add `RunWorktreeSetupAfterCreate` to `internal/git/setup.go`**

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

- [x] **Step 4: Verify the package compiles** — `GOTOOLCHAIN=go1.25.11 go build ./cmd/agent-deck/...`

- [x] **Step 5: Run upstream's existing fork tests** — `GOTOOLCHAIN=go1.25.11 go test ./cmd/agent-deck/... ./internal/git/... -run "Fork|WithState|RegressionFor1029" -race -count=1`. Should still pass.

- [x] **Step 6: Commit**

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

- [x] **Step 1: Add the hook variable in `cmd/agent-deck/session_cmd.go`**

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

- [x] **Step 2: Write `cmd/agent-deck/session_cmd_fork_state_test.go`**

(Full test file — uses `runAgentDeck` test helper from existing tests if available; otherwise inline shell-out to the built binary. Covers:)
- `TestSessionFork_WithStateRequiresExplicitDestinationBranch` — `--with-state` without `-w` → exit non-zero, error message
- `TestSessionFork_WithStateAndGitignoredRequiresExplicitDestinationBranch` — same for `--with-state-and-gitignored`
- `TestSessionFork_WithState_RejectsExistingDestinationBranch` — `-w fork/existing --with-state` → error mentions "already exists"
- `TestSessionFork_WithState_RejectsExistingDestinationWorktree` — pre-create worktree, then `-w fork/used --with-state` → error mentions "already has a worktree"
- `TestSessionFork_WithStateOptionsPropagatedBeforeStart` — uses `sessionForkBeforeStartHook` to capture the resolved `git.WorktreeStateOptions` plus the forked `*session.Instance`, asserts `state.WithState && state.WithIgnored` and that the forked instance was created on the requested worktree branch (e.g. `fork/with-env`). The flags do **not** live on `session.ClaudeOptions` — upstream's #1030 routes them through the git layer.
- `TestSessionFork_WithStateHookCapturesResolvedStateBeforeStart` — verifies the hook captures the resolved parent/fork instances and state before `Start()`.
- `TestSessionFork_WithState_RefusesMidOpWithActionableHint_StructuralGuard` — source-level guard that the CLI surfaces actionable mid-op abort guidance before worktree creation.

- [x] **Step 3: Add 4 missing mid-op refusal tests in `internal/git/issue1029_edge_test.go`**

Upstream has merge coverage (or similar). Add `TestRefuseUnsafeParentState_Rebase_RegressionForFollowup`, `_CherryPick_RegressionForFollowup`, `_Revert_RegressionForFollowup`, and `_Bisect_RegressionForFollowup` — each forces the corresponding mid-op state then asserts `MaterializeWipFromParent` returns an error mentioning the kind.

- [x] **Step 4: Run** — `GOTOOLCHAIN=go1.25.11 go test ./cmd/agent-deck/... ./internal/git/... -run "SessionFork_WithState|RefuseUnsafeParentState" -race -count=1`

- [x] **Step 5: Commit**

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

- [x] **Step 1: Add the test, run, expect PASS** (upstream's implementation already satisfies this invariant; this is a regression test against future changes)

- [x] **Step 2: Commit** — `git commit -m "test(git): assert MaterializeWipFromParent leaves parent byte-identical"`

---

## Task A6 — Bare-repo + linked parent worktree test (gap 6)

**Files:**
- Create: `internal/git/fork_with_state_integration_test.go`

Test that fork-with-state works when:
- The repository is a bare-layout project (`.bare/` directory inside the project root)
- The parent session lives in a linked worktree (not in the bare dir itself)
- The fork is anchored at the parent worktree's HEAD via `CreateWorktreeAtStartPoint`

- [x] **Step 1: Write `TestForkWithState_BareRepoLayoutLinkedParentWorktree`** — initialize bare repo, create seed clone, push initial commit, create parent linked worktree from bare, dirty parent (WIP), capture parent HEAD, create fork worktree via `CreateWorktreeAtStartPoint(GetWorktreeBaseRoot(root), fork-path, "fork/bare", parentHead)`, materialize, assert fork's WIP matches parent's.

- [x] **Step 2: Run, commit**

---

## Task A7 — Setup-hook observation test (gap 7)

**Files:**
- Modify: `internal/git/fork_with_state_integration_test.go`

Setup script writes the SHA of a parent-WIP file into a marker. Test asserts the marker contains the parent-WIP content's SHA, proving setup ran AFTER materialization.

- [x] **Step 1: Add `TestForkWithState_SetupHookObservesMaterializedState`** — places `.agent-deck/worktree-setup.sh` in parent that does `sha256sum wip.txt > /tmp/marker.txt`; dirty parent with `wip.txt`; run the full A3 sequence; assert `/tmp/marker.txt` contains the SHA of "wip-content".

- [x] **Step 2: Commit**

---

## Task A8 — CLI behavioral eval (gap 10 CLI)

**Files:**
- Create: `tests/eval/session/fork_with_state_test.go`

Eval-tagged (`//go:build eval_smoke`) suite that runs the compiled `agent-deck` binary against a scratch HOME and real git repos. As merged, it covers `TestEval_SessionForkWithState_RealBinary`, `TestEval_SessionForkWithState_RejectsExistingBranch`, `TestEval_SessionForkWithState_RejectsExistingWorktree`, `TestEval_SessionForkWithState_RefusesMidRebaseParent`, and `TestEval_SessionForkWithState_SubmoduleWarning`.

- [x] **Step 1: Write the eval suite** — model after existing evals in `tests/eval/session/`
- [x] **Step 2: Run with `-tags eval_smoke`, commit**

---

## Task A9 — PR-A verification

- [x] **Step 1: Run formatter + linter + tests**

```bash
GOTOOLCHAIN=go1.25.11 make fmt
GOTOOLCHAIN=go1.25.11 make lint
GOTOOLCHAIN=go1.25.11 make test
```

- [x] **Step 2: Run the mandate suite** — from the followup spec's `## Mandatory test coverage` section.

- [x] **Step 3: Re-run upstream's existing tests to confirm no regression**

```bash
GOTOOLCHAIN=go1.25.11 go test ./internal/git/... -run "Issue1029|RegressionFor1029" -race -count=1
```

- [x] **Step 4: Commit fixes if any**

---

## Task A10 — Open PR-A

- [x] **Step 1: Push** — `git push -u origin feature/fork-worktree-with-state` (or a sub-branch named `feature/fork-with-state-pr-a` if PR-A is on a different branch from PR-B)
- [x] **Step 2: Open PR-A** via `gh pr create` against `upstream/main`. Reference issue #1029 and PR #1030. Body cites the followup spec, the gap analysis, and lists the gaps PR-A closes.
- [x] **Step 3: Report PR-A URL** — merged as **PR #1263** (merge commit `5dc3e912`, 2026-06-03).

---

# PR-B — TUI integration (depends on PR-A merge)

> **STATUS (2026-06-04 audit):** PR-B is being reconstructed in `worktree/fork-state-tui-followup` on current main + the VCS backend abstraction. The old PR-B branch is reference-only: salvage the dialog-UI commits (B1–B3, B5 — backend-agnostic); **rewrite B4** against the backend.

Closes gap 1, plus the TUI portions of gaps 3, 4, and 10.

> **DESIGN DECISIONS (2026-06-04, resolved with @smorin).** These bind B1–B4:
>
> 1. **Collision UX — static hint + reject-at-submit.** Always render a static hint line `↳ creates a NEW branch at parent HEAD` under "Carry parent state" (pure text — no git existence check in the dialog). An existing-branch collision is caught at submit by `git.ValidateForkWithStateDestination` → error banner via `h.setError`. No async dialog state.
> 2. **Labels — `Carry parent state` (y) and `Include gitignored files` (i).** Matches the `--with-state` / `--with-state-and-gitignored` CLI flags.
> 3. **Shortcuts — keep `y` / `i`.** Verified safe: the dialog's key switch only intercepts a letter when focus is on a toggle target; on the Name/Branch text inputs the key falls through and is typed normally (same pattern as `w`/`s`). No clash, including branch names containing `i` (`fix`, `ui`, `init`).
> 4. **Toggle model — focus targets + `Space` (the B2 refactor is REQUIRED, not optional).** The two checkboxes are real focus stops in the `focusTargets` slice; `Space`/`Enter` toggles the focused box; `y`/`i` remain as shortcuts. Nested invariants are enforced by skipping disabled targets in the focus order (gitignored skipped unless with-state on; with-state skipped unless worktree on).

## Task B1 — `ForkDialog` state + getters (gap 1)

**Files:**
- Modify: `internal/ui/forkdialog.go`

Add `withStateEnabled bool` and `withStateAndGitignored bool` fields. Exported getters: `IsWithStateEnabled()`, `IsWithStateAndGitignoredEnabled()`. Toggle methods: `ToggleWithState()`, `ToggleWithStateAndGitignored()`. Nested-state invariants: `ToggleWithState` is a no-op unless worktree is on; `ToggleWorktree` clears with-state if turning off; `ToggleWithStateAndGitignored` is a no-op unless with-state is on; `ToggleWithState` clears gitignored if turning off.

> **Still valid (backend-agnostic).** Main's `forkdialog.go` still uses numeric `focusIndex` and has zero with-state UI, so this task is genuinely absent on main. Reference commit `e573f4f8` (B1 state+getters) is salvageable as-is.

- [x] **Step 1: Add fields, exported getters, and toggle methods**
- [x] **Step 2: Reset fields in `Show()` and `Hide()`**
- [x] **Step 3: Verify compile** — `GOTOOLCHAIN=go1.25.11 go build ./internal/ui/...`
- [x] **Step 4: Commit** — `git commit -m "feat(tui): ForkDialog state + getters for fork-with-state"`

## Task B2 — `ForkDialog` focus targets (gap 1)

**Files:**
- Modify: `internal/ui/forkdialog.go`

Refactor to use the existing `NewDialog` focus-target pattern: declare `forkFocusTarget` enum, ordered `focusTargets` slice rebuilt on conditional toggles. Replace numeric `focusIndex` arithmetic.

> **Still valid (backend-agnostic).** Main's `forkdialog.go` still uses the numeric `focusIndex` arithmetic this task replaces. Reference commit `dc5d42d2` (B2 focus-target) is salvageable.
>
> **Required by Decision 4 (no longer optional).** B3 depends on this: add `carryState` and `gitignored` as focus targets in the rebuilt `focusTargets` slice, each **skipped** when its parent toggle is off (gitignored skipped unless with-state on; with-state skipped unless worktree on). `Space`/`Enter` on a focused target toggles it; `y`/`i` stay as shortcuts.

- [x] **Step 1-5: Apply focus-target refactor as documented in the deprecated plan's Task 15A**
- [x] **Step 6: Commit**

## Task B3 — `ForkDialog` rendering + key handlers + tests (gap 1)

**Files:**
- Modify: `internal/ui/forkdialog.go`
- Modify: `internal/ui/forkdialog_test.go`

Render the two new checkboxes when worktree is on; render the gitignored checkbox nested when with-state is on. Wire the toggles. Add state-machine tests (toggle requires worktree, toggling worktree off clears with-state, etc.).

> **Still valid (backend-agnostic).** Pure dialog UI; no VCS coupling. Reference commit `ba3ec451` (B3 checkboxes+handlers) is salvageable — but reconcile its key handling to the Decision-4 model below.
>
> **Per Decisions 1–4 (exact UI contract):**
> - Checkbox labels are exactly `Carry parent state` and `Include gitignored files`.
> - Render a static hint line `↳ creates a NEW branch at parent HEAD` directly under "Carry parent state" (no git check — Decision 1).
> - `Include gitignored files` renders nested (one extra indent) and only when with-state is on.
> - Toggle via `Space`/`Enter` on the focused checkbox (Decision 4), plus `y`/`i` shortcuts (Decision 3). Shortcuts intercept only when focus is on a toggle target, so they remain typeable in the Name/Branch inputs.

- [x] **Step 1: Add checkbox rendering in `View()`** (labels + static hint + nested indent)
- [x] **Step 2: Add `Space`/`Enter` toggle on the focused checkbox + `y`/`i` shortcut handlers in `Update()`**
- [x] **Step 3: Add state-machine tests in `forkdialog_test.go`**
- [x] **Step 4: Run, commit**

## Task B4 — TUI submit wires collision check + cleanup-on-error (gaps 1, 3-TUI, 4-TUI)

**Files:**
- Modify: `internal/ui/home.go`

> **REWRITTEN (2026-06-03) to match main.** The original B4 text below the dashed rule was written against the dead git-direct PR-A base and is superseded. On current main, `forkSessionCmdWithOptions` (in `internal/ui/home.go`, ~line 9404) already routes the fork through the VCS backend abstraction: `backend, _ := vcsbackend.Detect(opts.WorktreeRepoRoot)` → `backend.GetWorktreeForBranch(...)` for reuse → otherwise `createWorktreeWithSetupAndLog(backend, opts.WorktreePath, opts.WorktreeBranch)` (which calls `vcsbackend.CreateWorktreeWithSetup(backend, ...)`). It has **no** with-state handling. B4 must MIRROR PR #1263's CLI reconciliation, on the TUI side:

1. **Backend is already detected** in `forkSessionCmdWithOptions` (`vcsbackend.Detect(...)`); reuse that handle.
2. **Thread the with-state booleans** from the dialog getters (`IsWithStateEnabled`, `IsWithStateAndGitignoredEnabled`) into this async `tea.Cmd` closure. NOTE: `forkSessionCmdWithOptions` is also called with `nil` opts from non-dialog paths — guard against `nil` before reading getters (default both booleans to `false`).
3. **Early non-git guard** (placed BEFORE any git-direct call): if with-state is requested and `backend.Type() != vcs.TypeGit`, return `sessionForkedMsg{err: fmt.Errorf("--with-state is only supported for git repositories"), sourceID: ...}`. This guard is what keeps the git-direct calls below jujutsu-safe.
4. **Non-state path — unchanged:** reuse via `backend.GetWorktreeForBranch(...)`, else `createWorktreeWithSetupAndLog(backend, ...)`. Do not touch this branch.
5. **With-state path (git-only, git-direct)** — implemented as `forkWithStateWorktree` in `home.go`, in this order:
   - `git.ValidateForkWithStateDestination(repoRoot, branch)` (collision gate; with-state never reuses an existing worktree). Checked **before** the path guard so the more actionable error wins.
   - Refuse if the computed worktree path already exists.
   - `git.DetectInProgressOperation(source.ProjectPath)` — actionable abort guidance (rebase/merge/cherry-pick/revert/bisect) before any creation.
   - `git.HasSubmodules(source.ProjectPath)` warning via `uiLog.Warn` (TUI has no stderr surface here).
   - `git.HeadCommit(source.ProjectPath)` (parent-HEAD anchor).
   - `git.CreateWorktreeAtStartPoint(repoRoot, worktreePath, branch, parentHead)`.
   - `git.MaterializeWipFromParent(source.ProjectPath, worktreePath, withIgnored)` with cleanup-on-error: force-remove the worktree and, if `createdBranch`, delete the branch; if cleanup also fails, return a manual-cleanup hint.
   - `git.ProcessWorktreeInclude(...)`.
   - `git.RunWorktreeSetupAfterCreate(...)` — **non-fatal** (warn on failure; the worktree + state already exist, mirroring #1263).
6. **Return `sessionForkedMsg{err: ..., sourceID: ...}` on every error path** — NOT `os.Exit` (that is the CLI surface only).

> **Design decision (mirrors #1263).** The collision check and the with-state worktree creation stay **git-direct** because with-state is git-only, gated behind the early `backend.Type() == vcs.TypeGit` guard; `vcsbackend` has no with-state methods. Reuse and the non-state path route through the **backend** (jujutsu-safe). This is the exact same split PR #1263 made on the CLI side.

> **Salvage note.** Reference commit `25623f6a` (B4 submit handler) was written against the dead git-direct PR-A API and **must be rewritten** against the backend abstraction per the steps above; it is NOT salvageable as-is.

- [x] **Step 1-5: Implement (per the steps above), test, commit**

## Task B5 — TUI behavioral eval (gap 10 TUI)

**Files:**
- Create: `internal/ui/forkdialog_eval_test.go`

Eval-tagged test that renders `ForkDialog`, drives `w → y → i` keystrokes, asserts visible checkbox text appears via substring checks on `View()` output, and asserts the getters report submitted values.

> **Still valid (minor).** Pure dialog-render eval, no backend coupling. Reference commit `fa14d61a` (B5 eval) is salvageable.

- [x] **Step 1: Write `TestEval_ForkDialog_WithStateVisibleInteraction`**
- [x] **Step 2: Commit**

## Task B6 — PR-B verification

- [x] Same as A9: fmt, lint, test, mandate suite, regression check.

## Task B7 — Open PR-B

- [x] **Step 1: Continue in `worktree/fork-state-tui-followup` on current main; do NOT rebase the old PR-B branch.** PR-A is already in main (#1263), so there is nothing to wait for. The old branch was built on the dead git-direct base; rebasing it would replay B4 against an API that no longer exists. Cherry-pick/port the salvageable dialog-UI commits (B1–B3 = `e573f4f8`, `dc5d42d2`, `ba3ec451`; B5 = `fa14d61a`) onto current main, then implement the rewritten B4 against the backend abstraction.
- [x] **Step 2: Push + open PR-B** referencing PR-A (#1263) and the followup spec.

---

## Mandate verification (post-implementation)

After Tasks A1-A10 and B1-B7 are all complete, run the followup spec's mandate suite (under the pinned `GOTOOLCHAIN=go1.25.11`):

```bash
go test ./internal/git/... -run "Materialize|RefuseUnsafeParentState|ValidateForkWithStateDestination|CreateWorktreeAtStartPoint|HeadCommit|ForkWithState|Issue1029" -race -count=1
go test ./cmd/agent-deck/... -run "SessionFork_WithState" -race -count=1
go test ./internal/ui/... -run "ForkDialog_(WithState|ToggleWith|ToggleGitignored|ToggleWorktreeOff|Focus_|View_|Space_|Y_Toggles|I_Toggles|I_Typeable|WorktreeControlsVisible)|ForkWithStateWorktree_|ForkSessionCmdWithOptions_(AcceptsForkState|WithStateRejectsNonGit)|ForkDialogSubmitCapturesWithStateBeforeHide" -race -count=1
go test -tags eval_smoke ./tests/eval/session/... ./internal/ui/... -run "TestEval_SessionForkWithState|TestEval_ForkDialog_WithState" -race -count=1
```

The UI regex matches the as-implemented PR-B test names (33 tests): B1 state/toggles (`WithState`, `ToggleWith`, `ToggleGitignored`, `ToggleWorktreeOff`), B2 focus order (`Focus_`), B3 render/keys (`View_`, `Space_`, `Y_Toggles`, `I_Toggles`, `I_Typeable`), and B4 submit — `ForkWithStateWorktree_`, `ForkSessionCmdWithOptions_WithStateRejectsNonGit` (the early non-git guard surfacing "--with-state is only supported for git repositories" via `sessionForkedMsg{err}`, mirroring #1263), `ForkSessionCmdWithOptions_AcceptsForkState`, `ForkDialogSubmitCapturesWithStateBeforeHide`, and `WorktreeControlsVisible` (bare-root visibility).

Each alternation matches ≥1 test (verified). If a future rename produces "no tests to run" for a branch, update the regex to the actual names.

---

## Spec coverage check

| Gap | Spec section | Plan task(s) |
|---|---|---|
| 1. TUI integration | G1 | B1, B2, B3, B4 — OPEN (PR-B) |
| 2. Parent-HEAD start point | G2 | A1, A3 — CLOSED by #1263 |
| 3. Destination collision validation | G3 | A2, A3, B4 — CLI CLOSED by #1263; TUI OPEN (PR-B) |
| 4. Cleanup-on-error (CLI) | G4 | A3 — CLOSED by #1263 |
| 4. Cleanup-on-error (TUI) | G4 | B4 — OPEN (PR-B) |
| 5. Parent-untouched invariant | G5 | A5 — CLOSED by #1263 |
| 6. Bare-repo + linked parent worktree | G6 | A6 — CLOSED by #1263 |
| 7. Setup-hook observation | G7 | A7 — CLOSED by #1263 |
| 8. 4 missing mid-op refusal tests | G8 | A4 (step 3) — CLOSED by #1263 |
| 9. CLI before-start hook contract test | G9 | A4 (steps 1-2) — CLOSED by #1263 |
| 10. Behavioral eval smoke (CLI) | G10 CLI | A8 — CLOSED by #1263 |
| 10. Behavioral eval smoke (TUI) | G10 TUI | B5 — OPEN (PR-B) |
| 11. Shared `PreflightForkWithState` extraction | Out of scope | Deferred PR-C — DEFERRED |

Gaps 2, 3-CLI, 4-CLI, 5, 6, 7, 8, 9, 10-CLI are CLOSED by PR #1263. Gap 1 and gap 10-TUI (plus the TUI portions of gaps 3 and 4) remain OPEN, pending PR-B. No gap without a task (except gap 11 by explicit design).

---

## Out of scope (deferred PR-C)

**Gap 11 — Shared `PreflightForkWithState` extraction.** Upstream's `refuseUnsafeParentState` is internal/lowercase and returns a plain formatted `error`. Promoting it to an exported helper that returns typed `InProgressOperationError` is a structural change to upstream's just-merged code; it deserves an RFC discussion with @asheshgoplani before implementation. PR-A and PR-B leave `refuseUnsafeParentState` alone — they call into `MaterializeWipFromParent` which has the refusal baked in. The price is that the CLI and TUI can't share a single explicit preflight gate with surface-specific error rendering; the refusal happens inside materialize. Acceptable for the user-visible behavior of PR-A and PR-B.

---

## Review change log

- 2026-05-19: FUS-002 — Removed stale references to ClaudeOptions.WithState/IncludeGitignored fields. Upstream's #1030 chose a different architecture (flags flow through git.WorktreeStateOptions, not ClaudeOptions). Plan corrected to reflect upstream's actual wiring; A4's CLI contract tests already adapted to the real shape. Dropped the `internal/session/tooloptions.go` file-map row and rewrote the A4 before-start hook signature + the propagation assertion to use `git.WorktreeStateOptions` instead of `*session.ClaudeOptions`.
- 2026-06-03: FUS-003 — Reconciled the plan with what has landed since 2026-05-18. **PR-A merged as PR #1263** (merge commit `5dc3e912`); the **VCS backend abstraction** (`internal/vcs`, `internal/vcsbackend`, `internal/jujutsu`, `cmd/agent-deck/vcs_helper.go`) and the **materialize-from-repo-root followup #1277** also landed on main. Plan updates: marked all PR-A tasks (A1–A10) complete with a LANDED banner and an as-merged note on A3 (the merged `session_cmd.go` is decomposed and backend-routed — early non-git guard, mutually-exclusive collision gate, backend-routed reuse gated `!wantState`, mid-op actionable messages, submodule warning, HeadCommit stdout-only, cleanup-on-error — superseding the A3 snippet). **Rewrote PR-B Task B4** to route through the backend abstraction on the TUI side (with-state stays git-direct behind an early `backend.Type() == vcs.TypeGit` guard; reuse and the non-state path go through the backend), mirroring #1263. Added salvage guidance for the old PR-B branch (B1–B3 + B5 dialog-UI commits salvageable; B4 must be rewritten). Bumped the toolchain pin from `go1.24.0` to `go1.25.11` throughout (main's `go.mod` now requires Go 1.25.11).
- 2026-06-04: FUS-004 — Updated PR-B reconstruction references to use the active `worktree/fork-state-tui-followup` worktree and demoted the old PR-B branch to reference-only commit salvage. Confirmed the plan's toolchain references match `go.mod`'s Go 1.25.11 requirement.
- 2026-06-04: FUS-005 — Reconciled all PR-A task references against checked-in code. Updated A1 for stdout-only `HeadCommit` and its stderr-warning regression test, A2 for collision constants and `GetWorktreeForBranch` error propagation, A3 for branch-prefix/path guards, and A8 for the expanded CLI eval suite that landed with PR #1263.
- 2026-06-04: FUS-006 — Folded four PR-B UI design decisions (resolved with @smorin) into the B1–B4 task specs: (1) collision UX is a static "creates a NEW branch at parent HEAD" hint + reject-at-submit (no async dialog state); (2) labels are `Carry parent state` / `Include gitignored files`, matching the CLI flags; (3) `y`/`i` shortcuts retained (verified they don't clash with branch-name typing); (4) the B2 focus-target refactor is now REQUIRED — the checkboxes are focus stops toggled with `Space`/`Enter` (plus `y`/`i`), with nested invariants enforced by skipping disabled targets. Added a DESIGN DECISIONS block under the PR-B banner and updated B2/B3 accordingly.
- 2026-06-04: FUS-007 — Executed doc-3 Task 5: reconciled the mandate UI regex in the plan + spec to the as-implemented PR-B test names (33 tests) — replaced the never-matching `GitignoredRequires`/`Toggling`/`FocusOrder` alternations with `ToggleWith`/`ToggleGitignored`/`ToggleWorktreeOff`/`Focus_`/`View_`/`Space_`/`Y_Toggles`/`I_Toggles`/`I_Typeable`/`WorktreeControlsVisible`/`ForkWithStateWorktree_`/`ForkSessionCmdWithOptions_(AcceptsForkState|WithStateRejectsNonGit)`/`ForkDialogSubmitCapturesWithStateBeforeHide`. Expanded B4 item 5 to the full implemented sequence. Marked PR-B B1–B5 and doc-3 Tasks 1–5 complete (commits f636a301, 1914a2da, ecb91357, 023ca226, 4f18a4ac).
