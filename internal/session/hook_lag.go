package session

import (
	"encoding/json"
	"log/slog"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Hook lag (status-light audit 2026-09-17, defect B; review round 2 P2-3/4/5).
//
// Claude's lifecycle hooks are the primary truth for a Claude session's light,
// and a "running" hook within its freshness window (hookFastPathWindow)
// short-circuits pane inspection. What the audit called an "8-minute Stop
// lag" has a plainer cause, read from the conductor's own transcript: Claude
// Code fires NO Stop hook for a turn that ends by calling ScheduleWakeup (the
// /loop wake-up) — 35 of 35 such turns carry a turn_duration record and no
// stop_hook_summary, while 328 of 330 text-terminated turns carry both. The
// pane still prints "✻ Sautéed for 3m 4s · done" and the empty prompt, so the
// UserPromptSubmit "running" is the last hook event and the light stays green
// until that event ages out of the fast-path window (at most two minutes
// after the prompt was submitted; the audit's extra minutes were defect F,
// the pane heuristic then misreading the idle footer as work).
//
// So the rule below covers a real gap — a running hook whose Stop will never
// come — and is bounded by the window it shortens. Its evidence is never a
// pane capture of its own (review P2-5): the completed-turn verdict is
// recorded by the reads GetStatus and GetSubstate already make
// (tmux.Session.recordCompletedTurnSampleLocked), and the running fast path
// only consults that cached sample. Two independent samples of a completed
// turn at an idle prompt (no spinner, no interrupt hint, no open menu, no
// background work — tmux.PromptDetector.CompletedTurnAtIdlePrompt), at least
// CompletedTurnSampleInterval apart and both taken after the hook event,
// flip the light to waiting; the substate says hook-lag from the first sample
// so the disagreement is visible before the light moves. Any busy sample
// clears the evidence, so a live spinner is never contradicted, and a new
// hook event starts over.
//
// The evidence is persisted on the instance record (tool_data.hook_lag,
// review P2-4) so that one-pass CLI callers — `list --json`, `session
// children --json`, `status`, fleet verify — accumulate samples across
// invocations, the transition daemon (which re-hydrates instances every
// poll) sees the same record, and the TUI picks it up from the status rows it
// already reads each sweep. Every surface therefore reaches the same verdict
// from the same two samples, whichever process captured them.

// toolDataHookLagKey is the tool_data extras-zone key (see
// statedb.MergeToolDataExtras: unknown keys survive full saves).
const toolDataHookLagKey = "hook_lag"

// hookLagSampleSeconds is CompletedTurnSampleInterval in whole seconds, the
// resolution the record is persisted at.
var hookLagSampleSeconds = int64(tmux.CompletedTurnSampleInterval / time.Second)

// hookLagRecord is the evidence for one hook event: the samples of a
// completed turn at an idle prompt taken while that event said "running".
// Unix seconds throughout, so the in-memory and persisted forms compare
// identically in every process.
type hookLagRecord struct {
	// HookTS is the hook event (hookLastUpdate) the samples were taken under.
	// A record for any other event is stale and ignored.
	HookTS int64 `json:"hook_ts"`
	// FirstIdleAt / LastIdleAt bound the run of consecutive idle samples.
	// Zero when no idle sample has been seen under HookTS.
	FirstIdleAt int64 `json:"first_idle_at,omitempty"`
	LastIdleAt  int64 `json:"last_idle_at,omitempty"`
	// LastSampleAt is the newest sample of any kind, so re-applying a cached
	// frame never counts twice.
	LastSampleAt int64 `json:"last_sample_at,omitempty"`
}

// note folds one pane sample into the record. idle is the completed-turn
// verdict, at the capture time (zero when nothing has been captured), hook
// the event the sample must postdate. Samples are only counted when they were
// captured at least a full second after the hook event, so a frame read in
// the same second the hook file was written can never be attributed to it.
func (r *hookLagRecord) note(idle bool, at, hook time.Time) {
	hookTS := hook.Unix()
	if r.HookTS != hookTS {
		*r = hookLagRecord{HookTS: hookTS}
	}
	if at.IsZero() {
		return
	}
	sec := at.Unix()
	if sec <= hookTS || sec <= r.LastSampleAt {
		return
	}
	r.LastSampleAt = sec
	if !idle {
		r.FirstIdleAt, r.LastIdleAt = 0, 0
		return
	}
	if r.FirstIdleAt == 0 {
		r.FirstIdleAt = sec
	}
	r.LastIdleAt = sec
}

// observed reports whether at least one idle sample was taken under hook:
// the substate says hook-lag from here on.
func (r hookLagRecord) observed(hook time.Time) bool {
	return r.HookTS == hook.Unix() && r.FirstIdleAt != 0
}

// confirmed reports whether two independent idle samples, at least
// CompletedTurnSampleInterval apart, were taken under hook: the light flips.
func (r hookLagRecord) confirmed(hook time.Time) bool {
	return r.HookTS == hook.Unix() && r.idleRunConfirmed()
}

// idleRunConfirmed reports whether the recorded run of idle samples spans at
// least CompletedTurnSampleInterval, whichever hook event it was taken under.
func (r hookLagRecord) idleRunConfirmed() bool {
	return r.FirstIdleAt != 0 && r.LastIdleAt-r.FirstIdleAt >= hookLagSampleSeconds
}

// newerThan reports whether r carries evidence o does not: a later hook
// event, or a later sample under the same one.
func (r hookLagRecord) newerThan(o hookLagRecord) bool {
	if r.HookTS != o.HookTS {
		return r.HookTS > o.HookTS
	}
	return r.LastSampleAt > o.LastSampleAt
}

// evidenceKey identifies what other processes need to know: which hook
// event, whether a lag has been observed, and whether it is confirmed. A
// record is persisted only when this changes, so a working session's busy
// samples (which merely advance LastSampleAt) never cost a DB write.
func (r hookLagRecord) evidenceKey() [3]int64 {
	confirmed := int64(0)
	if r.idleRunConfirmed() {
		confirmed = 1
	}
	return [3]int64{r.HookTS, r.FirstIdleAt, confirmed}
}

// ReadHookLagFromToolData extracts the persisted record; the zero record for
// missing/malformed/legacy rows.
func ReadHookLagFromToolData(td json.RawMessage) hookLagRecord {
	var blob struct {
		HookLag hookLagRecord `json:"hook_lag"`
	}
	if len(td) > 0 {
		_ = json.Unmarshal(td, &blob)
	}
	return blob.HookLag
}

// noteHookLagSampleLocked folds the tmux session's cached completed-turn
// sample (no capture) into the record under the current hook event and
// reports whether the lag is confirmed. Caller holds i.mu.
func (i *Instance) noteHookLagSampleLocked() bool {
	if i.tmuxSession != nil {
		idle, at := i.tmuxSession.CachedCompletedTurnSample()
		i.hookLag.note(idle, at, i.hookLastUpdate)
	}
	return i.hookLag.confirmed(i.hookLastUpdate)
}

// hookSaysRunningLocked reports whether the hook fast path would currently
// take a Claude session as running: the condition under which a pane sample
// is hook-lag evidence. Caller holds i.mu.
func (i *Instance) hookSaysRunningLocked() bool {
	return IsClaudeCompatible(i.Tool) && i.hookStatus == "running" &&
		time.Since(i.hookLastUpdate) < hookFastPathFreshnessForTool(i.Tool, i.hookStatus)
}

// absorbCompletedTurnSample is the seam behind Instance.Substate: the
// substate read just captured and classified a frame, so feed its
// completed-turn verdict to the hook-lag rule and, when that confirms the
// lag, report the flip in the SAME pass that captured the confirming frame
// (a one-pass CLI caller would otherwise print running beside hook-lag once
// more). Persists the record when it changed. Takes i.mu briefly.
func (i *Instance) absorbCompletedTurnSample() {
	i.mu.Lock()
	if !i.hookSaysRunningLocked() {
		i.mu.Unlock()
		return
	}
	if i.noteHookLagSampleLocked() && i.Status == StatusRunning {
		i.Status = StatusWaiting
	}
	i.mu.Unlock()
	i.persistHookLag()
}

// resetHookLagLocked drops the evidence. Caller holds i.mu.
func (i *Instance) resetHookLagLocked() {
	i.hookLag = hookLagRecord{}
}

// ApplyPersistedHookLag merges a record read from the instance's DB row
// (statedb.StatusRow.HookLag) into the in-memory evidence when it is newer —
// how a long-lived TUI learns of samples a CLI pass took. A nil/empty raw is
// a no-op. Thread-safe.
func (i *Instance) ApplyPersistedHookLag(raw json.RawMessage) {
	if len(raw) == 0 || !IsClaudeCompatible(i.Tool) {
		return
	}
	var rec hookLagRecord
	if err := json.Unmarshal(raw, &rec); err != nil || rec.HookTS == 0 {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if rec.newerThan(i.hookLag) {
		i.hookLag = rec
		i.hookLagPersisted = rec
	}
}

// persistHookLag writes the record to tool_data.hook_lag when it differs from
// what this process last wrote. Caller must NOT hold i.mu: the DB write goes
// through withBusyRetry and can stall. Only the sampling path
// (absorbCompletedTurnSample) writes; the fast path merely reads, so a
// process that never captures never publishes an empty record over a
// sampler's evidence.
func (i *Instance) persistHookLag() {
	i.mu.RLock()
	rec, prev, db := i.hookLag, i.hookLagPersisted, i.hookLagDB
	i.mu.RUnlock()
	if rec.HookTS == 0 || rec.evidenceKey() == prev.evidenceKey() {
		return
	}
	if db == nil {
		db = statedb.GetGlobal()
	}
	if db == nil {
		return
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return
	}
	if err := db.WriteToolDataExtra(i.ID, toolDataHookLagKey, raw); err != nil {
		sessionLog.Debug("hook_lag_persist_failed",
			slog.String("instance", i.ID),
			slog.String("error", err.Error()),
		)
		return
	}
	i.mu.Lock()
	i.hookLagPersisted = rec
	i.mu.Unlock()
}

// reconcileSubstate applies reconcileSubstateWithStatus to sub under the
// instance's current status and hook-lag evidence. Takes i.mu briefly.
func (i *Instance) reconcileSubstate(sub Substate) Substate {
	i.mu.Lock()
	status, lagged := i.Status, i.hookLag.observed(i.hookLastUpdate)
	i.mu.Unlock()
	return reconcileSubstateWithStatus(status, sub, lagged)
}

// reconcileSubstateWithStatus closes the contradictory pair at the accessor:
// idle-at-empty-prompt pairs with idle/waiting only. Beside a running status
// it is either hook lag (when the lag rule has seen the finished frame) or
// simply unknown — never a claim that the session is both working and idle.
// A confirmed lag keeps its name after the light flips to waiting, so the
// reason for the waiting light stays visible until the hook catches up.
func reconcileSubstateWithStatus(status Status, sub Substate, lagged bool) Substate {
	if sub != SubstateIdleAtEmptyPrompt {
		return sub
	}
	if lagged {
		return SubstateHookLag
	}
	if status == StatusRunning {
		return SubstateNone
	}
	return sub
}
