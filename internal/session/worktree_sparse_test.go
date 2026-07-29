package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Issue #1708: `[worktree] sparse_checkout` is opt-in and string-valued, so
// exactly one spelling may enable it. Anything else — including a typo — must
// leave checkout behavior alone.
func TestWorktreeSettings_InheritSparseCheckout(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{value: "", want: false},
		{value: "off", want: false},
		{value: "OFF", want: false},
		{value: "inherit", want: true},
		{value: "INHERIT", want: true},
		{value: "  inherit  ", want: true},
		{value: "inherited", want: false},
		{value: "true", want: false},
	} {
		t.Run("value="+tc.value, func(t *testing.T) {
			w := WorktreeSettings{SparseCheckout: tc.value}
			assert.Equal(t, tc.want, w.InheritSparseCheckout())
		})
	}
}

// initSparseTestRepo builds a repo whose MAIN worktree is a full checkout and
// whose linked worktree is cone-sparse on keep/. It returns both, mirroring the
// real shape of a session running inside a sparse worktree.
func initSparseTestRepo(t *testing.T) (mainWT, sparseWT string) {
	t.Helper()
	mainWT = t.TempDir()
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	run(mainWT, "init", "-b", "main")
	run(mainWT, "config", "user.email", "test@test.com")
	run(mainWT, "config", "user.name", "Test")
	writeTestFile(t, filepath.Join(mainWT, "keep", "keep.txt"), "keep")
	writeTestFile(t, filepath.Join(mainWT, "heavy", "heavy.txt"), "heavy")
	run(mainWT, "add", ".")
	run(mainWT, "commit", "-m", "init")

	sparseWT = filepath.Join(t.TempDir(), "sparse-src")
	run(mainWT, "worktree", "add", sparseWT, "-b", "sparse-src")
	run(sparseWT, "sparse-checkout", "set", "keep")
	require.NoDirExists(t, filepath.Join(sparseWT, "heavy"), "fixture: source worktree must be sparse")
	require.DirExists(t, filepath.Join(mainWT, "heavy"), "fixture: main worktree must be a full checkout")
	return mainWT, sparseWT
}

// Multi-repo creation resolves each input path to its base root, so inheritance
// must be captured from the INPUT path (the user's own worktree) — see
// git.CaptureSparseCheckout. Sparse and non-sparse inputs are mixed here so one
// repo's sparsity can never leak into the other.
func TestCreateMultiRepoWorktreesWithOptions_InheritsSparsePerInputPath(t *testing.T) {
	_, sparseWT := initSparseTestRepo(t)
	plainRepo := initTestGitRepo(t)
	writeTestFile(t, filepath.Join(plainRepo, "heavy", "heavy.txt"), "heavy")
	testGitAdd(t, plainRepo, ".")
	testGitCommit(t, plainRepo, "add heavy")

	parentDir := t.TempDir()
	result := CreateMultiRepoWorktreesWithOptions([]string{sparseWT, plainRepo}, parentDir, "multirepo-sparse", 0, true)

	require.Empty(t, result.Warnings)
	require.Len(t, result.MappedPaths, 2)

	assert.NoDirExists(t, filepath.Join(result.MappedPaths[0], "heavy"),
		"sparse source's worktree materialized the excluded tree")
	assert.FileExists(t, filepath.Join(result.MappedPaths[0], "keep", "keep.txt"))
	assert.DirExists(t, filepath.Join(result.MappedPaths[1], "heavy"),
		"non-sparse repo must keep its full checkout")
}

// With inheritance off (the default) the sparse source still gets a full
// checkout: existing behavior is unchanged unless the user opts in.
func TestCreateMultiRepoWorktrees_DefaultKeepsFullCheckout(t *testing.T) {
	_, sparseWT := initSparseTestRepo(t)

	parentDir := t.TempDir()
	result := CreateMultiRepoWorktrees([]string{sparseWT}, parentDir, "multirepo-default", 0)

	require.Empty(t, result.Warnings)
	require.Len(t, result.MappedPaths, 1)
	info, err := os.Lstat(result.MappedPaths[0])
	require.NoError(t, err)
	require.True(t, info.IsDir())
	assert.DirExists(t, filepath.Join(result.MappedPaths[0], "heavy"),
		"default multi-repo creation must keep git's full checkout")
}
