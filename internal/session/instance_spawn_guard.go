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
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// agentDeckDirOverride lets tests redirect ~/.agent-deck without colliding
// with the user's real install. Production code reads it via
// resolveLocksDirForSpawnLock(), which falls back to the effective XDG data
// locks dir when the override is empty.
var agentDeckDirOverride string

// SpawnAttempt is the single-flight wrapper used by Restart(), Start(),
// and StartWithMessage(). The lock acquisition + sibling-detect window
// is implicit — call Run().
//
// The storm-discriminator is *time*, not "is a session alive". A
// legitimate manual restart of a long-running session must proceed even
// though tmux holds a live AGENTDECK_INSTANCE_ID-matching session; only
// the *storm* shape — multiple spawns racing while one is in-flight —
// should be suppressed. Run() snapshots the per-instance spawn-stamp
// generation before acquiring the lock and re-reads it after acquisition.
// If a sibling completed during our wait (generation advanced past our
// snapshot), we skip; otherwise we run Spawn and stamp on success.
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
	beforeLock := spawnGenerationSnapshot(a.InstanceID)
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

// Issue #2220: the sibling-spawn gate is a generation counter, not a clock.
//
// The stamp used to carry its only signal in its mtime, compared against
// the caller's pre-lock wall-clock time. That breaks in both directions
// when the clock steps: a stamp written before a backwards correction sat
// in the future and suppressed every later Start()/Restart() (the silent
// no-op from #2220), and any clamp or skew tolerance that ignores future
// stamps discards a genuine sibling that stamped just before the step.
//
// Instead the stamp body holds a monotonically increasing generation.
// A caller snapshots it before waiting for the lock and compares after
// acquisition: a higher generation means a sibling spawned during the
// wait, regardless of what the wall clock did in between. Suppression is
// therefore bounded by the lock wait itself. The mtime is still set to
// now for `ls` triage; a future-dated one is reported once per stamp as
// a clock anomaly and heals on the next successful spawn.
//
// A missing, empty (pre-#2220) or unparseable stamp reads as generation 0.

// instanceSpawnStampSkewTolerance is how far ahead of now a stamp's mtime
// may sit before it is reported as a clock anomaly. It only gates the
// warning — the spawn decision never looks at the mtime.
const instanceSpawnStampSkewTolerance = 2 * time.Second

// spawnStampClockAnomalyLogFn is a test seam for the clock anomaly warning.
var spawnStampClockAnomalyLogFn = func(instanceID string, mtime, now time.Time) {
	slog.Warn("spawn_stamp_future_dated",
		slog.String("instance", instanceID),
		slog.Time("stamp_mtime", mtime),
		slog.Time("now", now),
		slog.String("hint", "clock was corrected backwards after a spawn; stamp mtime ignored"),
	)
}

// spawnStampClockAnomalySeen dedupes the warning per stamp (path + mtime),
// so each anomalous stamp is reported once and a re-stamped file that is
// future-dated again is reported again.
var spawnStampClockAnomalySeen sync.Map

func noteSpawnStampClockAnomaly(instanceID, path string, mtime, now time.Time) {
	if mtime.Sub(now) <= instanceSpawnStampSkewTolerance {
		return
	}
	key := fmt.Sprintf("%s|%d", path, mtime.UnixNano())
	if _, seen := spawnStampClockAnomalySeen.LoadOrStore(key, struct{}{}); !seen {
		spawnStampClockAnomalyLogFn(instanceID, mtime, now)
	}
}

// spawnGenerationSnapshot returns the stamp's current generation for use as
// the pre-lock reference. Callers take it before acquireInstanceSpawnLock.
func spawnGenerationSnapshot(instanceID string) uint64 {
	gen, _ := readSpawnGeneration(instanceID)
	return gen
}

// spawnedSince reports whether the per-instance spawn stamp's generation
// has advanced past the given pre-lock snapshot, i.e. a sibling spawned
// while we waited for the lock. A missing stamp = false (no sibling has
// spawned yet for this instance ID).
func spawnedSince(instanceID string, before uint64) bool {
	gen, _ := readSpawnGeneration(instanceID)
	return gen > before
}

// readSpawnGeneration reads the stamp body. Missing, empty or unparseable
// content is generation 0. It also reports a future-dated mtime while it
// has the stat in hand.
func readSpawnGeneration(instanceID string) (uint64, error) {
	stamp, err := instanceSpawnStampPath(instanceID)
	if err != nil {
		return 0, err
	}
	info, err := os.Stat(stamp)
	if err != nil {
		return 0, err
	}
	noteSpawnStampClockAnomaly(instanceID, stamp, info.ModTime(), nowFn())
	data, err := os.ReadFile(stamp)
	if err != nil {
		return 0, err
	}
	gen, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0, nil
	}
	return gen, nil
}

// recordInstanceSpawn bumps the stamp's generation and sets its mtime to
// now. Best-effort: stamp errors are silent; they just turn the gate into
// a no-op for the next storm sibling (no worse than current behavior).
// The body is written to a sibling temp file and renamed so a pre-lock
// snapshot never observes a half-written generation.
func recordInstanceSpawn(instanceID string) {
	stamp, err := instanceSpawnStampPath(instanceID)
	if err != nil {
		return
	}
	gen, _ := readSpawnGeneration(instanceID)
	tmp := stamp + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.FormatUint(gen+1, 10)), 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, stamp); err != nil {
		_ = os.Remove(tmp)
		return
	}
	now := nowFn()
	_ = os.Chtimes(stamp, now, now)
}

// instanceSpawnStampPath sits next to the lock file. Same dir, "-stamp"
// suffix so an `ls` triages spawn activity per instance.
func instanceSpawnStampPath(instanceID string) (string, error) {
	locks, err := resolveLocksDirForSpawnLock()
	if err != nil {
		return "", err
	}
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
	locks, err := resolveLocksDirForSpawnLock()
	if err != nil {
		return "", err
	}
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

func resolveLocksDirForSpawnLock() (string, error) {
	if agentDeckDirOverride != "" {
		return filepath.Join(agentDeckDirOverride, "locks"), nil
	}
	return dataPath("locks", "locks")
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
