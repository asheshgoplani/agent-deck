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

// MigrateClaudeProjectDir moves <configDir>/projects/<oldSlug>/ to
// <configDir>/projects/<newSlug>/ so `claude --resume` in the new project
// location picks up the prior conversation history. configDir is the session's
// resolved Claude config dir (GetClaudeConfigDirForInstance), which is
// ~/.claude only when no per-account or per-group override applies (#2086).
//
//   - No-op when the source dir doesn't exist (fresh sessions have nothing).
//   - If copy=true, the source is preserved and the destination gets a
//     recursive copy — useful when other sessions still reference oldPath.
//   - If copy=false (default), rename is attempted; falls back to copy+remove
//     across filesystems.
//   - Errors when the destination already exists to avoid silent overwrite.
//
// Returns how many files were migrated so callers can report a real count
// instead of claiming success over an empty migration.
func MigrateClaudeProjectDir(configDir, oldProjectPath, newProjectPath string, copy bool) (int, error) {
	if configDir == "" || oldProjectPath == "" || newProjectPath == "" {
		return 0, fmt.Errorf("migrate claude project dir: config dir/old/new path required")
	}
	if oldProjectPath == newProjectPath {
		return 0, nil
	}
	oldSlug := SlugifyClaudeProjectPath(oldProjectPath)
	newSlug := SlugifyClaudeProjectPath(newProjectPath)
	if oldSlug == newSlug {
		return 0, nil
	}

	projectsDir := filepath.Join(configDir, "projects")
	srcDir := filepath.Join(projectsDir, oldSlug)
	dstDir := filepath.Join(projectsDir, newSlug)

	if _, err := os.Stat(srcDir); os.IsNotExist(err) {
		return 0, nil
	}
	if _, err := os.Stat(dstDir); err == nil {
		return 0, fmt.Errorf("migrate claude project dir: destination already exists at %s", dstDir)
	}

	count, err := countRegularFiles(srcDir)
	if err != nil {
		return 0, fmt.Errorf("migrate claude project dir: count source files: %w", err)
	}

	if err := os.MkdirAll(projectsDir, 0o755); err != nil {
		return 0, fmt.Errorf("migrate claude project dir: mkdir projects root: %w", err)
	}

	if !copy {
		if err := os.Rename(srcDir, dstDir); err == nil {
			return count, nil
		}
		// Fall through to copy+remove for cross-filesystem case.
	}

	if err := copyDirRecursive(srcDir, dstDir); err != nil {
		return 0, fmt.Errorf("migrate claude project dir: copy: %w", err)
	}
	if !copy {
		if err := os.RemoveAll(srcDir); err != nil {
			return 0, fmt.Errorf("migrate claude project dir: remove source after copy: %w", err)
		}
	}
	return count, nil
}

// countRegularFiles counts non-directory entries under dir, recursively.
func countRegularFiles(dir string) (int, error) {
	count := 0
	err := filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			count++
		}
		return nil
	})
	return count, err
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
			// #nosec G122 -- walk callback operating on a Claude-managed
			// project-dir tree owned by the user, not on attacker-controlled
			// input. TOCTOU symlink races here only affect this user's own dir.
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
