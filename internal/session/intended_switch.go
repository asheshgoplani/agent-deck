// Intended account/model switch record (limit-guard BUG 5).
//
// A conductor that moves a session to another account or model on purpose
// records why, with an expiry, so an external guard (limit-guard) can tell
// the switch from an unexpected one. Guard contract: a session's switch is
// intended while its current account and model equal the record's and now is
// before expires_at. The record lives in the tool_data extras zone (same
// pattern as context_level_persist.go) and is listed in statedb's
// toolDataBlob so an explicit clear is not resurrected by MergeToolDataExtras.
package session

import (
	"encoding/json"
	"fmt"
	"time"
)

const (
	toolDataIntendedSwitchKey = "intended_switch"

	// DefaultIntendedSwitchTTL is how long a record stays valid when no
	// duration is given; MaxIntendedSwitchTTL caps an explicit one.
	DefaultIntendedSwitchTTL = 24 * time.Hour
	MaxIntendedSwitchTTL     = 7 * 24 * time.Hour
)

// IntendedSwitch records that the session's Account and Model were chosen on
// purpose. Model is the per-session model override ("" = tool default).
type IntendedSwitch struct {
	Account   string    `json:"account"`
	Model     string    `json:"model"`
	Reason    string    `json:"reason"`
	At        time.Time `json:"at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// IntendedSwitchJSON is the list/show --json view: RFC3339 timestamps.
type IntendedSwitchJSON struct {
	Account   string `json:"account"`
	Model     string `json:"model"`
	Reason    string `json:"reason"`
	At        string `json:"at"`
	ExpiresAt string `json:"expires_at"`
}

// ValidateIntendedSwitchTTL rejects a duration that is not in (0, 7d].
func ValidateIntendedSwitchTTL(d time.Duration) error {
	if d <= 0 || d > MaxIntendedSwitchTTL {
		return fmt.Errorf("duration %v out of range: must be > 0 and at most %v", d, MaxIntendedSwitchTTL)
	}
	return nil
}

// MarkIntendedSwitch records the session's current account and model as
// intended for ttl from now. Timestamps are whole UTC seconds so the JSON
// view is plain RFC3339.
func (i *Instance) MarkIntendedSwitch(reason string, ttl time.Duration, now time.Time) {
	at := now.UTC().Truncate(time.Second)
	i.IntendedSwitch = &IntendedSwitch{
		Account:   i.GetAccountThreadSafe(),
		Model:     i.LaunchModelID(),
		Reason:    reason,
		At:        at,
		ExpiresAt: at.Add(ttl),
	}
}

// ActiveIntendedSwitchJSON returns the record for --json output, or nil when
// there is none or it has expired.
func (i *Instance) ActiveIntendedSwitchJSON(now time.Time) *IntendedSwitchJSON {
	if i == nil || i.IntendedSwitch == nil || !now.Before(i.IntendedSwitch.ExpiresAt) {
		return nil
	}
	r := i.IntendedSwitch
	return &IntendedSwitchJSON{
		Account:   r.Account,
		Model:     r.Model,
		Reason:    r.Reason,
		At:        r.At.UTC().Format(time.RFC3339),
		ExpiresAt: r.ExpiresAt.UTC().Format(time.RFC3339),
	}
}

// WriteIntendedSwitchToToolData merges the record into the tool_data blob,
// removing the key when rec is nil. Sibling keys are preserved.
func WriteIntendedSwitchToToolData(td json.RawMessage, rec *IntendedSwitch) json.RawMessage {
	m := map[string]json.RawMessage{}
	if len(td) > 0 {
		_ = json.Unmarshal(td, &m)
	}
	if rec == nil {
		delete(m, toolDataIntendedSwitchKey)
	} else {
		raw, err := json.Marshal(rec)
		if err != nil {
			return td
		}
		m[toolDataIntendedSwitchKey] = raw
	}
	out, err := json.Marshal(m)
	if err != nil {
		return td
	}
	return out
}

// ReadIntendedSwitchFromToolData returns the record, or nil for missing,
// malformed or legacy rows.
func ReadIntendedSwitchFromToolData(td json.RawMessage) *IntendedSwitch {
	if len(td) == 0 {
		return nil
	}
	var blob struct {
		IntendedSwitch *IntendedSwitch `json:"intended_switch"`
	}
	if err := json.Unmarshal(td, &blob); err != nil {
		return nil
	}
	return blob.IntendedSwitch
}
