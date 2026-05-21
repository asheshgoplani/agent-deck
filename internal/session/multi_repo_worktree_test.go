package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateMultiRepoWorktrees_BothReposGetWorktreeWithInclude(t *testing.T) {
	repoA := initTestGitRepo(t)
	repoB := initTestGitRepo(t)

	// Both repos have a .worktreeinclude that references a gitignored .env file
	for _, repo := range []string{repoA, repoB} {
		writeTestFile(t, filepath.Join(repo, ".gitignore"), ".env\n")
		testGitAdd(t, repo, ".gitignore")
		writeTestFile(t, filepath.Join(repo, ".env"), "SECRET="+filepath.Base(repo))
		writeTestFile(t, filepath.Join(repo, ".worktreeinclude"), ".env\n")
		testGitAdd(t, repo, ".worktreeinclude")
		testGitCommit(t, repo, "add worktreeinclude")
	}

	parentDir := t.TempDir()
	branch := "test-branch"

	result := CreateMultiRepoWorktrees([]string{repoA, repoB}, parentDir, branch, 0)

	// Both mapped paths exist and are real directories (not symlinks)
	require.Len(t, result.MappedPaths, 2)
	for _, mp := range result.MappedPaths {
		info, err := os.Lstat(mp)
		require.NoError(t, err)
		assert.True(t, info.IsDir(), "expected real directory, not symlink")
		assert.Zero(t, info.Mode()&os.ModeSymlink)
	}

	// .env from .worktreeinclude landed in both worktrees
	envA, err := os.ReadFile(filepath.Join(result.MappedPaths[0], ".env"))
	require.NoError(t, err)
	assert.Equal(t, "SECRET="+filepath.Base(repoA), string(envA))

	envB, err := os.ReadFile(filepath.Join(result.MappedPaths[1], ".env"))
	require.NoError(t, err)
	assert.Equal(t, "SECRET="+filepath.Base(repoB), string(envB))

	// Worktrees metadata is correct
	require.Len(t, result.Worktrees, 2)
	assert.Equal(t, repoA, result.Worktrees[0].OriginalPath)
	assert.Equal(t, result.MappedPaths[0], result.Worktrees[0].WorktreePath)
	assert.Equal(t, branch, result.Worktrees[0].Branch)
	assert.Equal(t, repoB, result.Worktrees[1].OriginalPath)
	assert.Equal(t, result.MappedPaths[1], result.Worktrees[1].WorktreePath)
	assert.Equal(t, branch, result.Worktrees[1].Branch)

	// No warnings
	assert.Empty(t, result.Warnings)
}

func TestCreateMultiRepoWorktrees_WorktreeCreationFailureFallsBackToSymlink(t *testing.T) {
	repo := initTestGitRepo(t)

	parentDir := t.TempDir()
	branch := "test-branch"

	// First call succeeds — creates the branch+worktree
	result1 := CreateMultiRepoWorktrees([]string{repo}, parentDir, branch, 0)
	require.Len(t, result1.Worktrees, 1)
	require.Empty(t, result1.Warnings)

	// Second call with same branch will fail (worktree already checked out)
	parentDir2 := t.TempDir()
	result2 := CreateMultiRepoWorktrees([]string{repo}, parentDir2, branch, 0)

	require.Len(t, result2.MappedPaths, 1)
	// Falls back to symlink
	info, err := os.Lstat(result2.MappedPaths[0])
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&os.ModeSymlink)
	// Reported as a warning
	require.Len(t, result2.Warnings, 1)
	assert.Contains(t, result2.Warnings[0], "worktree_create_fail")
	// Not in Worktrees
	assert.Empty(t, result2.Worktrees)
}

func TestCreateMultiRepoWorktrees_NonGitPathGetsSymlinked(t *testing.T) {
	repo := initTestGitRepo(t)
	writeTestFile(t, filepath.Join(repo, ".gitignore"), ".env\n")
	testGitAdd(t, repo, ".gitignore")
	writeTestFile(t, filepath.Join(repo, ".env"), "SECRET=repo")
	writeTestFile(t, filepath.Join(repo, ".worktreeinclude"), ".env\n")
	testGitAdd(t, repo, ".worktreeinclude")
	testGitCommit(t, repo, "setup")

	nonGitDir := t.TempDir()
	writeTestFile(t, filepath.Join(nonGitDir, "data.txt"), "hello")

	parentDir := t.TempDir()

	result := CreateMultiRepoWorktrees([]string{repo, nonGitDir}, parentDir, "test-branch", 0)

	require.Len(t, result.MappedPaths, 2)

	// First path: real worktree
	info, err := os.Lstat(result.MappedPaths[0])
	require.NoError(t, err)
	assert.True(t, info.IsDir())
	assert.Zero(t, info.Mode()&os.ModeSymlink)

	// Second path: symlink to original non-git dir
	info, err = os.Lstat(result.MappedPaths[1])
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&os.ModeSymlink)
	target, err := os.Readlink(result.MappedPaths[1])
	require.NoError(t, err)
	assert.Equal(t, nonGitDir, target)

	// Only the git repo appears in Worktrees
	require.Len(t, result.Worktrees, 1)
	assert.Equal(t, repo, result.Worktrees[0].OriginalPath)

	assert.Empty(t, result.Warnings)
}

// --- test helpers ---

func initTestGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	writeTestFile(t, filepath.Join(dir, "README.md"), "# test")
	testGitAdd(t, dir, ".")
	testGitCommit(t, dir, "init")
	return dir
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func testGitAdd(t *testing.T, dir, path string) {
	t.Helper()
	cmd := exec.Command("git", "add", path)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git add %s: %s", path, out)
}

func testGitCommit(t *testing.T, dir, msg string) {
	t.Helper()
	cmd := exec.Command("git", "commit", "-m", msg)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git commit: %s", out)
}
