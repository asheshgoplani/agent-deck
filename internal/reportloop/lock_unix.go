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
// two Store values in one process exclude each other as well.
func lockFile(path string) (func(), error) {
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
			f.Close()
			return nil, fmt.Errorf("flock: %w", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
