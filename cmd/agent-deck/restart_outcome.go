package main

import (
	"fmt"
	"io"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// adoptStateDB registers this command's storage handle as the process-wide
// StateDB, so targeted writes inside internal/session can reach the database
// this command already has open.
//
// main() dispatches subcommands well before the block that registers the global
// for the TUI, so in a plain `agent-deck mcp attach --restart` process
// statedb.GetGlobal() is nil. Instance.restart records the tmux session name it
// mints through that handle (#1870) and would otherwise have nowhere to write
// it — the failure is silent, and its symptom is the very bug being fixed.
//
// Called only from the commands that can restart a session, not from every
// command: registering a global changes where unrelated targeted writes land,
// and a command that never restarts anything has no reason to widen that.
// Existing globals are left alone — the TUI sets its own before this could run,
// and cross-profile lookups must not rebind it to another profile's database.
func adoptStateDB(storage *session.Storage) {
	if storage == nil || statedb.GetGlobal() != nil {
		return
	}
	if db := storage.GetDB(); db != nil {
		statedb.SetGlobal(db)
	}
}

// restartOutcome separates the two facts a `--restart` command has to report,
// which are not the same fact.
//
// The first is that the replacement process is running. The second is that
// agent-deck can still find it after this command exits: a restart mints a new
// tmux session name, and until that name is recorded the stored one still
// points at the session the restart killed. A command that prints "restarted"
// on the strength of the first fact alone is claiming the second without
// checking it — and the case it hides is exactly #1870, where the session runs
// fine, agent-deck shows it as errored, and the live tmux session is orphaned
// because nothing knows its name.
//
// Restarted && !Recorded is therefore its own outcome, not a success and not a
// failure: the operator is told the restart happened but its record did not, so
// they can look rather than restart again and leak another tmux session.
type restartOutcome struct {
	Restarted bool
	Recorded  bool
	Reason    string
}

// restartOutcomeFor reads back what Instance.Restart was able to make durable.
// restarted is the caller's own view of whether the restart ran and returned
// without error; a restart that never ran has nothing to record.
func restartOutcomeFor(inst *session.Instance, restarted bool) restartOutcome {
	if !restarted || inst == nil {
		return restartOutcome{Restarted: false, Recorded: true}
	}
	recorded, err := inst.RestartTmuxNameRecorded()
	o := restartOutcome{Restarted: true, Recorded: recorded}
	if err != nil {
		o.Reason = err.Error()
	}
	return o
}

// unrecorded reports whether this outcome is the live-but-untracked one.
func (o restartOutcome) unrecorded() bool { return o.Restarted && !o.Recorded }

// warn writes the unrecorded-restart warning, and nothing otherwise.
//
// It deliberately ignores quiet mode. Quiet suppresses the routine confirmation
// that an operation succeeded; this is the one case where it did not fully
// succeed, and staying silent would leave the operator with a session that
// looks broken in the TUI and no hint as to why.
func (o restartOutcome) warn(w io.Writer) {
	if !o.unrecorded() {
		return
	}
	fmt.Fprintf(w, "Warning: the session restarted, but its new tmux name could not be recorded: %s\n", o.Reason)
	fmt.Fprintln(w, "         The session is running; agent-deck may show it as errored until the name is saved.")
}

// describe appends the outcome to a human-readable success message.
func (o restartOutcome) describe(message string) string {
	switch {
	case o.unrecorded():
		return message + " - session restarted (new tmux name not recorded)"
	case o.Restarted:
		return message + " - session restarted"
	default:
		return message
	}
}

// addTo publishes the outcome into a JSON payload.
//
// tmux_name_recorded is present whenever a restart ran, so a script can tell a
// fully-recorded restart from an unrecorded one without string-matching a
// warning. It is absent when no restart was requested, where there is nothing
// to record and a blanket `false` would read as a failure.
func (o restartOutcome) addTo(payload map[string]interface{}) {
	payload["restarted"] = o.Restarted
	if !o.Restarted {
		return
	}
	payload["tmux_name_recorded"] = o.Recorded
	if o.unrecorded() {
		payload["restart_warning"] = o.Reason
	}
}
