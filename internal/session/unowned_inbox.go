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
	// Deliberately parentless children keep the #805 warn-once-drop contract:
	// every top-level session is an "orphan", and persisting all their
	// transitions would fill the unowned ledger with events nobody owns.
	// Only a parent that WAS named but cannot be resolved goes unowned.
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
	if signal := strings.TrimSpace(event.LastOutputHash); signal != "" {
		return signal
	}
	if event.Kind == transitionKindFinished || event.Timestamp.IsZero() {
		return ""
	}
	return "emit:" + strconv.FormatInt(event.Timestamp.UTC().UnixNano(), 10)
}
