package statedb

import (
	"errors"
	"fmt"
)

// ErrInstanceNotStored reports that a targeted UPDATE matched no row, so the
// value the caller asked to record was not recorded. SQLite reports an UPDATE
// that matches nothing as success, which would let a caller announce a durable
// write that never happened.
var ErrInstanceNotStored = errors.New("instance row not found")

// WriteTmuxSession atomically records the tmux session name for one instance.
//
// A restart mints a NEW tmux session name — tmux.NewSession appends a fresh
// short id unconditionally — and that name exists only on the in-memory
// Instance until something writes the tmux_session column. Four CLI --restart
// paths never wrote it at all (#1870), so the stored name kept naming the
// session the restart had just killed: the TUI polled a tmux session that no
// longer existed and reported `error` for a process that was running fine,
// while the live tmux session was orphaned because nothing knew its name.
//
// This is a targeted single-column UPDATE for the same reason as WriteStatus
// and WriteClaudeSessionBinding. The alternative — pushing a whole preloaded
// snapshot back through SaveWithGroups — makes a CLI command that loaded its
// rows before a slow restart overwrite every unrelated change another process
// made in between, which is a much larger blast radius than the one column
// the restart actually invalidated.
//
// It touches last_modified so peers notice: a TUI watcher polls mtime, and
// without the bump it would keep serving the dead name from its snapshot.
//
// A zero-row UPDATE returns ErrInstanceNotStored rather than nil, so a caller
// can tell "recorded" from "silently dropped" (the instance was never saved,
// or another process deleted it mid-restart).
func (s *StateDB) WriteTmuxSession(id, tmuxSession string) error {
	var affected int64
	if err := withBusyRetry(func() error {
		res, err := s.db.Exec(
			`UPDATE instances SET tmux_session = ? WHERE id = ?`,
			tmuxSession, id,
		)
		if err != nil {
			return err
		}
		affected, err = res.RowsAffected()
		return err
	}); err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("write tmux session for %q: %w", id, ErrInstanceNotStored)
	}
	return s.touchWithRetry()
}
