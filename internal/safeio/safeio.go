// Package safeio centralizes the "never delete or overwrite a user artifact
// without a recovery path" primitives that agent-deck grew piecemeal after a
// recurring class of data-loss incidents (the 2026-06-04 profile-index wipe and
// its 2025-12-11 / 2026-04-17 predecessors).
//
// Three bespoke guards previously lived at three sites:
//   - #1383/S1+S2 — SaveInstances refused an empty-payload sweep and backed up
//     the DB before a large drop; SaveUserConfig refused dropping populated
//     sections and backed up config.toml before the atomic rename.
//   - #1429 — the conductor dir relocation copied + verified meta.json before
//     RemoveAll'ing the source, refusing to delete the only copy.
//   - #1449 — worktree teardown refused to remove a worktree still referenced by
//     a sibling session.
//
// safeio distills those into two composable primitives:
//
//	SafeOverwrite — backup → atomic durable write, with pluggable refusal guards
//	               (refuse-empty, refuse-section-drop) checked BEFORE any mutation.
//	SafeRemove    — refuse to delete a path that is still referenced / is the only
//	               copy (via a pluggable predicate), so a destructive RemoveAll is
//	               structurally guarded rather than guarded ad-hoc at each caller.
//
// The package depends only on the standard library and internal/atomicfile, so
// any internal package can adopt it without an import cycle.
package safeio

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/asheshgoplani/agent-deck/internal/atomicfile"
)

// ErrRefusingEmptyOverwrite is returned by SafeOverwrite when Options.RefuseEmpty
// is set and the call would replace a NON-empty file with empty bytes. An empty
// payload over an already-empty (or absent) file is allowed — only the
// destructive empty-over-populated case is refused.
var ErrRefusingEmptyOverwrite = errors.New("safeio: refusing to overwrite a populated file with empty content")

// ErrStillReferenced is returned by SafeRemove when Options.StillReferenced
// reports the path is in use (e.g. a worktree shared by a sibling session, or a
// source whose only copy this would delete).
var ErrStillReferenced = errors.New("safeio: refusing to remove a still-referenced path")

// Options configures SafeOverwrite.
type Options struct {
	// Perm is the file mode for the written file. Defaults to 0o600 when zero,
	// matching the private treatment of config.toml / state.db backups.
	Perm os.FileMode

	// RefuseEmpty refuses to replace a non-empty file with empty bytes. This is
	// the generalization of the SaveInstances empty-sweep guard for file writes:
	// a stray empty payload over a populated artifact is almost always a bug.
	RefuseEmpty bool

	// Guard, when non-nil, is called with (oldContent, newContent) BEFORE any
	// backup or write. A non-nil error aborts the overwrite with the file
	// untouched and no backup made. oldContent is nil when the target does not
	// exist yet. This is the extension point for caller-specific refusals such
	// as the SaveUserConfig section-drop guard. The error is returned verbatim
	// (wrap with %w upstream for errors.Is matching).
	Guard func(oldContent, newContent []byte) error

	// SkipBackup disables the pre-write .bak copy. Off by default — the backup
	// is the recovery net the whole package exists to provide.
	SkipBackup bool
}

// RemoveOptions configures SafeRemove.
type RemoveOptions struct {
	// StillReferenced, when non-nil, is consulted before removal. Returning
	// (true, reason) refuses the removal with ErrStillReferenced (reason is
	// folded into the error message). This generalizes the #1449 sibling-use
	// guard and the #1429 "destination still aliases the source" defense: the
	// caller supplies the reference check; safeio enforces the refusal.
	StillReferenced func(path string) (referenced bool, reason string)
}

// Backup copies an existing file to "<path>.bak" via a temp-file + rename so the
// .bak is never torn. It returns the backup path on success, "" when the source
// does not exist (a no-op), or an error. The backup is written 0o600 to keep it
// as private as the originals it protects (config.toml / state.db may carry
// session metadata).
func Backup(path string) (string, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil // nothing to back up
		}
		return "", err
	}
	bak := path + ".bak"
	// Temp-write + rename so the .bak is never torn. We deliberately do NOT use
	// atomicfile.WriteFile here: it RESOLVES a symlink at the destination and
	// writes through to the link's target, whereas a backup must always be a
	// plain regular file at "<path>.bak" — replacing any pre-existing .bak (even
	// a stray symlink) rather than following it to clobber an unrelated file.
	//
	// The staging file uses os.CreateTemp (O_EXCL, randomized name) in the
	// destination's directory: a predictable "<bak>.tmp" path could itself be a
	// pre-placed symlink that os.WriteFile would follow to clobber an unrelated
	// target. CreateTemp never follows a symlink and never collides. 0o600 keeps
	// the snapshot as private as the original.
	dir := filepath.Dir(bak)
	tmpf, err := os.CreateTemp(dir, ".safeio-bak-*")
	if err != nil {
		return "", err
	}
	tmp := tmpf.Name()
	if err := tmpf.Chmod(0o600); err != nil {
		_ = tmpf.Close()
		_ = os.Remove(tmp)
		return "", err
	}
	if _, err := tmpf.Write(src); err != nil {
		_ = tmpf.Close()
		_ = os.Remove(tmp)
		return "", err
	}
	if err := tmpf.Close(); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, bak); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return bak, nil
}

// SafeOverwrite replaces path's contents with newBytes, guarded against the
// destructive-write footguns. Order of operations:
//
//  1. Read the existing content (nil if the file is new).
//  2. Run Options.Guard(old, new) — refuse with the guard's error, untouched.
//  3. RefuseEmpty: refuse empty-over-populated with ErrRefusingEmptyOverwrite.
//  4. Back up the existing file to <path>.bak (unless SkipBackup or new file).
//  5. Atomically + durably write newBytes (temp → fsync → rename → dir fsync).
//
// All refusals happen BEFORE any backup or write, so a refused call leaves both
// the original and any prior .bak exactly as they were.
func SafeOverwrite(path string, newBytes []byte, opts Options) error {
	perm := opts.Perm
	if perm == 0 {
		perm = 0o600
	}

	old, existed, err := readIfExists(path)
	if err != nil {
		return err
	}

	// 1. Caller-supplied guard runs first and can veto on any criterion.
	if opts.Guard != nil {
		if gErr := opts.Guard(old, newBytes); gErr != nil {
			return gErr
		}
	}

	// 2. Empty-over-populated refusal (generalized empty-sweep guard).
	if opts.RefuseEmpty && len(newBytes) == 0 && existed && len(old) > 0 {
		return fmt.Errorf("%w: %s had %d bytes on disk", ErrRefusingEmptyOverwrite, path, len(old))
	}

	// 3. Backup the existing file before mutating it.
	if existed && !opts.SkipBackup {
		if _, bErr := Backup(path); bErr != nil {
			// Best-effort: the caller asked to save, and the insurance copy must
			// not become a new hard failure mode. Surface it so callers may log,
			// but do not abort the write.
			return fmt.Errorf("safeio: backup before overwrite of %s failed: %w", path, bErr)
		}
	}

	// 4. Atomic + durable write.
	return atomicfile.WriteFileDurable(path, newBytes, perm)
}

// SafeRemove removes path (file or directory tree) only when it is safe to do
// so. It refuses an empty path (guarding against a stray RemoveAll("")), is a
// no-op on a non-existent path, and consults Options.StillReferenced before
// deleting — refusing with ErrStillReferenced when the path is in use.
func SafeRemove(path string, opts RemoveOptions) error {
	if path == "" {
		return errors.New("safeio: refusing to remove an empty path")
	}
	if _, err := os.Lstat(path); err != nil {
		if os.IsNotExist(err) {
			return nil // already gone
		}
		return err
	}
	if opts.StillReferenced != nil {
		if referenced, reason := opts.StillReferenced(path); referenced {
			if reason == "" {
				reason = "still in use"
			}
			return fmt.Errorf("%w: %s (%s)", ErrStillReferenced, path, reason)
		}
	}
	return os.RemoveAll(path)
}

// readIfExists returns the file's content, whether it existed, and any non
// not-exist error. A missing file yields (nil, false, nil).
func readIfExists(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return data, true, nil
}
