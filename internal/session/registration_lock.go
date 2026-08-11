package session

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// registrationMu serializes session-registration decisions WITHIN this process;
// the sibling advisory flock serializes them ACROSS processes. One global mutex
// is enough: registration is rare, and the resource being protected — "which
// (title, location) pairs are taken in this profile" — is one shared thing.
var registrationMu sync.Mutex

// RegistrationLock holds both lock layers; Release unwinds them in reverse.
type RegistrationLock struct {
	file *os.File
}

// Release drops the advisory flock and the in-process mutex. Safe on nil.
func (l *RegistrationLock) Release() {
	if l == nil {
		return
	}
	if l.file != nil {
		// Best-effort: LOCK_UN errors are non-actionable; Close drops the fd,
		// which also releases the advisory lock.
		_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
		_ = l.file.Close()
	}
	registrationMu.Unlock()
}

// AcquireRegistrationLock serializes the read-decide-write window that turns a
// requested title into a registered session.
//
// Why it exists: `add` and `launch` both LOAD the instance list, ask "is this
// (title, location) taken?", and only then INSERT. Between the question and the
// answer another process can register the same pair, so two concurrent
// `add -t dup <path>` runs could both see "free" and both create — the exact
// state #1850 makes `add` refuse. The same window makes the auto-rename pick
// the same "(2)" suffix twice, and makes `rename` / `session set <id> title`
// able to move a session onto a title another process is simultaneously taking.
//
// Holding this across [load → decide → insert] makes each of those decisions
// atomic with respect to every other registration in the same profile:
//
//   - the in-process mutex covers goroutines (the TUI, tests with t.Parallel);
//   - the advisory flock covers separate `agent-deck` processes;
//   - callers MUST re-read the instance list INSIDE the lock, because a list
//     loaded before acquiring it is exactly the stale snapshot the lock exists
//     to invalidate.
//
// The lockfile is per profile: two profiles are separate namespaces with
// separate state.db files, so serializing them together would only add
// contention. It lives in the locks dir next to the spawn and conductor locks,
// outside any directory a migration might relocate.
//
// Not reentrant. No entry point that takes it calls another that does.
func AcquireRegistrationLock(profile string) (*RegistrationLock, error) {
	registrationMu.Lock()
	path, err := registrationLockPath(profile)
	if err != nil {
		registrationMu.Unlock()
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		registrationMu.Unlock()
		return nil, fmt.Errorf("open registration lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		registrationMu.Unlock()
		return nil, fmt.Errorf("flock registration lock: %w", err)
	}
	return &RegistrationLock{file: f}, nil
}

func registrationLockPath(profile string) (string, error) {
	locks, err := resolveLocksDirForSpawnLock()
	if err != nil {
		return "", fmt.Errorf("resolve locks dir for registration lock: %w", err)
	}
	if err := os.MkdirAll(locks, 0o700); err != nil {
		return "", fmt.Errorf("create locks dir for registration lock: %w", err)
	}
	return filepath.Join(locks, fmt.Sprintf("session-registration-%s.lock", spawnLockSafeID(profile))), nil
}
