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
// (cached per file state) as the inbox path classifies it, when the tail
// still describes the spooled turn (same full-text hash, not signalled
// before the tail record); a backlog entry for an older turn is classified
// from the text its hook carried instead. The two stores then agree on
// identity, trigger and tier for every turn, except for what only the inbox
// path sees: a flip into the error status and an observed running->waiting
// flip with a stale transcript, both of which the inbox tiers urgent. Codex
// is classified from what its notify carried: the prompt that started the
// turn (a send envelope, an inbox or heartbeat prompt, a human) gives the
// trigger, the text gives the hash, the sentinel and the question flag; a
// turn whose prompt the daemon never saw is trigger unknown and tiered by
// the same rule (urgent only for a sentinel, an error or a question). A
// repeated background answer is committed with tier noise so
// dedup and noise share are countable from the ledger.
//
// Every other harness is status-only in P1: the legacy branch of emitTurn
// spools a status edge AFTER the inbox record is committed, and the next
// pass commits it with the inbox's own content rule.

// commsHasTextProducer reports whether a harness spools turn text. P1
// enables Claude (hook-handler) and Codex (codex-notify); every other
// harness is status-only (docs/comms.md) and gets status records.
func commsHasTextProducer(tool string) bool {
	return IsClaudeCompatible(tool) || IsCodexCompatible(tool)
}

// commsToolName is the harness name stamped on a record's Tool field.
func commsToolName(inst *Instance) string {
	return strings.ToLower(strings.TrimSpace(inst.Tool))
}

// commsOpenRetry is how long a failed ledger open or commit is remembered
// before the next pass tries again, so a broken disk costs one attempt per
// minute, not one per poll.
const commsOpenRetry = time.Minute

// commsTailSkew is how much earlier than the transcript record's own
// timestamp a spool entry may be signalled and still be that turn (clock
// granularity between the harness and the hook). Shorter than the daemon's
// poll interval so two same-text turns seconds apart stay distinct.
const commsTailSkew = time.Second

// commsLedgerFor returns the open ledger for a profile, opening it on first
// use. The open takes a non-blocking exclusive lock on <ledger>/daemon.lock
// so a second daemon process (`notify-daemon --once` next to the service)
// never becomes a second ingester of the same spool. nil when the ledger
// cannot be opened or is owned by another process; retried after
// commsOpenRetry. The open is bounded: the dedup rebuild fails visibly
// (comms.ErrTailUnreadable) instead of holding the poll loop.
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
	_ = os.Chmod(dir, 0o700)
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

// dropCommsLedger closes a profile's ledger after a write failure and
// starts the retry backoff: the events bus disables itself after a failed
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
	if d.ledgerOpenFailed == nil {
		d.ledgerOpenFailed = map[string]time.Time{}
	}
	d.ledgerOpenFailed[profile] = time.Now()
}

// closeCommsLedgers releases every open ledger (daemon shutdown).
func (d *TransitionDaemon) closeCommsLedgers() {
	for profile := range d.ledgers {
		d.dropCommsLedger(profile)
	}
	d.ledgerOpenFailed = nil
}

// ingestCommsSpool drains the spool for every child of this profile. It runs
// only with [comms] ledger on; with it off the daemon never creates the
// ledger directory and only prunes entries no daemon will ever consume.
// Entries for instances this profile does not know are left for the owning
// profile's pass.
func (d *TransitionDaemon) ingestCommsSpool(profile string, byID map[string]*Instance) {
	if !CommsLedgerEnabled() {
		// Switch off: nothing will consume the spool, so age it out (each
		// expiry logged). With the switch on an entry is kept until it
		// commits; the per-instance cap bounds an outage.
		if now := time.Now(); now.Sub(d.lastCommsPrune) > time.Hour {
			d.lastCommsPrune = now
			PruneCommsSpool(now)
		}
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
				// in order for the next pass; the ledger is reopened then,
				// after the backoff.
				d.dropCommsLedger(profile)
				return
			}
		}
	}
}

// ingestCommsEntry turns one spooled edge into at most one ledger record
// and removes the entry once it is durable. It returns false when the
// commit failed transiently so the next pass retries the same file.
//
// A prompt edge is remembered, not removed: it is removed together with the
// turn that consumes it, so a restart between the two still knows the
// trigger. A newer prompt edge for the same child replaces (and removes) an
// older one that no turn consumed.
func (d *TransitionDaemon) ingestCommsEntry(l *comms.Ledger, profile string, inst *Instance, byID map[string]*Instance, e CommsSpoolEntry) bool {
	switch e.Edge {
	case CommsEdgePromptStart:
		if prev, ok := d.commsPrompts[inst.ID]; ok && prev.path != e.path {
			RemoveCommsSpoolEntry(prev)
		}
		d.commsPrompts[inst.ID] = e
		return true
	case CommsEdgeStatus:
		return d.commitCommsStatus(l, profile, inst, e)
	case CommsEdgeTurnEnd:
	default:
		RemoveCommsSpoolEntry(e)
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
	prompt := d.commsPrompts[inst.ID]
	facts, classified := commsTurnFacts(inst, e, prompt)
	owed := commsOwedNone
	owedFrom := strings.TrimSpace(facts.FromID)
	if IsClaudeCompatible(inst.Tool) {
		facts.FromID, owed = commsHeldSendReplyTo(inst, facts, classified)
	}
	if !cfg.GetQuestionWakes() {
		facts.Question = false
	}

	// Identity, most stable first: the transcript uuid, the harness thread
	// and turn id, else the spool entry itself (its file name is minted once
	// by the producer and survives a retry), so two turns with the same text
	// are two records and one entry observed twice is one. The tier rule
	// reads the identity as the turn uuid: a distinct turn that repeats the
	// last answer is news when a human or a send started it, noise only for
	// a background trigger.
	identity := facts.UUID
	if identity == "" && e.TurnID != "" {
		identity = e.SessionID + ":" + e.TurnID
	}
	if identity == "" {
		identity = e.SessionID + "|" + facts.TextHash + "|" + e.ID()
	}
	if facts.UUID == "" {
		facts.UUID = identity
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
	case errors.Is(err, comms.ErrDuplicate):
	case errors.Is(err, comms.ErrConflict):
		// Same identity, different content: a producer bug or a replayed
		// entry from another turn. Kept aside for a human, never consumed
		// as the first record and never retried as a new one.
		commsLog.Warn("comms_conflict", slog.String("child", inst.ID), slog.String("key", rec.Key), slog.String("entry", e.ID()))
		QuarantineCommsSpoolEntry(e)
		return true
	default:
		commsLog.Warn("comms_turn_commit_failed", slog.String("child", inst.ID), slog.String("error", err.Error()))
		return false
	}
	// The turn is durable (or already was): settle what it owes or pays,
	// then the entry and the prompt edge it consumed can go.
	switch owed {
	case commsOwedRemember:
		rememberLedgerOwedSender(inst.ID, owedFrom)
	case commsOwedPaid:
		clearLedgerOwedSender(inst.ID)
	}
	// The turn is durable (or already was): the entry and the prompt edge
	// it consumed can go. A later turn with no new prompt edge is unknown,
	// not a repeat of the old trigger.
	RemoveCommsSpoolEntry(e)
	if prompt.path != "" {
		RemoveCommsSpoolEntry(prompt)
	}
	delete(d.commsPrompts, inst.ID)
	return true
}

// commsOwed says what committing a Claude turn does to the sender the
// ledger owes a held send's result.
type commsOwed int

const (
	commsOwedNone     commsOwed = iota
	commsOwedRemember           // a held send turn: its sender is owed the result
	commsOwedPaid               // the settling task turn: the owed sender is answered
)

// commsHeldSendReplyTo returns the sender a Claude turn's record replies to
// when a tagged send hands off to background work (issue #2473), and what
// committing the turn does to the ledger's owed sender. The ledger keeps this
// state itself (rememberLedgerOwedSender): it never reads the inbox's held
// send record or its turn journal, which are written only when the child's
// transition notifications are on, are cleared when the inbox answers through
// another turn (a permission menu while the work runs), and drift from the
// ledger whenever the two run at different times. So the sender is answered
// exactly once in the ledger whatever the inbox does:
//   - a send turn the background work still holds when it is committed
//     replies to no one, and its sender becomes owed;
//   - the first classified task turn committed once the work no longer holds
//     the turn replies to the owed sender and pays it;
//   - every other turn replies to its own sender, and a send turn committed
//     unheld (a late drain after the work settled) owes nothing.
//
// The ledger may answer on a different turn than the inbox (the result turn
// where the inbox answered the send turn at a menu); each store answers once.
func commsHeldSendReplyTo(inst *Instance, facts TurnFacts, classified bool) (string, commsOwed) {
	from := strings.TrimSpace(facts.FromID)
	switch facts.Trigger {
	case TurnTriggerSend:
		if from != "" && backgroundWorkHoldsTurn(inst) {
			return "", commsOwedRemember
		}
	case TurnTriggerTask:
		if from == "" && classified && !backgroundWorkHoldsTurn(inst) {
			if owed := loadLedgerOwedSender(inst.ID); owed != "" {
				return owed, commsOwedPaid
			}
		}
	}
	return from, commsOwedNone
}

// commsTurnFacts reduces a spooled turn to the facts the tier rule needs.
// classified is true when the Claude transcript classifier produced them
// (same identity and trigger as the inbox record for this turn); false when
// they were derived from the hook payload and the remembered prompt. The
// transcript tail is used only while it still describes the spooled turn:
// the hash of the full text the hook saw must equal the tail's, and the
// entry must not predate the tail record (a backlog of same-text turns
// takes the hook path, each with its own identity). On the hook path the
// hash is the full-text hash the producer spooled, and the sentinel and
// question are read from the spooled text (capped at commsSpoolTextBytes,
// which keeps the end of any ordinary reply).
func commsTurnFacts(inst *Instance, e CommsSpoolEntry, prompt CommsSpoolEntry) (TurnFacts, bool) {
	text := strings.TrimSpace(e.Text)
	if IsClaudeCompatible(inst.Tool) {
		path := e.TranscriptPath
		if path == "" {
			path = inst.GetJSONLPath()
		}
		if clean, ok := ValidateTranscriptPath(path); ok {
			if facts, err := turnFacts.Facts(clean); err == nil && !facts.Pending && facts.TextHash != "" &&
				commsTailDescribes(facts, e, text) {
				return facts, true
			}
		}
	}
	facts := TurnFacts{Text: text, TextHash: e.TH}
	if facts.TextHash == "" {
		facts.TextHash = turnTextHash(text)
	}
	facts.Question = textAsksParent(text)
	facts.Done, facts.HasDone = ScanDoneSentinel(text)
	trigger := e.Prompt
	if trigger == "" {
		trigger = prompt.Prompt
	}
	facts.Trigger, facts.FromID = commsPromptTrigger(trigger)
	return facts, false
}

// commsTailDescribes reports whether the transcript tail is the spooled
// turn: same full-text hash (the spool carries it uncapped as TH; an entry
// without one compares its capped text to the tail's capped text) and not
// signalled before the tail record was written.
func commsTailDescribes(facts TurnFacts, e CommsSpoolEntry, text string) bool {
	switch {
	case e.TH != "":
		if facts.TextHash != e.TH {
			return false
		}
	case text != "":
		if turnTextHash(capBytes(strings.TrimSpace(facts.Text), commsSpoolTextBytes)) != turnTextHash(text) {
			return false
		}
	}
	if !facts.At.IsZero() && e.TSignal > 0 && e.TSignal < facts.At.Add(-commsTailSkew).UnixMilli() {
		return false
	}
	return true
}

// commsStatusEdge spools a status-only edge for a tool with no text
// producer (plain shell, a custom --cmd, and every harness whose producer
// is not enabled in P1), so the ledger still shows the edge the inbox's
// legacy record carries. Called from the legacy branch of emitTurn AFTER
// the inbox record is committed; the spool keeps the edge retryable and
// the daemon never opens the ledger on the inbox path. A no-op with the
// ledger off or for tools that spool text.
func (d *TransitionDaemon) commsStatusEdge(inst *Instance, from, to string, at time.Time) {
	if inst == nil || !CommsLedgerEnabled() || commsHasTextProducer(inst.Tool) {
		return
	}
	if err := WriteCommsSpool(CommsSpoolEntry{
		Harness: commsToolName(inst), Event: "status", Edge: CommsEdgeStatus, Instance: inst.ID,
		From: normalizeStatusString(from), State: normalizeStatusString(to), TH: transitionEventOutputHash(inst),
		TSignal: at.UnixMilli(),
	}); err != nil {
		commsLog.Warn("comms_status_spool_failed", slog.String("child", inst.ID), slog.String("error", err.Error()))
	}
}

// commitCommsStatus commits a spooled status edge. Its identity is the
// spool entry id (minted once by the producer), so a replay of the same
// entry is a duplicate of its key whatever was committed in between and
// however late it comes. Separately, a re-observation of the same edge is
// collapsed by the inbox's own rule (issues #1142 and #824): the same
// from->to edge with the same non-empty output signal within the 2 h TTL,
// or, when either signal is empty, within the 90 s short window. That rule
// mirrors what the inbox records; it is not what makes a replay safe. Two
// distinct edges are never folded by a time bucket.
func (d *TransitionDaemon) commitCommsStatus(l *comms.Ledger, profile string, inst *Instance, e CommsSpoolEntry) bool {
	if last, ok := l.LastStatus(inst.ID); ok && last.State == e.State && last.Ref == e.From {
		window := defaultOutputHashDedupTTL
		if last.TH == "" || e.TH == "" {
			window = shortWindowDedupSeconds * time.Second
		}
		if last.TH == e.TH && e.TSignal-last.TSignal < window.Milliseconds() {
			RemoveCommsSpoolEntry(e)
			return true
		}
	}
	rec := comms.Record{
		Kind:    comms.KindStatus,
		From:    inst.ID,
		To:      []string{statsParentFor(inst)},
		Profile: profile,
		Tool:    commsToolName(inst),
		State:   e.State,
		Ref:     e.From, // the status the edge left
		TH:      e.TH,   // the output signal the edge was observed with
		TSignal: e.TSignal,
	}
	if id := e.ID(); id != "" {
		rec.Key = comms.Key(comms.KindStatus, inst.ID, id)
	}
	_, _, err := l.Commit(rec)
	switch {
	case err == nil, errors.Is(err, comms.ErrDuplicate):
	case errors.Is(err, comms.ErrConflict):
		// The entry's key holds different content (a replay after the child
		// was re-parented): kept aside as a turn would be, so it never
		// blocks the edges behind it.
		commsLog.Warn("comms_conflict", slog.String("child", inst.ID), slog.String("key", rec.Key), slog.String("entry", e.ID()))
		QuarantineCommsSpoolEntry(e)
		return true
	default:
		commsLog.Warn("comms_status_commit_failed", slog.String("child", inst.ID), slog.String("error", err.Error()))
		return false
	}
	RemoveCommsSpoolEntry(e)
	return true
}
