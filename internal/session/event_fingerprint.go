package session

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// EventFingerprint returns a stable identifier for a transition event, keyed on
// its intrinsic identity: which child, which status flip (or completion
// outcome), and which turn produced it. Two attempts to persist the same logical
// event collapse to the same fingerprint and are deduplicated by the inbox
// writer and the notifier-missed log.
//
// The emit instant is deliberately not part of the identity. Retries and
// independent producers can stamp one event at different times. LastOutputHash
// distinguishes interactive turns; completion kind, status, and summary
// distinguish finished turns.
//
// The fingerprint is a hex SHA-256 so it can safely be embedded in a JSON
// string field without escaping concerns and is cheap to grep for.
func EventFingerprint(e TransitionNotificationEvent) string {
	var b strings.Builder
	b.Grow(len(e.SourceRemote) + len(e.ChildSessionID) + len(e.FromStatus) + len(e.ToStatus) + len(e.LastOutputHash) + 33)
	b.WriteString(strings.TrimSpace(e.SourceRemote))
	b.WriteByte('|')
	b.WriteString(strings.TrimSpace(e.ChildSessionID))
	b.WriteByte('|')
	b.WriteString(strings.ToLower(strings.TrimSpace(e.FromStatus)))
	b.WriteByte('|')
	b.WriteString(strings.ToLower(strings.TrimSpace(e.ToStatus)))
	b.WriteByte('|')
	b.WriteString(strings.TrimSpace(e.LastOutputHash))
	// Issue #1186: finished events carry no from→to transition, so without
	// the kind + outcome the fingerprint would collapse distinct completions
	// (and could collide with a same-timestamp transition). Append them so
	// each completion assertion is its own logical event.
	if e.Kind != "" {
		b.WriteByte('|')
		b.WriteString(e.Kind)
		b.WriteByte('|')
		b.WriteString(strings.ToLower(strings.TrimSpace(e.DoneStatus)))
		b.WriteByte('|')
		b.WriteString(strings.TrimSpace(e.DoneSummary))
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}
