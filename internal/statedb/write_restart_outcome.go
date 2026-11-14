package statedb

import (
	"context"
	"database/sql"
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
//
// The read and the write are one transaction, BEGIN IMMEDIATE on a pinned
// connection (as TakeQueued and the snapshot merge do): it holds the database's
// write lock from before the read, so no other writer, in this process or
// another, whatever it writes, can land between them, and Before is always the
// stamp this write replaced. Two writers that both read the same stamp would
// otherwise each report it as Before and each pass SoleWriterSince.
func (s *StateDB) touchStamp() (WriteStamps, error) {
	var stamps WriteStamps
	err := withBusyRetry(func() error {
		ctx := context.Background()
		conn, err := s.db.Conn(ctx)
		if err != nil {
			return err
		}
		defer conn.Close()
		if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
			return err
		}
		committed := false
		defer func() {
			if !committed {
				_, _ = conn.ExecContext(ctx, "ROLLBACK")
			}
		}()
		var raw string
		switch err := conn.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key = 'last_modified'`).Scan(&raw); {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return err
		}
		var before int64
		if raw != "" {
			if _, err := fmt.Sscanf(raw, "%d", &before); err != nil {
				return err
			}
		}
		after := time.Now().UnixNano()
		// A clock that has not advanced (or has gone backwards) must not produce a
		// stamp that another writer could already have used; step past it instead.
		if after <= before {
			after = before + 1
		}
		if _, err := conn.ExecContext(ctx, `INSERT OR REPLACE INTO metadata (key, value) VALUES ('last_modified', ?)`, fmt.Sprintf("%d", after)); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
			return err
		}
		committed = true
		stamps = WriteStamps{Before: before, After: after}
		return nil
	})
	if err != nil {
		return WriteStamps{}, err
	}
	return stamps, nil
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
	return s.writeRestartOutcome(id, tmuxSession, status, tool, "tool_data")
}

// WriteRestartOutcomeWithLaunch is WriteRestartOutcome that also records, in
// the same UPDATE, the launch decision: the tool the restart launched
// (launched_tool), whether a pi's launch exported the wake socket, and the tmux
// session the decision describes (launch_tmux_session). The launched tool is
// what the pane runs, which a tool edit does not change until the next restart.
func (s *StateDB) WriteRestartOutcomeWithLaunch(id, tmuxSession, status, tool string, exported bool) (WriteStamps, error) {
	return s.writeRestartOutcome(id, tmuxSession, status, tool, launchToolDataExpr(exported), tool, tmuxSession)
}

// launchToolDataExpr records the launch decision; its two parameters are the
// launched tool and the tmux session the launch spawned in.
//
// The decision lives in tool_data extras, which every binary's full save
// carries forward (MergeToolDataExtras), including a binary that launches the
// session without writing a decision of its own. So the decision names the
// tmux session it was made for, and a reader trusts it only while the row
// still names that session. A restart that recreates the session mints a new
// name, so an older binary's restart reads as a launch with no record. (A
// start reuses the stored name, so an older binary's start of a stopped pi is
// not caught here; the bound on refused socket wakes makes that one visible.)
func launchToolDataExpr(exported bool) string {
	return `json_set(` + piSocketToolDataExpr(exported) + `, '$.launched_tool', ?, '$.launch_tmux_session', ?)`
}

// piSocketToolDataExpr is the tool_data expression that records the decision.
func piSocketToolDataExpr(exported bool) string {
	if exported {
		return `json_set(COALESCE(tool_data, '{}'), '$.pi_socket_exported', json('true'))`
	}
	return `json_remove(COALESCE(tool_data, '{}'), '$.pi_socket_exported')`
}

func (s *StateDB) writeRestartOutcome(id, tmuxSession, status, tool, toolDataExpr string, exprArgs ...any) (WriteStamps, error) {
	var affected int64
	if err := withBusyRetry(func() error {
		res, err := s.db.Exec(
			`UPDATE instances
			   SET tmux_session = ?, status = ?, tool = ?,
			       acknowledged = CASE WHEN ? = 'running' THEN 0 ELSE acknowledged END,
			       acknowledged_at = CASE WHEN ? = 'running' THEN 0 ELSE acknowledged_at END,
			       tool_data = `+toolDataExpr+`
			 WHERE id = ?`,
			append(append([]any{tmuxSession, status, tool, status, status}, exprArgs...), id)...,
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

// WriteLaunchDecision records the decision of the launch that just spawned its
// pane in tmuxSession: in tool_data, the tool it launched (launched_tool),
// whether a pi's launch exported the wake socket, and the tmux session it
// describes (see launchToolDataExpr); and that session's name in tmux_session,
// in the same UPDATE, since a start whose name was taken spawns under a new one
// (tmux.Session.Start) and not every start is followed by a save. A start
// writes it after a successful spawn; a restart folds it into its outcome
// write, or writes it here on a respawn-pane path. An instance with no row yet
// (started before its first save) returns ErrInstanceNotStored, and its first
// save records the decision instead.
//
// Like the restart outcome, it moves last_modified, so a peer reloads the name,
// and returns the stamps, so the launching process can tell the bump is its own
// (Instance.LaunchRecordStamps).
func (s *StateDB) WriteLaunchDecision(id, tmuxSession, tool string, exported bool) (WriteStamps, error) {
	if tmuxSession == "" {
		return WriteStamps{}, fmt.Errorf("record launch decision for %q: the launch has no tmux session name", id)
	}
	var affected int64
	if err := withBusyRetry(func() error {
		res, err := s.db.Exec(`UPDATE instances SET tmux_session = ?, tool_data = `+launchToolDataExpr(exported)+` WHERE id = ?`,
			tmuxSession, tool, tmuxSession, id)
		if err != nil {
			return err
		}
		affected, err = res.RowsAffected()
		return err
	}); err != nil {
		return WriteStamps{}, err
	}
	if affected == 0 {
		return WriteStamps{}, fmt.Errorf("record launch decision for %q: %w", id, ErrInstanceNotStored)
	}
	return s.touchStamp()
}
