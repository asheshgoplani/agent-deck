package session

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Messaging audit P2-2 (#2104): a target pane is a shared, unlocked medium.
// The daemon's [INBOX] nudge, the heartbeat, the Telegram bridge and sibling
// sessions all run `session send` from separate processes, and two bodies
// landing in one composer at once corrupt each other (and, before #2104's
// guard fix, killed the conductor). AcquireSendLock is the per-target,
// cross-process lock a sender holds through the readiness guard, the paste
// and the Enter, so concurrent sends to one target serialize instead.

// SendTargetLockWait bounds how long `session send` waits for another send to
// finish with the same target before answering "target busy". A tmux send
// holds the lock through its readiness guard, paste, Enter and verification
// window (well under this), so a wait that runs out means a stuck holder.
// Exported so the daemon's wake-nudge subprocess timeout can be derived from
// it (review round 2, P3): the nudge must outlive the lock wait or it is
// killed while still queued behind another sender.
const SendTargetLockWait = 30 * time.Second

func sendLockDir() string {
	dir, err := runtimeDataPath("send-locks")
	if err != nil {
		return tempAgentDeckPath("runtime", "send-locks")
	}
	return dir
}

// AcquireSendLock takes the per-target send lock for instanceID, waiting at
// most wait. It returns an error wrapping ErrConfigLockBusy when another send
// still holds the target after the wait. The lock is not reentrant.
func AcquireSendLock(instanceID string, wait time.Duration) (*ConfigFileLock, error) {
	if strings.TrimSpace(instanceID) == "" {
		return nil, errors.New("send lock: empty target id")
	}
	dir := sendLockDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return AcquireConfigFileLockTimeout(filepath.Join(dir, sanitizeInboxName(instanceID)), wait)
}
