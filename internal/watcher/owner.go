package watcher

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
)

// EngineLockFile is the engine owner's lock, kept in the profile runtime dir
// next to the profile daemon's daemon.lock.
const EngineLockFile = "watcher-engine.lock"

// EngineLockPath returns the engine owner lock of profile (#2530).
func EngineLockPath(profile string) (string, error) {
	dir, err := agentpaths.ProfileRuntimeDir(profile)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, EngineLockFile), nil
}

// AlreadyRunningError is returned by AcquireEngineOwner when another live
// process holds the engine owner lock.
type AlreadyRunningError struct{ PID int }

func (e *AlreadyRunningError) Error() string {
	if e.PID > 0 {
		return fmt.Sprintf("watcher engine already running (pid %d)", e.PID)
	}
	return "watcher engine already running"
}

// EngineOwner is the one process allowed to run a profile's watcher engine:
// two engines would bind a webhook or github port twice and subscribe to an
// ntfy or slack topic twice (#2530).
type EngineOwner struct {
	lock *os.File
}

// AcquireEngineOwner takes the engine owner lock at path and records this
// process's pid in it. It is the lock half of the profile daemon's owner
// (internal/core/daemon/owner.go): an exclusive flock, so the lock dies with
// its process, SIGKILL included, and there is never a stale lock to clean up.
// While a live process holds it, AcquireEngineOwner fails with
// *AlreadyRunningError and changes nothing.
func AcquireEngineOwner(path string) (*EngineOwner, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create runtime dir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("restrict runtime dir: %w", err)
	}
	lock, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open watcher engine lock: %w", err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, &AlreadyRunningError{PID: readEngineOwnerPID(path)}
		}
		return nil, fmt.Errorf("lock watcher engine: %w", err)
	}
	if err := lock.Truncate(0); err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("write watcher engine lock: %w", err)
	}
	if _, err := lock.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0); err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("write watcher engine lock: %w", err)
	}
	return &EngineOwner{lock: lock}, nil
}

// Close releases the lock. The file stays, so a waiting process keeps
// locking the same inode.
func (o *EngineOwner) Close() error {
	return o.lock.Close()
}

// readEngineOwnerPID returns the pid recorded in the lock file, or 0.
func readEngineOwnerPID(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return pid
}
