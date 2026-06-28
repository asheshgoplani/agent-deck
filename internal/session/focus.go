package session

import (
	"encoding/json"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// FocusRequestKey is the metadata key the CLI writes and the TUI polls to drive
// session selection. The state.db is per-profile, so the key needs no profile
// suffix — the CLI and TUI operate on the same profile's db.
const FocusRequestKey = "focus_request"

// FocusRequestTTL bounds how long a focus request stays actionable. A TUI that
// starts minutes after a click must not jump on the stale request.
const FocusRequestTTL = 10 * time.Second

// FocusRequest is the JSON payload stored under FocusRequestKey.
type FocusRequest struct {
	ID string `json:"id"`
	TS int64  `json:"ts"` // unix nanoseconds when the request was written
}

// EncodeFocusRequest serializes a focus request payload.
func EncodeFocusRequest(id string, nowNano int64) (string, error) {
	b, err := json.Marshal(FocusRequest{ID: id, TS: nowNano})
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// DecodeFocusRequest parses a stored payload. fresh is true only when the
// payload is well-formed, has a non-empty id, and ts is within ttl of nowNano.
// A stale-but-parseable payload returns its id with fresh=false so the caller
// can still log/clear it.
func DecodeFocusRequest(val string, nowNano int64, ttl time.Duration) (id string, fresh bool) {
	if val == "" {
		return "", false
	}
	var fr FocusRequest
	if err := json.Unmarshal([]byte(val), &fr); err != nil {
		return "", false
	}
	if fr.ID == "" {
		return "", false
	}
	if nowNano-fr.TS > int64(ttl) {
		return fr.ID, false
	}
	return fr.ID, true
}

// WriteFocusRequest stores a focus request for the running TUI to consume.
func WriteFocusRequest(db *statedb.StateDB, id string, nowNano int64) error {
	val, err := EncodeFocusRequest(id, nowNano)
	if err != nil {
		return err
	}
	return db.SetMeta(FocusRequestKey, val)
}

// ReadFocusRequest returns the raw stored payload ("" if none).
func ReadFocusRequest(db *statedb.StateDB) (string, error) {
	return db.GetMeta(FocusRequestKey)
}

// ClearFocusRequest consumes the request (consume-once). statedb has no
// DeleteMeta, so an empty value is the documented "no request" sentinel.
func ClearFocusRequest(db *statedb.StateDB) error {
	return db.SetMeta(FocusRequestKey, "")
}
