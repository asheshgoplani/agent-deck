package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// setupDefaultWorktreeTest creates an isolated home directory, sets XDG env vars,
// clears the user-config cache, and returns (home, profile).
func setupDefaultWorktreeTest(t *testing.T) (home, profile string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)
	return home, "default_worktree_test"
}

// writeWorktreeConfig writes a config.toml with [worktree] default_enabled = true/false.
func writeWorktreeConfig(t *testing.T, home string, enabled bool) {
	t.Helper()
	val := "false"
	if enabled {
		val = "true"
	}
	configDir := filepath.Join(home, ".config", "agent-deck")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir configDir: %v", err)
	}
	content := "[worktree]\ndefault_enabled = " + val + "\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
	session.ClearUserConfigCache()
}

// initGitRepo initialises a bare git repo with one commit so worktrees can be created.
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	cleanEnv := testutil.CleanGitEnv(os.Environ())

	cmds := [][]string{
		{"git", "init"},
		{"git", "commit", "--allow-empty", "-m", "init"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Env = append(cleanEnv,
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", args, out)
		}
	}
}

// onlyAddedSession returns the single session added to the given profile, or
// fatals if zero or more than one session exists.
func onlyAddedSession(t *testing.T, profile string) *session.Instance {
	t.Helper()
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	defer storage.Close()
	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("LoadWithGroups: %v", err)
	}
	if len(instances) != 1 {
		t.Fatalf("expected 1 session, got %d", len(instances))
	}
	return instances[0]
}

// TestHandleAdd_DefaultWorktree_CreatesWorktreeInGitRepo verifies that when
// [worktree] default_enabled = true and the path is a git repo, a worktree with
// an auto-generated "wt/<slug>-<8hex>" branch is created.
func TestHandleAdd_DefaultWorktree_CreatesWorktreeInGitRepo(t *testing.T) {
	home, profile := setupDefaultWorktreeTest(t)

	repoDir := filepath.Join(home, "myrepo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	initGitRepo(t, repoDir)

	writeWorktreeConfig(t, home, true)

	handleAdd(profile, []string{"--title", "my-feature", "--quiet", repoDir})

	inst := onlyAddedSession(t, profile)

	// The session must be a worktree session.
	if !inst.IsWorktree() {
		t.Fatalf("expected session to be a worktree, WorktreePath=%q WorktreeBranch=%q",
			inst.WorktreePath, inst.WorktreeBranch)
	}

	// Branch must match "wt/<slug>-<8hex>" pattern.
	if !strings.HasPrefix(inst.WorktreeBranch, "wt/") {
		t.Errorf("WorktreeBranch %q does not start with wt/", inst.WorktreeBranch)
	}

	// Worktree directory must exist.
	if _, err := os.Stat(inst.WorktreePath); os.IsNotExist(err) {
		t.Errorf("worktree path %q does not exist", inst.WorktreePath)
	}
}

// TestHandleAdd_DefaultWorktree_NonGitPath_CreatesNormalSession verifies
// graceful degradation: when default_enabled = true but the path is NOT a git
// repo, session creation succeeds without a worktree.
func TestHandleAdd_DefaultWorktree_NonGitPath_CreatesNormalSession(t *testing.T) {
	home, profile := setupDefaultWorktreeTest(t)

	nonGitDir := filepath.Join(home, "not-a-repo")
	if err := os.MkdirAll(nonGitDir, 0o755); err != nil {
		t.Fatalf("mkdir non-git dir: %v", err)
	}

	writeWorktreeConfig(t, home, true)

	// Capture stderr to verify warning is printed (not a fatal error).
	origStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	handleAdd(profile, []string{"--title", "non-git-session", "--quiet", nonGitDir})

	w.Close()
	os.Stderr = origStderr
	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	stderrOutput := string(buf[:n])

	inst := onlyAddedSession(t, profile)

	// Session must NOT be a worktree.
	if inst.IsWorktree() {
		t.Errorf("expected normal session, got worktree: branch=%q path=%q",
			inst.WorktreeBranch, inst.WorktreePath)
	}

	// Warning must have been printed.
	if !strings.Contains(stderrOutput, "not a git repository") {
		t.Errorf("expected warning about non-git repo in stderr, got: %q", stderrOutput)
	}
}

// TestHandleAdd_ExplicitWorktreeFlagWins verifies that an explicit -w flag
// overrides the default_enabled setting (the user-specified branch wins).
func TestHandleAdd_ExplicitWorktreeFlagWins(t *testing.T) {
	home, profile := setupDefaultWorktreeTest(t)

	repoDir := filepath.Join(home, "myrepo2")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	initGitRepo(t, repoDir)

	writeWorktreeConfig(t, home, true)

	// Pass an explicit branch name; -b creates a new branch.
	handleAdd(profile, []string{
		"--title", "explicit-branch",
		"-w", "feat/explicit-branch",
		"-b",
		"--quiet",
		repoDir,
	})

	inst := onlyAddedSession(t, profile)

	if !inst.IsWorktree() {
		t.Fatalf("expected worktree session")
	}

	// The branch must contain the user-specified name and must NOT be an auto-generated wt/* branch.
	if strings.HasPrefix(inst.WorktreeBranch, "wt/") {
		t.Errorf("explicit -w flag should win over default_enabled auto-generation; got wt/* branch %q", inst.WorktreeBranch)
	}
	if !strings.Contains(inst.WorktreeBranch, "explicit-branch") {
		t.Errorf("expected WorktreeBranch to contain explicit-branch, got %q", inst.WorktreeBranch)
	}
}
