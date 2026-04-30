package session

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
)

// EventFingerprint returns a stable identifier for a transition event,
// keyed on its intrinsic identity rather than the moment of any particular
// emit. Two attempts to persist the "same" logical transition (same child,
// same status flip, same observed timestamp) collapse to the same
// fingerprint and are deduplicated by the inbox writer and the
// notifier-missed log.
//
// Keying on Timestamp.UnixNano() is load-bearing: scheduleBusyRetry and the
// deferred-queue drain both fire from the same TransitionNotificationEvent,
// so the timestamp set by the daemon when it first observed the flip is
// stable across retry attempts. time.Now() at write-emit time would NOT be
// stable and would silently break dedup.
//
// The fingerprint is a hex SHA-256 so it can safely be embedded in a JSON
// string field without escaping concerns and is cheap to grep for.
func EventFingerprint(e TransitionNotificationEvent) string {
	var b strings.Builder
	b.Grow(len(e.ChildSessionID) + len(e.FromStatus) + len(e.ToStatus) + 32)
	b.WriteString(strings.TrimSpace(e.ChildSessionID))
	b.WriteByte('|')
	b.WriteString(strings.ToLower(strings.TrimSpace(e.FromStatus)))
	b.WriteByte('|')
	b.WriteString(strings.ToLower(strings.TrimSpace(e.ToStatus)))
	b.WriteByte('|')
	b.WriteString(strconv.FormatInt(e.Timestamp.UnixNano(), 10))
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}
