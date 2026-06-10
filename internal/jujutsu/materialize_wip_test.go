package jujutsu

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

// TestWorkingCopyRevision_CleanIDWithOversizedWorkingFile guards against jj
// snapshot warnings corrupting the resolved commit id. A working-copy file
// larger than jj's default snapshot.max-new-file-size (1MiB) makes `jj log`
// print a multi-line "Refused to snapshot some files" warning to STDERR while
// still exiting 0. If the resolver reads combined stdout+stderr, that warning is
// returned as the "commit id" and every downstream `jj --revision <id>` fails.
// Oversized untracked files (build artifacts, binaries) are common, so this
// would break `f` with-state on jj for many real repos.
func TestWorkingCopyRevision_CleanIDWithOversizedWorkingFile(t *testing.T) {
	requireJJ(t)
	parent := setupJJParentWithWIP(t)
	// 2MiB untracked file exceeds jj's default 1MiB snapshot limit.
	big := make([]byte, 2*1024*1024)
	require.NoError(t, os.WriteFile(filepath.Join(parent, "big.bin"), big, 0o644))

	hexID := regexp.MustCompile(`^[0-9a-f]{40}$`)

	at, err := WorkingCopyRevision(parent)
	require.NoError(t, err)
	require.Regexp(t, hexID, at, "@ commit id must be a clean hash, not jj's stderr snapshot warning")

	base, err := WorkingCopyParentRevision(parent)
	require.NoError(t, err)
	require.Regexp(t, hexID, base, "@- commit id must be a clean hash, not jj's stderr snapshot warning")
}

// TestMaterializeWipFromParent_SucceedsWithOversizedWorkingFile is the
// end-to-end guard: a with-state fork must still succeed (carrying the tracked
// WIP) when the parent working copy holds an oversized file jj refuses to snapshot.
func TestMaterializeWipFromParent_SucceedsWithOversizedWorkingFile(t *testing.T) {
	requireJJ(t)
	parent := setupJJParentWithWIP(t)
	big := make([]byte, 2*1024*1024)
	require.NoError(t, os.WriteFile(filepath.Join(parent, "big.bin"), big, 0o644))

	base, err := WorkingCopyParentRevision(parent)
	require.NoError(t, err)

	dest := filepath.Join(t.TempDir(), "fork")
	require.NoError(t, CreateWorkspaceAtRevision(parent, dest, "forkbranch", base))
	require.NoError(t, MaterializeWipFromParent(parent, dest, false))

	got, err := os.ReadFile(filepath.Join(dest, "tracked.txt"))
	require.NoError(t, err)
	require.Equal(t, "base content\nWIP EDIT\n", string(got), "tracked WIP must still carry despite the oversized file")
}

// TestBookmarkExists_MatchesExactlyNotGlob guards against jj's default glob
// matching producing false-positive collisions: `jj bookmark list <name>` treats
// <name> as a glob, so a query containing a metacharacter (or a query that is a
// glob-prefix of a real bookmark) would spuriously report the bookmark as
// existing. BookmarkExists must match the literal name only.
func TestBookmarkExists_MatchesExactlyNotGlob(t *testing.T) {
	requireJJ(t)
	repo := t.TempDir()
	jjMust(t, repo, "git", "init", "--colocate")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "f.txt"), []byte("x\n"), 0o644))
	jjMust(t, repo, "describe", "-m", "base")
	jjMust(t, repo, "bookmark", "create", "release-v1.0", "-r", "@")

	exists, err := BookmarkExists(repo, "release-v1.0")
	require.NoError(t, err)
	require.True(t, exists, "the literal bookmark name must be found")

	globbed, err := BookmarkExists(repo, "release-v1*")
	require.NoError(t, err)
	require.False(t, globbed, "a glob pattern must NOT match — exact name comparison only")
}

// TestSupportsGitignoredCopy distinguishes repos where with-ignored can carry
// gitignored files (colocated jj, has a git worktree to enumerate against) from
// those where it would silently no-op. A linked jj workspace has only .jj and no
// .git, so forking such a session (fork-of-a-fork) can't enumerate ignored files.
// The fork path uses this to surface a notice instead of dropping files silently
// (#1305 BUG-04).
func TestSupportsGitignoredCopy(t *testing.T) {
	requireJJ(t)

	colocated := setupJJParentWithWIP(t)
	require.True(t, SupportsGitignoredCopy(colocated), "colocated jj repo has git metadata for ignored-file enumeration")

	linked := filepath.Join(t.TempDir(), "workspace")
	jjMust(t, colocated, "workspace", "add", "--name", "linked", linked)
	require.False(t, SupportsGitignoredCopy(linked), "a linked jj workspace has no git worktree root, so ignored-file copy can't run")
}

// TestCreateWorkspaceAtRevision_RelativeWorkspacePathResolvesAbsolutely guards
// the path-resolution hardening (Copilot review): a relative workspace path must
// resolve to a stable absolute location for both the workspace creation and the
// subsequent materialize, regardless of jj's -R repoDir argument.
func TestCreateWorkspaceAtRevision_RelativeWorkspacePathResolvesAbsolutely(t *testing.T) {
	requireJJ(t)
	parent := setupJJParentWithWIP(t)
	base, err := WorkingCopyParentRevision(parent)
	require.NoError(t, err)

	work := t.TempDir()
	t.Chdir(work)
	rel := "fork-rel"

	require.NoError(t, CreateWorkspaceAtRevision(parent, rel, "forkrel", base))
	require.DirExists(t, filepath.Join(work, rel), "relative workspace path must resolve under the process working directory, not repoDir")

	require.NoError(t, MaterializeWipFromParent(parent, rel, false))
	got, err := os.ReadFile(filepath.Join(work, rel, "tracked.txt"))
	require.NoError(t, err)
	require.Equal(t, "base content\nWIP EDIT\n", string(got))
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
