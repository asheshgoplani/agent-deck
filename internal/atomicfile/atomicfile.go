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
	} else if !os.IsNotExist(evalErr) {
		// A non-"not exist" failure (symlink loop / ELOOP, permission, ...) must
		// NOT fall through to a write that could clobber a link. Refuse instead.
		return "", fmt.Errorf("atomicfile: resolve symlink %s: %w", path, evalErr)
	}
	// Dangling chain: the chain ends at a missing leaf. Walk it hop-by-hop to
	// that leaf so the write lands past every symlink — a single Readlink would
	// stop at an intermediate link and the rename would clobber it.
	return resolveDanglingLeaf(path)
}

// resolveDanglingLeaf walks a chain of symlinks whose final target does not yet
// exist, following each hop (relative links resolved against the link's own
// directory) until it reaches a non-symlink or a missing leaf. It refuses
// symlink loops rather than clobbering a link.
func resolveDanglingLeaf(path string) (string, error) {
	const maxHops = 40
	current := path
	visited := make(map[string]bool, maxHops)
	for hop := 0; hop < maxHops; hop++ {
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				return current, nil // missing leaf — safe to create here
			}
			return "", fmt.Errorf("atomicfile: lstat %s: %w", current, err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			return current, nil // real non-symlink leaf
		}
		if visited[current] {
			return "", fmt.Errorf("atomicfile: symlink loop at %s", current)
		}
		visited[current] = true

		next, err := os.Readlink(current)
		if err != nil {
			return "", fmt.Errorf("atomicfile: readlink %s: %w", current, err)
		}
		if !filepath.IsAbs(next) {
			next = filepath.Join(filepath.Dir(current), next)
		}
		current = next
	}
	return "", fmt.Errorf("atomicfile: too many symlink hops resolving %s", path)
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
