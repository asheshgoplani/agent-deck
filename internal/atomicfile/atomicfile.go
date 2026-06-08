// Package atomicfile provides symlink-preserving atomic file writes for
// user-managed config files.
//
// WriteFile writes to a temp file in the destination's directory and renames it
// into place. When the destination path is a symlink, the real target is
// resolved first and the write lands on that target, so the symlink itself is
// preserved — a dotfiles-managed ~/.claude/settings.json stays a symlink.
//
// This is the OPPOSITE of the intentional behavior in
// internal/credrefresh.atomicWriteFile, internal/session.atomicWriteFile
// (worker_scratch.go), and internal/session.writeFileDurable (inbox.go), which
// replace a symlink at the path with a regular file. Those helpers target
// agent-deck's own internal state and must not be consolidated here.
//
// The package imports only the standard library so any internal package can
// depend on it without import cycles.
package atomicfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// WriteFile atomically writes data to path, preserving a symlink AT path. If
// path is a symlink, the write targets the resolved real file and the link is
// left intact. For a regular or new file it behaves like a temp-file + rename.
// perm is applied to the written file.
//
// Writing across filesystems via a symlink is unsupported: os.Rename cannot
// cross filesystems, so a symlink whose target lives on a different filesystem
// than the link returns an explicit error rather than silently falling back to
// a non-atomic write.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	target, err := resolveTarget(path)
	if err != nil {
		return err
	}
	return writeAtomic(target, data, perm)
}

// resolveTarget returns the real file path that should be written. For a regular
// or non-existent path it returns the path unchanged. For a symlink it returns
// the resolved target so the link is preserved by a later rename onto the
// target (rename onto the link itself would replace the link).
func resolveTarget(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return path, nil
		}
		return "", fmt.Errorf("atomicfile: lstat %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return path, nil
	}
	// Symlink: resolve to the real target. EvalSymlinks follows the full chain
	// (including intermediate symlinked directories) when the target exists.
	if resolved, evalErr := filepath.EvalSymlinks(path); evalErr == nil {
		return resolved, nil
	}
	// Dangling symlink (target does not exist yet): resolve the raw link value
	// relative to the link's own directory.
	link, err := os.Readlink(path)
	if err != nil {
		return "", fmt.Errorf("atomicfile: readlink %s: %w", path, err)
	}
	if !filepath.IsAbs(link) {
		link = filepath.Join(filepath.Dir(path), link)
	}
	return link, nil
}

// writeAtomic writes data to target via a uniquely-named temp file in target's
// directory, then renames it onto target. The temp lives in the target's
// directory so the rename stays on one filesystem.
func writeAtomic(target string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("atomicfile: mkdir %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".atomicfile-*")
	if err != nil {
		return fmt.Errorf("atomicfile: create temp in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()

	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("atomicfile: chmod temp: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("atomicfile: write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("atomicfile: close temp: %w", err)
	}

	if err := os.Rename(tmpPath, target); err != nil {
		if errors.Is(err, syscall.EXDEV) {
			return fmt.Errorf("atomicfile: cross-filesystem symlink not supported for %s: %w", target, err)
		}
		return fmt.Errorf("atomicfile: rename to %s: %w", target, err)
	}
	committed = true
	return nil
}
