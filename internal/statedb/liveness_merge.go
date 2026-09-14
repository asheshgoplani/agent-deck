package statedb

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// Liveness merge policy (issue #2209).
//
// The snapshot merge treats every column and tool_data key as configuration:
// when this caller changed a value and the committed row holds something that
// is neither the caller's baseline nor its new value, two writers proposed
// two different values and the save is refused. That is the right contract
// for a group path or a session id. It is the wrong contract for values that
// are an OBSERVATION of a running session rather than something anyone asked
// for: a pane-derived status, an activity timestamp, or the wall-clock stamp
// of a conversation detection. Two processes observing the same event will
// read the clock at different moments and sample the pane in different
// states, and neither of them has lost anything.
//
// `agent-deck launch` hits this deterministically: its post-start save carries
// the tmux session name (which exists nowhere else) together with its own
// Status and *_detected_at, while any concurrent UpdateHookStatus caller or
// monitor tick has already committed its own stamp and status for the same
// session. Refusing that save loses the spawn receipt.
//
// Resolution for liveness values: the COMMITTED value wins. It was written
// after this caller loaded its baseline and is therefore the fresher
// observation. LastAccessed keeps the later of the two, since activity is
// monotonic. Everything that expresses intent still conflicts: a
// stopped/queued status, a diverging session id, a stamp whose paired id
// diverges (the stamps are timing two different conversations), and an
// explicit clear racing a stamp (a clear is intent; the sticky-key protocol
// in tool_data_extras.go already treats it that way).

// paneDerivedStatuses are the statuses the monitor re-derives from the pane
// on every tick. "stopped" and "queued" are absent on purpose: they record a
// decision, not a sample.
var paneDerivedStatuses = map[string]bool{
	"running":  true,
	"waiting":  true,
	"idle":     true,
	"starting": true,
	"error":    true,
}

// mergeLivenessScalar resolves a conflicting persisted column when both sides
// are observations. It returns the value to keep and whether the column is
// a liveness value at all; a false return means the caller's normal conflict
// contract applies.
func mergeLivenessScalar(name string, want, actual any) (any, bool) {
	switch name {
	case "Status":
		mine, _ := want.(string)
		theirs, _ := actual.(string)
		if paneDerivedStatuses[mine] && paneDerivedStatuses[theirs] {
			return actual, true
		}
	case "LastAccessed":
		mine, _ := want.(time.Time)
		theirs, _ := actual.(time.Time)
		if mine.Unix() > theirs.Unix() {
			return want, true
		}
		return actual, true
	}
	return nil, false
}

const detectedAtSuffix = "_detected_at"

// pairedSessionIDKey maps a *_detected_at key to the *_session_id it stamps.
func pairedSessionIDKey(key string) (string, bool) {
	if !strings.HasSuffix(key, detectedAtSuffix) {
		return "", false
	}
	return strings.TrimSuffix(key, detectedAtSuffix) + "_session_id", true
}

// isToolDataClear reports whether a sticky value is an explicit clear or an
// omission, the two forms the sticky-key protocol treats as the same intent.
func isToolDataClear(value json.RawMessage) bool {
	return value == nil || sameJSON(value, json.RawMessage(`""`)) || sameJSON(value, json.RawMessage(`0`))
}

// mergeLivenessToolData resolves a conflicting *_detected_at stamp. The
// committed stamp wins only when both sides carry a stamp and the committed
// paired session id is the one this caller believes it is stamping.
func mergeLivenessToolData(key string, desired, actual map[string]json.RawMessage) (json.RawMessage, bool) {
	idKey, ok := pairedSessionIDKey(key)
	if !ok || !stickyToolDataKeys()[key] {
		return nil, false
	}
	if isToolDataClear(desired[key]) || isToolDataClear(actual[key]) {
		return nil, false
	}
	if !sameStoredToolValue(idKey, actual[idKey], desired[idKey]) {
		return nil, false
	}
	return actual[key], true
}

// orderToolDataKeys returns keys sorted so every *_detected_at key follows
// the rest. A diverging session id is then reported as the conflict, never
// the stamp it happens to pair with, regardless of map iteration order.
func orderToolDataKeys(keys map[string]bool) []string {
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Slice(ordered, func(i, j int) bool {
		si, sj := strings.HasSuffix(ordered[i], detectedAtSuffix), strings.HasSuffix(ordered[j], detectedAtSuffix)
		if si != sj {
			return !si
		}
		return ordered[i] < ordered[j]
	})
	return ordered
}
