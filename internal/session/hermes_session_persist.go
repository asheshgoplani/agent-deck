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
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/docker"
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

// ReadHermesDetectedAtFromToolData returns when the binding was recorded
// (hermes_detected_at), or zero.
func ReadHermesDetectedAtFromToolData(td json.RawMessage) time.Time {
	if len(td) == 0 {
		return time.Time{}
	}
	var blob struct {
		At int64 `json:"hermes_detected_at"`
	}
	if json.Unmarshal(td, &blob) != nil || blob.At == 0 {
		return time.Time{}
	}
	return time.Unix(blob.At, 0).UTC()
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
func (i *Instance) bindHermesSessionFromHook(sessionID, hookSource, hookEvent, platform, cwd string, at time.Time) {
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
	case i.hermesHookCwdIsForeign(cwd):
		// A process outside the entry's paths that inherited its
		// AGENTDECK_INSTANCE_ID (a nested hermes), issue #1729's rule.
		reason = "candidate_cwd_outside_instance_paths"
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
	binding, boundAt := i.HermesSessionID, i.hermesBoundAt
	i.mu.Unlock()

	// The hook handler's record comes first: it saw every hook event of the
	// current launch, while the binding above comes only from the hook
	// status samples a watcher happened to read (hermesResumeTarget).
	recorded := i.hermesResumeTarget(binding, boundAt)

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

// hermesOwnedSessionSuffix names the per-entry sidecar in the hooks dir that
// holds the Hermes session id the hook handler recorded for the entry.
//
// The binding in tool_data is fed by hook status samples: the status file
// keeps only the latest event, the watcher reads it 100ms debounced, and a
// CLI restart reads none. A Hermes turn writes its parent-only edges
// (on_session_start, pre_llm_call, post_llm_call) and, within milliseconds,
// the api, tool and on_session_end events that replace them in that file, so
// a sample rarely shows a parent edge and the entry often never bound. The
// hook handler sees every event, so it records the id of each parent edge
// here, after the launch-generation check, where a replaced process cannot
// write. The sidecar survives on_session_finalize: the finalized conversation
// is the one a later restart resumes.
const hermesOwnedSessionSuffix = ".hermes-session"

// HermesHookRecordsOwnSession reports whether a Hermes hook event identifies
// the entry's own conversation: a parent-only edge (hermesBindsOnEvent) that
// is not a delegate_task child's (platform "subagent") and carries a Hermes
// session id.
func HermesHookRecordsOwnSession(event, platform, sessionID string) bool {
	return hermesBindsOnEvent(event) &&
		!strings.EqualFold(strings.TrimSpace(platform), hermesSubagentPlatform) &&
		hermesSessionIDPattern.MatchString(strings.TrimSpace(sessionID))
}

// hermesOwnedSession is the sidecar record: the id, the cwd the event
// reported, whether the event carried extra.platform, and when it was
// written. Sidecars from before these fields hold a bare JSON string.
type hermesOwnedSession struct {
	ID       string `json:"id"`
	Cwd      string `json:"cwd,omitempty"`
	Platform bool   `json:"platform,omitempty"`
	At       int64  `json:"at,omitempty"` // unix nanos
}

func readHermesOwnedSession(path string) (hermesOwnedSession, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return hermesOwnedSession{}, false
	}
	var rec hermesOwnedSession
	if json.Unmarshal(data, &rec) != nil {
		var id string
		if json.Unmarshal(data, &id) != nil {
			return hermesOwnedSession{}, false
		}
		// A legacy record came from a platform-carrying parent edge (the
		// only kind earlier versions recorded); its time is the file's.
		rec = hermesOwnedSession{ID: id, Platform: true}
		if info, err := os.Stat(path); err == nil {
			rec.At = info.ModTime().UnixNano()
		}
	}
	rec.ID = strings.TrimSpace(rec.ID)
	return rec, hermesSessionIDPattern.MatchString(rec.ID)
}

// WriteHermesOwnedSessionID records sessionID (with the cwd its event
// reported) as the entry's own Hermes conversation. The hook handler calls it
// inside the launch-generation critical section.
//
// cwdRoots are the directories the entry owns as the hook sees them (the
// seed writes them into the generation control file; /workspace for a
// sandboxed entry): an event whose cwd is outside all of them comes from
// another process that inherited AGENTDECK_INSTANCE_ID (a nested hermes) and
// is not written, so it cannot replace the entry's record. Empty roots (an
// older seed) skip the check.
//
// platformKnown is whether the event carried extra.platform. Without it the
// subagent filter had nothing to go on, so such an event never replaces an id
// already recorded; it can only record the first one, and is marked so that
// a newer binding wins over it at resume.
func WriteHermesOwnedSessionID(instanceID, sessionID, cwd string, platformKnown bool, cwdRoots []string) error {
	sessionID = strings.TrimSpace(sessionID)
	if !hermesHookInstanceIDPattern.MatchString(instanceID) || strings.Contains(instanceID, "..") {
		return fmt.Errorf("invalid instance id %q", instanceID)
	}
	if !hermesSessionIDPattern.MatchString(sessionID) {
		return fmt.Errorf("invalid hermes session id %q", sessionID)
	}
	cwd = strings.TrimSpace(cwd)
	if cwd != "" && len(cwdRoots) > 0 && !pathUnderAny(cwd, cwdRoots) {
		return nil
	}
	path := filepath.Join(hermesHookScope(instanceID, false), instanceID+hermesOwnedSessionSuffix)
	if current, ok := readHermesOwnedSession(path); ok {
		if current.ID == sessionID && current.Cwd == cwd && current.Platform == platformKnown {
			return nil // most turn edges repeat the same id
		}
		if !platformKnown && current.ID != sessionID {
			return nil
		}
	}
	return atomicHermesJSON(path, hermesOwnedSession{ID: sessionID, Cwd: cwd, Platform: platformKnown, At: time.Now().UnixNano()})
}

// pathUnderAny reports whether p is one of roots or below one of them,
// symlinks resolved on both sides.
func pathUnderAny(p string, roots []string) bool {
	resolve := func(x string) string {
		if r, err := filepath.EvalSymlinks(x); err == nil {
			return filepath.Clean(r)
		}
		return filepath.Clean(x)
	}
	candidate := resolve(p)
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		r := resolve(root)
		if candidate == r || strings.HasPrefix(candidate, r+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

// hermesCwdRoots are the directories the entry owns as its hook sees them:
// inside a sandbox the project is mounted at /workspace, otherwise the host
// paths. The seed writes them into the generation control file for
// WriteHermesOwnedSessionID.
func (i *Instance) hermesCwdRoots() []string {
	if i.IsSandboxed() {
		return []string{docker.ContainerWorkDir()}
	}
	roots := append([]string{i.EffectiveWorkingDir()}, i.AllProjectPaths()...)
	var out []string
	for _, r := range roots {
		if strings.TrimSpace(r) != "" {
			out = append(out, r)
		}
	}
	return out
}

// sandboxCwdToHost maps a cwd a sandboxed hook reported (the container's
// /workspace or below) to the host path mounted there: the project path, or
// for a multi-repo entry the repo mounted at /workspace/<dirname>, named as
// docker.WithMultiRepoPaths names it. ok is false when the cwd is not under
// /workspace or names no mounted repo.
func (i *Instance) sandboxCwdToHost(cwd string) (string, bool) {
	work := docker.ContainerWorkDir()
	cwd = filepath.Clean(cwd)
	if cwd != work && !strings.HasPrefix(cwd, work+"/") {
		return "", false
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(cwd, work), "/")
	if !i.MultiRepoEnabled {
		if strings.TrimSpace(i.ProjectPath) == "" {
			return "", false
		}
		return filepath.Join(i.ProjectPath, rel), true
	}
	if rel == "" {
		return i.EffectiveWorkingDir(), i.EffectiveWorkingDir() != ""
	}
	first, rest, _ := strings.Cut(rel, "/")
	seen := map[string]int{}
	for _, p := range i.AllProjectPaths() {
		name := filepath.Base(p)
		if n := seen[name]; n > 0 {
			name = fmt.Sprintf("%s-%d", name, n)
		}
		seen[filepath.Base(p)]++
		if name == first {
			return filepath.Join(p, rest), true
		}
	}
	return "", false
}

// hermesHookCwdIsForeign is hookCwdIsForeign for Hermes hooks, sandbox
// aware: a /workspace cwd (Hermes reports the container's cwd) is mapped to
// the host path mounted there first, and a sandboxed entry's cwd that maps
// to nothing is not evidence (the rule is skipped). A record written by a
// sandboxed run and read after the sandbox was turned off maps the same way.
func (i *Instance) hermesHookCwdIsForeign(cwd string) bool {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return false
	}
	if host, ok := i.sandboxCwdToHost(cwd); ok && !pathUnderAny(docker.ContainerWorkDir(), i.hermesCwdRootsOnHost()) {
		return i.hookCwdIsForeign(host)
	}
	if i.IsSandboxed() {
		return false
	}
	return i.hookCwdIsForeign(cwd)
}

// hermesCwdRootsOnHost are the entry's host paths (never /workspace).
func (i *Instance) hermesCwdRootsOnHost() []string {
	return append([]string{i.EffectiveWorkingDir()}, i.AllProjectPaths()...)
}

// hermesOwnedSession returns the record the hook handler wrote for this
// entry. A sandboxed entry's hook handler writes in its sandbox scope, so that
// scope is read first. A record whose cwd is outside the entry's paths came
// from another process that inherited the entry's AGENTDECK_INSTANCE_ID (a
// nested hermes, issue #1729's rule) and is ignored.
func (i *Instance) hermesOwnedSession() (hermesOwnedSession, bool) {
	if !hermesHookInstanceIDPattern.MatchString(i.ID) || strings.Contains(i.ID, "..") {
		return hermesOwnedSession{}, false
	}
	sandboxed := i.IsSandboxed()
	for _, dir := range []string{hermesHookScope(i.ID, sandboxed), hermesHookScope(i.ID, !sandboxed)} {
		rec, ok := readHermesOwnedSession(filepath.Join(dir, i.ID+hermesOwnedSessionSuffix))
		if !ok {
			continue
		}
		if i.hermesHookCwdIsForeign(rec.Cwd) {
			sessionLog.Warn("hermes_owned_session_cwd_foreign", slog.String("instance_id", i.ID), slog.String("candidate", rec.ID))
			continue
		}
		return rec, true
	}
	return hermesOwnedSession{}, false
}

// hermesOwnedSessionID returns the recorded id, or "".
func (i *Instance) hermesOwnedSessionID() string {
	rec, _ := i.hermesOwnedSession()
	return rec.ID
}

// hermesResumeTarget is the id a start or restart resumes: the hook
// handler's record first, except that a record written from an event without
// extra.platform (which cannot follow /new or a compression) loses to a
// newer, different binding. binding and boundAt are the entry's binding.
func (i *Instance) hermesResumeTarget(binding string, boundAt time.Time) string {
	rec, ok := i.hermesOwnedSession()
	if !ok {
		return binding
	}
	if !rec.Platform && binding != "" && binding != rec.ID && boundAt.After(time.Unix(0, rec.At)) {
		sessionLog.Info("hermes_owned_session_superseded_by_binding",
			slog.String("instance_id", i.ID), slog.String("record", rec.ID), slog.String("binding", binding))
		return binding
	}
	return rec.ID
}

// preferHermesOwnedSessionForStart makes Start and StartWithMessage resume
// the conversation Restart would (hermesResumeTarget). A start right after
// RestartFresh begins a new conversation instead.
func (i *Instance) preferHermesOwnedSessionForStart() {
	if i.hermesStartFresh {
		i.hermesStartFresh = false
		i.mu.Lock()
		i.HermesSessionID = ""
		i.mu.Unlock()
		return
	}
	i.mu.Lock()
	binding, boundAt := i.HermesSessionID, i.hermesBoundAt
	i.mu.Unlock()
	if target := i.hermesResumeTarget(binding, boundAt); target != "" {
		i.mu.Lock()
		i.HermesSessionID = target
		i.mu.Unlock()
	}
}

// clearHermesOwnedSessionID forgets the recorded id (fresh restart).
func (i *Instance) clearHermesOwnedSessionID() {
	if !hermesHookInstanceIDPattern.MatchString(i.ID) || strings.Contains(i.ID, "..") {
		return
	}
	for _, dir := range []string{hermesHookScope(i.ID, false), hermesHookScope(i.ID, true)} {
		_ = os.Remove(filepath.Join(dir, i.ID+hermesOwnedSessionSuffix))
	}
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
