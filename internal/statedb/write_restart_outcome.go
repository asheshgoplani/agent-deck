package statedb

import (
	"errors"
	"fmt"
	"time"
)

// ErrInstanceNotStored reports that a targeted UPDATE matched no row, so the
// value the caller asked to record was not recorded. SQLite reports an UPDATE
// that matches nothing as success, which would let a caller announce a durable
// write that never happened.
var ErrInstanceNotStored = errors.New("instance row not found")

// WriteStamps are the metadata.last_modified values a targeted write replaced
// and wrote.
//
// They exist so a process that is both a writer and a reader of this database
// can recognise its own bump. The TUI decides whether to save by comparing the
// database's last_modified against the value it captured when it last loaded;
// a targeted write of its own moves last_modified, so without a way to identify
// that bump the TUI reads its own write as somebody else's change. Before tells
// it whether anything ELSE changed since it loaded, and After identifies the
// value its own write produced. last_modified is a UnixNano stamp, so equality
// is an exact identity test rather than a heuristic.
type WriteStamps struct {
	Before int64
	After  int64
}

// SoleWriterSince reports whether this write was the only change to the
// database since the caller observed loadedAt, and produced exactly currentAt.
//
// Both halves are needed. Before <= loadedAt says nothing landed between the
// caller's load and this write; After == currentAt says nothing landed after
// it. A caller that checked only the second half would treat a database that
// had already moved on as up to date, and then overwrite whatever moved it.
func (w WriteStamps) SoleWriterSince(loadedAt, currentAt int64) bool {
	return w.After != 0 && w.Before <= loadedAt && w.After == currentAt
}

// touchStamp records a modification and returns the value written along with
// the one it replaced, so a caller can identify its own bump afterwards.
func (s *StateDB) touchStamp() (WriteStamps, error) {
	before, err := s.LastModified()
	if err != nil {
		return WriteStamps{}, err
	}
	after := time.Now().UnixNano()
	// A clock that has not advanced (or has gone backwards) must not produce a
	// stamp that another writer could already have used; step past it instead.
	if after <= before {
		after = before + 1
	}
	if err := withBusyRetry(func() error {
		return s.SetMeta("last_modified", fmt.Sprintf("%d", after))
	}); err != nil {
		return WriteStamps{}, err
	}
	return WriteStamps{Before: before, After: after}, nil
}

// WriteRestartOutcome atomically records what a restart produced for one
// instance: the tmux session name it minted and the status it ended in.
//
// A restart mints a NEW tmux session name — tmux.NewSession appends a fresh
// short id unconditionally — and that name exists only on the in-memory
// Instance until something writes the tmux_session column. Four CLI --restart
// paths never wrote it at all (#1870), so the stored name kept naming the
// session the restart had just killed: the TUI polled a tmux session that no
// longer existed and reported `error` for a process that was running fine,
// while the live tmux session was orphaned because nothing knew its name.
//
// It is a targeted two-column UPDATE, and specifically NOT a snapshot save.
// The alternative — pushing a whole preloaded instance list back through
// SaveWithGroups — makes a process that loaded its rows before a slow restart
// revert every unrelated change anything else made in between: an archive, a
// rename, a group move. That lost-update shape is the one behind this
// repository's data-loss incidents, and it is a far worse failure than the
// stale tmux name it would be curing.
//
// Status travels with the name because the two are one fact about the restart
// and are read together: a row naming a live pane but still marked `error`
// misreports the session just as badly as a row naming a dead one. Writing them
// separately would also leave a window where the DB describes a session that
// never existed.
//
// A zero-row UPDATE returns ErrInstanceNotStored rather than nil, so a caller
// can tell "recorded" from "silently dropped" (the instance was never saved, or
// another process deleted it mid-restart).
func (s *StateDB) WriteRestartOutcome(id, tmuxSession, status, tool string) (WriteStamps, error) {
	var affected int64
	if err := withBusyRetry(func() error {
		res, err := s.db.Exec(
			`UPDATE instances
			   SET tmux_session = ?, status = ?, tool = ?,
			       acknowledged = CASE WHEN ? = 'running' THEN 0 ELSE acknowledged END
			 WHERE id = ?`,
			tmuxSession, status, tool, status, id,
		)
		if err != nil {
			return err
		}
		affected, err = res.RowsAffected()
		return err
	}); err != nil {
		return WriteStamps{}, err
	}
	if affected == 0 {
		return WriteStamps{}, fmt.Errorf("record restart outcome for %q: %w", id, ErrInstanceNotStored)
	}
	// Peers poll last_modified; without the bump a running TUI keeps serving
	// the dead name from its own snapshot.
	return s.touchStamp()
}
