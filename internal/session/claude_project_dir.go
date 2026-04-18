package session

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// SlugifyClaudeProjectPath converts a project path to Claude Code's encoded
// directory name format. Claude stores conversation history in
// ~/.claude/projects/<slug>/ where <slug> is the project path with `/` and
// `.` replaced by `-`.
//
// Example: /home/user/foo.v1 → -home-user-foo-v1
//
// Kept in the session package so both the costs sync path and the CLI
// `session move` command share one implementation (issue #414).
func SlugifyClaudeProjectPath(projectPath string) string {
	projectPath = strings.TrimRight(projectPath, "/")
	slug := strings.ReplaceAll(projectPath, "/", "-")
	slug = strings.ReplaceAll(slug, ".", "-")
	return slug
}

// MigrateClaudeProjectDir moves ~/.claude/projects/<oldSlug>/ to
// ~/.claude/projects/<newSlug>/ so `claude --resume` in the new project
// location picks up the prior conversation history.
//
//   - No-op when the source dir doesn't exist (fresh sessions have nothing).
//   - If copy=true, the source is preserved and the destination gets a
//     recursive copy — useful when other sessions still reference oldPath.
//   - If copy=false (default), rename is attempted; falls back to copy+remove
//     across filesystems.
//   - Errors when the destination already exists to avoid silent overwrite.
func MigrateClaudeProjectDir(home, oldProjectPath, newProjectPath string, copy bool) error {
	if home == "" || oldProjectPath == "" || newProjectPath == "" {
		return fmt.Errorf("migrate claude project dir: home/old/new path required")
	}
	if oldProjectPath == newProjectPath {
		return nil
	}
	oldSlug := SlugifyClaudeProjectPath(oldProjectPath)
	newSlug := SlugifyClaudeProjectPath(newProjectPath)
	if oldSlug == newSlug {
		return nil
	}

	projectsDir := filepath.Join(home, ".claude", "projects")
	srcDir := filepath.Join(projectsDir, oldSlug)
	dstDir := filepath.Join(projectsDir, newSlug)

	if _, err := os.Stat(srcDir); os.IsNotExist(err) {
		return nil
	}
	if _, err := os.Stat(dstDir); err == nil {
		return fmt.Errorf("migrate claude project dir: destination already exists at %s", dstDir)
	}

	if err := os.MkdirAll(projectsDir, 0o755); err != nil {
		return fmt.Errorf("migrate claude project dir: mkdir projects root: %w", err)
	}

	if !copy {
		if err := os.Rename(srcDir, dstDir); err == nil {
			return nil
		}
		// Fall through to copy+remove for cross-filesystem case.
	}

	if err := copyDirRecursive(srcDir, dstDir); err != nil {
		return fmt.Errorf("migrate claude project dir: copy: %w", err)
	}
	if !copy {
		if err := os.RemoveAll(srcDir); err != nil {
			return fmt.Errorf("migrate claude project dir: remove source after copy: %w", err)
		}
	}
	return nil
}

// copyDirRecursive copies a directory tree from src to dst, preserving file
// contents and permissions. Symlinks are copied as links.
func copyDirRecursive(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)

		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode()&os.ModePerm)
		}
		return copyFileWithPerm(path, target, info.Mode()&os.ModePerm)
	})
}

func copyFileWithPerm(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
