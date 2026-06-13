package session

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// conductorBaseMu serializes conductor base mutations (setup, meta writes, and
// dir migration) WITHIN this process; the sibling advisory flock serializes
// them ACROSS processes. A single global mutex is sufficient — the conductor
// base is one shared resource and these operations are infrequent.
//
// Together they close the migrate-dir race (finding #5): a concurrent
// `conductor setup`/meta write cannot interleave with the
// enumerate→copy→verify→commit→remove window and be stranded at the old base or
// deleted along with the source tree.
var conductorBaseMu sync.Mutex

// conductorBaseLock holds both lock layers; release() unwinds them in reverse.
type conductorBaseLock struct {
	file *os.File
}

func (l *conductorBaseLock) release() {
	if l == nil {
		return
	}
	if l.file != nil {
		// Best-effort: LOCK_UN errors are non-actionable; Close drops the fd,
		// which also releases the advisory lock.
		_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
		_ = l.file.Close()
	}
	conductorBaseMu.Unlock()
}

// acquireConductorBaseLock takes the in-process mutex then an exclusive advisory
// flock on a stable lockfile in the locks dir. The lockfile lives OUTSIDE the
// conductor base on purpose: migrate-dir relocates the base, so a lock kept
// under it could be moved out from under a concurrent holder. Callers MUST defer
// release().
//
// Reentrancy: only the high-level entry points acquire this lock
// (SetupConductorWithAgent, MigrateConductorDir, the public SaveConductorMeta),
// and none of them calls another while holding it — Setup writes meta via the
// unlocked saveConductorMetaLocked, and Migrate never calls SaveConductorMeta —
// so the non-reentrant mutex never self-deadlocks.
func acquireConductorBaseLock() (*conductorBaseLock, error) {
	conductorBaseMu.Lock()
	locks, err := resolveLocksDirForSpawnLock()
	if err != nil {
		conductorBaseMu.Unlock()
		return nil, fmt.Errorf("resolve locks dir for conductor base lock: %w", err)
	}
	if err := os.MkdirAll(locks, 0o700); err != nil {
		conductorBaseMu.Unlock()
		return nil, fmt.Errorf("create locks dir for conductor base lock: %w", err)
	}
	lockPath := filepath.Join(locks, "conductor-base.lock")
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		conductorBaseMu.Unlock()
		return nil, fmt.Errorf("open conductor base lock %q: %w", lockPath, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		conductorBaseMu.Unlock()
		return nil, fmt.Errorf("flock conductor base: %w", err)
	}
	return &conductorBaseLock{file: f}, nil
}
