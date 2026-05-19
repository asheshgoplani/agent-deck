// Per-instance spawn singleflight — issue #1040 (concurrent restart storm).
//
// When an external watcher (the bug reporter's `claude --remote-control`
// wrapper, or a conductor polling `agent-deck session show` on a 1-2s
// cadence) calls `agent-deck session start <id>` more than once after the
// underlying Claude process exits naturally, the storm shape on v1.9.17 is:
//
//	N CLI invocations → each loads Instance from SQLite → each sees
//	tmuxSession.Exists()==false (old session died on Claude exit) → each
//	falls through Restart()'s respawn-pane fast path → each calls
//	recreateTmuxSession(), which mints a fresh random suffix → each spawns
//	a new tmux session in parallel.
//
// The #666 sweepDuplicateToolSessions runs *after* each spawn, so every
// new spawn kills its older siblings — the journalctl shape from the bug
// report (3-5 scopes started within 2-4s, all dead by the next minute).
//
// Fix shape: acquire a per-instance file lock at
// ~/.agent-deck/locks/instance-spawn-<safeID>.lock around the spawn step,
// with an in-lock AlreadyAlive gate so the second waiter exits with nil
// instead of re-spawning. Mirrors acquirePluginLock (#735, plugin_install.go):
// O_CREATE|O_EXCL marker + PID + stale-reclaim by `kill -0` or by age TTL.
//
// Related-but-not-the-same: #1031 was a *storage-layer* race in
// SaveInstances' DELETE-NOT-IN sweep during concurrent `launch`. The fix
// in #1032 (InsertSessionAndVerify) does not reach this code path — by
// the time we hit Restart(), the instance row already exists. #1040 is
// purely a spawn-step race; #1032's fix is necessary upstream but not
// sufficient downstream.

package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// agentDeckDirOverride lets tests redirect ~/.agent-deck without colliding
// with the user's real install. Production code reads it via
// resolveAgentDeckDirForSpawnLock(), which falls back to GetAgentDeckDir
// when the override is empty.
var agentDeckDirOverride string

// SpawnAttempt is the single-flight wrapper used by Restart(), Start(),
// and StartWithMessage(). The lock acquisition + sibling-detect window
// is implicit — call Run().
//
// The storm-discriminator is *time*, not "is a session alive". A
// legitimate manual restart of a long-running session must proceed even
// though tmux holds a live AGENTDECK_INSTANCE_ID-matching session; only
// the *storm* shape — multiple spawns racing while one is in-flight —
// should be suppressed. Run() captures a timestamp before acquiring the
// lock and consults the per-instance spawn-stamp after acquisition. If
// a sibling completed during our wait (stamp mtime > our pre-lock time),
// we skip; otherwise we run Spawn and stamp on success.
type SpawnAttempt struct {
	// InstanceID is the lock-key partition. Different instances do not
	// serialize against each other.
	InstanceID string

	// AlreadyAlive is an optional supplementary gate. When non-nil and
	// it returns true, Run() skips Spawn even if no stamp exists. Used
	// by callers that already know the spawn is unnecessary (e.g. CLI
	// `session start` pre-checks `Exists()`); leave nil to let the
	// stamp logic decide.
	AlreadyAlive func() bool

	// Spawn is the protected critical section. Run() invokes it exactly
	// once across concurrent callers in a storm window, and never if a
	// sibling already completed while we were waiting on the lock.
	Spawn func() error
}

// Run acquires the per-instance spawn lock, gates on a "spawned-while-
// we-waited" check, and invokes Spawn. Returns the first non-nil error
// from any step.
func (a SpawnAttempt) Run() error {
	if a.Spawn == nil {
		return fmt.Errorf("SpawnAttempt: nil Spawn func")
	}
	beforeLock := nowFn()
	release, err := acquireInstanceSpawnLock(a.InstanceID)
	if err != nil {
		return err
	}
	defer release()

	if a.AlreadyAlive != nil && a.AlreadyAlive() {
		return nil
	}
	if spawnedSince(a.InstanceID, beforeLock) {
		return nil
	}

	if err := a.Spawn(); err != nil {
		return err
	}
	recordInstanceSpawn(a.InstanceID)
	return nil
}

// nowFn is a test seam so tests can pin time without sleeping.
var nowFn = time.Now

// spawnedSince reports whether the per-instance spawn stamp's mtime is
// newer than the given reference. A missing stamp = false (no sibling
// has spawned yet for this instance ID).
func spawnedSince(instanceID string, ref time.Time) bool {
	stamp, err := instanceSpawnStampPath(instanceID)
	if err != nil {
		return false
	}
	info, err := os.Stat(stamp)
	if err != nil {
		return false
	}
	return info.ModTime().After(ref)
}

// recordInstanceSpawn updates the stamp's mtime to now. Best-effort:
// stamp errors are silent; they just turn the gate into a no-op for
// the next storm sibling (no worse than current behavior).
func recordInstanceSpawn(instanceID string) {
	stamp, err := instanceSpawnStampPath(instanceID)
	if err != nil {
		return
	}
	now := nowFn()
	// O_CREATE+TRUNC so the stamp file exists with a fresh mtime even on
	// first use. We don't write anything — only ModTime matters.
	f, err := os.OpenFile(stamp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return
	}
	_ = f.Close()
	_ = os.Chtimes(stamp, now, now)
}

// instanceSpawnStampPath sits next to the lock file. Same dir, "-stamp"
// suffix so an `ls` triages spawn activity per instance.
func instanceSpawnStampPath(instanceID string) (string, error) {
	dir, err := resolveAgentDeckDirForSpawnLock()
	if err != nil {
		return "", err
	}
	locks := filepath.Join(dir, "locks")
	if err := os.MkdirAll(locks, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(locks, fmt.Sprintf("instance-spawn-%s.stamp", spawnLockSafeID(instanceID))), nil
}

// Tunables match the plugin-install lock budget (#735) so an unrelated
// stuck plugin install and a stuck restart fail with the same shape on
// the same wall clock — easier triage.
const (
	instanceSpawnLockRetryInterval  = 100 * time.Millisecond
	instanceSpawnLockBudget         = 30 * time.Second
	instanceSpawnLockLegacyStaleTTL = 2 * time.Minute
)

// Test seam — paralleling pluginLockAcquireFn. Tests substitute this to
// inject contention behaviors without touching the filesystem.
var instanceSpawnLockAcquireFn = defaultAcquireInstanceSpawnLock

func acquireInstanceSpawnLock(instanceID string) (func(), error) {
	return instanceSpawnLockAcquireFn(instanceID)
}

func defaultAcquireInstanceSpawnLock(instanceID string) (func(), error) {
	path, err := instanceSpawnLockPath(instanceID)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(instanceSpawnLockBudget)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintf(f, "%d", os.Getpid())
			_ = f.Close()
			return func() { _ = os.Remove(path) }, nil
		}

		if reclaimStaleInstanceSpawnLock(path) {
			continue
		}

		if time.Now().After(deadline) {
			return nil, fmt.Errorf(
				"instance spawn lock %q held by live process; gave up after %s",
				path, instanceSpawnLockBudget,
			)
		}
		time.Sleep(instanceSpawnLockRetryInterval)
	}
}

// instanceSpawnLockPath returns the lockfile path for the given instance.
// The directory is created with 0700 (same permission as plugin_install
// uses for the same locks/ dir).
func instanceSpawnLockPath(instanceID string) (string, error) {
	dir, err := resolveAgentDeckDirForSpawnLock()
	if err != nil {
		return "", err
	}
	locks := filepath.Join(dir, "locks")
	if err := os.MkdirAll(locks, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(locks, fmt.Sprintf("instance-spawn-%s.lock", spawnLockSafeID(instanceID))), nil
}

// spawnLockSafeID strips characters that would break a single-segment
// filename. Empty input becomes "unknown" so the path is always valid;
// concurrent callers with empty IDs serialize on the same lock (no
// instance ID == "the unknown bucket").
func spawnLockSafeID(id string) string {
	if id == "" {
		return "unknown"
	}
	mapped := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-' || r == '_':
			return r
		default:
			return '_'
		}
	}, id)
	if mapped == "" {
		return "unknown"
	}
	return mapped
}

func resolveAgentDeckDirForSpawnLock() (string, error) {
	if agentDeckDirOverride != "" {
		return agentDeckDirOverride, nil
	}
	return GetAgentDeckDir()
}

// reclaimStaleInstanceSpawnLock mirrors reclaimStalePluginLock — older
// than 2m means the holder timed out anyway; PID-not-alive means the
// holder crashed without unlinking.
func reclaimStaleInstanceSpawnLock(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if time.Since(info.ModTime()) > instanceSpawnLockLegacyStaleTTL {
		_ = os.Remove(path)
		return true
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	pid, parseErr := parseInstanceSpawnLockPID(string(data))
	if parseErr != nil {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		_ = os.Remove(path)
		return true
	}
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		_ = os.Remove(path)
		return true
	}
	return false
}

func parseInstanceSpawnLockPID(content string) (int, error) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return 0, fmt.Errorf("empty marker")
	}
	var pid int
	if _, err := fmt.Sscanf(trimmed, "%d", &pid); err != nil {
		return 0, err
	}
	if pid <= 0 {
		return 0, fmt.Errorf("invalid pid %d", pid)
	}
	return pid, nil
}
