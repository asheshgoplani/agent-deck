package session

import (
	"fmt"
	"time"
)

// UnarchiveAround runs op, a start, restart or queued start of inst, as a
// durable unarchive + op when inst is archived. Bringing an archived session
// back to life must take it out of the archive: a live session still flagged
// archived is hidden from the active list and counted (and woken) as parked.
//
// The cleared flag is persisted before op runs, so a crash mid-start never
// leaves a running process behind an archived row. If op fails, the exact
// archive timestamp is restored and persisted again, so a session never moves
// lists without the process the move promised. verb names op in the error of
// a failed unarchive ("start", "restart").
//
// unarchived reports whether inst was archived and op succeeded. A session
// that is not archived runs op unchanged and never calls persist.
func UnarchiveAround(inst *Instance, verb string, persist func(*Instance) error, op func() error) (unarchived bool, err error) {
	if !inst.IsArchived() {
		return false, op()
	}

	archivedAt := inst.ArchivedAtThreadSafe()
	inst.SetArchivedAtThreadSafe(time.Time{})
	if err := persist(inst); err != nil {
		inst.SetArchivedAtThreadSafe(archivedAt)
		return false, fmt.Errorf("failed to unarchive session before %s: %w", verb, err)
	}

	if err := op(); err != nil {
		inst.SetArchivedAtThreadSafe(archivedAt)
		if rollbackErr := persist(inst); rollbackErr != nil {
			return false, fmt.Errorf("%s failed: %w; restoring archive state failed: %v", verb, err, rollbackErr)
		}
		return false, err
	}
	return true, nil
}

// PersistArchived writes inst's archive timestamp with the scoped
// StateDB.SetArchived update, never the full-registry save: under a concurrent
// writer (a running TUI) the full save can abort and reload, silently
// reverting the archive change. It is the persist function UnarchiveAround
// takes on every surface that owns a Storage.
func (s *Storage) PersistArchived(inst *Instance) error {
	db := s.GetDB()
	if db == nil {
		return fmt.Errorf("state database unavailable")
	}
	return db.SetArchived(inst.ID, inst.ArchivedAtThreadSafe())
}
