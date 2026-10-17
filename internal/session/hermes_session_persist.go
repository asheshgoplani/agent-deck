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
	"path/filepath"
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
// UpdateHookStatus callers do not save afterwards (same gap as #1139). It uses
// the owning database with the same closed-handle retry as the Codex binding,
// so a web mutator's short-lived Storage never drops the write and an
// unrelated profile's global database is never written.
func (i *Instance) persistHermesSessionBinding(sessionID string) {
	db := i.restartDB.Load()
	if db == nil {
		db = statedb.GetGlobal()
	}
	if db == nil {
		return
	}
	now := time.Now()
	err := db.WriteHermesSessionBinding(i.ID, sessionID, now)
	if err != nil && db.DB().Ping() != nil && i.storageSnapshot != nil && i.storageSnapshot.dbPath != "" {
		var retryDB *statedb.StateDB
		retryDB, err = statedb.Open(i.storageSnapshot.dbPath)
		if err == nil {
			err = retryDB.WriteHermesSessionBinding(i.ID, sessionID, now)
			_ = retryDB.Close()
		}
	}
	if err != nil {
		sessionLog.Warn("hermes_session_bind_persist_failed",
			slog.String("instance_id", i.ID),
			slog.String("new_id", sessionID),
			slog.String("error", err.Error()))
	}
}

// hermesBindsOnEvent reports whether a hook event may bind or rotate the
// entry's Hermes session id. Only the parent conversation's turn edges
// qualify: on_session_start, pre_llm_call and post_llm_call. Tool, API and
// session-end events also fire for delegate_task children running inside the
// same process with their own session ids, so they never bind.
func hermesBindsOnEvent(event string) bool {
	switch event {
	case "on_session_start", "pre_llm_call", "post_llm_call":
		return true
	}
	return false
}

// hermesSubagentPlatform is the platform Hermes gives a delegate_task child
// (delegate_tool builds it as AIAgent(platform="subagent")). The child runs
// its own turns in the parent's process, so it fires on_session_start,
// pre_llm_call and post_llm_call with its own session id, and the hook
// forwarder reports that platform in extra.platform.
const hermesSubagentPlatform = "subagent"

// bindHermesSessionFromHook records the session id reported by this entry's
// own hook events. Caller holds i.mu.
//
// Hook events arrive as samples (the latest hook file per tick, or only the
// last event on a cold load), so "first id seen" is not evidence of
// ownership: the first sample may be a delegate's tool call. Instead, only a
// parent-only edge (hermesBindsOnEvent) carrying the id in its own payload
// binds, and a different id on such an edge is a rotation of this entry's
// conversation and replaces the recorded one. That covers /new and /reset
// (new session id) and context compression, where Hermes rotates session_id
// without on_session_finalize and links the new session back through
// parent_session_id: the next turn edge of the continued conversation carries
// the new id. A rotation older than the current binding is a stale sample and
// is ignored.
//
// Delegate children fire the same turn edges with their own ids, so an event
// whose platform is "subagent" never binds or rotates, on any edge. Without
// that, a delegate's turn edge would rebind the entry to the child id, and
// only a later successful parent post_llm_call would move it back: an
// interrupted parent turn, a delegation outliving the parent turn, or a
// restart mid-turn would then resume the delegate's session.
func (i *Instance) bindHermesSessionFromHook(sessionID, hookSource, hookEvent, platform string, at time.Time) {
	if sessionID == i.HermesSessionID {
		if at.After(i.hermesBoundAt) {
			i.hermesBoundAt = at
		}
		return
	}
	reason := ""
	switch {
	case hookSource != "hook_payload":
		reason = "candidate_not_in_hook_payload"
	case strings.EqualFold(strings.TrimSpace(platform), hermesSubagentPlatform):
		reason = "candidate_from_subagent"
	case !hermesBindsOnEvent(hookEvent):
		reason = "candidate_from_non_parent_edge"
	case i.HermesSessionID != "" && at.Before(i.hermesBoundAt):
		reason = "candidate_older_than_binding"
	}
	if reason != "" {
		// One lifecycle line per distinct candidate: a delegate fires a hook
		// on every tool and API call, which must not flood the log.
		if i.hermesLastRejected != sessionID {
			i.hermesLastRejected = sessionID
			_ = WriteSessionIDLifecycleEvent(SessionIDLifecycleEvent{
				InstanceID: i.ID, Tool: i.Tool, Action: "reject",
				Source: hookSource, OldID: i.HermesSessionID, Candidate: sessionID,
				HookEvent: hookEvent, Reason: reason,
			})
		}
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
	i.hermesBoundAt = at
	i.hermesLastRejected = ""
	i.persistHermesSessionBinding(sessionID)
}

// prepareHermesResume runs on Restart, before buildHermesCommand. The
// recorded id is the resume target. With no recorded id (an entry created
// before ids were recorded, or one whose parent never completed a turn edge),
// the newest conversation in the directory is used only when no other Hermes
// entry shares that directory, which is the single-entry behaviour from
// before #2582. In a shared directory that guess may be another entry's
// conversation, so the restart starts a new one and says so.
func (i *Instance) prepareHermesResume() {
	i.mu.Lock()
	recorded := i.HermesSessionID
	i.mu.Unlock()

	resume, warning := recorded, ""
	if recorded == "" {
		workspace := i.EffectiveWorkingDir()
		if i.hermesWorkspaceShared(workspace) {
			warning = hermesResumeUnknownWarning(workspace)
			sessionLog.Warn("hermes_resume_ownership_unknown", slog.String("instance_id", i.ID))
		} else {
			resume = captureHermesSessionID(workspace)
		}
	}

	i.mu.Lock()
	i.HermesSessionID = resume
	i.hermesBoundAt = time.Time{}
	i.hermesLastRejected = ""
	i.pendingCodexRestartWarning = warning
	i.mu.Unlock()
}

// hermesWorkspaceShared reports whether another Hermes entry in the owning
// database uses this entry's project or working directory. It fails closed
// (true) when the database cannot be read, since a wrong guess resumes
// another entry's conversation.
func (i *Instance) hermesWorkspaceShared(workspace string) bool {
	db := i.restartDB.Load()
	if db == nil {
		db = statedb.GetGlobal()
	}
	if db == nil {
		return true
	}
	rows, err := db.LoadInstances()
	if err != nil {
		return true
	}
	mine := map[string]bool{}
	for _, p := range []string{i.ProjectPath, workspace} {
		if strings.TrimSpace(p) != "" {
			mine[filepath.Clean(p)] = true
		}
	}
	for _, r := range rows {
		if r.ID == i.ID || r.Tool != "hermes" || strings.TrimSpace(r.ProjectPath) == "" {
			continue
		}
		if mine[filepath.Clean(r.ProjectPath)] {
			return true
		}
	}
	return false
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
