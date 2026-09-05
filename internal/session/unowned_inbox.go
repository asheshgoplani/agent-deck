package session

import (
	"strconv"
	"strings"
)

// UnownedInboxID is the durable ledger for events with no resolvable parent on
// this host. It uses the inbox store and therefore inherits fsync, dedup, and
// TTL sweeping.
const UnownedInboxID = "_unowned"

func isUnownedReason(reason string) bool {
	switch strings.TrimSpace(reason) {
	case deadLetterReasonParentMissing, deadLetterReasonUnresolvable:
		return true
	default:
		return false
	}
}

func recordUnownedTransition(event TransitionNotificationEvent) (bool, error) {
	if strings.TrimSpace(event.ChildSessionID) == "" {
		return false, nil
	}
	event.DoneSummary = capDoneSummary(event.DoneSummary)
	event.TargetKind = "unowned"
	event.DeliveryResult = transitionDeliveryCommitted
	event.LastOutputHash = unownedTurnSignal(event)
	if event.TurnFingerprint == "" {
		event.TurnFingerprint = TurnFingerprint(event)
	}
	return WriteInboxEventIfNew(UnownedInboxID, event)
}

// unownedTurnSignal supplies the turn signal for a transition whose producer
// could not derive one, so that a RECURRING stall stays a distinct record.
//
// Why this ledger needs it and a parent inbox does not:
//
// transitionEventOutputHash returns "" whenever the child's transcript is not
// resolvable — a non-Claude tool, or a session whose transcript has not been
// written at the moment of the flip — and that is the ordinary case for the
// quota stall this ledger exists to keep. With an empty signal, EventFingerprint
// for a transition reduces to child|from|to and TurnFingerprint to
// "flip|running>waiting"; both are then CONSTANT for a given child.
//
// For a parent-owned inbox that is harmless, because the parent CONSUMES it:
// ReadAndTruncateInbox empties the pending set, the fingerprints go with it, and
// a later recurrence lands normally. The _unowned ledger has NO consumer — the
// export is read-only by contract, and only SweepInboxByTTL ever removes a
// record — so the first stall's record stays pending and PINS both fingerprints.
// A session that stalls, is noticed, is unblocked and stalls AGAIN is then
// dropped on the producing host for the whole TTL window, and the drain has
// nothing to return. That is the reported failure — quota-stalled remote
// sessions unnoticed for hours — reappearing one layer below the fix for it.
//
// The emit instant is the right substitute precisely BECAUSE of what review P2b
// established. P2b is that the instant is a bad identity for a COMPLETION, which
// two independent producers stamp separately: the completion ledger writes
// CompletionRecord.FinishedAt while the inbox copy is stamped time.Now(), so
// keying on it made one completion look like two. A transition has ONE producer,
// and NotifyTransition stamps it once — a retry of an already-stamped event
// keeps its stamp ("if event.Timestamp.IsZero()"), so retries still collapse
// (issue #824) while a genuinely later flip is distinct. Completions are
// therefore left strictly alone: synthesizing a signal for one would make the
// _unowned copy hash differently from the ledger copy of the same completion and
// re-open P2b itself.
func unownedTurnSignal(event TransitionNotificationEvent) string {
	if signal := strings.TrimSpace(event.LastOutputHash); signal != "" {
		return signal
	}
	if event.Kind == transitionKindFinished || event.Timestamp.IsZero() {
		return strings.TrimSpace(event.LastOutputHash)
	}
	return "emit:" + strconv.FormatInt(event.Timestamp.UTC().UnixNano(), 10)
}
