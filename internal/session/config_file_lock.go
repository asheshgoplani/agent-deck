package session

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// configFileMu serializes mutations to one config file WITHIN this process,
// keyed by the file's absolute path so two writers of the same file wait for
// each other while writers of different files do not contend.
//
// Cross-process serialization comes from an advisory flock on a sibling
// `<path>.lock`. Together the two layers cover both cases that matter for a
// read-modify-write over a config file agent-deck does not own: the web server
// and the TUI racing inside one binary, and a separate `agent-deck` process
// racing the running one.
//
// Atomic replacement alone does NOT cover this. Two writers that each read,
// modify and rename produce a last-writer-wins result in which the loser's
// change is silently gone — the rename is atomic, the read-modify-write around
// it is not. The lock has to span the whole sequence.
//
// This is the one implementation of that rule. It was factored out of
// hermes_hooks.go and codex_trust.go, which each carried a private copy;
// mcp_catalog.go needed a third and a third private copy is how a shared rule
// drifts. Both callers now delegate here.
var configFileMu sync.Map // map[string]*sync.Mutex

// configFileLock holds both lock layers; Release unwinds them in reverse.
type configFileLock struct {
	inProc *sync.Mutex
	file   *os.File
}

// Release drops the advisory flock and the in-process mutex. Safe on nil.
func (l *configFileLock) Release() {
	if l == nil {
		return
	}
	if l.file != nil {
		// Best-effort: LOCK_UN errors are non-actionable; Close drops the fd,
		// which also releases the advisory lock.
		_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
		_ = l.file.Close()
	}
	if l.inProc != nil {
		l.inProc.Unlock()
	}
}

// acquireConfigFileLock takes the in-process mutex for configPath, then an
// exclusive advisory flock on `<configPath>.lock`. Callers MUST defer
// Release(). `what` names the config in error messages ("claude config").
//
// The lockfile is a sibling of the config rather than a file in agent-deck's
// locks dir on purpose: the identity that must be serialized is the file
// itself. Two profiles can point CLAUDE_CONFIG_DIR at the same .claude.json,
// and a per-profile lockfile would let those two writers run concurrently over
// one file — exactly the race being closed.
//
// Not reentrant: no entry point that takes this lock calls another that takes
// it for the same path.
func acquireConfigFileLock(configPath, what string) (*configFileLock, error) {
	key := configFileLockKey(configPath)
	mIface, _ := configFileMu.LoadOrStore(key, &sync.Mutex{})
	m := mIface.(*sync.Mutex)
	m.Lock()

	lockPath := key + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		m.Unlock()
		return nil, fmt.Errorf("ensure %s lock dir: %w", what, err)
	}
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		m.Unlock()
		return nil, fmt.Errorf("open %s lock file: %w", what, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		m.Unlock()
		return nil, fmt.Errorf("flock %s: %w", what, err)
	}
	return &configFileLock{inProc: m, file: f}, nil
}

// configFileLockKey normalises a path so two spellings of the same file take
// the same in-process mutex and the same lockfile. Symlinks are deliberately
// NOT resolved: the config may not exist yet, and the sibling lockfile has to
// sit next to the path callers actually name.
func configFileLockKey(configPath string) string {
	if abs, err := filepath.Abs(configPath); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(configPath)
}
