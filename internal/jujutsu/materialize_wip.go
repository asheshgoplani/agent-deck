package jujutsu

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// WorkingCopyRevision returns the commit id of the working-copy commit (@) of
// the workspace rooted at dir.
//
// It intentionally does NOT pass --ignore-working-copy: jj only snapshots the
// on-disk working copy into @ when a normal command runs, so resolving with the
// flag would return a stale @ and silently drop the parent's most recent
// uncommitted edits — exactly the state a with-state fork must carry.
func WorkingCopyRevision(dir string) (string, error) {
	return resolveRevision(dir, "@")
}

// WorkingCopyParentRevision returns the commit id of @- — the committed parent
// of the working-copy commit. This is the anchor a with-state fork workspace is
// created at, so that the materialized working copy reads as uncommitted changes
// on top of it (mirroring git's "fork at parent HEAD, lay WIP on top").
func WorkingCopyParentRevision(dir string) (string, error) {
	return resolveRevision(dir, "@-")
}

// resolveRevision runs `jj log` from dir (snapshotting the working copy first)
// and returns the resolved commit id for rev.
//
// It reads STDOUT only (not combined output): jj emits non-fatal snapshot
// warnings (e.g. "Refused to snapshot some files" for a working-copy file above
// snapshot.max-new-file-size) to STDERR while still exiting 0. Folding stderr
// into the parsed value would return that multi-line warning as the "commit id"
// and break every downstream `jj --revision <id>` call. Stderr is captured
// separately and surfaced only on error.
func resolveRevision(dir, rev string) (string, error) {
	cmd := exec.Command("jj", "log", "-r", rev, "--no-graph", "-T", "commit_id") // #nosec G204 -- fixed args + caller-controlled revset
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("resolve revision %q: %s: %w", rev, strings.TrimSpace(stderr.String()), err)
	}
	id := strings.TrimSpace(string(out))
	if id == "" {
		return "", fmt.Errorf("resolve revision %q: empty commit id", rev)
	}
	return id, nil
}

// CreateWorkspaceAtRevision creates a new jj workspace at workspacePath whose
// working copy is anchored at revision (the parent session's committed point),
// then creates branchName as a bookmark on the new workspace's @. Existing
// bookmarks are refused because with-state forks must create a fresh
// destination, matching the git path's collision semantics.
//
// This is the jj equivalent of git.CreateWorktreeAtStartPoint: it differs from
// the plain CreateWorktree only by pinning the start revision, which is what
// lets a subsequent MaterializeWipFromParent reproduce the parent's WIP as
// uncommitted changes rather than as an extra ancestor commit.
func CreateWorkspaceAtRevision(repoDir, workspacePath, branchName, revision string) error {
	wsName := workspaceNameFromPath(workspacePath)
	if branchName != "" {
		exists, err := BookmarkExists(repoDir, branchName)
		if err != nil {
			return fmt.Errorf("failed to check bookmark: %w", err)
		}
		if exists {
			return fmt.Errorf("bookmark %q already exists; choose a new destination branch for --with-state", branchName)
		}
	}

	cmd := exec.Command("jj", "workspace", "add", "--name", wsName, "--revision", revision, workspacePath, "-R", repoDir) // #nosec G204 -- slice args, not shell-formed
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to create workspace at revision: %s: %w", strings.TrimSpace(string(out)), err)
	}

	if branchName != "" {
		bc := exec.Command("jj", "bookmark", "create", branchName, "-r", "@", "-R", workspacePath) // #nosec G204 -- slice args, not shell-formed
		if out, err := bc.CombinedOutput(); err != nil {
			cleanupErr := forgetWorkspaceAndRemoveDir(repoDir, wsName, workspacePath)
			if cleanupErr != nil {
				return fmt.Errorf("failed to create bookmark: %s: %w; cleanup also failed: %v", strings.TrimSpace(string(out)), err, cleanupErr)
			}
			return fmt.Errorf("failed to create bookmark: %s: %w; new workspace cleaned up", strings.TrimSpace(string(out)), err)
		}
	}

	return nil
}

// MaterializeWipFromParent reproduces the parent workspace's uncommitted
// working-copy state inside workspacePath, which must be a freshly created jj
// workspace anchored at the parent's @- (see CreateWorkspaceAtRevision).
//
// Because jj's working copy *is* a commit and untracked (non-ignored) files are
// auto-snapshotted into it, a single `jj restore --from <parent @>` carries both
// tracked edits and untracked files — no separate untracked-copy pass is needed
// (unlike git). Gitignored files are not part of @, so they are filesystem-copied
// only when includeIgnored is set.
//
// Contract:
//   - parentDir is treated read-only (no commit/describe/squash); only a
//     snapshot of its working copy is taken when resolving @.
//   - On a colocated jj repo (has .git), gitignored files are enumerated via
//     git's exclude machinery from the parent workspace root. If git metadata
//     is unavailable, ignored-file copy is a documented no-op.
func MaterializeWipFromParent(parentDir, workspacePath string, includeIgnored bool) error {
	parentRev, err := WorkingCopyRevision(parentDir)
	if err != nil {
		return fmt.Errorf("resolve parent working copy: %w", err)
	}

	cmd := exec.Command("jj", "restore", "--from", parentRev, "-R", workspacePath) // #nosec G204 -- slice args, not shell-formed
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("restore parent state: %s: %w", strings.TrimSpace(string(out)), err)
	}

	if includeIgnored {
		if err := copyGitignoredFromParent(parentDir, workspacePath); err != nil {
			return fmt.Errorf("copy gitignored: %w", err)
		}
	}

	return nil
}

// BookmarkExists reports whether a jj bookmark (branch) named name exists in the
// repo. Newer jj versions accept bookmark names positionally; --name is not
// supported by jj 0.42.0.
func BookmarkExists(repoDir, name string) (bool, error) {
	cmd := exec.Command("jj", "bookmark", "list", name, "-R", repoDir, "--ignore-working-copy") // #nosec G204 -- slice args, not shell-formed
	out, err := cmd.Output()
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) != "", nil
}

func forgetWorkspaceAndRemoveDir(repoDir, wsName, workspacePath string) error {
	var cleanupErrs []string
	cmd := exec.Command("jj", "workspace", "forget", wsName, "-R", repoDir) // #nosec G204 -- slice args, not shell-formed
	if out, err := cmd.CombinedOutput(); err != nil {
		cleanupErrs = append(cleanupErrs, fmt.Sprintf("workspace forget failed: %s: %v", strings.TrimSpace(string(out)), err))
	}
	if err := os.RemoveAll(workspacePath); err != nil && !os.IsNotExist(err) {
		cleanupErrs = append(cleanupErrs, fmt.Sprintf("remove workspace directory failed: %v", err))
	}
	if len(cleanupErrs) > 0 {
		return fmt.Errorf("%s", strings.Join(cleanupErrs, "; "))
	}
	return nil
}

// copyGitignoredFromParent copies the parent's gitignored files into destDir.
// jj workspaces backed by git can use git's exclude machinery as the
// authoritative "what is ignored" source. parentDir may be a subdirectory, so
// paths are enumerated from the git working-tree root and copied root-relative.
// If git metadata is unavailable, the copy is skipped (documented limitation,
// #1305).
func copyGitignoredFromParent(parentDir, destDir string) error {
	parentRoot, ok := gitWorktreeRoot(parentDir)
	if !ok {
		return nil
	}
	cmd := exec.Command("git", "-C", parentRoot, "ls-files", "--others", "--ignored", "--exclude-standard", "-z") // #nosec G204 -- fixed args
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("list gitignored: %w", err)
	}
	raw := strings.TrimRight(string(out), "\x00")
	if raw == "" {
		return nil
	}
	for _, rel := range strings.Split(raw, "\x00") {
		if rel == "" || isVCSInternalPath(rel) {
			continue
		}
		if err := copyFilePreserving(filepath.Join(parentRoot, rel), filepath.Join(destDir, rel)); err != nil {
			return fmt.Errorf("copy %s: %w", rel, err)
		}
	}
	return nil
}

func gitWorktreeRoot(dir string) (string, bool) {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel") // #nosec G204 -- fixed args + caller-controlled repo dir
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return "", false
	}
	return root, true
}

// isVCSInternalPath reports whether a repository-relative path lives inside a
// VCS metadata directory. A colocated jj repo's .jj/ store is gitignored, so
// `git ls-files --ignored` would otherwise enumerate (and the fork would copy)
// jj's entire internal state — corrupting the new workspace. .git is excluded by
// git itself but guarded here for safety.
func isVCSInternalPath(rel string) bool {
	for _, dir := range []string{".jj", ".git"} {
		if rel == dir || strings.HasPrefix(rel, dir+"/") {
			return true
		}
	}
	return false
}

// copyFilePreserving copies a single file (or symlink) from src to dst,
// preserving mode and symlink target. Mirrors git.copyOneFile's behavior for
// the gitignored-file case.
func copyFilePreserving(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		_ = os.Remove(dst)
		return os.Symlink(target, dst)
	}
	if info.IsDir() {
		return nil
	}
	in, err := os.Open(src) // #nosec G304 -- path derived from git ls-files within parentDir
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm()) // #nosec G304 -- repository-relative dest
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
