// Custom-tool conversation identity across reboot.
//
// Built-in tools (claude/gemini/codex/…) persist *_session_id in tool_data so
// Restart can rebuild `<cmd> --resume <id>` after tmux dies. Custom tools
// configured via [tools.*] only stored the id in the live tmux environment
// (session_id_env), so a machine reboot left the deck row knowing the tool
// but not which conversation to resume.
//
// These helpers close that gap the same way last_started_at (#1704) and
// idle_timeout_secs (#1143) do: merge/extract keys on the tool_data JSON blob
// without extending the positional MarshalToolData signature. MergeToolDataExtras
// treats generic_session_id as sticky so a full-table save whose in-memory
// snapshot has not yet observed the id cannot wipe a live mapping.
//
// Sticky vs intentional clear: omission of generic_session_id is treated as
// "unaware writer — preserve". A deliberate clear (SetField tool-session-id "",
// RestartFresh) must therefore either:
//  1. set Instance.genericSessionIDCleared so instanceToRow writes an EXPLICIT
//     empty string (sticky honors explicit empty), or
//  2. call WriteGenericSessionBinding("", …) (json_remove) before Save.
//
// Writing explicit empty on every empty GenericSessionID would break sticky
// protection for concurrent full-table saves — do not do that.
//
// The clear flag is one-shot: SaveWithGroups / InsertSessionAndVerify /
// PersistRecoveredInstances call consumeGenericSessionIDCleared after a
// successful DB write so a later unrelated full save cannot keep emitting
// explicit empty and wipe a concurrent re-bind. Do not clear the flag inside
// instanceToRow alone — a failed Upsert must still retry with intentional clear.
package session

import (
	"encoding/json"
	"log/slog"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

const (
	toolDataGenericSessionIDKey  = "generic_session_id"
	toolDataGenericDetectedAtKey = "generic_detected_at"
)

// WriteGenericSessionIDToToolData merges generic_session_id (+ detected_at)
// into the given tool_data blob.
//
//   - Non-empty sessionID: writes the id (and detected_at when non-zero).
//   - Empty sessionID + intentionalClear: writes explicit "" / 0 so
//     MergeToolDataExtras honors the clear (sticky only preserves on omission).
//   - Empty sessionID + !intentionalClear: omits both keys so a stale full-table
//     save cannot wipe a binding written concurrently via WriteGenericSessionBinding.
func WriteGenericSessionIDToToolData(td json.RawMessage, sessionID string, detectedAt time.Time, intentionalClear bool) json.RawMessage {
	m := map[string]json.RawMessage{}
	if len(td) > 0 {
		_ = json.Unmarshal(td, &m)
	}
	if sessionID == "" {
		if intentionalClear {
			m[toolDataGenericSessionIDKey] = json.RawMessage(`""`)
			m[toolDataGenericDetectedAtKey] = json.RawMessage(`0`)
		} else {
			delete(m, toolDataGenericSessionIDKey)
			delete(m, toolDataGenericDetectedAtKey)
		}
	} else {
		rawID, _ := json.Marshal(sessionID)
		m[toolDataGenericSessionIDKey] = rawID
		if !detectedAt.IsZero() {
			rawAt, _ := json.Marshal(detectedAt.Unix())
			m[toolDataGenericDetectedAtKey] = rawAt
		} else {
			delete(m, toolDataGenericDetectedAtKey)
		}
	}
	out, _ := json.Marshal(m)
	return out
}

// ReadGenericSessionIDFromToolData extracts generic_session_id from the blob.
// Returns "" for missing/malformed/legacy rows.
func ReadGenericSessionIDFromToolData(td json.RawMessage) string {
	if len(td) == 0 {
		return ""
	}
	var blob struct {
		GenericSessionID string `json:"generic_session_id"`
	}
	_ = json.Unmarshal(td, &blob)
	return blob.GenericSessionID
}

// ReadGenericDetectedAtFromToolData extracts generic_detected_at (unix seconds).
// Values are stored as Unix epoch seconds (timezone-independent); the returned
// time is UTC so Equal comparisons against time.Unix(...).UTC() round-trip.
func ReadGenericDetectedAtFromToolData(td json.RawMessage) time.Time {
	if len(td) == 0 {
		return time.Time{}
	}
	var blob struct {
		GenericDetectedAt int64 `json:"generic_detected_at"`
	}
	_ = json.Unmarshal(td, &blob)
	if blob.GenericDetectedAt == 0 {
		return time.Time{}
	}
	return time.Unix(blob.GenericDetectedAt, 0).UTC()
}

// PersistGenericSessionBinding write-throughs generic_session_id to the given
// StateDB. Used by CLI `session set tool-session-id` which may not have
// registered statedb.SetGlobal. Empty GenericSessionID clears via json_remove;
// non-empty sets/updates. Defense in depth alongside genericSessionIDCleared +
// SaveWithGroups. Safe no-op when db or inst is nil.
func PersistGenericSessionBinding(db *statedb.StateDB, inst *Instance) error {
	if db == nil || inst == nil {
		return nil
	}
	return db.WriteGenericSessionBinding(
		inst.ID, inst.GenericSessionID,
		inst.GenericSessionTool, inst.GenericSessionCommand, inst.GenericSessionLocation,
		inst.GenericDetectedAt,
	)
}

// persistGenericSessionIDIfChanged adopts a conversation id observed live and
// writes it through to StateDB, together with the tool and execution location
// it was observed under.
//
// A failed write is reported rather than dropped. It is not returned, because
// every caller is a read path (GetGenericSessionID, SyncSessionIDsFromTmux)
// that has a perfectly good in-memory answer and no way to act on a DB failure
// — but silence here means the id is live in this process and absent from disk,
// so the next cold start silently begins a new conversation and the operator is
// left to work out why. The log line is what makes that diagnosable, and the
// recorded error lets a caller that does have somewhere to put it say so.
func (i *Instance) persistGenericSessionIDIfChanged(sessionID string) {
	if i == nil || sessionID == "" {
		return
	}
	scope := i.currentGenericSessionScope()
	if i.GenericSessionID == sessionID && i.recordedGenericSessionScope() == scope {
		return
	}
	i.GenericSessionID = sessionID
	i.GenericSessionTool = scope.Tool
	i.GenericSessionCommand = scope.Command
	i.GenericSessionLocation = scope.Location
	i.genericSessionIDCleared = false
	if i.GenericDetectedAt.IsZero() {
		i.GenericDetectedAt = time.Now()
	}
	db := statedb.GetGlobal()
	if db == nil {
		return
	}
	err := db.WriteGenericSessionBinding(i.ID, sessionID, scope.Tool, scope.Command, scope.Location, i.GenericDetectedAt)
	i.setGenericSessionPersistError(err)
	if err != nil {
		sessionLog.Warn("generic_session_bind_persist_failed",
			slog.String("instance_id", logging.SanitizeValue(i.ID)),
			slog.String("tool", logging.SanitizeValue(i.Tool)),
			slog.String("session_id_fingerprint", fingerprintSessionID(sessionID)),
			slog.String("error", logging.SanitizeValue(err.Error())))
	}
}

// setGenericSessionPersistError records the outcome of the last write-through.
func (i *Instance) setGenericSessionPersistError(err error) {
	i.genericSessionPersistMu.Lock()
	i.genericSessionPersistErr = err
	i.genericSessionPersistMu.Unlock()
}

// GenericSessionPersistError reports why the last custom-tool conversation-id
// write-through failed, or nil if the binding on this instance is durable.
//
// It exists so that "the id is bound" and "the id survives a reboot" can be
// told apart by anything that cares — the whole value of this feature is the
// second one, and a bind that only ever lived in memory is indistinguishable
// from a working one until the machine restarts.
func (i *Instance) GenericSessionPersistError() error {
	i.genericSessionPersistMu.Lock()
	defer i.genericSessionPersistMu.Unlock()
	return i.genericSessionPersistErr
}

// invalidateGenericSessionBindingOnScopeChange drops a custom-tool conversation
// binding whose scope no longer describes this session, and returns a
// post-commit that erases the live copies from the pane.
//
// Refusing to RESUME a mismatched id is not enough on its own. The id also
// lives in the pane's environment, and the pane outlives the settings it was
// launched under: after `session set <id> tool other`, the old tool's variable
// is still set, still readable, and still the first thing consulted for a live
// value. Leaving it there means the binding comes back the moment anything
// reads the pane, which is the same as never having refused it.
//
// So a scope-changing mutation invalidates rather than merely disqualifies:
// the stored binding is cleared with intent (so sticky merge does not
// resurrect it), and every variable that could carry the id is unset in the
// pane. Nothing is lost that the operator has not already replaced — they
// changed which tool, which executable, or which machine this session is.
//
// Returns nil when there is no binding to invalidate or the scope still
// matches, so callers can chain it without checking.
func (i *Instance) invalidateGenericSessionBindingOnScopeChange() func() {
	if i == nil {
		return nil
	}
	recorded := i.recordedGenericSessionScope()
	if i.GenericSessionID == "" && !recorded.complete() {
		return nil
	}
	if recorded.complete() && recorded == i.currentGenericSessionScope() {
		return nil
	}

	dropped := i.GenericSessionID
	// The variables to erase include the ones the RECORDED tool declared: after
	// a tool change the current tool's name list no longer mentions them, and
	// an unerased variable is exactly what the pane would hand back.
	stale := i.genericSessionEnvNamesFor(recorded.Tool, i.Tool)
	i.GenericSessionID = ""
	i.GenericDetectedAt = time.Time{}
	i.GenericSessionTool = ""
	i.GenericSessionCommand = ""
	i.GenericSessionLocation = ""
	i.genericSessionIDCleared = true

	if db := statedb.GetGlobal(); db != nil {
		err := db.WriteGenericSessionBinding(i.ID, "", "", "", "", time.Time{})
		i.setGenericSessionPersistError(err)
		if err != nil {
			sessionLog.Warn("generic_session_invalidate_persist_failed",
				slog.String("instance_id", logging.SanitizeValue(i.ID)),
				slog.String("session_id_fingerprint", fingerprintSessionID(dropped)),
				slog.String("error", logging.SanitizeValue(err.Error())))
		}
	}
	sessionLog.Info("generic_session_binding_invalidated",
		slog.String("instance_id", logging.SanitizeValue(i.ID)),
		slog.String("session_id_fingerprint", fingerprintSessionID(dropped)),
		slog.String("reason", "tool, command or execution location changed"))

	// Unset in the pane too, or the next read re-adopts what was just dropped.
	return makeGenericSessionEnvPostCommit(i, "", stale)
}
