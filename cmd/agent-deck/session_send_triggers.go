package main

import (
	"log/slog"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/events"
	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/triggers"
)

// sendSourceFlagHelp documents `session send --source`.
const sendSourceFlagHelp = "Name the sender for 'agent-deck triggers' (kind:name such as heartbeat:ops or channel:telegram, or a job name); default $AGENTDECK_SOURCE, else inferred from launchd/systemd/cron/ssh"

// sendTriggerFire records the trigger fire of one `session send` (issue
// #2702). Recording is best-effort: an error is logged and never changes
// the send's result or exit code. Each send records at most one fire.
type sendTriggerFire struct {
	db            *statedb.StateDB
	source        triggers.Source
	target        string
	targetProfile string
	message       string
	sessionID     string
	// exitOutcome is what a non-zero exit records (failed, or dead-target
	// when the target names no session).
	exitOutcome string
	done        bool
}

// newSendTriggerFire resolves the source of this send: --source, then
// AGENTDECK_SOURCE, then the sending session (none under --no-tag), then
// inference from how this process was started.
func newSendTriggerFire(db *statedb.StateDB, explicitSource, senderID, target, targetProfile, message string) *sendTriggerFire {
	return &sendTriggerFire{
		db:            db,
		source:        triggers.Resolve(explicitSource, senderID, triggers.OSEnv()),
		target:        target,
		targetProfile: targetProfile,
		message:       message,
		exitOutcome:   triggers.OutcomeFailed,
	}
}

// record stores the fire once.
func (f *sendTriggerFire) record(outcome string) {
	if f == nil || f.done {
		return
	}
	f.done = true
	err := triggers.Record(f.db, events.CurrentProfile(), triggers.Hostname(), triggers.Fire{
		Source: f.source, Target: f.target, TargetProfile: f.targetProfile,
		SessionID: f.sessionID, Message: f.message, Outcome: outcome,
	})
	if err != nil {
		logging.ForComponent(logging.CompSession).Warn("trigger_fire_not_recorded",
			slog.String("trigger_id", f.source.ID()), slog.String("outcome", outcome), slog.String("error", err.Error()))
	}
}

// skip marks a send that is not a fire (a draft, a skipped heartbeat).
func (f *sendTriggerFire) skip() {
	if f != nil {
		f.done = true
	}
}

// onExit is installed as cliExitHook: a send that exits non-zero before it
// recorded an outcome records exitOutcome, and the bus frame is flushed
// before the process ends (os.Exit skips main's deferred close).
func (f *sendTriggerFire) onExit(code int) {
	if f == nil || f.done || code == 0 {
		return
	}
	f.record(f.exitOutcome)
	_ = events.CloseDefault()
}

// mutedUntil reports whether this sender is muted now: its own row, or the
// launchd job / systemd unit it runs under (so a job cannot pass a fresh
// --source to get past a mute on its unit).
// It returns the id whose mute holds, for the message.
func (f *sendTriggerFire) mutedUntil() (time.Time, string) {
	if f == nil {
		return time.Time{}, ""
	}
	now := time.Now()
	id := f.source.ID()
	until := triggers.MutedUntil(f.db, id, now)
	if unit := f.source.UnitID(); unit != "" && unit != id {
		if u := triggers.MutedUntil(f.db, unit, now); u.After(until) {
			until, id = u, unit
		}
	}
	return until, id
}
