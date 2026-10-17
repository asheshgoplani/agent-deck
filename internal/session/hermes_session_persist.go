// Hermes per-entry session ownership (issue #2582).
//
// Hermes mints a native session id per conversation and reports it on every
// hook event. Restart used to ask `hermes sessions list --workspace <dir>` for
// the newest CLI conversation in the directory, which belongs to whichever
// entry spoke last when several entries share a workspace. Instead, each entry
// now records the id its OWN hook events report and resumes only that id.
//
// The id lives in the tool_data extras zone (key hermes_session_id), so no
// schema change is needed and older binaries carry it forward untouched.
package session

import (
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

const toolDataHermesSessionIDKey = "hermes_session_id"

// WriteHermesSessionIDToToolData merges the entry's own Hermes session id into
// tool_data. An empty id omits the key: MergeToolDataExtras then keeps the
// value already on disk, so a writer that never observed the binding cannot
// wipe it. An intentional clear (fresh restart) goes through
// persistHermesSessionBinding, which writes the empty value directly.
func WriteHermesSessionIDToToolData(td json.RawMessage, sessionID string) json.RawMessage {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return td
	}
	m := map[string]json.RawMessage{}
	if len(td) > 0 {
		_ = json.Unmarshal(td, &m)
	}
	raw, err := json.Marshal(sessionID)
	if err != nil {
		return td
	}
	m[toolDataHermesSessionIDKey] = raw
	out, err := json.Marshal(m)
	if err != nil {
		return td
	}
	return out
}

// ReadHermesSessionIDFromToolData returns the recorded Hermes session id, or ""
// for rows that never recorded one.
func ReadHermesSessionIDFromToolData(td json.RawMessage) string {
	if len(td) == 0 {
		return ""
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(td, &m); err != nil {
		return ""
	}
	var id string
	if err := json.Unmarshal(m[toolDataHermesSessionIDKey], &id); err != nil {
		return ""
	}
	return strings.TrimSpace(id)
}

// persistHermesSessionBinding writes the binding straight to SQLite, because
// UpdateHookStatus callers do not save afterwards (same gap as #1139).
func (i *Instance) persistHermesSessionBinding(sessionID string) {
	db := statedb.GetGlobal()
	if db == nil {
		return
	}
	if err := db.WriteHermesSessionBinding(i.ID, sessionID, time.Now()); err != nil {
		sessionLog.Warn("hermes_session_bind_persist_failed",
			slog.String("instance_id", i.ID),
			slog.String("new_id", sessionID),
			slog.String("error", err.Error()))
	}
}

// bindHermesSessionFromHook records the session id reported by this entry's
// own hook events. Caller holds i.mu.
//
// Only the first id seen while no id is recorded is accepted. Hermes runs
// delegated agents (delegate_task) inside the same process, and their hooks
// carry their own session ids; the parent always reports first (its turn
// starts before it can delegate), so later different ids are delegated
// agents and are ignored. The one legitimate change of id inside a process
// is a /new or /reset, which ends the owned session with on_session_finalize
// first; UpdateHookStatus sets hermesRebindAllowed on that edge.
func (i *Instance) bindHermesSessionFromHook(sessionID, hookSource, hookEvent string) {
	if sessionID == i.HermesSessionID {
		i.hermesRebindAllowed = false
		return
	}
	if i.HermesSessionID != "" && !i.hermesRebindAllowed {
		_ = WriteSessionIDLifecycleEvent(SessionIDLifecycleEvent{
			InstanceID: i.ID, Tool: i.Tool, Action: "reject",
			Source: hookSource, OldID: i.HermesSessionID, Candidate: sessionID,
			HookEvent: hookEvent, Reason: "candidate_is_not_owned_session",
		})
		return
	}
	action := "bind"
	if i.HermesSessionID != "" {
		action = "rebind"
	}
	_ = WriteSessionIDLifecycleEvent(SessionIDLifecycleEvent{
		InstanceID: i.ID, Tool: i.Tool, Action: action,
		Source: hookSource, OldID: i.HermesSessionID, NewID: sessionID,
		HookEvent: hookEvent,
	})
	i.HermesSessionID = sessionID
	i.hermesRebindAllowed = false
	i.persistHermesSessionBinding(sessionID)
}

// prepareHermesResume runs on Restart, before buildHermesCommand. It keeps the
// recorded id as the only resume target. A shared working directory is not
// evidence of ownership, so with no recorded id the restart starts a new
// conversation and says so, rather than resuming the newest conversation in
// the directory, which may belong to another entry.
func (i *Instance) prepareHermesResume() {
	warning := ""
	if i.HermesSessionID == "" {
		warning = hermesResumeUnknownWarning(i.EffectiveWorkingDir())
		sessionLog.Warn("hermes_resume_ownership_unknown", slog.String("instance_id", i.ID))
	}
	i.mu.Lock()
	i.hermesRebindAllowed = false
	i.pendingCodexRestartWarning = warning
	i.mu.Unlock()
}

// hermesResumeUnknownWarning explains a restart that started a new Hermes
// conversation because this entry has no recorded session of its own.
func hermesResumeUnknownWarning(workspace string) string {
	msg := "Hermes: no conversation is recorded for this entry, so a new one was started instead of guessing from the shared directory."
	if newest := captureHermesSessionID(workspace); newest != "" {
		msg += " The newest Hermes conversation in this directory is " + newest +
			"; it may belong to another entry. If it is yours, set the command to `hermes --resume " + newest + "`."
	}
	return msg
}
