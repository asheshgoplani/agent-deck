package ui

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/git"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestForkDialogSubmitCapturesStateBeforeHide(t *testing.T) {
	srcBytes, err := os.ReadFile("home.go")
	if err != nil {
		t.Fatalf("read home.go: %v", err)
	}
	src := string(srcBytes)

	// The dialog submit must read its toggle state (passed as args to
	// buildForkCmd) and dispatch the fork BEFORE Hide() resets the dialog.
	build := strings.Index(src, "result := h.buildForkCmd(")
	if build < 0 {
		t.Fatal("submit handler must dispatch through h.buildForkCmd")
	}
	after := src[build:]
	if !strings.Contains(after, "h.forkDialog.IsWithStateEnabled()") {
		t.Fatal("submit handler must pass dialog with-state into buildForkCmd")
	}
	if !strings.Contains(after, "h.forkDialog.IsSandboxEnabled()") {
		t.Fatal("submit handler must pass dialog sandbox into buildForkCmd")
	}
	if !strings.Contains(after, "h.forkDialog.Hide()") {
		t.Fatal("submit handler must Hide() after building the fork command")
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
	if !strings.Contains(err.Error(), `git -C "repo" branch -D "fork/state"`) {
		t.Fatalf("error = %v, want quoted branch deletion hint", err)
	}
}

type fakeFileInfo struct{}

func (fakeFileInfo) Name() string       { return "existing-path" }
func (fakeFileInfo) Size() int64        { return 0 }
func (fakeFileInfo) Mode() os.FileMode  { return 0 }
func (fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (fakeFileInfo) IsDir() bool        { return true }
func (fakeFileInfo) Sys() any           { return nil }

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

func TestForkWithStateWorktree_FailsClosedWhenDetectErrors(t *testing.T) {
	var created bool
	deps := defaultForkWithStateWorktreeDeps()
	deps.statPath = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
	deps.mkdirAll = func(string, os.FileMode) error { return nil }
	deps.validateDestination = func(string, string) error { return nil }
	deps.detectInProgressOperation = func(string) (string, error) {
		return "", errors.New("probe boom")
	}
	deps.createAtStartPoint = func(string, string, string, string) (bool, error) {
		created = true
		return true, nil
	}

	err := forkWithStateWorktree("parent", "repo", "fork-path", "fork/state", git.WorktreeStateOptions{WithState: true}, deps)
	if err == nil || !strings.Contains(err.Error(), "failed to inspect parent session state") {
		t.Fatalf("error = %v, want fail-closed inspect error", err)
	}
	if created {
		t.Fatal("CreateWorktreeAtStartPoint must not run when the mid-op probe errors")
	}
}

// --- Behavioral tests for completeFork (review finding G1) ---
//
// These exercise the three post-helper rollback paths via the forkInstanceDeps
// DI seam, replacing the brittle source-scanning rollback test. Fakes use a
// source whose Tool is "" and sandboxEnabled=false / parentSessionID="" so no
// real *session.Instance behavior runs.

func newForkStateOpts() *session.ClaudeOptions {
	return &session.ClaudeOptions{
		WorktreeRepoRoot: "/repo/root",
		WorktreePath:     "/repo/root/.worktrees/feature",
		WorktreeBranch:   "feature",
	}
}

type rollbackRecorder struct {
	calls    int
	repoRoot string
	wtPath   string
	branch   string
}

func (r *rollbackRecorder) fn(repoRoot, worktreePath, branch string) {
	r.calls++
	r.repoRoot = repoRoot
	r.wtPath = worktreePath
	r.branch = branch
}

func TestCompleteFork_RollsBackOnInstanceCreateFailure(t *testing.T) {
	source := &session.Instance{}
	opts := newForkStateOpts()
	rec := &rollbackRecorder{}
	createErr := errors.New("boom")

	deps := forkInstanceDeps{
		createInstance: func(_ *session.Instance, _, _ string, _ *session.ClaudeOptions) (*session.Instance, error) {
			return nil, createErr
		},
		createMultiRepoDir: func(_, _ *session.Instance) error { return nil },
		startInstance:      func(_ *session.Instance) error { return nil },
		rollback:           rec.fn,
	}

	inst, err := completeFork(source, "title", "group", opts, false, "", "", true, deps)
	if inst != nil {
		t.Fatalf("expected nil instance on create failure, got %v", inst)
	}
	if err == nil || !strings.Contains(err.Error(), "cannot create forked instance") {
		t.Fatalf("expected wrapped 'cannot create forked instance' error, got %v", err)
	}
	if rec.calls != 1 {
		t.Fatalf("expected exactly one rollback, got %d", rec.calls)
	}
	if rec.repoRoot != opts.WorktreeRepoRoot || rec.wtPath != opts.WorktreePath || rec.branch != opts.WorktreeBranch {
		t.Fatalf("rollback args mismatch: got (%q,%q,%q)", rec.repoRoot, rec.wtPath, rec.branch)
	}
}

func TestCompleteFork_RollsBackOnMultiRepoDirFailure(t *testing.T) {
	source := &session.Instance{}
	opts := newForkStateOpts()
	rec := &rollbackRecorder{}
	fake := &session.Instance{}
	mrErr := errors.New("failed to create multi-repo dir: disk full")

	deps := forkInstanceDeps{
		createInstance: func(_ *session.Instance, _, _ string, _ *session.ClaudeOptions) (*session.Instance, error) {
			return fake, nil
		},
		createMultiRepoDir: func(_, _ *session.Instance) error { return mrErr },
		startInstance:      func(_ *session.Instance) error { return nil },
		rollback:           rec.fn,
	}

	inst, err := completeFork(source, "title", "group", opts, false, "", "", true, deps)
	if inst != nil {
		t.Fatalf("expected nil instance on multi-repo-dir failure, got %v", inst)
	}
	if err != mrErr {
		t.Fatalf("expected bare multi-repo-dir error, got %v", err)
	}
	if rec.calls != 1 {
		t.Fatalf("expected exactly one rollback, got %d", rec.calls)
	}
	if rec.repoRoot != opts.WorktreeRepoRoot || rec.wtPath != opts.WorktreePath || rec.branch != opts.WorktreeBranch {
		t.Fatalf("rollback args mismatch: got (%q,%q,%q)", rec.repoRoot, rec.wtPath, rec.branch)
	}
}

func TestCompleteFork_RollsBackOnStartFailure(t *testing.T) {
	source := &session.Instance{}
	opts := newForkStateOpts()
	rec := &rollbackRecorder{}
	fake := &session.Instance{}
	startErr := errors.New("start failed")

	deps := forkInstanceDeps{
		createInstance: func(_ *session.Instance, _, _ string, _ *session.ClaudeOptions) (*session.Instance, error) {
			return fake, nil
		},
		createMultiRepoDir: func(_, _ *session.Instance) error { return nil },
		startInstance:      func(_ *session.Instance) error { return startErr },
		rollback:           rec.fn,
	}

	inst, err := completeFork(source, "title", "group", opts, false, "", "", true, deps)
	if inst != nil {
		t.Fatalf("expected nil instance on start failure, got %v", inst)
	}
	if err != startErr {
		t.Fatalf("expected bare start error, got %v", err)
	}
	if rec.calls != 1 {
		t.Fatalf("expected exactly one rollback, got %d", rec.calls)
	}
	if rec.repoRoot != opts.WorktreeRepoRoot || rec.wtPath != opts.WorktreePath || rec.branch != opts.WorktreeBranch {
		t.Fatalf("rollback args mismatch: got (%q,%q,%q)", rec.repoRoot, rec.wtPath, rec.branch)
	}
}

func TestCompleteFork_NoRollbackOnSuccess(t *testing.T) {
	source := &session.Instance{}
	opts := newForkStateOpts()
	rec := &rollbackRecorder{}
	fake := &session.Instance{}

	deps := forkInstanceDeps{
		createInstance: func(_ *session.Instance, _, _ string, _ *session.ClaudeOptions) (*session.Instance, error) {
			return fake, nil
		},
		createMultiRepoDir: func(_, _ *session.Instance) error { return nil },
		startInstance:      func(_ *session.Instance) error { return nil },
		rollback:           rec.fn,
	}

	inst, err := completeFork(source, "title", "group", opts, false, "", "", true, deps)
	if err != nil {
		t.Fatalf("expected no error on success, got %v", err)
	}
	if inst != fake {
		t.Fatalf("expected returned instance to be the createInstance fake")
	}
	if rec.calls != 0 {
		t.Fatalf("expected no rollback on success, got %d", rec.calls)
	}
}

func TestCompleteFork_NoRollbackWhenWorktreeNotCreated(t *testing.T) {
	source := &session.Instance{}
	rec := &rollbackRecorder{}
	createErr := errors.New("boom")

	deps := forkInstanceDeps{
		createInstance: func(_ *session.Instance, _, _ string, _ *session.ClaudeOptions) (*session.Instance, error) {
			return nil, createErr
		},
		createMultiRepoDir: func(_, _ *session.Instance) error { return nil },
		startInstance:      func(_ *session.Instance) error { return nil },
		rollback:           rec.fn,
	}

	// withStateWorktreeCreated=false and a nil opts: the rollback gate must not
	// fire, so opts is never dereferenced.
	inst, err := completeFork(source, "title", "group", nil, false, "", "", false, deps)
	if inst != nil {
		t.Fatalf("expected nil instance on create failure, got %v", inst)
	}
	if err == nil || !strings.Contains(err.Error(), "cannot create forked instance") {
		t.Fatalf("expected wrapped 'cannot create forked instance' error, got %v", err)
	}
	if rec.calls != 0 {
		t.Fatalf("expected no rollback when worktree not created, got %d", rec.calls)
	}
}
