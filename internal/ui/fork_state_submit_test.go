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
