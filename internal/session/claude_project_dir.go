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

// MigrateClaudeProjectDir moves <srcConfigDir>/projects/<oldSlug>/ to
// <dstConfigDir>/projects/<newSlug>/ so `claude --resume` in the new project
// location picks up the prior conversation history. srcConfigDir/dstConfigDir
// are the session's resolved Claude config dir before/after the move
// (GetClaudeConfigDirForInstance / GetClaudeConfigDirForInstanceInGroup),
// which differ whenever the move crosses a per-account or per-group
// config_dir boundary — e.g. `session move --group <target>` retargeting to
// a group with its own [groups."<target>".claude].config_dir (#2086,
// cross-config-dir follow-up). Pass the same value for both for a same-dir
// move (the common case).
//
//   - No-op when source and destination fully coincide (same config dir,
//     same slug), or when the source dir doesn't exist (fresh sessions have
//     nothing to migrate).
//   - If copy=true, the source is preserved and the destination gets a
//     recursive copy — useful when other sessions still reference oldPath.
//   - If copy=false (default), rename is attempted; falls back to copy+remove
//     across filesystems/config dirs.
//   - Errors when the destination already exists to avoid silent overwrite.
//
// Returns how many files were migrated so callers can report a real count
// instead of claiming success over an empty migration.
func MigrateClaudeProjectDir(srcConfigDir, dstConfigDir, oldProjectPath, newProjectPath string, copy bool) (int, error) {
	if srcConfigDir == "" || dstConfigDir == "" || oldProjectPath == "" || newProjectPath == "" {
		return 0, fmt.Errorf("migrate claude project dir: config dir/old/new path required")
	}
	oldSlug := SlugifyClaudeProjectPath(oldProjectPath)
	newSlug := SlugifyClaudeProjectPath(newProjectPath)
	if srcConfigDir == dstConfigDir && oldSlug == newSlug {
		return 0, nil
	}

	dstProjectsDir := filepath.Join(dstConfigDir, "projects")
	srcDir := filepath.Join(srcConfigDir, "projects", oldSlug)
	dstDir := filepath.Join(dstProjectsDir, newSlug)

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

	if err := os.MkdirAll(dstProjectsDir, 0o755); err != nil {
		return 0, fmt.Errorf("migrate claude project dir: mkdir projects root: %w", err)
	}

	if !copy {
		if err := os.Rename(srcDir, dstDir); err == nil {
			return verifyMigratedCount(dstDir, count)
		}
		// Fall through to copy+remove for cross-filesystem/cross-config-dir case.
	}

	if err := copyDirRecursive(srcDir, dstDir); err != nil {
		return 0, fmt.Errorf("migrate claude project dir: copy: %w", err)
	}
	if !copy {
		if err := os.RemoveAll(srcDir); err != nil {
			return 0, fmt.Errorf("migrate claude project dir: remove source after copy: %w", err)
		}
	}
	return verifyMigratedCount(dstDir, count)
}

// verifyMigratedCount re-counts the files that actually landed at dstDir and
// refuses to report success unless they match what was counted at the source
// before the move. A caller must never claim history was migrated when the
// destination doesn't hold it — that silence is how #2086 went unnoticed, and
// a cross-config-dir move adds a new way for a rename/copy to land partially
// or in the wrong place.
func verifyMigratedCount(dstDir string, wantCount int) (int, error) {
	gotCount, err := countRegularFiles(dstDir)
	if err != nil {
		return 0, fmt.Errorf("migrate claude project dir: verify destination: %w", err)
	}
	if gotCount != wantCount {
		return 0, fmt.Errorf("migrate claude project dir: verification failed: %d file(s) at source, %d found at destination %s", wantCount, gotCount, dstDir)
	}
	return gotCount, nil
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
