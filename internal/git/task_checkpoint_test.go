package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func checkpointTestGit(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return out
}

func checkpointTestRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required")
	}
	root := t.TempDir()
	checkpointTestGit(t, root, "init", "-q")
	checkpointTestGit(t, root, "config", "user.name", "Checkpoint Test")
	checkpointTestGit(t, root, "config", "user.email", "checkpoint@example.invalid")
	checkpointTestWrite(t, root, "tracked file", "base\n")
	checkpointTestWrite(t, root, "removed", "remove me\n")
	checkpointTestGit(t, root, "add", ".")
	checkpointTestGit(t, root, "commit", "-qm", "base")
	return root
}

func checkpointTestWrite(t *testing.T, dir, name, data string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600))
}

func TestTaskCheckpointRoundTrip(t *testing.T) {
	ctx := context.Background()
	source := checkpointTestRepo(t)
	dest := filepath.Join(t.TempDir(), "clone")
	checkpointTestGit(t, source, "clone", "-q", source, dest)
	checkpointTestWrite(t, source, "tracked file", "staged\n")
	checkpointTestWrite(t, source, "new", "addition\n")
	checkpointTestGit(t, source, "add", "tracked file", "new")
	checkpointTestWrite(t, source, "tracked file", "staged and working\n")
	checkpointTestWrite(t, source, "untracked", "do not copy\n")
	require.NoError(t, os.Remove(filepath.Join(source, "removed")))
	before := checkpointTestGit(t, source, "status", "--porcelain")
	c, err := CaptureTaskCheckpoint(ctx, source, "resume with tests")
	require.NoError(t, err)
	paths, err := c.Paths(ctx)
	require.NoError(t, err)
	slices.Sort(paths)
	require.Equal(t, []string{"new", "removed", "tracked file"}, paths)
	filename := filepath.Join(t.TempDir(), "checkpoint.json")
	require.NoError(t, WriteTaskCheckpoint(filename, c))
	require.Error(t, WriteTaskCheckpoint(filename, c))
	read, sha, err := ReadTaskCheckpoint(filename)
	require.NoError(t, err)
	require.Len(t, sha, 64)
	require.Equal(t, c, read)
	target := filepath.Join(t.TempDir(), "restored")
	require.NoError(t, RestoreTaskCheckpoint(ctx, dest, target, read))
	require.Equal(t, before, checkpointTestGit(t, source, "status", "--porcelain"))
	require.Empty(t, checkpointTestGit(t, dest, "status", "--porcelain"))
	require.Equal(t, "staged\n", string(checkpointTestGit(t, source, "show", ":tracked file")))
	for _, name := range []string{"new", "tracked file"} {
		a, err := os.ReadFile(filepath.Join(source, name))
		require.NoError(t, err)
		b, err := os.ReadFile(filepath.Join(target, name))
		require.NoError(t, err)
		require.Equal(t, a, b)
	}
	require.NoFileExists(t, filepath.Join(target, "untracked"))
	require.NoFileExists(t, filepath.Join(target, "removed"))
	require.Error(t, RestoreTaskCheckpoint(ctx, dest, target, read))
	require.FileExists(t, filepath.Join(target, "tracked file"))
}

func TestTaskCheckpointFailedRestoreCleansWorktree(t *testing.T) {
	ctx := context.Background()
	repo := checkpointTestRepo(t)
	checkpointTestWrite(t, repo, "tracked file", "changed\n")
	c, err := CaptureTaskCheckpoint(ctx, repo, "")
	require.NoError(t, err)
	c.Patch = []byte(strings.Replace(string(c.Patch), "-base\n", "-other\n", 1))
	before := checkpointTestGit(t, repo, "worktree", "list", "--porcelain")
	target := filepath.Join(t.TempDir(), "failed")
	require.ErrorContains(t, RestoreTaskCheckpoint(ctx, repo, target, c), "does not apply")
	require.NoDirExists(t, target)
	require.Equal(t, before, checkpointTestGit(t, repo, "worktree", "list", "--porcelain"))
	c.Base = strings.Repeat("f", 40)
	require.ErrorContains(t, RestoreTaskCheckpoint(ctx, repo, target, c), "base commit")
	require.NoDirExists(t, target)
}

func TestTaskCheckpointRejectsUnsupportedChanges(t *testing.T) {
	for _, name := range []string{"binary", "symlink", "mode", "filter"} {
		t.Run(name, func(t *testing.T) {
			repo := checkpointTestRepo(t)
			switch name {
			case "binary":
				checkpointTestWrite(t, repo, "tracked file", "a\x00b")
			case "symlink":
				require.NoError(t, os.Symlink("tracked file", filepath.Join(repo, "link")))
				checkpointTestGit(t, repo, "add", "link")
			case "mode":
				require.NoError(t, os.Chmod(filepath.Join(repo, "tracked file"), 0o700))
			case "filter":
				checkpointTestGit(t, repo, "config", "filter.example.clean", "cat")
			}
			_, err := CaptureTaskCheckpoint(context.Background(), repo, "")
			require.Error(t, err)
		})
	}
}

func TestTaskCheckpointRestoreSkipsHooksAndPreservesRegistration(t *testing.T) {
	ctx := context.Background()
	repo := checkpointTestRepo(t)
	checkpointTestWrite(t, repo, "tracked file", "changed\n")
	c, err := CaptureTaskCheckpoint(ctx, repo, "")
	require.NoError(t, err)

	hooks := t.TempDir()
	hook := filepath.Join(hooks, "post-checkout")
	require.NoError(t, os.WriteFile(hook, []byte("#!/bin/sh\nexit 99\n"), 0o700))
	checkpointTestGit(t, repo, "config", "core.hooksPath", hooks)
	require.ErrorContains(t, RestoreTaskCheckpoint(ctx, repo, filepath.Join(repo, "nested"), c), "outside existing worktrees")
	target := filepath.Join(t.TempDir(), "restored")
	require.NoError(t, RestoreTaskCheckpoint(ctx, repo, target, c))

	require.NoError(t, os.RemoveAll(target))
	before := checkpointTestGit(t, repo, "worktree", "list", "--porcelain")
	require.ErrorContains(t, RestoreTaskCheckpoint(ctx, repo, target, c), "already registered")
	require.Equal(t, before, checkpointTestGit(t, repo, "worktree", "list", "--porcelain"))
	require.NoDirExists(t, target)
}

func TestTaskCheckpointRejectsInvalidMetadata(t *testing.T) {
	for name, data := range map[string]string{
		"duplicate": `{"version":1,"version":1,"base":"abc","patch":null}`,
		"unknown":   `{"version":1,"base":"abc","patch":null,"extra":true}`,
		"missing":   `{"version":1,"base":"abc"}`,
		"trailing":  `{"version":1,"base":"abc","patch":null} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			checkpointTestWrite(t, dir, "checkpoint.json", data)
			_, _, err := ReadTaskCheckpoint(filepath.Join(dir, "checkpoint.json"))
			require.Error(t, err)
		})
	}
}
