package session

import (
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

func unownedTurnSignal(event TransitionNotificationEvent) string {
	return strings.TrimSpace(event.LastOutputHash)
}
