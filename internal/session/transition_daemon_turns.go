package session

import (
	"log/slog"
	"strings"
	"time"
)

// emitTurn is the single producer chokepoint for a child's finished turn
// (issue #2469). The three observation paths in the daemon (snapshot edge,
// fresh hook candidate, recorded-turn key change) all land here, so one turn
// is classified once, journaled once and committed once whatever path saw it
// first:
//
//  1. Read the turn facts from the transcript tail (cached per file state).
//     A turn whose assistant record has not flushed yet is PENDING: nothing
//     is emitted and the caller leaves its own bookkeeping untouched so the
//     next poll retries.
//  2. Tier it against the child's last journaled turn: noise is counted and
//     dropped; urgent and info get one journal line and one inbox record that
//     carries the child's text. A completion sentinel makes that one record
//     a finished record (no separate transition record).
//
// Tools without a readable Claude transcript take the legacy path: the size
// or Codex signal, no text, treated as urgent. That is today's behaviour.
//
// observedFlip is true when the caller SAW the child run and stop (the
// snapshot edge). An observed flip with an unchanged transcript is a real turn
// whose transcript signal is stale (issue #2184: the resolved path is no
// longer the file being written), so it bypasses the noise rule; the
// notifier then flags it OutputHashStale as before. Hook re-fires and
// recorded-turn re-scans observe no flip and are subject to the noise rule.
//
// An observed flip of a turn this daemon already journaled during the same
// run (the hook or recorded-turn path saw it first) is that turn again, not a
// stale signal, so it stays noise (issue #2481).
//
// The returned bool is false for a pending turn and for a transiently failed
// commit; in both cases the caller leaves its bookkeeping untouched so the
// next poll retries.
func (d *TransitionDaemon) emitTurn(profile string, inst *Instance, byID map[string]*Instance, from, to string, ts time.Time, observedFlip bool) (TransitionNotificationEvent, bool) {
	event := TransitionNotificationEvent{
		ChildSessionID: inst.ID,
		ChildTitle:     inst.Title,
		Profile:        profile,
		FromStatus:     from,
		ToStatus:       to,
		Timestamp:      ts,
		Substate:       string(inst.CachedSubstate()),
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}

	facts, classified := instanceTurnFacts(inst)
	if classified && facts.Pending {
		return event, false
	}
	if classified && facts.UUID == "" && facts.TextHash == "" {
		// No turn identity (an interrupted turn, a reply with no text): the
		// noise rule cannot recognise it next poll, so journaling it would add
		// one line per observation. Treat it as unclassified: legacy signal,
		// the notifier's dedup is the bound.
		classified = false
	}
	event.LastOutputHash = transitionEventOutputHash(inst)
	statsParent := statsParentFor(inst)
	selfConductor := isSelfSuppressedConductor(inst)

	if !classified {
		if selfConductor {
			// Issue #2481: no transcript, so no sender to answer; the notifier
			// would drop it as self_conductor after a registry load.
			d.commsStatusEdge(inst, from, to, event.Timestamp)
			return dropSelfConductorTurn(event, true), true
		}
		// Legacy signal, no text: emit as before. The notifier's dedup is the
		// only improvement available without a transcript.
		result := d.notifier.NotifyTransition(event)
		if result.DeliveryResult == transitionDeliveryCommitted {
			_ = BumpInboxStats(statsParent, func(s *InboxStats) { s.RecordsLegacy++ })
		}
		// Comms Ledger: the same edge, spooled after the inbox record so a
		// ledger problem can never delay or lose the parent's wake.
		d.commsStatusEdge(inst, from, to, event.Timestamp)
		if IsClaudeCompatible(inst.Tool) && strings.TrimSpace(inst.ParentSessionID) != "" {
			// A Claude turn with no identity (interrupted, no text) reaches
			// the inbox only; the ledger gets the same edge so a parent it
			// delivers to misses nothing the inbox would have shown.
			d.commsInboxOnlyEdge(inst, from, to, event.Timestamp)
		}
		return result, true
	}

	// Issue #2473: the task turn that settles background work a held tagged
	// send started answers that send. Carry the sender over so it receives
	// the result (the held send turn itself was never recorded).
	var carried *heldSendOrigin
	if facts.Trigger == TurnTriggerTask && strings.TrimSpace(facts.FromID) == "" {
		if carried = loadHeldSend(inst.ID); carried != nil {
			facts.FromID = carried.FromID
		}
	}

	// Issue #2481: a top-level conductor's own turn reaches no inbox; only a
	// turn that answers a tagged send (its reply goes to the asker) or carries
	// a completion sentinel (the completion ledger) needs the rest of the
	// path. Drop the others before tiering, journaling, stats and the
	// notifier's registry load: they were 42% of all transition frames.
	if selfConductor && !facts.HasDone &&
		!eventAnswersSend(TransitionNotificationEvent{FromID: facts.FromID, Trigger: facts.Trigger}) {
		// One bus frame per turn, as the noise rule gave before: a repeat
		// sighting publishes only when the child was seen to run again.
		seen := TurnJournalEntry{UUID: facts.UUID, TextHash: facts.TextHash, Status: to}
		repeat := d.lastSelfTurn[inst.ID] == seen
		if d.lastSelfTurn == nil {
			d.lastSelfTurn = map[string]TurnJournalEntry{}
		}
		d.lastSelfTurn[inst.ID] = seen
		return dropSelfConductorTurn(event, observedFlip || !repeat), true
	}

	cfg := ResolveInboxConfig(parentTitleFor(inst, byID))
	if !cfg.GetQuestionWakes() {
		facts.Question = false
	}
	prev := LastTurnJournalEntry(inst.ID)
	classPrev := prev
	if last, ok := d.lastSelfTurn[inst.ID]; ok && prev == nil {
		// A conductor reparented after its last (skipped, unjournaled) turn:
		// a re-observation of that turn is not news for the new parent.
		classPrev = &last
	}
	tier := ClassifyTurnTier(facts, to, classPrev)
	staleFlip := false
	if tier == TurnTierNoise && observedFlip && !d.journaledThisRun(profile, inst.ID, facts.UUID) {
		tier = TurnTierUrgent // a real turn the transcript cannot distinguish; never silent
		staleFlip = true
	}
	if tier == TurnTierNoise {
		_ = BumpInboxStats(statsParent, func(s *InboxStats) {
			if prev != nil && prev.UUID != "" && prev.UUID == facts.UUID {
				s.DedupSuppressed++
			} else {
				s.NoiseSuppressed++
			}
		})
		event.DeliveryResult = transitionDeliveryDropped
		return event, true
	}
	// Issue #2481: an identical completion re-printed by a finished worker's
	// leftover scheduled check is counted on the ledger, not delivered.
	if facts.HasDone {
		if repeat, counted := checkDoneRepeat(inst.ID, profile, facts.Done, facts.UUID, doneRepeatBackground(facts), true, event.Timestamp); repeat {
			_ = BumpInboxStats(statsParent, func(s *InboxStats) {
				if counted {
					s.DoneRepeats++
				} else {
					s.DedupSuppressed++
				}
			})
			d.rememberDone(profile, inst.ID, facts.Done)
			event.DeliveryResult = transitionDeliveryDropped
			return event, true
		}
	}

	text := CapTurnText(facts.Text, cfg.GetMaxTextBytes())
	entry := TurnJournalEntry{
		TS:       event.Timestamp,
		Child:    inst.ID,
		Profile:  profile,
		Status:   to,
		Tier:     tier,
		Trigger:  facts.Trigger,
		UUID:     facts.UUID,
		TextHash: facts.TextHash,
		Text:     text,
		Question: facts.Question,
		FromID:   facts.FromID,
	}
	if facts.HasDone {
		entry.DoneStatus = facts.Done.Status
		entry.DoneSummary = facts.Done.Summary
	}
	event.Tier = tier
	event.Trigger = facts.Trigger
	event.TurnUUID = facts.UUID
	event.TextHash = facts.TextHash
	event.Text = text
	event.Question = facts.Question
	event.FromID = facts.FromID
	// The journal seq is assigned on append; stamp the record with the seq it
	// WILL get so a reader can line the two up (appends are serialised per
	// child in this single daemon goroutine).
	if prev != nil {
		event.Seq = prev.Seq + 1
	} else {
		event.Seq = 1
	}

	// Commit first, journal second. A transiently failed commit (storage
	// hiccup, the per-child pending cap) must NOT leave a journal line, or the
	// next poll would read this turn as already seen and never retry it.
	var result TransitionNotificationEvent
	if facts.HasDone {
		event.DoneStatus = facts.Done.Status
		event.DoneSummary = facts.Done.Summary
		result = d.notifier.NotifyFinished(event)
	} else {
		result = d.notifier.NotifyTransition(event)
	}
	if result.DeliveryResult == transitionDeliveryFailed {
		return result, false
	}
	// The held send is answered: by the task turn that carried its sender,
	// or by the send turn itself once its hold lapsed. Either way no later
	// turn may reply to that sender again.
	if carried != nil {
		clearHeldSend(inst.ID)
	} else if facts.Trigger == TurnTriggerSend {
		// Matched by turn, or by sender: a poll can remember the send while
		// its turn is still writing, so the stored uuid may be an earlier
		// assistant record of this same turn.
		if held := loadHeldSend(inst.ID); held != nil && (held.UUID == facts.UUID || held.FromID == facts.FromID) {
			clearHeldSend(inst.ID)
		}
	}
	if _, err := UpsertTurnJournal(entry, cfg.GetJournalKeep()); err != nil {
		commsLog.Warn("turn_journal_append_failed",
			slog.String("child", inst.ID), slog.String("error", err.Error()))
	}
	d.noteJournaledTurn(profile, inst.ID, facts.UUID)
	// Counters count records that landed, not observations: a turn the
	// notifier dropped (a duplicate, a dead letter) is journaled but is not a
	// record.
	if result.DeliveryResult == transitionDeliveryCommitted {
		_ = BumpInboxStats(statsParent, func(s *InboxStats) {
			if tier == TurnTierUrgent {
				s.RecordsUrgent++
			} else {
				s.RecordsInfo++
			}
			s.TextBytes += int64(len(text))
		})
	}
	if facts.HasDone {
		d.noteDoneEmitted(profile, inst, facts.Done, facts.UUID, event.Timestamp)
	}
	if (staleFlip || to == string(StatusError)) && strings.TrimSpace(inst.ParentSessionID) != "" {
		// Urgent only to the inbox (its own inputs: a flip into the error
		// status, an observed flip with a stale transcript): the ledger,
		// whose spooled turn cannot see either, gets the edge as a status
		// record after the inbox record is committed.
		d.commsInboxOnlyEdge(inst, from, to, event.Timestamp)
	}
	return result, true
}

// journaledThisRun reports whether emitTurn journaled turn uuid for the
// child since the daemon last saw the child running.
func (d *TransitionDaemon) journaledThisRun(profile, childID, uuid string) bool {
	return uuid != "" && d.journaledRun[profile][childID] == uuid
}

// noteJournaledTurn remembers the turn uuid just journaled for the child.
func (d *TransitionDaemon) noteJournaledTurn(profile, childID, uuid string) {
	if uuid == "" {
		return
	}
	if d.journaledRun == nil {
		d.journaledRun = map[string]map[string]string{}
	}
	if d.journaledRun[profile] == nil {
		d.journaledRun[profile] = map[string]string{}
	}
	d.journaledRun[profile][childID] = uuid
}

// forgetJournaledTurnsOfRunning starts a new run for every child the pass
// sees running, and drops children that left the profile.
func (d *TransitionDaemon) forgetJournaledTurnsOfRunning(profile string, statuses map[string]string) {
	run := d.journaledRun[profile]
	for id := range run {
		if st, ok := statuses[id]; !ok || normalizeStatusString(st) == string(StatusRunning) {
			delete(run, id)
		}
	}
}

// dropSelfConductorTurn is the result the notifier would have produced for a
// top-level conductor's own turn. The bus frame (when publish) is kept so
// event consumers still see the edge; nothing else is written.
func dropSelfConductorTurn(event TransitionNotificationEvent, publish bool) TransitionNotificationEvent {
	event.DeliveryResult = transitionDeliveryDropped
	event.DeadLetterReason = deadLetterReasonSelfConductor
	if publish {
		publishSelfTurnFrame("session.transition", event)
	}
	return event
}

// publishSelfTurnFrame is publishTransitionEvent; tests count its calls (the
// process-wide bus can be closed only once per test binary).
var publishSelfTurnFrame = publishTransitionEvent

// noteDoneEmitted records that emitTurn already delivered this completion so
// emitDoneSignals (which reads the hook file's done fields) does not emit a
// second finished record, and mirrors it into the non-destructive completion
// ledger that `session children` reads.
func (d *TransitionDaemon) noteDoneEmitted(profile string, inst *Instance, sig DoneSignal, turnUUID string, at time.Time) {
	d.rememberDone(profile, inst.ID, sig)
	_ = WriteLedgerEntry(CompletionLedgerEntry{
		ChildID:    inst.ID,
		Profile:    profile,
		Title:      inst.Title,
		Status:     sig.Status,
		Summary:    sig.Summary,
		FinishedAt: at,
		TurnUUID:   turnUUID,
	})
}

// rememberDone marks sig as the child's handled completion so the hook-file
// path (emitDoneSignals) does not deliver or count it a second time.
func (d *TransitionDaemon) rememberDone(profile, childID string, sig DoneSignal) {
	if d.lastDone[profile] == nil {
		d.lastDone[profile] = map[string]DoneSignal{}
	}
	d.lastDone[profile][childID] = sig
}

// parentTitleFor resolves the registered parent's title for config
// overrides, or "" when the parent is not in this pass's registry.
func parentTitleFor(inst *Instance, byID map[string]*Instance) string {
	if inst == nil || inst.ParentSessionID == "" || byID == nil {
		return ""
	}
	if p := byID[inst.ParentSessionID]; p != nil {
		return p.Title
	}
	return ""
}
