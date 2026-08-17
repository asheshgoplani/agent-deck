package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
)

// Serialization for read-modify-write cycles on a shared config file.
//
// Every config mutation in this package is read-modify-write: load the JSON or
// TOML document, change one section, write the whole thing back. Two of those
// running at once on the same file interleave — the second reader loads a
// document that predates the first writer, then writes it back and erases the
// first writer's section. The web MCP pane and the TUI MCP dialog both write
// .claude.json, so this is reachable by a user with both open.
//
// This is the ONE implementation of that rule. acquireCodexConfigLock in
// codex_trust.go is now a thin alias over it rather than a second copy: a
// private copy of a shared rule is how the same defect keeps reappearing in
// different files.
//
// Both halves matter:
//   - the in-process mutex, keyed by path, serializes goroutines in this binary
//     (flock is per open file description, so it does NOT serialize two
//     goroutines in one process that each open the lock file);
//   - the advisory flock on a sibling `.lock` file serializes separate
//     processes, e.g. the headless web server and an interactive TUI.
//
// The lock is NOT reentrant. A caller that needs to span several writes must
// acquire once and call the *Locked variants — see WriteGlobalMCPAndClearProjectMCPs.

// configFileMu holds one mutex per config path for in-process serialization.
var configFileMu sync.Map // map[string]*sync.Mutex

// configLockAcquisitions counts successful acquisitions. Tests use it to assert
// that a multi-write operation takes the lock ONCE and spans both writes,
// rather than taking and dropping it per write — a property the end state
// cannot distinguish, because both orderings finish identically.
var configLockAcquisitions atomic.Int64

// ConfigLockAcquisitionsForTest reports the acquisition count. Test-only.
func ConfigLockAcquisitionsForTest() int64 { return configLockAcquisitions.Load() }

// ConfigFileLock is held for the whole read-modify-write cycle and released
// with Release, conventionally by defer.
type ConfigFileLock struct {
	inProc *sync.Mutex
	file   *os.File
}

// Release unlocks both halves. Safe to call on a zero value.
func (l *ConfigFileLock) Release() {
	if l == nil {
		return
	}
	if l.file != nil {
		_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
		_ = l.file.Close()
		l.file = nil
	}
	if l.inProc != nil {
		l.inProc.Unlock()
		l.inProc = nil
	}
}

// resolveConfigLockPath normalizes configPath and derives the sibling `.lock`
// path, refusing anything that would escape the config's own directory.
//
// Callers are all internal (a profile's Claude config, ~/.claude.json, a
// project's .mcp.json, a Codex config.toml), so this is defence in depth rather
// than a live injection vector — but the path is still assembled from values
// that trace back to configuration, and a lock file is created wherever it
// points. Cleaning and containment-checking here is cheap and removes the
// question. It also normalizes the mutex key, so two spellings of the same file
// share one lock instead of silently getting two.
//
// Returns the resolved config path (the mutex key) and the lock path.
func resolveConfigLockPath(configPath string) (resolved, lockPath string, err error) {
	if strings.TrimSpace(configPath) == "" {
		return "", "", fmt.Errorf("config file lock: empty path")
	}
	resolved, err = filepath.Abs(filepath.Clean(configPath))
	if err != nil {
		return "", "", fmt.Errorf("config file lock: resolve %q: %w", configPath, err)
	}

	dir := filepath.Dir(resolved)
	base := filepath.Base(resolved)
	if base == "." || base == string(os.PathSeparator) {
		return "", "", fmt.Errorf("config file lock: %q has no file name component", configPath)
	}

	lockPath = filepath.Join(dir, base+".lock")
	// The lock must sit beside the config it guards. After cleaning this can
	// only fail on a crafted name, but checking it is what makes the property
	// explicit rather than incidental.
	if !strings.HasPrefix(lockPath, dir+string(os.PathSeparator)) || filepath.Dir(lockPath) != dir {
		return "", "", fmt.Errorf("config file lock: refusing lock path %q outside config directory %q", lockPath, dir)
	}
	return resolved, lockPath, nil
}

// AcquireConfigFileLock blocks until this process and this host both hold
// exclusive access to configPath. The returned lock must be released.
func AcquireConfigFileLock(configPath string) (*ConfigFileLock, error) {
	resolved, lockPath, err := resolveConfigLockPath(configPath)
	if err != nil {
		return nil, err
	}

	mIface, _ := configFileMu.LoadOrStore(resolved, &sync.Mutex{})
	m := mIface.(*sync.Mutex)
	m.Lock()

	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		m.Unlock()
		return nil, fmt.Errorf("ensure config lock dir for %s: %w", resolved, err)
	}
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		m.Unlock()
		return nil, fmt.Errorf("open config lock file %s: %w", lockPath, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		m.Unlock()
		return nil, fmt.Errorf("flock config %s: %w", resolved, err)
	}
	configLockAcquisitions.Add(1)
	return &ConfigFileLock{inProc: m, file: f}, nil
}
