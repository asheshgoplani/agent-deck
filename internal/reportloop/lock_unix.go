//go:build unix

package reportloop

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

// lockTimeout bounds how long a store change waits for another process.
const lockTimeout = 10 * time.Second

// lockFile takes an exclusive flock on path (created 0600 if missing),
// waiting at most lockTimeout. flock is per open file description, so
// two Store values in one process exclude each other as well. The
// returned release unlocks and closes the file and returns both errors
// (the file is open for writing, so a Close error is never dropped).
func lockFile(path string) (func() error, error) {
	// Read-write although nothing is written: flock on some file systems
	// (NFS among them) needs a descriptor open for writing.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600) // #nosec G304 -- the store's own lock file
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(lockTimeout)
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) || time.Now().After(deadline) {
			return nil, errors.Join(fmt.Errorf("flock: %w", err), closeLock(f))
		}
		time.Sleep(10 * time.Millisecond)
	}
	return func() error {
		var unlockErr error
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
			unlockErr = fmt.Errorf("unlocking %s: %w", path, err)
		}
		return errors.Join(unlockErr, closeLock(f))
	}, nil
}

func closeLock(f *os.File) error {
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing lock file %s: %w", f.Name(), err)
	}
	return nil
}
