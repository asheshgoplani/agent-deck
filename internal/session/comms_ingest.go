package session

import (
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/comms"
)

// Comms Ledger daemon side (docs/comms.md). Once per poll and per profile,
// after the existing inbox path has run untouched, the notify daemon drains
// the spool its producers wrote and commits one ledger record per turn. The
// ledger is written next to the inbox and the turn journal (dual write);
// nothing that reaches a parent today changes while [comms] ledger is on.
//
// Classification: a Claude child is classified exactly as the inbox path
// classifies it (the transcript tail, cached per file state), so the two
// stores agree record for record. Every other harness is classified from
// what its hook carried: the prompt that started the turn (a send envelope,
// an inbox or heartbeat prompt, a human) gives the trigger, the text gives
// the hash, the sentinel and the question flag; a turn whose prompt the
// daemon never saw is trigger unknown and tiers urgent. Noise is committed
// too (tier noise) so dedup and noise share are countable from the ledger.

// commsStatusTools lists harnesses that have a text producer: a status-only
// record is committed only for the rest (plain shell, custom --cmd).
func commsHasTextProducer(tool string) bool {
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "opencode", "pi", "omp":
		return true
	}
	return HookStatusTool(tool)
}

// commsLedgerFor returns the open ledger for a profile, opening it on first
// use. nil when the ledger cannot be opened (logged once per profile).
func (d *TransitionDaemon) commsLedgerFor(profile string) *comms.Ledger {
	if d.ledgers == nil {
		d.ledgers = map[string]*comms.Ledger{}
	}
	if l, ok := d.ledgers[profile]; ok {
		return l
	}
	l, err := comms.Open(profile)
	if err != nil {
		commsLog.Warn("comms_ledger_open_failed", slog.String("profile", profile), slog.String("error", err.Error()))
		d.ledgers[profile] = nil
		return nil
	}
	d.ledgers[profile] = l
	return l
}

// closeCommsLedgers releases every open ledger (daemon shutdown).
func (d *TransitionDaemon) closeCommsLedgers() {
	for profile, l := range d.ledgers {
		if l != nil {
			_ = l.Close()
		}
		delete(d.ledgers, profile)
	}
}

// ingestCommsSpool drains the spool for every child of this profile. It runs
// only with [comms] ledger on; with it off the daemon never creates the
// ledger directory. Entries for instances this profile does not know are
// left for the owning profile's pass; the prune bounds leftovers.
func (d *TransitionDaemon) ingestCommsSpool(profile string, byID map[string]*Instance) {
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
			if d.ingestCommsEntry(l, profile, inst, e) {
				RemoveCommsSpoolEntry(e)
			}
		}
	}
	if time.Since(d.lastCommsPrune) > time.Hour {
		d.lastCommsPrune = time.Now()
		PruneCommsSpool(time.Now())
	}
}

// ingestCommsEntry turns one spooled edge into at most one ledger record.
// It returns true when the entry is consumed (committed, a duplicate, or a
// prompt edge remembered) and false when the commit failed transiently so
// the next poll retries the same file.
func (d *TransitionDaemon) ingestCommsEntry(l *comms.Ledger, profile string, inst *Instance, e CommsSpoolEntry) bool {
	switch e.Edge {
	case CommsEdgePromptStart:
		d.commsPrompts[inst.ID] = e
		return true
	case CommsEdgeTurnEnd:
	default:
		return true
	}

	rec := comms.Record{
		Kind:    comms.KindTurn,
		From:    inst.ID,
		To:      []string{statsParentFor(inst)},
		Profile: profile,
		Tool:    strings.ToLower(strings.TrimSpace(inst.Tool)),
		TSignal: e.TSignal,
	}
	cfg := ResolveInboxConfig("")
	facts, classified := commsTurnFacts(inst, e, d.commsPrompts[inst.ID])
	if !cfg.GetQuestionWakes() {
		facts.Question = false
	}
	// The prompt edge is consumed by the turn it started; a later turn with
	// no new prompt edge is unknown, not a repeat of the old trigger.
	delete(d.commsPrompts, inst.ID)

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
	identity := facts.UUID
	if identity == "" {
		identity = e.TurnID
	}
	if identity == "" {
		identity = e.SessionID + "|" + facts.TextHash
	}
	if classified {
		rec.Key = comms.Key(comms.KindTurn, inst.ID, identity)
	} else {
		rec.Key = comms.Key(comms.KindTurn, inst.ID, e.Harness, identity)
	}

	_, cursor, err := l.Commit(rec)
	switch {
	case err == nil:
		commsLog.Debug("comms_turn_committed", slog.String("child", inst.ID), slog.String("tool", rec.Tool),
			slog.String("tier", rec.Tier), slog.String("trigger", rec.Trigger), slog.Uint64("cursor", uint64(cursor)))
		return true
	case errors.Is(err, comms.ErrDuplicate):
		return true
	default:
		commsLog.Warn("comms_turn_commit_failed", slog.String("child", inst.ID), slog.String("error", err.Error()))
		return false
	}
}

// commsTurnFacts reduces a spooled turn to the facts the tier rule needs.
// classified is true when the Claude transcript classifier produced them
// (same identity and trigger as the inbox record for this turn); false when
// they were derived from the hook payload and the remembered prompt.
func commsTurnFacts(inst *Instance, e CommsSpoolEntry, prompt CommsSpoolEntry) (TurnFacts, bool) {
	if IsClaudeCompatible(inst.Tool) {
		path := e.TranscriptPath
		if path == "" {
			path = inst.GetJSONLPath()
		}
		if clean, ok := ValidateTranscriptPath(path); ok {
			if facts, err := turnFacts.Facts(clean); err == nil && !facts.Pending && facts.TextHash != "" {
				return facts, true
			}
		}
	}
	text := strings.TrimSpace(e.Text)
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
	rec := comms.Record{
		Kind:    comms.KindStatus,
		From:    inst.ID,
		To:      []string{statsParentFor(inst)},
		Profile: profile,
		Tool:    strings.ToLower(strings.TrimSpace(inst.Tool)),
		State:   normalizeStatusString(to),
		TSignal: at.UnixMilli(),
		Key:     comms.Key(comms.KindStatus, inst.ID, normalizeStatusString(to), transitionEventOutputHash(inst), at.Truncate(shortWindowDedupSeconds*time.Second).String()),
	}
	if _, _, err := l.Commit(rec); err != nil && !errors.Is(err, comms.ErrDuplicate) {
		commsLog.Warn("comms_status_commit_failed", slog.String("child", inst.ID), slog.String("error", err.Error()))
	}
}
