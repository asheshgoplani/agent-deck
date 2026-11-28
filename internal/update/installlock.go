package update

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ErrUpdateInProgress is returned when another installer still holds the
// install lock after installLockWait.
var ErrUpdateInProgress = errors.New("update already in progress")

// installLockWait is how long a second installer waits for the first before
// giving up with ErrUpdateInProgress. A var so tests can shorten it.
var installLockWait = 60 * time.Second

// acquireInstallLock takes an exclusive flock on the directory holding the
// binary, so no lock file is left next to it. The returned func releases the
// lock and reports the Close error.
func acquireInstallLock(execPath string) (func() error, error) {
	f, err := os.Open(filepath.Dir(execPath))
	if err != nil {
		return nil, fmt.Errorf("failed to open install lock: %w", err)
	}
	deadline := time.Now().Add(installLockWait)
	for {
		ok, err := flockExclusiveNB(f)
		if err != nil {
			return nil, errors.Join(fmt.Errorf("failed to lock install: %w", err), f.Close())
		}
		if ok {
			return func() error { flockRelease(f); return f.Close() }, nil
		}
		if time.Now().After(deadline) {
			return nil, errors.Join(ErrUpdateInProgress, f.Close())
		}
		time.Sleep(20 * time.Millisecond)
	}
}
