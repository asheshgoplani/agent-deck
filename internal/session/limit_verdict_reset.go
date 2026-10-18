// Limit-verdict reset on an account/model switch (limit-guard review, item 2).
//
// `session set <id> account|model ...` followed by a restart resumes (or
// migrates) the same conversation, so its last assistant record is still the
// rejection the switch was meant to fix. Read naively, that record keeps the
// session account-blocked (24h) or usage/model-limited (5h) on the new
// account. The switch therefore records when it happened, and the transcript
// scan ignores any rejection stamped at or before that time: no verdict until
// the next real turn decides. The time lives in the tool_data extras zone and
// is listed in statedb's toolDataBlob so a clear is authoritative, exactly as
// intended_switch is.
package session

import (
	"encoding/json"
	"time"
)

const toolDataLimitVerdictResetKey = "limit_verdict_reset_at"

// resetLimitVerdict records a switch at now and drops the in-memory verdict
// memo so the next scan reads the transcript afresh against the new reset.
func (i *Instance) resetLimitVerdict(now time.Time) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.LimitVerdictResetAt = now.UTC()
	i.usageLimitedCached = false
	i.usageLimitVerdict = limitVerdict{}
	i.lastUsageLimitScanAt = time.Time{}
	// Invalidate any scan in flight so it cannot publish the pre-switch verdict.
	i.usageLimitScanGen++
	i.shownTranscriptLimit = limitVerdict{}
}

// WriteLimitVerdictResetToToolData merges the reset time into the tool_data
// blob, removing the key when at is zero. Sibling keys are preserved.
func WriteLimitVerdictResetToToolData(td json.RawMessage, at time.Time) json.RawMessage {
	m := map[string]json.RawMessage{}
	if len(td) > 0 {
		_ = json.Unmarshal(td, &m)
	}
	if at.IsZero() {
		delete(m, toolDataLimitVerdictResetKey)
	} else {
		raw, err := json.Marshal(at.UTC())
		if err != nil {
			return td
		}
		m[toolDataLimitVerdictResetKey] = raw
	}
	out, err := json.Marshal(m)
	if err != nil {
		return td
	}
	return out
}

// ReadLimitVerdictResetFromToolData returns the reset time, or zero for
// missing, malformed or legacy rows.
func ReadLimitVerdictResetFromToolData(td json.RawMessage) time.Time {
	if len(td) == 0 {
		return time.Time{}
	}
	var blob struct {
		At *time.Time `json:"limit_verdict_reset_at"`
	}
	if err := json.Unmarshal(td, &blob); err != nil || blob.At == nil {
		return time.Time{}
	}
	return *blob.At
}
