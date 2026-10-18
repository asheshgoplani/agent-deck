// Limit-verdict reset on the harness/account switch commit path.
//
// `session switch-account`, the TUI account switch and `session switch` move
// the same conversation (same Claude session id) to another account through
// ExecuteHarnessSwitch and Storage.CommitNativeHarnessSwitch, never through
// SetField. The moved transcript still ends in the old account's rejection,
// so the commit records the reset time (and drops an intended-switch record
// that names another account) exactly as `session set ... account` does, and
// writes both in the same scoped CAS as the account itself.
package session

import (
	"encoding/json"
	"time"
)

// limitSwitchState is the verdict-reset and intent state an account switch
// replaces, kept so a rolled-back switch can put it back.
type limitSwitchState struct {
	resetAt time.Time
	intent  *IntendedSwitch
}

func captureLimitSwitchState(inst *Instance) limitSwitchState {
	if inst == nil {
		return limitSwitchState{}
	}
	inst.mu.RLock()
	defer inst.mu.RUnlock()
	return limitSwitchState{resetAt: inst.LimitVerdictResetAt, intent: inst.IntendedSwitch}
}

// applyLimitSwitchState sets the in-memory reset time and intent and drops
// the verdict memo so the next scan reads the transcript against them.
func (i *Instance) applyLimitSwitchState(st limitSwitchState) {
	i.resetLimitVerdict(st.resetAt) // a zero time clears the reset
	i.IntendedSwitch = st.intent
}

// limitSwitchStateAfterMove is the state a committed move to account leaves:
// the verdict reset at now, and the intent only when it already names the
// destination account.
func limitSwitchStateAfterMove(prev limitSwitchState, account string, now time.Time) limitSwitchState {
	next := limitSwitchState{resetAt: now.UTC()}
	if prev.intent != nil && prev.intent.Account == account {
		next.intent = prev.intent
	}
	return next
}

// limitSwitchToolData is the tool_data patch for st; a nil value removes the
// key so a cleared record is not left behind in the row.
func limitSwitchToolData(st limitSwitchState) map[string]json.RawMessage {
	extras := map[string]json.RawMessage{
		toolDataLimitVerdictResetKey: nil,
		toolDataIntendedSwitchKey:    nil,
	}
	if !st.resetAt.IsZero() {
		if raw, err := json.Marshal(st.resetAt.UTC()); err == nil {
			extras[toolDataLimitVerdictResetKey] = raw
		}
	}
	if st.intent != nil {
		if raw, err := json.Marshal(st.intent); err == nil {
			extras[toolDataIntendedSwitchKey] = raw
		}
	}
	return extras
}
