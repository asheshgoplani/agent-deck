package jujutsu

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// requireJJ skips the test when the jj binary is absent (most CI), and pins a
// throwaway JJ_CONFIG that supplies a commit identity so jj will create the
// working-copy/restore commits the with-state path relies on. t.Setenv makes
// the config visible to both the test setup and the production jj invocations
// (which inherit os.Environ via exec.Command).
func requireJJ(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj not on PATH")
	}
	cfg := filepath.Join(t.TempDir(), "jjconfig.toml")
	if err := os.WriteFile(cfg, []byte("[user]\nname = \"Test User\"\nemail = \"test@example.com\"\n"), 0o644); err != nil {
		t.Fatalf("write jj config: %v", err)
	}
	t.Setenv("JJ_CONFIG", cfg)
}

func jjMust(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("jj", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("jj %v in %s failed: %v\n%s", args, dir, err, out)
	}
}

// setupJJParentWithWIP builds a colocated jj repo with a committed "base" and an
// uncommitted working copy carrying: a tracked edit, an untracked file, and a
// gitignored file. Returns the repo dir.
func setupJJParentWithWIP(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	jjMust(t, repo, "git", "init", "--colocate")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("base content\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("ign/\n"), 0o644))
	jjMust(t, repo, "describe", "-m", "base commit")
	jjMust(t, repo, "new", "-m", "wip")
	// Uncommitted working-copy state on top of the base commit.
	require.NoError(t, os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("base content\nWIP EDIT\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "untracked.txt"), []byte("new untracked\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "ign"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "ign", "secret.env"), []byte("secret=1\n"), 0o644))
	return repo
}

// TestMaterializeWipFromParent_CarriesTrackedAndUntracked is the core jj-native
// with-state behavior (#1305): a fork workspace anchored at the parent's
// committed point (@-) must end up with the parent's tracked edits AND untracked
// files in its working copy, while gitignored files stay behind unless opted in.
func TestMaterializeWipFromParent_CarriesTrackedAndUntracked(t *testing.T) {
	requireJJ(t)
	parent := setupJJParentWithWIP(t)

	base, err := WorkingCopyParentRevision(parent)
	require.NoError(t, err)
	require.NotEmpty(t, base)

	dest := filepath.Join(t.TempDir(), "fork")
	require.NoError(t, CreateWorkspaceAtRevision(parent, dest, "forkbranch", base))

	require.NoError(t, MaterializeWipFromParent(parent, dest, false /* includeIgnored */))

	got, err := os.ReadFile(filepath.Join(dest, "tracked.txt"))
	require.NoError(t, err)
	require.Equal(t, "base content\nWIP EDIT\n", string(got), "tracked WIP edit must carry into the fork workspace")

	untracked, err := os.ReadFile(filepath.Join(dest, "untracked.txt"))
	require.NoError(t, err)
	require.Equal(t, "new untracked\n", string(untracked), "untracked file must carry (jj snapshots it into @)")

	_, statErr := os.Stat(filepath.Join(dest, "ign", "secret.env"))
	require.True(t, os.IsNotExist(statErr), "gitignored file must NOT carry without includeIgnored")
}

// TestMaterializeWipFromParent_IncludeIgnoredCopiesGitignored verifies the
// opt-in gitignored copy (the .env / .mcp.json case) that `f`'s comprehensive
// default and Shift+F's gitignored toggle request.
func TestMaterializeWipFromParent_IncludeIgnoredCopiesGitignored(t *testing.T) {
	requireJJ(t)
	parent := setupJJParentWithWIP(t)

	base, err := WorkingCopyParentRevision(parent)
	require.NoError(t, err)

	dest := filepath.Join(t.TempDir(), "fork")
	require.NoError(t, CreateWorkspaceAtRevision(parent, dest, "forkbranch", base))

	require.NoError(t, MaterializeWipFromParent(parent, dest, true /* includeIgnored */))

	secret, err := os.ReadFile(filepath.Join(dest, "ign", "secret.env"))
	require.NoError(t, err)
	require.Equal(t, "secret=1\n", string(secret), "gitignored file must carry when includeIgnored is set")
}

func TestMaterializeWipFromParent_FromSubdirectoryCopiesGitignored(t *testing.T) {
	requireJJ(t)
	parent := setupJJParentWithWIP(t)
	parentSubdir := filepath.Join(parent, "subdir")
	require.NoError(t, os.MkdirAll(parentSubdir, 0o755))

	base, err := WorkingCopyParentRevision(parentSubdir)
	require.NoError(t, err)

	dest := filepath.Join(t.TempDir(), "fork")
	require.NoError(t, CreateWorkspaceAtRevision(parent, dest, "forkbranch", base))

	require.NoError(t, MaterializeWipFromParent(parentSubdir, dest, true /* includeIgnored */))

	secret, err := os.ReadFile(filepath.Join(dest, "ign", "secret.env"))
	require.NoError(t, err, "gitignored files must be copied repo-relative even when parentDir is a subdirectory")
	require.Equal(t, "secret=1\n", string(secret))
}

func TestCreateWorkspaceAtRevision_RejectsExistingBookmarkBeforeWorkspaceAdd(t *testing.T) {
	requireJJ(t)
	parent := setupJJParentWithWIP(t)
	base, err := WorkingCopyParentRevision(parent)
	require.NoError(t, err)
	jjMust(t, parent, "bookmark", "create", "fork/existing", "-r", base)

	before := jjWorkspaceList(t, parent)
	dest := filepath.Join(t.TempDir(), "fork")
	err = CreateWorkspaceAtRevision(parent, dest, "fork/existing", base)
	require.Error(t, err)
	require.Contains(t, err.Error(), "bookmark")
	require.NoDirExists(t, dest, "existing-bookmark rejection must happen before jj workspace add")
	require.Equal(t, before, jjWorkspaceList(t, parent), "failed preflight must not register a workspace")
}

func TestCreateWorkspaceAtRevision_CleansWorkspaceWhenBookmarkSetupFails(t *testing.T) {
	requireJJ(t)
	parent := setupJJParentWithWIP(t)
	base, err := WorkingCopyParentRevision(parent)
	require.NoError(t, err)

	before := jjWorkspaceList(t, parent)
	dest := filepath.Join(t.TempDir(), "fork")
	err = CreateWorkspaceAtRevision(parent, dest, "bad~name", base)
	require.Error(t, err)
	require.Contains(t, err.Error(), "bookmark")
	require.NoDirExists(t, dest, "post-add bookmark failure must remove the workspace directory")
	require.Equal(t, before, jjWorkspaceList(t, parent), "post-add bookmark failure must forget the registered workspace")
}

func jjWorkspaceList(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("jj", "workspace", "list", "-R", dir)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "jj workspace list: %s", out)
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}
	return strings.Join(lines, "\n")
}
