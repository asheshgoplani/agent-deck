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
	// Attach asks the TUI to open/attach the session (as if the user pressed
	// Enter), not merely move the cursor to it. omitempty keeps a plain
	// select-only request byte-identical to the pre-attach format.
	Attach bool `json:"attach,omitempty"`
}

// EncodeFocusRequest serializes a select-only focus request payload.
func EncodeFocusRequest(id string, nowNano int64) (string, error) {
	return EncodeFocusRequestAttach(id, nowNano, false)
}

// EncodeFocusRequestAttach serializes a focus request, optionally asking the TUI
// to attach the session rather than just selecting it.
func EncodeFocusRequestAttach(id string, nowNano int64, attach bool) (string, error) {
	b, err := json.Marshal(FocusRequest{ID: id, TS: nowNano, Attach: attach})
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
	id, _, fresh = DecodeFocusRequestAttach(val, nowNano, ttl)
	return id, fresh
}

// DecodeFocusRequestAttach parses a stored payload and additionally reports
// whether the request asked to attach the session. attach is only meaningful
// when fresh is true.
func DecodeFocusRequestAttach(val string, nowNano int64, ttl time.Duration) (id string, attach bool, fresh bool) {
	if val == "" {
		return "", false, false
	}
	var fr FocusRequest
	if err := json.Unmarshal([]byte(val), &fr); err != nil {
		return "", false, false
	}
	if fr.ID == "" {
		return "", false, false
	}
	if nowNano-fr.TS > int64(ttl) {
		return fr.ID, fr.Attach, false
	}
	return fr.ID, fr.Attach, true
}

// WriteFocusRequest stores a select-only focus request for the running TUI to consume.
func WriteFocusRequest(db *statedb.StateDB, id string, nowNano int64) error {
	return WriteFocusRequestAttach(db, id, nowNano, false)
}

// WriteFocusRequestAttach stores a focus request, optionally flagged to attach
// the session on consume.
func WriteFocusRequestAttach(db *statedb.StateDB, id string, nowNano int64, attach bool) error {
	val, err := EncodeFocusRequestAttach(id, nowNano, attach)
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
