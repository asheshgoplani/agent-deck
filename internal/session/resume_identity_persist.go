package session

import "encoding/json"

// Persistence of the resume-identity taint (#1815).
//
// The in-memory taint alone was bypassable: a writer that saves a discovered
// conversation id WITHOUT going through the resume builder (`switch-account
// --no-restart` is the plain example) puts an unverified id on disk, and the
// next process to load it sees a plain recorded id and happily resumes it.
// That is the incident's own shape — one step diagnoses a problem, a later
// step acts as if it had not.
//
// So the taint travels with the id. It rides the tool_data extras zone,
// outside the positional MarshalToolData signature, exactly like #1143's
// idle_timeout_secs: MergeToolDataExtras preserves unknown keys across
// INSERT OR REPLACE, so a row written by a new binary survives a round-trip
// through an old one and vice versa. A legacy row simply has no key, which
// reads as "not tainted" — the pre-#1815 status quo for ids that were already
// on disk.
const toolDataClaudeSessionUnverifiedKey = "claude_session_id_unverified"

// WriteClaudeSessionUnverifiedToToolData merges the taint flag into a
// tool_data blob. false writes the key explicitly rather than deleting it,
// keeping the write idempotent (see the comment below); a legacy row with no
// key at all still reads as "not tainted" via ReadClaudeSessionUnverifiedFromToolData.
func WriteClaudeSessionUnverifiedToToolData(td json.RawMessage, unverified bool) json.RawMessage {
	m := map[string]json.RawMessage{}
	if len(td) > 0 {
		if err := json.Unmarshal(td, &m); err != nil {
			// Malformed or non-object blob: leave it untouched rather than
			// silently replacing every other persisted tool_data key
			// (claude_session_id, sandbox, extra_args, plugins, ...) with a
			// blob containing only the taint marker (CodeRabbit finding on
			// #1830).
			return td
		}
	}
	// The key is written EXPLICITLY in both directions, never deleted.
	// MergeToolDataExtras preserves keys absent from the replacement blob, so
	// a delete would let a stale `true` merge itself back on the next save and
	// strand a since-verified session as permanently unresumable.
	if unverified {
		m[toolDataClaudeSessionUnverifiedKey] = json.RawMessage("true")
	} else {
		m[toolDataClaudeSessionUnverifiedKey] = json.RawMessage("false")
	}
	out, err := json.Marshal(m)
	if err != nil {
		return td
	}
	return out
}

// ReadClaudeSessionUnverifiedFromToolData extracts the taint flag. Missing,
// malformed or legacy rows read as false.
func ReadClaudeSessionUnverifiedFromToolData(td json.RawMessage) bool {
	if len(td) == 0 {
		return false
	}
	var blob struct {
		Unverified bool `json:"claude_session_id_unverified"`
	}
	_ = json.Unmarshal(td, &blob)
	return blob.Unverified
}

// claudeSessionIDIsUnverified reports whether the instance's CURRENT
// conversation id is the tainted one. This is the value that gets persisted;
// keeping it a derived predicate (rather than a second stored bool) means the
// flag can never disagree with the id it describes.
func (i *Instance) claudeSessionIDIsUnverified() bool {
	if i == nil || i.ClaudeSessionID == "" {
		return false
	}
	i.claudeSessionIDsFromDiskScanMu.Lock()
	defer i.claudeSessionIDsFromDiskScanMu.Unlock()
	return i.claudeSessionIDsFromDiskScan[i.ClaudeSessionID]
}

// restoreClaudeSessionVerification maps the persisted NEGATIVE marker back
// onto the in-memory POSITIVE state at load time, so a process restart cannot
// launder an unverified id into a recorded one.
//
// The marker is negative on purpose: a row written before #1815 carries no
// key and must keep resuming exactly as it did — a positive-on-disk model
// would refuse every pre-existing session once, on upgrade, for nothing.
func restoreClaudeSessionVerification(claudeSessionID string, unverified bool) map[string]bool {
	if !unverified || claudeSessionID == "" {
		return nil
	}
	return map[string]bool{claudeSessionID: true}
}
