package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initTestRepo stands up a real git repository with one commit and returns its
// resolved root. Real git, not a fixture: ProjectRoot's whole job is agreeing
// with git about what the root worktree is.
func initTestRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	// Isolate git's global config: repositoryGlobalGitExcludePath consults
	// core.excludesFile before falling back to XDG, and an unisolated test
	// would append its rule to the developer's real excludes file.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))

	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-qm", "init")
	return root
}

// Every worktree of a repository must fold to the same root, so a session in
// .worktrees/feature-x and one in the main checkout share one .agent-deck tree
// instead of scattering a run's artifacts across checkouts.
func TestProjectRootFoldsWorktreesToMainCheckout(t *testing.T) {
	root := initTestRepo(t)
	wt := filepath.Join(root, ".worktrees", "feature-x")
	cmd := exec.Command("git", "-C", root, "worktree", "add", "-q", "-b", "feature-x", wt)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v\n%s", err, out)
	}

	fromRoot, err := ProjectRoot(root)
	if err != nil {
		t.Fatalf("ProjectRoot(root): %v", err)
	}
	fromWorktree, err := ProjectRoot(wt)
	if err != nil {
		t.Fatalf("ProjectRoot(worktree): %v", err)
	}
	if fromRoot != root {
		t.Errorf("ProjectRoot(root) = %q, want %q", fromRoot, root)
	}
	if fromWorktree != root {
		t.Errorf("ProjectRoot(worktree) = %q, want the main checkout %q", fromWorktree, root)
	}
}

// A directory outside any repository is still a project. It keeps its
// artifacts beside itself rather than falling back to somewhere global.
func TestProjectRootNonRepoIsItsOwnRoot(t *testing.T) {
	dir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	got, err := ProjectRoot(dir)
	if err != nil {
		t.Fatalf("ProjectRoot: %v", err)
	}
	if got != dir {
		t.Errorf("ProjectRoot = %q, want %q", got, dir)
	}
}

func TestProjectRootRejectsEmpty(t *testing.T) {
	if _, err := ProjectRoot("   "); err == nil {
		t.Error("expected an error for an empty working directory")
	}
}

// Session ids come straight from the registry. A separator or ".." in one
// must not be able to steer a write anywhere in the user's repository.
func TestProjectDataPathRejectsPathTraversal(t *testing.T) {
	project := t.TempDir()
	for _, part := range []string{"..", ".", "a/b", "../escape", "  ", `a\b`} {
		if got, err := ProjectDataPath(project, "handoff", part); err == nil {
			t.Errorf("ProjectDataPath accepted %q and returned %q", part, got)
		}
	}
}

// Creating the directory must also stop it polluting the user's `git status`,
// and it must never write the repository's tracked .gitignore — that would be
// agent-deck editing the user's committed files.
func TestEnsureProjectDataPathIgnoresTheDirectory(t *testing.T) {
	// Redirect the global excludes file this repository's rule is written to,
	// so the test never touches the developer's real ~/.config/git/ignore.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := initTestRepo(t)

	dir, err := EnsureProjectDataPath(root, "handoff", "sess-1")
	if err != nil {
		t.Fatalf("EnsureProjectDataPath: %v", err)
	}
	want := filepath.Join(root, ProjectDataDirName, "handoff", "sess-1")
	if dir != want {
		t.Fatalf("EnsureProjectDataPath = %q, want %q", dir, want)
	}
	if info, statErr := os.Stat(dir); statErr != nil || !info.IsDir() {
		t.Fatalf("directory not created: %v", statErr)
	}

	out, err := exec.Command("git", "-C", root, "status", "--porcelain").Output()
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	if strings.Contains(string(out), ProjectDataDirName) {
		t.Errorf("%s shows up in git status:\n%s", ProjectDataDirName, out)
	}
	if _, err := os.Stat(filepath.Join(root, ".gitignore")); err == nil {
		t.Error("a tracked .gitignore was created; agent-deck must not write the user's committed ignore rules")
	}
}

// Rerunning must not append a duplicate rule every time a session wraps up.
func TestEnsureProjectDataPathIsIdempotent(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	root := initTestRepo(t)

	for i := 0; i < 3; i++ {
		if _, err := EnsureProjectDataPath(root, "handoff", "sess-1"); err != nil {
			t.Fatalf("EnsureProjectDataPath: %v", err)
		}
	}
	data, err := os.ReadFile(filepath.Join(configHome, "git", "ignore"))
	if err != nil {
		t.Fatalf("read global excludes: %v", err)
	}
	if count := strings.Count(string(data), repositoryGitExcludeRule); count != 1 {
		t.Errorf("exclude rule written %d times, want 1:\n%s", count, data)
	}
}

// A worktree session's artifacts belong to the repository, so the whole
// project tree resolves through the main checkout.
func TestInstanceProjectDirPrefersProjectPath(t *testing.T) {
	inst := &Instance{ProjectPath: "/a/project", WorktreePath: "/a/project/.worktrees/x"}
	if got := instanceProjectDir(inst); got != "/a/project" {
		t.Errorf("instanceProjectDir = %q, want %q", got, "/a/project")
	}
	inst = &Instance{WorktreePath: "/a/project/.worktrees/x"}
	if got := instanceProjectDir(inst); got != "/a/project/.worktrees/x" {
		t.Errorf("instanceProjectDir fallback = %q, want the worktree path", got)
	}
	if got := instanceProjectDir(nil); got != "" {
		t.Errorf("instanceProjectDir(nil) = %q, want empty", got)
	}
}
