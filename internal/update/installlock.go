package update

import (
	"errors"
	"fmt"
	"os"
	"time"
)

// ErrUpdateInProgress is returned when another installer still holds the
// install lock after installLockWait.
var ErrUpdateInProgress = errors.New("update already in progress")

// installLockWait is how long a second installer waits for the first before
// giving up with ErrUpdateInProgress. A var so tests can shorten it.
var installLockWait = 60 * time.Second

// acquireInstallLock takes an exclusive flock on <execPath>.lock. The lock
// file is never removed (removing it would let two installers lock different
// inodes). The returned func releases the lock.
func acquireInstallLock(execPath string) (func(), error) {
	f, err := os.OpenFile(execPath+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("failed to open install lock: %w", err)
	}
	deadline := time.Now().Add(installLockWait)
	for {
		ok, err := flockExclusiveNB(f)
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("failed to lock install: %w", err)
		}
		if ok {
			return func() { flockRelease(f); f.Close() }, nil
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, ErrUpdateInProgress
		}
		time.Sleep(20 * time.Millisecond)
	}
}
