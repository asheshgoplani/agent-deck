package session

import (
	"os"
	"strings"
	"time"
)

// Issue #2806: a child idle at its prompt whose process dies on its own (a
// crash, its pane killed, OOM) flips waiting -> error with no running in
// between. ShouldNotifyTransition only reports flips out of running, and the
// hook path has no candidate for SessionEnd, so no path told the parent: it
// believed the child alive and waiting. The daemon now reports that death
// once, urgent, as its own record.
//
// An idle child also shows error briefly, or with a live process, when it is
// not dead: Claude's /clear (SessionEnd then SessionStart, and the async
// SessionEnd can land last), pi's /new and /resume, a Hermes reset, a tmux
// error banner, an unreachable gateway, a restart's own kill. So one error
// sample is never a death. The flip is confirmed only when the error holds on
// a later pass AND the child's tmux pane has been gone or dead for
// idleExitGoneGrace() AND its saved row is not a stop or a removal.
//
// A restart's own kill is not a death: while the child holds a restart token
// (runtime/restart-resume/, #2802) the restart judges its exit and its boot,
// and reports a resume that fails. Seeing a token drops the pending flip for
// good, so the restart's verdict is the only record.

// isIdleExitFlip reports a flip from idle at the prompt straight to error.
func isIdleExitFlip(from, to string) bool {
	switch normalizeStatusString(from) {
	case string(StatusWaiting), string(StatusIdle):
		return normalizeStatusString(to) == string(StatusError)
	}
	return false
}

// idleChildProcessAlive reports whether the child's tmux pane still runs a
// process. Exists confirms a cache miss with a bounded probe and answers
// "alive" when the probe is inconclusive, so a doubt never reads as a death.
func idleChildProcessAlive(inst *Instance) bool {
	ts := inst.GetTmuxSession()
	return ts != nil && ts.Exists() && !ts.IsPaneDead()
}

// idleExitGoneGrace is how long the pane must stay gone before the death is
// reported. A CLI `session stop`, `session remove --force` or a worktree kill
// takes the pane down first and saves its stopped (or removed) row after the
// kill and any cleanup; a pass in between reads the row as waiting -> error
// with no pane. The grace lets that save land, and savedRowDeliberate then
// reads it. AGENTDECK_IDLE_EXIT_GRACE overrides it (tests; 0s is allowed).
func idleExitGoneGrace() time.Duration {
	if d, err := time.ParseDuration(os.Getenv("AGENTDECK_IDLE_EXIT_GRACE")); err == nil && d >= 0 {
		return d
	}
	return 5 * time.Second
}

// idleExitEntry is a child flipped from idle to error, not yet confirmed dead.
type idleExitEntry struct {
	from      string    // the idle status it flipped from
	goneSince time.Time // first pass that saw its pane gone; zero while alive
}

// confirmIdleExits runs once per pass after the snapshot edge. A waiting/idle
// -> error flip becomes pending; a pending child still in error whose pane
// has been gone for idleExitGoneGrace(), and whose saved row is not a stop or a
// removal, gets its one record. Any other status, or a restart token, drops it.
func (d *TransitionDaemon) confirmIdleExits(profile string, byID map[string]*Instance, prev, statuses map[string]string, notifyEnabled bool, now time.Time) {
	if d.idleExitPending == nil {
		d.idleExitPending = map[string]map[string]idleExitEntry{}
	}
	pending := d.idleExitPending[profile]
	next := map[string]idleExitEntry{}
	grace := idleExitGoneGrace()
	told := d.idleExitTold[profile]
	for id := range told {
		// Told dead already (#2813); a removed child, or one back with a live
		// pane and out of error, is watched again.
		if inst := byID[id]; inst == nil || (normalizeStatusString(statuses[id]) != string(StatusError) && d.idleExitPaneAlive(inst)) {
			delete(told, id)
		}
	}
	for id, to := range statuses {
		if normalizeStatusString(to) != string(StatusError) || told[id] {
			continue
		}
		entry, seen := pending[id]
		if !seen && !isIdleExitFlip(prev[id], to) {
			continue
		}
		if _, restarting := readRestartToken(id); restarting {
			continue
		}
		if !seen {
			entry = idleExitEntry{from: normalizeStatusString(prev[id])}
		}
		inst := byID[id]
		if inst == nil {
			next[id] = entry
			continue
		}
		if d.idleExitPaneAlive(inst) {
			entry.goneSince = time.Time{} // still running something: not a death (yet)
			next[id] = entry
			continue
		}
		if entry.goneSince.IsZero() {
			entry.goneSince = now
		}
		if !seen || now.Sub(entry.goneSince) < grace {
			next[id] = entry // confirm on a later pass, once the grace is over
			continue
		}
		if d.savedRowDeliberate(profile, id) {
			continue // stopped or removed on purpose: not a death
		}
		if notifyEnabled && instanceAcceptsTransitionEvents(inst) {
			d.emitIdleExit(profile, inst, entry.from, to, now)
		}
	}
	d.idleExitPending[profile] = next
}

// idleExitPaneAlive is the pane liveness check, through the test seam.
func (d *TransitionDaemon) idleExitPaneAlive(inst *Instance) bool {
	if d.idleExitAlive != nil {
		return d.idleExitAlive(inst)
	}
	return idleChildProcessAlive(inst)
}

// watchIdleExit hands a child already reading error to the death watch, as if
// it had just flipped from `from` (#2813): a resume whose old process's late
// SessionEnd shows error for up to the hook's freshness window makes no
// waiting -> error flip if the resumed process dies in that window. The watch
// still reports only a pane that is gone. It is a new process, so a told mark
// left by the one before (a banner record, then a second restart while the
// light still read error) no longer holds.
func (d *TransitionDaemon) watchIdleExit(profile, id, from string) {
	delete(d.idleExitTold[profile], id)
	if d.idleExitPending == nil {
		d.idleExitPending = map[string]map[string]idleExitEntry{}
	}
	if d.idleExitPending[profile] == nil {
		d.idleExitPending[profile] = map[string]idleExitEntry{}
	}
	if _, ok := d.idleExitPending[profile][id]; !ok {
		d.idleExitPending[profile][id] = idleExitEntry{from: from}
	}
}

// markIdleExitTold records that the parent already got an urgent record for
// this child's failure (the restart sweep's), so its error or death is not
// reported a second time. The mark lasts until the child is back with a live
// pane and out of error, the child is handed to the death watch again, or a
// restart token naming a started process is seen for it.
func (d *TransitionDaemon) markIdleExitTold(profile, id string) {
	if d.idleExitTold == nil {
		d.idleExitTold = map[string]map[string]bool{}
	}
	if d.idleExitTold[profile] == nil {
		d.idleExitTold[profile] = map[string]bool{}
	}
	d.idleExitTold[profile][id] = true
	delete(d.idleExitPending[profile], id)
}

// savedRowDeliberate re-reads the child's saved row right before the record:
// gone (removed), stopped or archived means someone ended it on purpose. A
// read that fails, or a profile with no open storage, proves nothing.
func (d *TransitionDaemon) savedRowDeliberate(profile, id string) bool {
	storage := d.storages[profile]
	if storage == nil {
		return false
	}
	db := storage.GetDB()
	if db == nil {
		return false
	}
	row, err := db.LoadInstanceByID(id)
	if err != nil {
		return false
	}
	return row == nil || normalizeStatusString(row.Status) == string(StatusStopped) || !row.ArchivedAt.IsZero()
}

// emitIdleExit commits the urgent record for an idle child that died. It is
// its own record (keyed by when it died), so it never folds into the turn
// the parent already has.
func (d *TransitionDaemon) emitIdleExit(profile string, inst *Instance, from, to string, now time.Time) {
	if inst == nil || strings.TrimSpace(inst.ParentSessionID) == "" || isSelfSuppressedConductor(inst) {
		return
	}
	event := TransitionNotificationEvent{
		ChildSessionID: inst.ID, ChildTitle: inst.Title, Profile: profile,
		FromStatus: normalizeStatusString(from), ToStatus: normalizeStatusString(to), Timestamp: now,
		Substate: string(inst.CachedSubstate()), ExitCode: inst.ExitCode(),
		Tier: TurnTierUrgent, Trigger: TurnTriggerSystem,
		LastOutputHash: "idle-exit:" + now.UTC().Format(time.RFC3339Nano),
		Text:           "The session's process exited while idle at its prompt; it is no longer running.",
	}
	if result := d.commitRestartRecord(inst, event); result.DeliveryResult == transitionDeliveryCommitted {
		d.commsInboxOnlyEdge(inst, from, to, now)
	}
}
