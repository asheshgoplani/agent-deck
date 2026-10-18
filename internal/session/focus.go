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
	// Remote names a configured remote when ID is that remote's own session
	// id (#2704). omitempty keeps a local request byte-identical to the
	// pre-remote format, and a TUI that predates the field reads the remote
	// id as an unknown local id and drops it.
	Remote string `json:"remote,omitempty"`
}

// EncodeFocusRequestFull serializes a complete focus request, remote included.
func EncodeFocusRequestFull(fr FocusRequest) (string, error) {
	b, err := json.Marshal(fr)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// WriteFocusRequestFull stores a complete focus request for the running TUI.
func WriteFocusRequestFull(db *statedb.StateDB, fr FocusRequest) error {
	val, err := EncodeFocusRequestFull(fr)
	if err != nil {
		return err
	}
	return db.SetMeta(FocusRequestKey, val)
}

// ParseFocusRequest parses a stored payload into the whole request, with the
// same freshness rule as DecodeFocusRequestAttach. ok is false for an empty,
// malformed or id-less payload; fresh is meaningful only when ok is true.
func ParseFocusRequest(val string, nowNano int64, ttl time.Duration) (fr FocusRequest, ok bool, fresh bool) {
	if val == "" {
		return FocusRequest{}, false, false
	}
	if err := json.Unmarshal([]byte(val), &fr); err != nil {
		return FocusRequest{}, false, false
	}
	if fr.ID == "" {
		return FocusRequest{}, false, false
	}
	return fr, true, focusRequestFresh(fr.TS, nowNano, ttl)
}

// focusRequestFresh reports whether a request written at ts is still
// actionable at nowNano.
func focusRequestFresh(ts, nowNano int64, ttl time.Duration) bool {
	// Stale when older than the TTL, and also when the timestamp is more than a
	// TTL in the future: a backward clock jump or corrupted payload must not be
	// honored as a fresh focus request (which would trigger a spurious switch)
	// indefinitely until wall-clock catches up. A modest skew within ±ttl still
	// counts as fresh.
	age := nowNano - ts
	return age <= int64(ttl) && age >= -int64(ttl)
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
	fr, ok, fresh := ParseFocusRequest(val, nowNano, ttl)
	if !ok {
		return "", false, false
	}
	return fr.ID, fr.Attach, fresh
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

// TakeFocusRequest atomically reads and clears the pending request in one
// statement (consume-once). Prefer it over ReadFocusRequest+ClearFocusRequest:
// the separate read+clear has a window where a concurrent CLI `session focus`
// write lands between them and is wiped by the clear. Returns "" when none.
func TakeFocusRequest(db *statedb.StateDB) (string, error) {
	return db.TakeMeta(FocusRequestKey)
}

// TUIAttachKey holds the tmux client the TUI is paused in while it attaches a
// local session in the classic layout (#2704). `session focus --remote
// --attach` detaches exactly that client so the TUI comes back and reads the
// request. No record (the TUI is on the list, in the embedded layout, or
// predates the key) means there is nothing to detach.
const TUIAttachKey = "tui_local_attach"

// TUIAttach is the JSON payload stored under TUIAttachKey.
type TUIAttach struct {
	Socket    string `json:"socket"`     // tmux socket of the attach ("" = default server)
	ClientPID int    `json:"client_pid"` // pid of the tmux attach client
}

// WriteTUIAttach records the client the TUI just started for a local attach.
func WriteTUIAttach(db *statedb.StateDB, a TUIAttach) error {
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	return db.SetMeta(TUIAttachKey, string(b))
}

// ReadTUIAttach returns the recorded attach client, ok=false when none.
func ReadTUIAttach(db *statedb.StateDB) (TUIAttach, bool) {
	val, err := db.GetMeta(TUIAttachKey)
	if err != nil || val == "" {
		return TUIAttach{}, false
	}
	var a TUIAttach
	if err := json.Unmarshal([]byte(val), &a); err != nil || a.ClientPID <= 0 {
		return TUIAttach{}, false
	}
	return a, true
}

// ClearTUIAttach removes the record when it still names clientPID, so a
// second TUI on the same profile does not wipe the record of another.
func ClearTUIAttach(db *statedb.StateDB, clientPID int) error {
	if a, ok := ReadTUIAttach(db); !ok || a.ClientPID != clientPID {
		return nil
	}
	return db.SetMeta(TUIAttachKey, "")
}
