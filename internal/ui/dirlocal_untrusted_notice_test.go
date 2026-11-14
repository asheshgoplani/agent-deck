package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/git"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/vcsbackend"
)

// A directory-local config skipped as untrusted (LOC-11) changes which
// [worktree] settings apply. The TUI has no permission-warning writer, so the
// skip must reach the footer warning of the worktree it affected.
// untrustedDirLocalRepo makes a git repo under a fresh HOME whose
// .agent-deck/config.toml is world-writable, so it is skipped as untrusted.
func untrustedDirLocalRepo(t *testing.T) (home, repo string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)

	repo = filepath.Join(home, "projects", "repo")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	makeGitRepo(t, repo)
	cfgDir := filepath.Join(repo, ".agent-deck")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(cfgDir, "config.toml")
	if err := os.WriteFile(cfg, []byte("[worktree]\nsparse_checkout = \"inherit\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(cfg, 0o666); err != nil { // world-writable: not trusted
		t.Fatal(err)
	}
	return home, repo
}

func TestCreateWorktree_UntrustedDirLocalConfigShownInFooter(t *testing.T) {
	home, repo := untrustedDirLocalRepo(t)
	backend, err := vcsbackend.Detect(repo)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	wtPath := filepath.Join(home, "wt", "feature-x")
	setupErr, err := createWorktreeWithSetupAndLog(backend, wtPath, "feature-x", repo)
	if err != nil {
		t.Fatalf("createWorktreeWithSetupAndLog: %v", err)
	}
	if setupErr == nil {
		t.Fatal("no footer warning for a skipped untrusted directory-local config")
	}
	got := formatSetupWarning(setupErr)
	if !strings.Contains(got, "ignoring directory-local config") || !strings.Contains(got, "world-writable") {
		t.Fatalf("footer warning = %q, want it to say the dir-local config was ignored and why", got)
	}
	if strings.Contains(got, "setup script failed") {
		t.Fatalf("footer warning = %q blames a setup script that did not fail", got)
	}
}

func TestFormatSetupWarning_DirLocalNoticeAfterSetupFailure(t *testing.T) {
	err := &dirLocalNoticeError{notice: "ignoring directory-local config /x: world-writable", setup: os.ErrPermission}
	got := formatSetupWarning(err)
	if !strings.HasPrefix(got, "worktree setup script failed: ") || !strings.Contains(got, "ignoring directory-local config /x") {
		t.Fatalf("formatSetupWarning = %q, want the setup failure and the notice", got)
	}
}

// Fork --with-state (git) resolves its settings through
// defaultForkWithStateWorktreeDeps; the skip must reach its setup result.
func TestForkWithStateWorktree_UntrustedDirLocalConfigShownInFooter(t *testing.T) {
	_, repo := untrustedDirLocalRepo(t)
	deps := defaultForkWithStateWorktreeDeps(repo)
	if !strings.Contains(deps.dirLocalNotice, "ignoring directory-local config") {
		t.Fatalf("deps.dirLocalNotice = %q, want the skip notice", deps.dirLocalNotice)
	}

	stub := forkWithStateSetupDeps()
	stub.dirLocalNotice = deps.dirLocalNotice
	setupErr, err := forkWithStateWorktree("parent", "repo", "fork-path", "fork/state", git.WorktreeStateOptions{WithState: true}, stub)
	if err != nil {
		t.Fatalf("fatal err = %v", err)
	}
	if setupErr == nil {
		t.Fatal("no footer warning for a skipped untrusted directory-local config on fork --with-state")
	}
	if got := formatSetupWarning(setupErr); !strings.Contains(got, "world-writable") || strings.Contains(got, "setup script failed") {
		t.Fatalf("footer warning = %q", got)
	}
}
