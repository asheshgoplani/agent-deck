package agentpaths

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// privateTempCache remembers the directory SecureTempDir settled on for each
// (TempDir, prefix) pair, so a refused per-user path costs one os.MkdirTemp
// per process instead of one per call, and the caller's notice fires once.
var privateTempCache sync.Map // key string -> string

// SecureTempDir returns a directory under os.TempDir() that only the current
// user can use. It is the fallback for state that would otherwise land in a
// shared, world-writable temp directory.
//
// The preferred path is the stable "<TempDir>/<prefix>-<uid>", so separate
// agent-deck processes of the same user (TUI and CLI) agree on it. It is
// created 0700 when missing. An existing one is accepted only when it is a
// real directory (not a symlink) owned by the current uid; a looser mode on a
// directory we own is tightened to 0700. Anything else (a symlink, a file, a
// directory another user planted) is refused and a fresh os.MkdirTemp
// directory is used instead for the rest of the process.
//
// note is non-empty only on the call that tightened the mode or refused the
// stable path, so the caller can log it once.
func SecureTempDir(prefix string) (dir string, note string) {
	base := os.TempDir()
	stable := filepath.Join(base, fmt.Sprintf("%s-%d", prefix, os.Getuid()))
	key := base + "\x00" + prefix
	if cached, ok := privateTempCache.Load(key); ok {
		// Re-check the cached path: it may have been removed (tmp cleaners).
		if tightened, err := ensurePrivateDir(cached.(string)); err == nil {
			if tightened {
				return cached.(string), fmt.Sprintf("tightened %s to mode 0700", cached.(string))
			}
			return cached.(string), ""
		}
		privateTempCache.Delete(key)
	}

	tightened, err := ensurePrivateDir(stable)
	if err == nil {
		privateTempCache.Store(key, stable)
		if tightened {
			return stable, fmt.Sprintf("tightened %s to mode 0700", stable)
		}
		return stable, ""
	}

	fresh, mkErr := os.MkdirTemp(base, fmt.Sprintf("%s-%d-*", prefix, os.Getuid()))
	if mkErr != nil {
		// Nothing safe is available. Return the stable path anyway; callers
		// that write there will hit the same refusal and surface an error.
		return stable, fmt.Sprintf("refused %s (%v) and could not create a private temp dir: %v", stable, err, mkErr)
	}
	privateTempCache.Store(key, fresh)
	return fresh, fmt.Sprintf("refused %s (%v); using private %s", stable, err, fresh)
}

// ensurePrivateDir creates dir 0700 if missing, then verifies it is a real
// directory owned by the current user with no group/other permissions,
// tightening the mode when we own it. tightened reports a mode change.
func ensurePrivateDir(dir string) (tightened bool, err error) {
	if err := os.Mkdir(dir, 0o700); err != nil && !os.IsExist(err) {
		return false, err
	}
	return checkPrivateDir(dir)
}
