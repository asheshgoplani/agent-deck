package git

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// MaterializeWipFromParent copies the working-tree state of parentDir (staged
// changes, unstaged edits, and untracked files) into childDir, which must be a
// freshly-created worktree pointing at parentDir's HEAD. parentDir may be any
// path inside the parent worktree; state is materialized from the worktree root
// so copied paths remain repository-relative. When includeIgnored is true,
// gitignored files are also copied.
//
// Contract:
//   - parentDir is treated read-only — no stash push, no add, no index mutation.
//   - childDir's `git status --porcelain` becomes equal to parentDir's
//     `git status --porcelain` after this call.
//   - Refuses to run when parent is in mid-rebase / merge / cherry-pick /
//     revert / bisect.
//
// Implements issue #1029 (--with-state / --with-state-and-gitignored).
func MaterializeWipFromParent(parentDir, childDir string, includeIgnored bool) error {
	parentRoot, err := GetRepoRoot(parentDir)
	if err != nil {
		return fmt.Errorf("resolve parent worktree root: %w", err)
	}

	if err := refuseUnsafeParentState(parentRoot); err != nil {
		return err
	}

	// 1. Apply parent's STAGED diff (vs HEAD) into child's index + working tree.
	if err := applyDiffFromParent(parentRoot, childDir, true /* cached */); err != nil {
		return fmt.Errorf("materialize staged: %w", err)
	}

	// 2. Apply parent's UNSTAGED diff (working tree vs index) into child's
	//    working tree only.
	if err := applyDiffFromParent(parentRoot, childDir, false /* cached */); err != nil {
		return fmt.Errorf("materialize unstaged: %w", err)
	}

	// 3. Copy untracked files (and gitignored on opt-in).
	if err := copyUntrackedFromParent(parentRoot, childDir, includeIgnored); err != nil {
		return fmt.Errorf("materialize untracked: %w", err)
	}

	return nil
}

// refuseUnsafeParentState returns a non-nil error if parentDir is in the middle
// of a rebase, merge, cherry-pick, revert, or bisect. The presence of any of
// these state files means materializing WIP could conflict with the in-flight
// operation.
func refuseUnsafeParentState(parentDir string) error {
	gitDir, err := gitDirOf(parentDir)
	if err != nil {
		return err
	}
	checks := []struct {
		path string
		kind string
	}{
		{filepath.Join(gitDir, "MERGE_HEAD"), "merge"},
		{filepath.Join(gitDir, "CHERRY_PICK_HEAD"), "cherry-pick"},
		{filepath.Join(gitDir, "REVERT_HEAD"), "revert"},
		{filepath.Join(gitDir, "BISECT_LOG"), "bisect"},
		{filepath.Join(gitDir, "rebase-merge"), "rebase"},
		{filepath.Join(gitDir, "rebase-apply"), "rebase"},
	}
	for _, c := range checks {
		if _, err := os.Stat(c.path); err == nil {
			return fmt.Errorf("parent is in mid-%s; resolve it before forking with --with-state", c.kind)
		}
	}
	return nil
}

func gitDirOf(dir string) (string, error) {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--git-dir")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("rev-parse --git-dir: %w", err)
	}
	gitDir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(dir, gitDir)
	}
	return gitDir, nil
}

// applyDiffFromParent runs `git diff [--cached] --binary` in parentDir, pipes
// it through `git apply [--cached --index | --3way]` in childDir. An empty
// diff is a no-op.
func applyDiffFromParent(parentDir, childDir string, cached bool) error {
	diffArgs := []string{"-C", parentDir, "diff", "--binary", "--no-color"}
	if cached {
		diffArgs = append(diffArgs, "--cached")
	}
	diffCmd := exec.Command("git", diffArgs...)
	var stdout, stderr bytes.Buffer
	diffCmd.Stdout = &stdout
	diffCmd.Stderr = &stderr
	if err := diffCmd.Run(); err != nil {
		return fmt.Errorf("git diff: %w: %s", err, stderr.String())
	}
	if stdout.Len() == 0 {
		return nil
	}

	applyArgs := []string{"-C", childDir, "apply", "--binary"}
	if cached {
		// --index (without --cached) writes both index and working tree, so
		// the staged file content ends up in both — which is what makes
		// `git status` show `A  file` (staged-added) instead of `AD file`
		// (staged-added, deleted in working tree).
		applyArgs = append(applyArgs, "--index")
	}
	applyCmd := exec.Command("git", applyArgs...)
	applyCmd.Stdin = &stdout
	var applyErr bytes.Buffer
	applyCmd.Stderr = &applyErr
	if err := applyCmd.Run(); err != nil {
		return fmt.Errorf("git apply: %w: %s", err, applyErr.String())
	}
	return nil
}

// copyUntrackedFromParent enumerates parent's untracked (and optionally
// gitignored) files and copies them to childDir, preserving file mode and
// symlink target.
func copyUntrackedFromParent(parentDir, childDir string, includeIgnored bool) error {
	lsArgs := []string{"-C", parentDir, "ls-files", "--others", "-z", "--exclude-standard"}
	if includeIgnored {
		// --ignored alone with --exclude-standard lists only ignored entries;
		// to capture *both* non-ignored untracked AND ignored, we run two
		// passes and union them — `--ignored` flips the filter, it doesn't
		// add to it.
		nonIgnored, err := runListZ(lsArgs...)
		if err != nil {
			return err
		}
		ignored, err := runListZ("-C", parentDir, "ls-files", "--others", "-z",
			"--ignored", "--exclude-standard")
		if err != nil {
			return err
		}
		return copyEachFile(parentDir, childDir, append(nonIgnored, ignored...))
	}
	files, err := runListZ(lsArgs...)
	if err != nil {
		return err
	}
	return copyEachFile(parentDir, childDir, files)
}

func runListZ(args ...string) ([]string, error) {
	cmd := exec.Command("git", args...)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("git ls-files: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, err
	}
	raw := strings.TrimRight(string(out), "\x00")
	if raw == "" {
		return nil, nil
	}
	return strings.Split(raw, "\x00"), nil
}

func copyEachFile(srcDir, dstDir string, rels []string) error {
	for _, rel := range rels {
		if rel == "" {
			continue
		}
		src := filepath.Join(srcDir, rel)
		dst := filepath.Join(dstDir, rel)
		if err := copyOneFile(src, dst); err != nil {
			return fmt.Errorf("copy %s: %w", rel, err)
		}
	}
	return nil
}

func copyOneFile(src, dst string) error {
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
		// Replace any existing entry — child is fresh, but be defensive.
		_ = os.Remove(dst)
		return os.Symlink(target, dst)
	}
	if info.IsDir() {
		// `git ls-files --others` doesn't emit directories directly, but
		// untracked submodules can appear. Skip safely.
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
