// Package atomicfile provides symlink-preserving atomic file writes for
// user-managed config files.
//
// WriteFile writes to a temp file in the destination's directory and renames it
// into place. When the destination path is a symlink, the real target is
// resolved first and the write lands on that target, so the symlink itself is
// preserved — a dotfiles-managed ~/.claude/settings.json stays a symlink.
//
// This is the OPPOSITE of the intentional behavior in
// internal/session.atomicWriteFile (worker_scratch.go) and
// internal/session.writeFileDurable (inbox.go), which replace a symlink at
// the path with a regular file. Those helpers target agent-deck's own internal
// state and must not be consolidated here.
//
// The package imports only the standard library so any internal package can
// depend on it without import cycles.
package atomicfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	return writeAtomic(target, data, perm, false)
}

// WriteFileDurable behaves like WriteFile but adds crash durability: the temp
// file is fsync'd before the rename and the target's parent directory is
// best-effort fsync'd after, so both the file contents and the rename survive a
// power loss. Use it for files where a torn or lost write on crash matters (for
// example agent-deck's own config.toml). The symlink-preserving resolution is
// identical to WriteFile.
func WriteFileDurable(path string, data []byte, perm os.FileMode) error {
	target, err := resolveTarget(path)
	if err != nil {
		return err
	}
	return writeAtomic(target, data, perm, true)
}

// WriteFileWithin is WriteFile for a file inside a directory tree someone else
// may control, such as a cloned repository. The real file it would write, after
// following every symlink on the way (the file itself and its parent
// directories), must lie under root; a link that leads outside is refused
// instead of followed, so a repository cannot point its config file at an
// unrelated file of the user. A link that stays inside root is preserved like
// WriteFile does.
func WriteFileWithin(root, path string, data []byte, perm os.FileMode) error {
	target, err := resolveTarget(path)
	if err != nil {
		return err
	}
	if err := requireWithin(root, path, target); err != nil {
		return err
	}
	return writeAtomic(target, data, perm, false)
}

// CheckWithin reports the error WriteFileWithin(root, path, ...) would refuse
// with, without writing. Callers that open a file beside path first (a lock)
// check both paths before touching either.
func CheckWithin(root, path string) error {
	target, err := resolveTarget(path)
	if err != nil {
		return err
	}
	return requireWithin(root, path, target)
}

// ErrOutsideRoot is wrapped by the errors WriteFileWithin and CheckWithin
// return when the real file lies outside root, so callers can add advice.
var ErrOutsideRoot = errors.New("resolves outside the allowed directory")

// requireWithin reports an error unless target, with every existing ancestor
// resolved through symlinks, lies under root (also resolved). Components that
// do not exist yet are kept as written; writeAtomic creates them as real
// directories. A missing component that is really a dangling symlink is also
// kept as written: MkdirAll then fails on it (mkdir does not follow a link),
// so nothing is created at the link's destination.
func requireWithin(root, path, target string) error {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("atomicfile: resolve %s: %w", root, err)
	}
	existing := filepath.Dir(target)
	missing := filepath.Base(target)
	realDir, err := filepath.EvalSymlinks(existing)
	for err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("atomicfile: resolve %s: %w", existing, err)
		}
		// A dangling symlink is not a missing directory: following it could
		// create directories wherever it points. Refuse it here rather than
		// rely on MkdirAll failing on it later. existing is a cleaned absolute
		// path, so it holds ".." only inside a name; such a path skips this
		// extra check and still cannot create anything through the link,
		// since mkdir does not follow it. The guard is also the traversal
		// check CodeQL go/path-injection recognises.
		if !strings.Contains(existing, "..") {
			if fi, lerr := os.Lstat(existing); lerr == nil && fi.Mode()&os.ModeSymlink != 0 {
				dest, _ := os.Readlink(existing)
				return fmt.Errorf("atomicfile: refusing to write %s: %s is a symlink to the missing path %s",
					target, existing, dest)
			}
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return fmt.Errorf("atomicfile: resolve %s: %w", target, err)
		}
		missing = filepath.Join(filepath.Base(existing), missing)
		existing = parent
		realDir, err = filepath.EvalSymlinks(existing)
	}
	rel, err := filepath.Rel(realRoot, filepath.Join(realDir, missing))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		real := filepath.Join(realDir, missing)
		if filepath.Clean(path) != filepath.Clean(target) {
			return fmt.Errorf("atomicfile: refusing to write %s: it is a symlink to %s, outside %s: %w",
				path, real, root, ErrOutsideRoot)
		}
		return fmt.Errorf("atomicfile: refusing to write %s: a symlinked parent directory puts it at %s, outside %s: %w",
			path, real, root, ErrOutsideRoot)
	}
	return nil
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
// directory so the rename stays on one filesystem. When durable is set the temp
// is fsync'd before the rename and the parent directory is best-effort fsync'd
// after, so the contents and the rename survive a crash.
func writeAtomic(target string, data []byte, perm os.FileMode, durable bool) error {
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
	if durable {
		if err := tmp.Sync(); err != nil {
			_ = tmp.Close()
			return fmt.Errorf("atomicfile: fsync temp: %w", err)
		}
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

	if durable {
		// Best-effort directory fsync so the rename itself is durable. Some
		// filesystems reject directory fsync (EINVAL/ENOTSUP); the data fsync +
		// atomic rename already give the core guarantee, so ignore the error.
		if d, derr := os.Open(dir); derr == nil {
			_ = d.Sync()
			_ = d.Close()
		}
	}
	return nil
}
