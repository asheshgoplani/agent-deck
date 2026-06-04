# Fork-State TUI Valid Findings Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** Fix the four still-valid PR-B audit findings: preserve with-state checkbox values through submit, expose worktree/with-state controls for bare-repo project roots, mirror the CLI with-state safeguards in the TUI submit path, and add concrete TUI submit-path coverage.

**Architecture:** Keep PR-B's split: non-state worktree creation remains backend-routed through `vcs.Backend`, while with-state creation is git-only behind `backend.Type() == vcs.TypeGit`. Use `git.WorktreeStateOptions` as the explicit handoff type from `ForkDialog` into `forkSessionCmdWithOptions`, captured before `ForkDialog.Hide()`. Extract the TUI with-state worktree creation into a small helper with injectable dependencies so cleanup and refusal paths can be unit-tested without driving tmux or Bubble Tea.

**Tech Stack:** Go 1.25.11, Bubble Tea/lipgloss TUI, `internal/git` helpers from PR #1263, `internal/vcs` backend abstraction, Go unit tests plus targeted source-structure guard tests where a real TUI run would be unnecessarily brittle.

---

## File Map

| File | Action | Purpose |
|---|---|---|
| `internal/ui/forkdialog.go` | Modify | Use bare-root-capable predicate for worktree UI visibility; keep with-state fields reset behavior from PR-B B1/B3. |
| `internal/ui/forkdialog_test.go` | Modify | Add bare-repo project-root visibility/toggle regression test. |
| `internal/ui/home.go` | Modify | Capture with-state and sandbox values before `Hide()`, pass `git.WorktreeStateOptions` explicitly, and implement git-only with-state submit helper with CLI safeguards. |
| `internal/ui/fork_state_submit_test.go` | Create | Test submit handoff shape and helper behavior: collision/no-reuse, parent-HEAD anchoring, mid-op refusal, cleanup failure wording, and non-git rejection. |
| `docs/superpowers/plans/2026-05-18-fork-with-state-followup.md` | Modify | Fold this supplemental plan back into B4 checklist and mandate regex after code lands. |
| `docs/superpowers/specs/2026-05-18-fork-with-state-followup-design.md` | Modify | Add any new TUI submit test names to mandatory coverage after code lands. |

---

## Task 1: Bare-Root Worktree Visibility

**Finding covered:** High: PR-B will hide the whole with-state UI for bare-repo project roots.

**Files:**
- Modify: `internal/ui/forkdialog.go`
- Modify: `internal/ui/forkdialog_test.go`

- [x] **Step 1: Write failing bare-root visibility test**

Add to `internal/ui/forkdialog_test.go`:

```go
func TestForkDialog_WorktreeControlsVisibleForBareProjectRoot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	projectRoot := t.TempDir()
	bareDir := filepath.Join(projectRoot, ".bare")
	if err := exec.Command("git", "init", "--bare", bareDir).Run(); err != nil {
		t.Fatalf("git init --bare: %v", err)
	}

	d := NewForkDialog()
	d.SetSize(90, 40)
	d.Show("Bare Root Parent", projectRoot, "", nil, "")

	view := d.View()
	if !strings.Contains(view, "Create in worktree") {
		t.Fatalf("bare project root should show worktree controls; view:\n%s", view)
	}

	updated, _ := d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}})
	if !updated.IsWorktreeEnabled() {
		t.Fatal("pressing w should enable worktree mode for a bare project root")
	}
}
```

Also add `os/exec` to the test imports if it is not already present.

- [x] **Step 2: Run test to verify it fails before the fix**

Run:

```bash
GOTOOLCHAIN=go1.25.11 go test ./internal/ui/... -run TestForkDialog_WorktreeControlsVisibleForBareProjectRoot -count=1
```

Expected before implementation: FAIL because `ForkDialog.Show` uses `git.IsGitRepo(projectPath)`, so bare-root project paths do not show the worktree controls.

- [x] **Step 3: Implement broader worktree-capable predicate**

In `internal/ui/forkdialog.go`, rename the field and use the broader predicate:

```go
// Worktree support
worktreeEnabled bool
worktreeToggled bool // true once the user explicitly toggled the worktree checkbox (vs config default_enabled); see #1185.
branchInput     textinput.Model
branchPicker    *BranchPickerDialog
worktreeCapable bool
```

In `Show`:

```go
d.worktreeCapable = git.IsGitRepoOrBareProjectRoot(projectPath)
```

In `Update`, replace the `w` guard:

```go
if d.focusIndex == 1 && d.worktreeCapable {
	d.ToggleWorktree()
	if d.worktreeEnabled {
		d.focusIndex = 2
		d.updateFocus()
	}
	return d, nil
}
```

In `View`, replace the worktree section guard:

```go
if d.worktreeCapable {
	// existing worktree checkbox and branch input rendering
}
```

- [x] **Step 4: Run test to verify it passes**

Run:

```bash
GOTOOLCHAIN=go1.25.11 go test ./internal/ui/... -run "TestForkDialog_WorktreeControlsVisibleForBareProjectRoot|TestRegression742_HomeWorktreeGuardsAcceptBareProjectRoot" -count=1
```

Expected: PASS.

- [x] **Step 5: Commit**

```bash
git add internal/ui/forkdialog.go internal/ui/forkdialog_test.go
git commit -m "fix(tui): show fork worktree controls for bare project roots"
```

---

## Task 2: Preserve With-State Selection Through Submit

**Finding covered:** High: PR-B state handoff is underspecified and likely to drop checkbox values.

**Files:**
- Modify: `internal/ui/home.go`
- Create: `internal/ui/fork_state_submit_test.go`

This task assumes B1/B3 from the main followup plan have added:

```go
func (d *ForkDialog) IsWithStateEnabled() bool
func (d *ForkDialog) IsWithStateAndGitignoredEnabled() bool
```

- [x] **Step 1: Write failing source-structure tests for the handoff**

Create `internal/ui/fork_state_submit_test.go`:

```go
package ui

import (
	"os"
	"strings"
	"testing"
)

func TestForkDialogSubmitCapturesWithStateBeforeHide(t *testing.T) {
	srcBytes, err := os.ReadFile("home.go")
	if err != nil {
		t.Fatalf("read home.go: %v", err)
	}
	src := string(srcBytes)

	captureState := strings.Index(src, "forkState := git.WorktreeStateOptions")
	captureSandbox := strings.Index(src, "sandboxEnabled := h.forkDialog.IsSandboxEnabled()")
	hide := strings.Index(src, "h.forkDialog.Hide()")
	call := strings.Index(src, "h.forkSessionCmdWithOptions(source, title, groupPath, opts, sandboxEnabled, forkState, parentID, parentPath)")

	if captureState < 0 {
		t.Fatal("submit handler must capture git.WorktreeStateOptions before hiding the dialog")
	}
	if captureSandbox < 0 {
		t.Fatal("submit handler must capture sandboxEnabled before hiding the dialog")
	}
	if hide < 0 {
		t.Fatal("submit handler must hide the dialog after capturing values")
	}
	if call < 0 {
		t.Fatal("submit handler must pass captured forkState into forkSessionCmdWithOptions")
	}
	if captureState > hide || captureSandbox > hide {
		t.Fatalf("dialog state must be captured before Hide(); captureState=%d captureSandbox=%d hide=%d", captureState, captureSandbox, hide)
	}
	if hide > call {
		t.Fatalf("forkSessionCmdWithOptions should be called after Hide with captured values; hide=%d call=%d", hide, call)
	}
}

func TestForkSessionCmdWithOptions_AcceptsForkState(t *testing.T) {
	srcBytes, err := os.ReadFile("home.go")
	if err != nil {
		t.Fatalf("read home.go: %v", err)
	}
	src := string(srcBytes)
	if !strings.Contains(src, "forkState git.WorktreeStateOptions") {
		t.Fatal("forkSessionCmdWithOptions must take forkState git.WorktreeStateOptions explicitly")
	}
	if !strings.Contains(src, "git.WorktreeStateOptions{}") {
		t.Fatal("non-dialog forkSessionCmd must pass zero git.WorktreeStateOptions")
	}
}
```

- [x] **Step 2: Run tests to verify they fail before implementation**

Run:

```bash
GOTOOLCHAIN=go1.25.11 go test ./internal/ui/... -run "TestForkDialogSubmitCapturesWithStateBeforeHide|TestForkSessionCmdWithOptions_AcceptsForkState" -count=1
```

Expected before implementation: FAIL because `forkSessionCmdWithOptions` has no `forkState` parameter and submit reads sandbox after `Hide()`.

- [x] **Step 3: Capture values before `Hide()` and pass state explicitly**

In `internal/ui/home.go`, update the submit path:

```go
parentID := h.forkDialog.GetParentSessionID()
parentPath := h.forkDialog.GetParentProjectPath()
sandboxEnabled := h.forkDialog.IsSandboxEnabled()
forkState := git.WorktreeStateOptions{
	WithState:   h.forkDialog.IsWithStateEnabled(),
	WithIgnored: h.forkDialog.IsWithStateAndGitignoredEnabled(),
}
h.forkDialog.Hide()
return h, h.forkSessionCmdWithOptions(source, title, groupPath, opts, sandboxEnabled, forkState, parentID, parentPath)
```

Update the wrapper call:

```go
func (h *Home) forkSessionCmd(source *session.Instance, title, groupPath, parentSessionID, parentProjectPath string) tea.Cmd {
	return h.forkSessionCmdWithOptions(source, title, groupPath, nil, false, git.WorktreeStateOptions{}, parentSessionID, parentProjectPath)
}
```

Update the signature:

```go
func (h *Home) forkSessionCmdWithOptions(
	source *session.Instance,
	title, groupPath string,
	opts *session.ClaudeOptions,
	sandboxEnabled bool,
	forkState git.WorktreeStateOptions,
	parentSessionID, parentProjectPath string,
) tea.Cmd {
```

At the top of the function, normalize ignored implies state:

```go
if forkState.WithIgnored {
	forkState.WithState = true
}
```

- [x] **Step 4: Run tests to verify they pass**

Run:

```bash
GOTOOLCHAIN=go1.25.11 go test ./internal/ui/... -run "TestForkDialogSubmitCapturesWithStateBeforeHide|TestForkSessionCmdWithOptions_AcceptsForkState" -count=1
```

Expected: PASS.

- [x] **Step 5: Commit**

```bash
git add internal/ui/home.go internal/ui/fork_state_submit_test.go
git commit -m "fix(tui): preserve fork-with-state selection through submit"
```

---

## Task 3: Implement TUI With-State Submit Helper With CLI Safeguards

**Findings covered:**
- Medium: B4 says “mirror #1263” but omits CLI safeguards from the task checklist.
- Medium: B4 test coverage is too vague for the risky submit path.

**Files:**
- Modify: `internal/ui/home.go`
- Modify: `internal/ui/fork_state_submit_test.go`

- [x] **Step 1: Add testable helper dependency type**

In `internal/ui/home.go`, near `forkSessionCmdWithOptions`, add:

```go
type forkWithStateWorktreeDeps struct {
	statPath                  func(string) (os.FileInfo, error)
	mkdirAll                  func(string, os.FileMode) error
	validateDestination       func(string, string) error
	detectInProgressOperation func(string) (string, error)
	hasSubmodules             func(string) bool
	headCommit                func(string) (string, error)
	createAtStartPoint        func(string, string, string, string) (bool, error)
	materialize               func(string, string, bool) error
	processInclude            func(string, string, io.Writer) error
	runSetup                  func(string, string, io.Writer, io.Writer, time.Duration) error
	removeWorktree            func(string, string, bool) error
	deleteBranch              func(string, string, bool) error
}

func defaultForkWithStateWorktreeDeps() forkWithStateWorktreeDeps {
	return forkWithStateWorktreeDeps{
		statPath:                  os.Stat,
		mkdirAll:                  os.MkdirAll,
		validateDestination:       git.ValidateForkWithStateDestination,
		detectInProgressOperation: git.DetectInProgressOperation,
		hasSubmodules:             git.HasSubmodules,
		headCommit:                git.HeadCommit,
		createAtStartPoint:        git.CreateWorktreeAtStartPoint,
		materialize:               git.MaterializeWipFromParent,
		processInclude:            git.ProcessWorktreeInclude,
		runSetup:                  git.RunWorktreeSetupAfterCreate,
		removeWorktree:            git.RemoveWorktree,
		deleteBranch:              git.DeleteBranch,
	}
}
```

`home.go` already imports `io`, `os`, `time`, and `git`, so this type should not require new imports.

> **Note (intentional divergence from #1263):** cleanup routes through `git.RemoveWorktree`/`git.DeleteBranch` (injected as deps) rather than #1263's raw `exec.Command("git", ...)`. This is deliberate — it keeps the helper unit-testable via dependency injection and retains `RemoveWorktree`'s `#1200` linked-worktree guard (the freshly-created worktree is always linked, so the guard passes). The error wording (`new worktree cleaned up` / `manual cleanup required: ...`) still matches #1263.

- [x] **Step 2: Add helper tests for safeguards and cleanup**

Append to `internal/ui/fork_state_submit_test.go`:

```go
func TestForkWithStateWorktree_RefusesExistingPathBeforeCreate(t *testing.T) {
	var created bool
	deps := defaultForkWithStateWorktreeDeps()
	deps.validateDestination = func(string, string) error { return nil }
	deps.statPath = func(string) (os.FileInfo, error) { return fakeFileInfo{}, nil }
	deps.createAtStartPoint = func(string, string, string, string) (bool, error) {
		created = true
		return true, nil
	}

	err := forkWithStateWorktree("parent", "repo", "existing-path", "fork/state", git.WorktreeStateOptions{WithState: true}, deps)
	if err == nil || !strings.Contains(err.Error(), "worktree path already exists") {
		t.Fatalf("error = %v, want existing-path refusal", err)
	}
	if created {
		t.Fatal("CreateWorktreeAtStartPoint must not run when destination path already exists")
	}
}

func TestForkWithStateWorktree_RefusesMidOperationBeforeCreate(t *testing.T) {
	var created bool
	deps := defaultForkWithStateWorktreeDeps()
	deps.statPath = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
	deps.mkdirAll = func(string, os.FileMode) error { return nil }
	deps.validateDestination = func(string, string) error { return nil }
	deps.detectInProgressOperation = func(string) (string, error) { return "rebase", nil }
	deps.createAtStartPoint = func(string, string, string, string) (bool, error) {
		created = true
		return true, nil
	}

	err := forkWithStateWorktree("parent", "repo", "fork-path", "fork/state", git.WorktreeStateOptions{WithState: true}, deps)
	if err == nil || !strings.Contains(err.Error(), "git rebase --abort") {
		t.Fatalf("error = %v, want actionable rebase abort hint", err)
	}
	if created {
		t.Fatal("CreateWorktreeAtStartPoint must not run during parent mid-operation")
	}
}

func TestForkWithStateWorktree_CleansUpMaterializeFailure(t *testing.T) {
	var removed bool
	var deleted bool
	deps := defaultForkWithStateWorktreeDeps()
	deps.statPath = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
	deps.mkdirAll = func(string, os.FileMode) error { return nil }
	deps.validateDestination = func(string, string) error { return nil }
	deps.detectInProgressOperation = func(string) (string, error) { return "", nil }
	deps.hasSubmodules = func(string) bool { return false }
	deps.headCommit = func(string) (string, error) { return "abc123", nil }
	deps.createAtStartPoint = func(string, string, string, string) (bool, error) { return true, nil }
	deps.materialize = func(string, string, bool) error { return errors.New("copy failed") }
	deps.removeWorktree = func(string, string, bool) error { removed = true; return nil }
	deps.deleteBranch = func(string, string, bool) error { deleted = true; return nil }

	err := forkWithStateWorktree("parent", "repo", "fork-path", "fork/state", git.WorktreeStateOptions{WithState: true}, deps)
	if err == nil || !strings.Contains(err.Error(), "new worktree cleaned up") {
		t.Fatalf("error = %v, want cleaned-up materialize failure", err)
	}
	if !removed || !deleted {
		t.Fatalf("cleanup removed=%v deleted=%v, want both true", removed, deleted)
	}
}

func TestForkWithStateWorktree_ReportsManualCleanupWhenCleanupFails(t *testing.T) {
	deps := defaultForkWithStateWorktreeDeps()
	deps.statPath = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
	deps.mkdirAll = func(string, os.FileMode) error { return nil }
	deps.validateDestination = func(string, string) error { return nil }
	deps.detectInProgressOperation = func(string) (string, error) { return "", nil }
	deps.hasSubmodules = func(string) bool { return false }
	deps.headCommit = func(string) (string, error) { return "abc123", nil }
	deps.createAtStartPoint = func(string, string, string, string) (bool, error) { return true, nil }
	deps.materialize = func(string, string, bool) error { return errors.New("copy failed") }
	deps.removeWorktree = func(string, string, bool) error { return errors.New("remove failed") }
	deps.deleteBranch = func(string, string, bool) error { return errors.New("delete failed") }

	err := forkWithStateWorktree("parent", "repo", "fork-path", "fork/state", git.WorktreeStateOptions{WithState: true}, deps)
	if err == nil || !strings.Contains(err.Error(), "manual cleanup required") {
		t.Fatalf("error = %v, want manual cleanup hint", err)
	}
	if !strings.Contains(err.Error(), "git -C repo branch -D fork/state") {
		t.Fatalf("error = %v, want branch deletion hint", err)
	}
}

type fakeFileInfo struct{}

func (fakeFileInfo) Name() string       { return "existing-path" }
func (fakeFileInfo) Size() int64        { return 0 }
func (fakeFileInfo) Mode() os.FileMode  { return 0 }
func (fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (fakeFileInfo) IsDir() bool        { return true }
func (fakeFileInfo) Sys() any           { return nil }
```

Add these imports to `internal/ui/fork_state_submit_test.go`:

```go
import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/git"
)
```

- [x] **Step 3: Run tests to verify they fail before helper implementation**

Run:

```bash
GOTOOLCHAIN=go1.25.11 go test ./internal/ui/... -run "TestForkWithStateWorktree_" -count=1
```

Expected before implementation: FAIL with `undefined: forkWithStateWorktree`.

- [x] **Step 4: Implement `forkWithStateWorktree`**

Add to `internal/ui/home.go`:

```go
func forkWithStateWorktree(parentPath, repoRoot, worktreePath, branch string, state git.WorktreeStateOptions, deps forkWithStateWorktreeDeps) error {
	if state.WithIgnored {
		state.WithState = true
	}
	if !state.WithState {
		return errors.New("forkWithStateWorktree called without WithState")
	}
	// Destination collision is the more actionable refusal, so check it before
	// the local path-existence guard (mirrors #1263's CLI precedence).
	if err := deps.validateDestination(repoRoot, branch); err != nil {
		var collErr *git.DestinationCollisionError
		if errors.As(err, &collErr) {
			switch collErr.Kind {
			case git.CollisionWorktreeExists:
				return fmt.Errorf("branch %q already has a worktree at %s; choose a new destination branch for --with-state", collErr.Branch, collErr.Path)
			case git.CollisionBranchExists:
				return fmt.Errorf("branch %q already exists; choose a new destination branch for --with-state", collErr.Branch)
			}
		}
		return fmt.Errorf("failed to validate destination: %w", err)
	}
	if _, statErr := deps.statPath(worktreePath); statErr == nil {
		return fmt.Errorf("worktree path already exists: %s", worktreePath)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("failed to stat worktree path: %w", statErr)
	}
	if kind, detectErr := deps.detectInProgressOperation(parentPath); detectErr == nil && kind != "" {
		abortCmd := map[string]string{
			"rebase":      "git rebase --abort",
			"merge":       "git merge --abort",
			"cherry-pick": "git cherry-pick --abort",
			"revert":      "git revert --abort",
			"bisect":      "git bisect reset",
		}[kind]
		return fmt.Errorf("parent session is mid-%s; finish or abort the %s before forking with state (cd %s && %s)", kind, kind, parentPath, abortCmd)
	}
	if err := deps.mkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}
	if deps.hasSubmodules(parentPath) {
		uiLog.Warn("fork_with_state_submodules_detected", slog.String("parent", parentPath))
	}
	parentHead, err := deps.headCommit(parentPath)
	if err != nil {
		return fmt.Errorf("failed to resolve parent session HEAD: %w", err)
	}
	createdBranch, err := deps.createAtStartPoint(repoRoot, worktreePath, branch, parentHead)
	if err != nil {
		return fmt.Errorf("worktree creation failed: %w", err)
	}
	if err := deps.materialize(parentPath, worktreePath, state.WithIgnored); err != nil {
		var cleanupErrs []string
		if rmErr := deps.removeWorktree(repoRoot, worktreePath, true); rmErr != nil {
			cleanupErrs = append(cleanupErrs, fmt.Sprintf("worktree remove failed: %v", rmErr))
		}
		if createdBranch {
			if brErr := deps.deleteBranch(repoRoot, branch, true); brErr != nil {
				cleanupErrs = append(cleanupErrs, fmt.Sprintf("branch delete failed: %v", brErr))
			}
		}
		if len(cleanupErrs) == 0 {
			return fmt.Errorf("failed to materialize parent state: %w; new worktree cleaned up", err)
		}
		branchHint := ""
		if createdBranch {
			branchHint = fmt.Sprintf(" && git -C %s branch -D %s", repoRoot, branch)
		}
		return fmt.Errorf("failed to materialize parent state: %w; cleanup also failed (%s); manual cleanup required: rm -rf %s%s", err, strings.Join(cleanupErrs, "; "), worktreePath, branchHint)
	}
	if err := deps.processInclude(repoRoot, worktreePath, io.Discard); err != nil {
		uiLog.Warn("fork_with_state_worktreeinclude_failed", slog.String("path", worktreePath), slog.String("err", err.Error()))
	}
	if err := deps.runSetup(repoRoot, worktreePath, io.Discard, io.Discard, session.GetWorktreeSettings().SetupTimeout()); err != nil {
		// Non-fatal: the worktree and parent state are already created. Mirror
		// #1263's CLI, which warns on a failed setup script rather than failing
		// the whole fork.
		uiLog.Warn("fork_with_state_setup_failed", slog.String("path", worktreePath), slog.String("err", err.Error()))
	}
	return nil
}
```

Ensure `home.go` imports `errors` if not already imported.

- [x] **Step 5: Wire helper into `forkSessionCmdWithOptions`**

Inside the worktree block in `forkSessionCmdWithOptions`, after backend detection:

```go
if forkState.WithState {
	if backend.Type() != vcs.TypeGit {
		return sessionForkedMsg{err: fmt.Errorf("--with-state is only supported for git repositories"), sourceID: sourceID}
	}
	if err := forkWithStateWorktree(
		source.ProjectPath,
		opts.WorktreeRepoRoot,
		opts.WorktreePath,
		opts.WorktreeBranch,
		forkState,
		defaultForkWithStateWorktreeDeps(),
	); err != nil {
		return sessionForkedMsg{err: err, sourceID: sourceID}
	}
} else if existingPath, err := backend.GetWorktreeForBranch(opts.WorktreeBranch); err == nil && existingPath != "" {
	uiLog.Info("worktree_reuse", slog.String("branch", opts.WorktreeBranch), slog.String("path", existingPath))
	opts.WorktreePath = existingPath
} else {
	if err := os.MkdirAll(filepath.Dir(opts.WorktreePath), 0o755); err != nil {
		return sessionForkedMsg{err: fmt.Errorf("failed to create directory: %w", err), sourceID: sourceID}
	}
	if err := createWorktreeWithSetupAndLog(backend, opts.WorktreePath, opts.WorktreeBranch); err != nil {
		return sessionForkedMsg{err: fmt.Errorf("worktree creation failed: %w", err), sourceID: sourceID}
	}
}
```

This explicitly prevents backend reuse under with-state and keeps non-state behavior unchanged.

- [x] **Step 6: Run tests to verify they pass**

Run:

```bash
GOTOOLCHAIN=go1.25.11 go test ./internal/ui/... -run "TestForkWithStateWorktree_|TestForkDialogSubmitCapturesWithStateBeforeHide|TestForkSessionCmdWithOptions_AcceptsForkState" -count=1
```

Expected: PASS.

- [x] **Step 7: Commit**

```bash
git add internal/ui/home.go internal/ui/fork_state_submit_test.go
git commit -m "fix(tui): mirror CLI safeguards for fork with state"
```

---

## Task 4: Parent-HEAD Anchoring and Non-Git Rejection Coverage

**Finding covered:** Medium: B4 test coverage is too vague for the risky submit path.

**Files:**
- Modify: `internal/ui/fork_state_submit_test.go`

- [x] **Step 1: Add parent-HEAD anchoring test with real git**

Append to `internal/ui/fork_state_submit_test.go`:

```go
func TestForkWithStateWorktree_UsesParentHead(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	root := t.TempDir()
	base := filepath.Join(root, "base")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	gitMustUI(t, base, "init")
	gitMustUI(t, base, "config", "user.email", "test@example.com")
	gitMustUI(t, base, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(base, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitMustUI(t, base, "add", ".")
	gitMustUI(t, base, "commit", "-m", "base")

	parent := filepath.Join(root, "parent")
	gitMustUI(t, base, "worktree", "add", "-b", "parent-branch", parent)
	if err := os.WriteFile(filepath.Join(parent, "README.md"), []byte("parent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitMustUI(t, parent, "commit", "-am", "parent change")

	baseHead := strings.TrimSpace(gitOutUI(t, base, "rev-parse", "HEAD"))
	parentHead := strings.TrimSpace(gitOutUI(t, parent, "rev-parse", "HEAD"))
	if baseHead == parentHead {
		t.Fatal("setup invalid: base and parent HEAD must differ")
	}

	forkPath := filepath.Join(root, "fork")
	err := forkWithStateWorktree(parent, base, forkPath, "fork/from-parent", git.WorktreeStateOptions{WithState: true}, defaultForkWithStateWorktreeDeps())
	if err != nil {
		t.Fatalf("forkWithStateWorktree: %v", err)
	}
	forkHead := strings.TrimSpace(gitOutUI(t, forkPath, "rev-parse", "HEAD"))
	if forkHead != parentHead {
		t.Fatalf("fork HEAD = %s, want parent HEAD %s (base HEAD %s)", forkHead, parentHead, baseHead)
	}
}

func gitMustUI(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git -C %s %s failed: %v\n%s", dir, strings.Join(args, " "), err, out)
	}
}

func gitOutUI(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git -C %s %s failed: %v\n%s", dir, strings.Join(args, " "), err, out)
	}
	return string(out)
}
```

Add `os/exec` and `path/filepath` to the imports.

- [x] **Step 2: Add structural non-git rejection test**

Append:

```go
func TestForkSessionCmdWithOptions_WithStateRejectsNonGitBeforeGitDirectCalls(t *testing.T) {
	srcBytes, err := os.ReadFile("home.go")
	if err != nil {
		t.Fatalf("read home.go: %v", err)
	}
	src := string(srcBytes)
	guard := strings.Index(src, "if forkState.WithState {")
	reject := strings.Index(src, `backend.Type() != vcs.TypeGit`)
	validate := strings.Index(src, "forkWithStateWorktree(")
	if guard < 0 || reject < 0 || validate < 0 {
		t.Fatalf("missing with-state guard/reject/helper call: guard=%d reject=%d helper=%d", guard, reject, validate)
	}
	if reject > validate {
		t.Fatalf("non-git rejection must happen before git-direct helper call; reject=%d helper=%d", reject, validate)
	}
}
```

- [x] **Step 3: Run tests**

Run:

```bash
GOTOOLCHAIN=go1.25.11 go test ./internal/ui/... -run "TestForkWithStateWorktree_UsesParentHead|TestForkSessionCmdWithOptions_WithStateRejectsNonGitBeforeGitDirectCalls" -count=1
```

Expected: PASS.

- [x] **Step 4: Commit**

```bash
git add internal/ui/fork_state_submit_test.go
git commit -m "test(tui): cover fork-with-state submit parent head and non-git guard"
```

---

## Task 5: Reconcile Existing PR-B Plan and Spec

**Findings covered:** Medium: B4 checklist and mandate coverage are underspecified.

**Files:**
- Modify: `docs/superpowers/plans/2026-05-18-fork-with-state-followup.md`
- Modify: `docs/superpowers/specs/2026-05-18-fork-with-state-followup-design.md`

- [x] **Step 1: Update B4 checklist**

In `docs/superpowers/plans/2026-05-18-fork-with-state-followup.md`, replace B4's numbered item 5 (the "With-state path (git-only, git-direct)" sequence) with the complete TUI with-state sequence:

```md
5. **With-state path (git-only, git-direct):**
   - Refuse if `opts.WorktreePath` already exists before creating anything.
   - `git.ValidateForkWithStateDestination(repoRoot, branch)` (collision gate; with-state never reuses existing worktrees).
   - `git.DetectInProgressOperation(source.ProjectPath)` before worktree creation; return actionable abort guidance on rebase/merge/cherry-pick/revert/bisect.
   - `git.HasSubmodules(source.ProjectPath)` warning via `uiLog.Warn` (TUI has no stderr surface here).
   - `git.HeadCommit(source.ProjectPath)` (parent-HEAD anchor).
   - `git.CreateWorktreeAtStartPoint(repoRoot, worktreePath, branch, parentHead)`.
   - `git.MaterializeWipFromParent(source.ProjectPath, worktreePath, withIgnored)` with cleanup-on-error: force-remove the worktree and, if `createdBranch`, delete the branch; if cleanup fails, return a manual cleanup hint.
   - `git.ProcessWorktreeInclude(...)`.
   - `git.RunWorktreeSetupAfterCreate(...)`.
```

- [x] **Step 2: Update B4 test mandate**

In the mandate section, update the UI test regex:

```bash
go test ./internal/ui/... -run "ForkDialog_(WithState|ToggleWith|ToggleGitignored|ToggleWorktreeOff|Focus_|View_|Space_|Y_Toggles|I_Toggles|I_Typeable|WorktreeControlsVisible)|ForkWithStateWorktree_|ForkSessionCmdWithOptions_(AcceptsForkState|WithStateRejectsNonGit)|ForkDialogSubmitCapturesWithStateBeforeHide" -race -count=1
```

In the explanatory text, list the coverage:

```md
The TUI submit mandate covers bare-root visibility, state handoff before `Hide()`, non-git rejection before git-direct calls, destination/path collision refusal, no reuse under with-state, parent-HEAD anchoring, mid-op actionable refusal, materialize cleanup, manual cleanup hints, and the existing dialog state-machine/eval tests.
```

- [x] **Step 3: Update spec mandatory coverage**

In `docs/superpowers/specs/2026-05-18-fork-with-state-followup-design.md`, update the UI command to include the new tests:

```bash
go test ./internal/ui/... -run "ForkDialog_(WithState|ToggleWith|ToggleGitignored|ToggleWorktreeOff|Focus_|View_|Space_|Y_Toggles|I_Toggles|I_Typeable|WorktreeControlsVisible)|ForkWithStateWorktree_|ForkSessionCmdWithOptions_(AcceptsForkState|WithStateRejectsNonGit)|ForkDialogSubmitCapturesWithStateBeforeHide" -race -count=1
```

Add a changelog entry:

```md
- 2026-06-04: FUS-007 — Added PR-B audit-finding plan coverage for preserving fork-with-state dialog values through submit, bare-repo project-root worktree visibility, full CLI-safeguard mirroring in TUI with-state submit, and concrete TUI submit-path tests. (FUS-006 is the four-UI-decisions fold in the plan/spec.)
```

- [x] **Step 4: Run doc checks**

Run:

```bash
rg -n "feature/fork-with-state-tui|go1\\.25\\.10|CreateWorktreeWithStateAndSetup with collision check" docs/superpowers/specs/2026-05-18-fork-with-state-followup-design.md docs/superpowers/plans/2026-05-18-fork-with-state-followup.md
git diff --check -- docs/superpowers/specs/2026-05-18-fork-with-state-followup-design.md docs/superpowers/plans/2026-05-18-fork-with-state-followup.md
```

Expected:
- `rg` returns no matches.
- `git diff --check` exits 0.

- [x] **Step 5: Commit**

```bash
git add docs/superpowers/plans/2026-05-18-fork-with-state-followup.md docs/superpowers/specs/2026-05-18-fork-with-state-followup-design.md
git commit -m "docs: tighten PR-B fork-with-state submit plan"
```

---

## Final Verification

Run after all tasks:

```bash
GOTOOLCHAIN=go1.25.11 go test ./internal/ui/... -run "ForkDialog_(WithState|ToggleWith|ToggleGitignored|ToggleWorktreeOff|Focus_|View_|Space_|Y_Toggles|I_Toggles|I_Typeable|WorktreeControlsVisible)|ForkWithStateWorktree_|ForkSessionCmdWithOptions_(AcceptsForkState|WithStateRejectsNonGit)|ForkDialogSubmitCapturesWithStateBeforeHide" -race -count=1
GOTOOLCHAIN=go1.25.11 go test ./internal/git/... -run "Materialize|RefuseUnsafeParentState|ValidateForkWithStateDestination|CreateWorktreeAtStartPoint|HeadCommit|ForkWithState|Issue1029" -race -count=1
git diff --check
```

Expected:
- UI focused suite passes and matches at least one test in every regex branch.
- Git fork-with-state suite still passes.
- `git diff --check` exits 0.

## Self-Review Checklist

- Finding 1, state handoff: Task 2 captures `git.WorktreeStateOptions` and sandbox before `Hide()` and passes state explicitly.
- Finding 2, bare-root visibility: Task 1 uses `git.IsGitRepoOrBareProjectRoot` and proves worktree controls can be toggled for `.bare` project roots.
- Finding 3, missing safeguards: Task 3 implements path-exists refusal, no with-state reuse, destination validation, mid-op refusal, submodule warning, parent-HEAD anchoring, cleanup, and manual cleanup hints.
- Finding 4, vague tests: Tasks 2-4 add concrete tests for handoff, non-git rejection, collision/path refusal, no-reuse by validator path, parent-HEAD anchoring, mid-op refusal, and cleanup.
