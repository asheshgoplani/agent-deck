package session

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Pending delete sweeps: the deferred half of CleanupRemovedSession.
//
// A TUI or web delete can be undone, so its inbox and notify-state sweep
// (#910) waits until the delete is final. MarkPendingDeleteSweep records
// such a delete in the profile's metadata table before the row is deleted,
// keyed by session id and owned by the deleting process. The record is
// cleared once the delete is final and swept (FinalizeRemovedSessions). A
// process that ends without finalizing (crash, kill -9, an exec restart)
// leaves its records behind, and ReconcileRemovedSessionSweeps, run once
// at startup, sweeps them.
//
// The reconcile only ever sweeps ids this profile recorded as deleted. The
// inbox and notify-state files are shared by every profile and also carry
// remote and talkback ids that no local storage lists, so "absent from this
// profile's storage" alone is not proof that a record is an orphan.

const pendingDeleteSweepPrefix = "pending_delete_sweep:"

// pendingSweepProcessStart tells this process image apart from an earlier
// one with the same pid (an exec restart keeps the pid).
var pendingSweepProcessStart = strconv.FormatInt(time.Now().UnixNano(), 10)

func pendingSweepOwner() string {
	return strconv.Itoa(os.Getpid()) + ":" + pendingSweepProcessStart
}

var errNoStorageDB = errors.New("storage database not initialized")

// MarkPendingDeleteSweep records that id's delete is undoable and its sweep
// is pending. Call it before the row is deleted, so a crash right after the
// delete still leaves the record for the startup reconcile.
func (s *Storage) MarkPendingDeleteSweep(id string) error {
	if s == nil || s.db == nil {
		return errNoStorageDB
	}
	if strings.TrimSpace(id) == "" {
		return nil
	}
	return s.db.SetMeta(pendingDeleteSweepPrefix+id, pendingSweepOwner())
}

// liveSessionIDs returns the ids of every session row in this profile.
func (s *Storage) liveSessionIDs() (map[string]bool, error) {
	if s == nil || s.db == nil {
		return nil, errNoStorageDB
	}
	instances, _, err := s.LoadLite()
	if err != nil {
		return nil, err
	}
	ids := make(map[string]bool, len(instances))
	for _, inst := range instances {
		ids[inst.ID] = true
	}
	return ids, nil
}

// FinalizeRemovedSessions runs the deferred sweep for deletes that can no
// longer be undone and clears their pending records. An id whose row is in
// storage again (restored, or the delete never committed) is not swept;
// only its record is cleared. If storage cannot be read nothing is swept and
// the records stay for the startup reconcile.
func (s *Storage) FinalizeRemovedSessions(ids []string) (map[string]RemovedSessionCleanup, error) {
	out := map[string]RemovedSessionCleanup{}
	if len(ids) == 0 {
		return out, nil
	}
	live, err := s.liveSessionIDs()
	if err != nil {
		return out, err
	}
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			continue
		}
		if !live[id] {
			out[id] = SweepRemovedSession(id)
		}
		_ = s.db.DeleteMeta(pendingDeleteSweepPrefix + id)
	}
	return out, nil
}

// ReconcileRemovedSessionSweeps sweeps the pending deletes that an earlier
// process recorded and never finalized (it crashed, was killed, or
// re-executed). It skips the records of processes still running, this one
// included (their deletes may still be undone), never sweeps an id whose row
// is in storage (it only clears that record), and does nothing when storage
// cannot be read. Returns the swept ids with their cleanup results.
func (s *Storage) ReconcileRemovedSessionSweeps() (map[string]RemovedSessionCleanup, error) {
	out := map[string]RemovedSessionCleanup{}
	if s == nil || s.db == nil {
		return out, errNoStorageDB
	}
	pending, err := s.db.ListMetaPrefix(pendingDeleteSweepPrefix)
	if err != nil || len(pending) == 0 {
		return out, err
	}
	live, err := s.liveSessionIDs()
	if err != nil {
		return out, err
	}
	self := pendingSweepOwner()
	for key, owner := range pending {
		id := strings.TrimPrefix(key, pendingDeleteSweepPrefix)
		if id == "" {
			continue
		}
		if live[id] {
			_ = s.db.DeleteMeta(key)
			continue
		}
		if owner == self || pendingSweepOwnerRunning(owner) {
			continue
		}
		out[id] = SweepRemovedSession(id)
		_ = s.db.DeleteMeta(key)
	}
	return out, nil
}

// pendingSweepOwnerRunning reports whether owner ("pid:start") names another
// process that is still running. The same pid with another start time is an
// earlier image of this process (exec restart), which is gone.
func pendingSweepOwnerRunning(owner string) bool {
	pidStr, _, _ := strings.Cut(owner, ":")
	pid, err := strconv.Atoi(pidStr)
	if err != nil || pid <= 0 || pid == os.Getpid() {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}
