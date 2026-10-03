package session

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/comms"
)

// Comms Ledger daemon side (docs/comms.md). Once per poll and per profile,
// after the existing inbox path has run untouched, the notify daemon drains
// the spool its producers wrote and commits one ledger record per turn. The
// ledger is written next to the inbox and the turn journal (dual write);
// nothing that reaches a parent today changes while [comms] ledger is on.
//
// Classification: a Claude child is classified from its transcript tail
// (cached per file state) exactly as the inbox path classifies it, when the
// tail still describes the spooled turn (same text hash); a backlog entry
// for an older turn is classified from the text its hook carried instead.
// The two stores then agree on identity, trigger and tier for every turn,
// except for what only the inbox path sees: a flip into the error status
// and an observed running->waiting flip with a stale transcript, both of
// which the inbox tiers urgent. Every other harness is classified from
// what its hook carried: the prompt that started the turn (a send envelope,
// an inbox or heartbeat prompt, a human) gives the trigger, the text gives
// the hash, the sentinel and the question flag; a turn whose prompt the
// daemon never saw is trigger unknown and tiers urgent. Noise is committed
// too (tier noise) so dedup and noise share are countable from the ledger.

// commsHasTextProducer reports whether a harness spools turn text: a
// status-only record is committed only for the rest (plain shell, custom
// --cmd).
func commsHasTextProducer(tool string) bool {
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "opencode", "pi", "omp":
		return true
	}
	return HookStatusTool(tool)
}

// commsToolName is the harness name stamped on a record's Tool field.
func commsToolName(inst *Instance) string {
	return strings.ToLower(strings.TrimSpace(inst.Tool))
}

// commsOpenRetry is how long a failed ledger open is remembered before the
// next pass tries again.
const commsOpenRetry = time.Minute

// commsLedgerFor returns the open ledger for a profile, opening it on first
// use. The open takes a non-blocking exclusive lock on <ledger>/daemon.lock
// so a second daemon process (`notify-daemon --once` next to the service)
// never becomes a second ingester of the same spool. nil when the ledger
// cannot be opened or is owned by another process; retried after
// commsOpenRetry.
func (d *TransitionDaemon) commsLedgerFor(profile string) *comms.Ledger {
	if d.ledgers == nil {
		d.ledgers = map[string]*comms.Ledger{}
		d.ledgerLocks = map[string]*os.File{}
		d.ledgerOpenFailed = map[string]time.Time{}
	}
	if l, ok := d.ledgers[profile]; ok {
		return l
	}
	if at, ok := d.ledgerOpenFailed[profile]; ok && time.Since(at) < commsOpenRetry {
		return nil
	}
	l, lock, err := openCommsLedgerOwned(profile)
	if err != nil {
		commsLog.Warn("comms_ledger_open_failed", slog.String("profile", profile), slog.String("error", err.Error()))
		d.ledgerOpenFailed[profile] = time.Now()
		return nil
	}
	delete(d.ledgerOpenFailed, profile)
	d.ledgers[profile] = l
	d.ledgerLocks[profile] = lock
	return l
}

// openCommsLedgerOwned opens the profile ledger after taking its daemon
// lock. The lock file lives in the ledger directory and is released by
// closing it.
func openCommsLedgerOwned(profile string) (*comms.Ledger, *os.File, error) {
	dir, err := comms.Dir(profile)
	if err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "daemon.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		return nil, nil, errors.New("comms: another daemon process owns the ledger")
	}
	l, err := comms.OpenDir(profile, dir)
	if err != nil {
		_ = lock.Close()
		return nil, nil, err
	}
	return l, lock, nil
}

// dropCommsLedger closes a profile's ledger after a write failure so the
// next pass reopens it: the events bus disables itself after a failed
// append, and a reopen is the only way back.
func (d *TransitionDaemon) dropCommsLedger(profile string) {
	if l := d.ledgers[profile]; l != nil {
		_ = l.Close()
	}
	delete(d.ledgers, profile)
	if lock := d.ledgerLocks[profile]; lock != nil {
		_ = lock.Close()
	}
	delete(d.ledgerLocks, profile)
}

// closeCommsLedgers releases every open ledger (daemon shutdown).
func (d *TransitionDaemon) closeCommsLedgers() {
	for profile := range d.ledgers {
		d.dropCommsLedger(profile)
	}
}

// ingestCommsSpool drains the spool for every child of this profile. It runs
// only with [comms] ledger on; with it off the daemon never creates the
// ledger directory. Entries for instances this profile does not know are
// left for the owning profile's pass; the prune bounds leftovers.
func (d *TransitionDaemon) ingestCommsSpool(profile string, byID map[string]*Instance) {
	// The prune runs whatever the switch says: a spool left by a child whose
	// daemon had the ledger on (or by a newer child binary) must not grow
	// under a daemon that never drains it.
	if now := time.Now(); now.Sub(d.lastCommsPrune) > time.Hour {
		d.lastCommsPrune = now
		PruneCommsSpool(now)
	}
	if !CommsLedgerEnabled() {
		return
	}
	instances := ListCommsSpoolInstances()
	if len(instances) == 0 {
		return
	}
	l := d.commsLedgerFor(profile)
	if l == nil {
		return
	}
	if d.commsPrompts == nil {
		d.commsPrompts = map[string]CommsSpoolEntry{}
	}
	for _, id := range instances {
		inst := byID[id]
		if inst == nil {
			continue
		}
		entries, err := ReadCommsSpool(id)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !d.ingestCommsEntry(l, profile, inst, byID, e) {
				// A failed commit keeps this entry AND everything after it
				// in order for the next pass; the ledger is reopened then.
				d.dropCommsLedger(profile)
				return
			}
			RemoveCommsSpoolEntry(e)
		}
	}
}

// ingestCommsEntry turns one spooled edge into at most one ledger record.
// It returns true when the entry is consumed (committed, a duplicate, or a
// prompt edge remembered) and false when the commit failed transiently so
// the next poll retries the same file.
func (d *TransitionDaemon) ingestCommsEntry(l *comms.Ledger, profile string, inst *Instance, byID map[string]*Instance, e CommsSpoolEntry) bool {
	if e.Edge == CommsEdgePromptStart {
		d.commsPrompts[inst.ID] = e
		return true
	}
	if e.Edge != CommsEdgeTurnEnd {
		return true
	}

	rec := comms.Record{
		Kind:    comms.KindTurn,
		From:    inst.ID,
		To:      []string{statsParentFor(inst)},
		Profile: profile,
		Tool:    commsToolName(inst),
		TSignal: e.TSignal,
	}
	cfg := ResolveInboxConfig(parentTitleFor(inst, byID))
	facts, classified := commsTurnFacts(inst, e, d.commsPrompts[inst.ID])
	if !cfg.GetQuestionWakes() {
		facts.Question = false
	}

	var prev *TurnJournalEntry
	if last, ok := l.LastTurn(inst.ID); ok {
		prev = &TurnJournalEntry{Status: string(StatusWaiting), Tier: last.Tier, TextHash: last.TH, DoneStatus: last.Done, DoneSummary: last.Summary}
	}
	rec.Tier = ClassifyTurnTier(facts, string(StatusWaiting), prev)
	rec.Trigger = facts.Trigger
	rec.Text = CapTurnText(facts.Text, cfg.GetMaxTextBytes())
	rec.TH = facts.TextHash
	rec.Q = facts.Question
	if facts.HasDone {
		rec.Done, rec.Summary = facts.Done.Status, facts.Done.Summary
	}
	if facts.FromID != "" {
		rec.ReplyTo = facts.FromID
		rec.To = append(rec.To, facts.FromID)
	}
	// Identity, most stable first: the transcript uuid, the harness turn id,
	// else the spool entry itself (its file name is minted once by the
	// producer and survives a retry), so two turns with the same text are
	// two records and one entry observed twice is one.
	identity := facts.UUID
	if identity == "" {
		identity = e.TurnID
	}
	if identity == "" {
		identity = e.SessionID + "|" + facts.TextHash + "|" + e.ID()
	}
	if classified {
		rec.Key = comms.Key(comms.KindTurn, inst.ID, identity)
	} else {
		rec.Key = comms.Key(comms.KindTurn, inst.ID, e.Harness, identity)
	}

	_, cursor, err := l.Commit(rec)
	if err != nil && !errors.Is(err, comms.ErrDuplicate) {
		commsLog.Warn("comms_turn_commit_failed", slog.String("child", inst.ID), slog.String("error", err.Error()))
		return false
	}
	// The prompt edge is consumed by the turn it started, once that turn is
	// durable; a later turn with no new prompt edge is unknown, not a repeat
	// of the old trigger.
	delete(d.commsPrompts, inst.ID)
	if err == nil {
		commsLog.Debug("comms_turn_committed", slog.String("child", inst.ID), slog.String("tool", rec.Tool),
			slog.String("tier", rec.Tier), slog.String("trigger", rec.Trigger), slog.Uint64("cursor", uint64(cursor)))
	}
	return true
}

// commsTurnFacts reduces a spooled turn to the facts the tier rule needs.
// classified is true when the Claude transcript classifier produced them
// (same identity and trigger as the inbox record for this turn); false when
// they were derived from the hook payload and the remembered prompt. The
// transcript tail is used only while it still describes the spooled turn:
// a backlog entry whose text the tail no longer matches keeps its own text.
func commsTurnFacts(inst *Instance, e CommsSpoolEntry, prompt CommsSpoolEntry) (TurnFacts, bool) {
	text := strings.TrimSpace(e.Text)
	if IsClaudeCompatible(inst.Tool) {
		path := e.TranscriptPath
		if path == "" {
			path = inst.GetJSONLPath()
		}
		if clean, ok := ValidateTranscriptPath(path); ok {
			if facts, err := turnFacts.Facts(clean); err == nil && !facts.Pending && facts.TextHash != "" &&
				(text == "" || facts.TextHash == turnTextHash(text)) {
				return facts, true
			}
		}
	}
	facts := TurnFacts{Text: text, TextHash: turnTextHash(text)}
	facts.Question = textAsksParent(text)
	facts.Done, facts.HasDone = ScanDoneSentinel(text)
	trigger := e.Prompt
	if trigger == "" {
		trigger = prompt.Prompt
	}
	facts.Trigger, facts.FromID = commsPromptTrigger(trigger)
	return facts, false
}

// commsStatusRecord commits a status-only record for a tool with no text
// producer (plain shell, a custom --cmd), so the ledger still shows the edge
// the inbox's legacy record carries. Called from the legacy branch of
// emitTurn; a no-op with the ledger off or for tools that spool text.
func (d *TransitionDaemon) commsStatusRecord(profile string, inst *Instance, to string, at time.Time) {
	if inst == nil || !CommsLedgerEnabled() || commsHasTextProducer(inst.Tool) {
		return
	}
	l := d.commsLedgerFor(profile)
	if l == nil {
		return
	}
	state := normalizeStatusString(to)
	rec := comms.Record{
		Kind:    comms.KindStatus,
		From:    inst.ID,
		To:      []string{statsParentFor(inst)},
		Profile: profile,
		Tool:    commsToolName(inst),
		State:   state,
		TSignal: at.UnixMilli(),
		Key:     comms.Key(comms.KindStatus, inst.ID, state, transitionEventOutputHash(inst), at.Truncate(shortWindowDedupSeconds*time.Second).String()),
	}
	if _, _, err := l.Commit(rec); err != nil && !errors.Is(err, comms.ErrDuplicate) {
		commsLog.Warn("comms_status_commit_failed", slog.String("child", inst.ID), slog.String("error", err.Error()))
		d.dropCommsLedger(profile)
	}
}
