// Identity-injection opt-out persistence.
//
// Follows the idle_timeout_persist.go pattern: the flag lives in the tool_data
// extras zone, outside the positional MarshalToolData / UnmarshalToolData
// signatures and without a SQL schema migration, so binaries that predate the
// key preserve it via MergeToolDataExtras. Like idle_timeout it deletes the
// key at the zero value: the flag can only be set at creation
// (`add`/`launch --no-identity`), there is no toggle that could be silently
// undone by a stale `true` resurfacing, and every legacy row means "inject",
// which is the default.
package session

import "encoding/json"

const toolDataIdentityInjectionDisabledKey = "identity_injection_disabled"

// WriteIdentityInjectionDisabledToToolData merges the opt-out into the given
// tool_data blob, removing the key when the flag is false. Sibling keys are
// preserved.
func WriteIdentityInjectionDisabledToToolData(td json.RawMessage, disabled bool) json.RawMessage {
	m := map[string]json.RawMessage{}
	if len(td) > 0 {
		_ = json.Unmarshal(td, &m)
	}
	if disabled {
		m[toolDataIdentityInjectionDisabledKey] = json.RawMessage("true")
	} else {
		delete(m, toolDataIdentityInjectionDisabledKey)
	}
	out, err := json.Marshal(m)
	if err != nil {
		return td
	}
	return out
}

// ReadIdentityInjectionDisabledFromToolData extracts the opt-out from the
// blob. Returns false (inject) for missing, malformed, or legacy rows.
func ReadIdentityInjectionDisabledFromToolData(td json.RawMessage) bool {
	if len(td) == 0 {
		return false
	}
	var blob struct {
		Disabled bool `json:"identity_injection_disabled"`
	}
	_ = json.Unmarshal(td, &blob)
	return blob.Disabled
}
