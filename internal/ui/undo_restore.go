package ui

import "github.com/asheshgoplani/agent-deck/internal/session"

// undoPrevState is what a deleted session looked like before the delete
// killed it: an undo brings it back in that state (TUI-7). The zero value
// (state not captured) restarts on undo, as every undo did before.
type undoPrevState struct {
	captured bool
	status   session.Status
	archived bool
}

// captureUndoState records inst's state for an undo. Call it before the
// delete kills the session: Kill writes StatusStopped.
func captureUndoState(inst *session.Instance) undoPrevState {
	return undoPrevState{
		captured: true,
		status:   inst.GetStatusThreadSafe(),
		archived: inst.IsArchived(),
	}
}

// wantsRestart reports whether an undo should start the session again: only
// one that was active when it was deleted. A stopped, errored or archived
// session comes back without a process.
func (p undoPrevState) wantsRestart() bool {
	if !p.captured {
		return true
	}
	if p.archived {
		return false
	}
	return p.status != session.StatusStopped && p.status != session.StatusError
}

// restartForUndo restarts a session an undo brings back (a seam for tests).
var restartForUndo = func(inst *session.Instance) error { return inst.Restart() }

// restoreForUndo puts inst back the way it was when it was deleted. It
// restarts an active session and otherwise restores the saved status (the
// archived flag is still on the instance). It is shared by the TUI ctrl+z and
// the web undo so both surfaces behave the same.
func restoreForUndo(inst *session.Instance, prev undoPrevState) (restarted bool, err error) {
	if prev.wantsRestart() {
		return true, restartForUndo(inst)
	}
	inst.SetStatusThreadSafe(prev.status)
	return false, nil
}
