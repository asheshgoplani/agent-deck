package statedb

import (
	"fmt"
	"os"
	"sync"
	"syscall"
)

// archiveEventBoundaryMu provides the in-process half of the archive/event
// serialization boundary. The advisory flock on the database sibling lockfile
// provides the cross-process half.
var archiveEventBoundaryMu sync.Mutex

// WithArchiveEventBoundary serializes archive persistence with durable event
// commits. Acquiring this lock is the linearization point: an event holder may
// finish its inbox commit before a later archive; once archive acquires and
// persists first, every later event holder observes the archived row and drops.
func (s *StateDB) WithArchiveEventBoundary(fn func() error) error {
	archiveEventBoundaryMu.Lock()
	defer archiveEventBoundaryMu.Unlock()
	if s == nil || s.path == "" || s.path == ":memory:" {
		return fn()
	}
	f, err := os.OpenFile(s.path+".archive-event.lock", os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("open archive/event boundary: %w", err)
	}
	defer f.Close()
	if s.archiveEventBoundaryAttempt != nil {
		s.archiveEventBoundaryAttempt()
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock archive/event boundary: %w", err)
	}
	defer func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	}()
	return fn()
}
