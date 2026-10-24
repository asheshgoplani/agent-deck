package session

import (
	"log/slog"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// errorRecheckInterval - how often to recheck sessions that don't exist
// Ghost sessions (in JSON but not in tmux) are rechecked at this interval
// instead of every 500ms tick, dramatically reducing subprocess spawns
const errorRecheckInterval = 30 * time.Second

// resumeCheckRetryDelay is the wait between the two sessionHasConversationData
// checks in buildClaudeResumeCommand (Issue #662). SessionEnd writes are
// observed to finish within ~100-150ms in practice; 200ms gives headroom
// without noticeably slowing the restart path when there truly is no jsonl.
var resumeCheckRetryDelay = 200 * time.Millisecond

// clearRebindMtimeGrace is the mtime gap (candidate.mtime - current.mtime)
// above which UpdateHookStatus treats a smaller candidate as a legitimate
// user-initiated new session (e.g. /clear) instead of a stale flap (issue
// #856). 5s is well above the ~2s hook poll cadence — a #661 flap touches
// both files within that window — but well below the time it takes a user
// to type /clear and a follow-up prompt.
var clearRebindMtimeGrace = 5 * time.Second

func hookFastPathFreshnessForTool(tool, hookStatus string) time.Duration {
	if !IsCodexCompatible(tool) {
		return hookFastPathWindow
	}

	// Codex hook events are turn-based and can be sparse depending on command mode.
	// Keep running freshness short, but preserve completion/waiting signals longer so
	// the user can reliably see attention-needed state.
	switch hookStatus {
	case "waiting":
		return codexHookWaitingFastPathWindow
	default:
		return codexHookRunningFastPathWindow
	}
}

// shellForegroundRunning reports whether a "shell" tool session currently has a
// genuine non-interactive foreground process running (e.g. "node" from
// `yarn dev`, "java" from `mvn spring-boot:run`). It returns false for the
// interactive shell itself and for interactive foreground programs (editors,
// pagers, system monitors, remote shells, multiplexers) that are really waiting
// for the user rather than doing background work — otherwise opening vim or
// sitting at an ssh prompt would show a perpetual running indicator.
//
// It relies on the pane-info cache warmed once per tick by RefreshPaneInfoCache
// (TUI backgroundStatusUpdate / Web refreshStatuses / CLI status refresh). When
// the cache is cold the lookup misses and this returns false, preserving the
// historical "shell maps to idle" behavior. Caller must hold i.mu.
//
// The feature is opt-in via [status] shell_running_indicator (default false):
// the interactive-program denylist cannot be complete, so without the flag a
// shell sitting at a psql/REPL/fzf prompt would flip everyone's historical
// "shell → idle" default to running.
//
// Staleness guards — only fresh pane info may promote idle→running:
//   - GetCachedPaneInfoSnapshot enforces the cache-wide 4s TTL (2 ticks).
//   - A dead pane (#{pane_dead}) means the command already exited.
//   - A snapshot taken before this instance's last start describes a previous
//     same-name session (kill+recreate within the TTL), not this one.
func (i *Instance) shellForegroundRunning() bool {
	if i.tmuxSession == nil {
		return false
	}
	cfg, _ := LoadUserConfig()
	if cfg == nil || !cfg.Status.ShellRunningIndicator {
		return false
	}
	paneInfo, snapshotAt, ok := tmux.GetCachedPaneInfoSnapshot(i.tmuxSession.TmuxName())
	if !ok || paneInfo.Dead || paneInfo.CurrentCommand == "" {
		return false
	}
	if !i.lastStartTime.IsZero() && snapshotAt.Before(i.lastStartTime) {
		return false
	}
	cmd := paneInfo.CurrentCommand
	if isShellBinary(cmd) || isInteractiveForegroundProgram(cmd) {
		return false
	}
	return true
}

// neverStarted reports whether this session was added but never started, so an
// absent tmux session is expected rather than a fault. Two conditions must both
// hold (caller holds i.mu):
//
//  1. The instance was added in THIS process (addedThisProcess), not reloaded
//     from storage. A reloaded session whose tmux later dies is a genuine error
//     (instance_cli_parity_test.go TestUpdateStatus_CLIvsTUIParity_Error builds
//     a reloaded struct literal, so addedThisProcess is false there).
//  2. Start() was never called (lastStartTime is zero). A started-then-killed
//     session has a non-zero lastStartTime and must surface as error
//     (lifecycle_regression_test.go phase5).
//  3. The status is still the pristine post-add state (idle or starting).
func (i *Instance) neverStarted() bool {
	return i.addedThisProcess && i.lastStartTime.IsZero() &&
		(i.Status == StatusIdle || i.Status == StatusStarting)
}

// UpdateStatus updates the session status by checking tmux.
// Thread-safe: acquires write lock to protect Status, Tool, and internal cache fields.
// debounceFlipFromRunning decides whether a tmux-derived status that flips AWAY
// from running should be held for one confirming sample. It returns the status
// to apply, the next value for the pending marker, and whether the flip was
// HELD (the caller applies `apply` and returns early when held is true).
//
// Held only on the FIRST tmux-inferred running→{waiting,error} sample (pending
// was false): a long single tool-call past the hook freshness window, or a
// transient CapturePane failure during subprocess churn, can present that flip
// for one tick and then recover. A genuinely dead pane (tmux raw "inactive")
// and a "dead" hook are real terminal signals and are never debounced. Pure for
// testability.
func debounceFlipFromRunning(prev, derived Status, tmuxRaw, hookStatus string, pending bool) (apply Status, nextPending bool, held bool) {
	flipAwayFromRunning := (derived == StatusWaiting || derived == StatusError) &&
		tmuxRaw != "inactive" && hookStatus != "dead"
	if prev == StatusRunning && flipAwayFromRunning && !pending {
		return StatusRunning, true, true
	}
	return derived, false, false
}

// SeedLiveStatusPrior hands a fresh Instance the verdict an earlier pass of
// the SAME long-lived process reached for it, so the running→waiting/error
// debounce (debounceFlipFromRunning) can hold across passes. The notify
// daemon reloads every Instance from storage on each pass; without this seam
// each pass looked like a one-shot process (statusSampledLive false), the
// hold never applied, and a single misread frame in a long turn became a
// real running→waiting transition event (status-detection audit 2026-09-23).
// A one-shot process must NOT call it: a persisted status is a guess, not an
// observation. No-op for an empty status.
func (i *Instance) SeedLiveStatusPrior(status Status, flipPending bool) {
	if status == "" {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.Status = status
	i.statusSampledLive = true
	i.tmuxFlipFromRunningPending = flipPending
}

// LiveStatusPrior returns what SeedLiveStatusPrior needs on the next pass:
// the status this process settled and whether a flip away from running is
// pending confirmation. sampled is false when this process never settled a
// verdict itself (nothing worth carrying).
func (i *Instance) LiveStatusPrior() (status Status, flipPending bool, sampled bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.Status, i.tmuxFlipFromRunningPending, i.statusSampledLive
}

// shouldDebounceTmuxFlipForTool is deliberately narrower than HookStatusTool:
// pi has hooks (#2222) but is excluded here on purpose, like shell and "".
// The call site in updateStatus explains why.
func shouldDebounceTmuxFlipForTool(tool string) bool {
	return tool == "" || IsClaudeCompatible(tool) || IsCodexCompatible(tool) ||
		tool == "gemini" || tool == "hermes" || tool == "cursor"
}

// setCodexGenerationEvidence copies the durable notify edge pair into the
// instance. Unlike tmuxFlipFromRunningPending, these fields survive a fresh
// CLI process because they are retained in the hook status file.
func (i *Instance) setCodexGenerationEvidence(status *HookStatus) {
	if status == nil || !IsCodexCompatible(i.Tool) {
		return
	}
	if status.codexCompletionConsumed {
		i.codexStartedGeneration, i.codexCompletedGeneration = "", ""
		i.codexStartedSessionID, i.codexCompletedSessionID = "", ""
		return
	}
	if status.CodexStartedGeneration == "" && status.CodexCompletedGeneration == "" &&
		status.CodexStartedSessionID == "" && status.CodexCompletedSessionID == "" {
		return
	}
	i.codexStartedGeneration = strings.TrimSpace(status.CodexStartedGeneration)
	i.codexCompletedGeneration = strings.TrimSpace(status.CodexCompletedGeneration)
	i.codexStartedSessionID = strings.TrimSpace(status.CodexStartedSessionID)
	i.codexCompletedSessionID = strings.TrimSpace(status.CodexCompletedSessionID)
	if i.codexInvalidatingGeneration != i.codexStartedGeneration {
		i.codexInvalidatingGeneration = ""
	}
}

// codexCompletionConverged is deliberately fail-closed. Only the same retained
// generation, for the same non-empty session bound to this instance, proves a
// completed turn. Missing, mismatched, and superseded evidence stays subject
// to the conservative running debounce.
func (i *Instance) codexCompletionConverged() bool {
	if !IsCodexCompatible(i.Tool) || i.codexStartedGeneration == "" ||
		i.codexInvalidatingGeneration == i.codexStartedGeneration ||
		i.codexStartedGeneration != i.codexCompletedGeneration ||
		i.codexStartedSessionID == "" ||
		i.codexStartedSessionID != i.codexCompletedSessionID {
		return false
	}
	return i.CodexSessionID != "" && i.codexStartedSessionID == i.CodexSessionID
}

func (i *Instance) shouldBypassCodexWaitingDebounce(derived Status) bool {
	return derived == StatusWaiting && i.codexCompletionConverged()
}

// invalidateCodexCompletionOnRunning makes completion-only proof one-turn
// evidence. Once tmux authoritatively observes newer running activity, turn
// A's completion must not let a later transient waiting sample masquerade as
// completion of turn B. Persist a host-owned consumption marker first, then
// clear the warm instance only after that write succeeds.
// Caller must hold i.mu. The method drops it around all durable coordination.
func (i *Instance) invalidateCodexCompletionOnRunning() {
	if !IsCodexCompatible(i.Tool) || i.codexStartedGeneration == "" ||
		i.codexStartedGeneration != i.codexCompletedGeneration {
		return
	}
	generation := i.codexCompletedGeneration
	i.codexInvalidatingGeneration = generation
	// The durable operation can briefly retry a non-blocking host lock and can
	// perform filesystem I/O. Never expose Instance.mu across either operation.
	i.mu.Unlock()
	consumed, err := consumeCodexCompletionEvidence(i.ID, generation)
	i.mu.Lock()
	if err != nil {
		sessionLog.Debug("consume_codex_completion_evidence_failed",
			slog.String("instance", i.ID),
			slog.String("error", err.Error()),
		)
		return
	}
	if consumed {
		if i.codexStartedGeneration == generation && i.codexCompletedGeneration == generation {
			i.codexStartedGeneration, i.codexCompletedGeneration = "", ""
			i.codexStartedSessionID, i.codexCompletedSessionID = "", ""
		}
		if i.codexInvalidatingGeneration == generation {
			i.codexInvalidatingGeneration = ""
		}
	}
}

// terminatedPaneStatus classifies a session whose tmux pane/session has
// vanished (or gone dead under remain-on-exit) AFTER having been started.
//
// Exit code first: when tmux still holds the dead pane (remain-on-exit — set
// for sandbox sessions, and opt-in via [tmux] options for any session) the
// real process exit code is available. A one-shot worker that finishes and
// exits 0 is a clean shutdown (done, ■ StatusStopped), NOT a crash — reporting
// it as StatusError (✕) makes a successful completion indistinguishable from a
// real failure. A non-zero exit is a genuine crash → StatusError.
//
// No exit code available (pane torn down without remain-on-exit, so tmux
// discarded the exit status) falls back to a per-tool heuristic. For
// hook-emitting tools a vanished pane genuinely means a crash → StatusError.
// OpenCode emits no lifecycle hooks (issue #1617), so an in-session `/exit`
// closes its pane exactly like a crash would; a vanished OpenCode pane is
// overwhelmingly a user-initiated exit → StatusStopped. Error stays reserved
// for a LIVE pane that renders an actual error banner.
//
// NOTE: PaneDeadExitStatus shells out to tmux and can block up to its probe
// timeout. Callers on the UpdateStatus path (which run under i.mu) must NOT use
// this directly when a live tmux session may be queried — use
// applyTerminatedPaneStatus, which drops i.mu around the query. This method is
// safe to call under i.mu only when i.tmuxSession is nil (no subprocess runs).
func (i *Instance) terminatedPaneStatus() Status {
	exitCode, haveExitCode := 0, false
	if i.tmuxSession != nil {
		exitCode, haveExitCode = i.tmuxSession.PaneDeadExitStatus()
	}
	if !haveExitCode {
		exitCode, haveExitCode = i.trackedCommandExitReceipt()
	}
	status, substate := classifyTerminatedPane(exitCode, haveExitCode, i.Tool, i.hookStatus)
	status, substate = i.classifyCustomCommandExit(status, substate, exitCode, haveExitCode)
	i.terminatedPaneSubstate = substate
	i.setTerminatedPaneExitCode(exitCode, haveExitCode)
	return status
}

// applyTerminatedPaneStatus writes the terminated-pane classification to
// i.Status without holding i.mu across the (potentially slow) tmux query.
//
// PaneDeadExitStatus can block for the full tmux probe timeout, and every
// UpdateStatus caller runs under i.mu — so querying tmux under the lock lets a
// wedged tmux server stall concurrent lifecycle operations. Instead: snapshot
// the tmux session and tool under the lock, release i.mu for the query, then
// reacquire and apply the result. The caller MUST hold i.mu on entry; i.mu is
// held again on return.
//
// Because the lock is dropped, i.Status may change under us (e.g. a concurrent
// Stop()), so the stopped-state guard is re-evaluated after reacquiring: a
// session the user stopped mid-query must not be clobbered with error/stopped.
func (i *Instance) applyTerminatedPaneStatus() {
	i.applyTerminatedPaneStatusOf(i.tmuxSession)
}

// applyTerminatedPaneStatusOf is applyTerminatedPaneStatus with the tmux
// session to ask for the exit status; nil skips that probe, for a session a
// listing already proved gone (no pane left to hold an exit status).
func (i *Instance) applyTerminatedPaneStatusOf(tmuxSession *tmux.Session) {
	tool := i.Tool
	receiptPath := i.trackedCommandExitReceiptPath()
	paneDeadExitStatus := i.paneDeadExitStatusForTest
	hookStatus := i.hookStatus

	i.mu.Unlock()
	exitCode, haveExitCode := 0, false
	if tmuxSession != nil {
		if paneDeadExitStatus == nil {
			paneDeadExitStatus = tmuxSession.PaneDeadExitStatus
		}
		exitCode, haveExitCode = paneDeadExitStatus()
	}
	if !haveExitCode && receiptPath != "" {
		exitCode, haveExitCode = readCommandExitReceipt(receiptPath)
	}
	status, substate := classifyTerminatedPane(exitCode, haveExitCode, tool, hookStatus)
	i.mu.Lock()

	if i.Status != StatusStopped {
		status, substate = i.classifyCustomCommandExit(status, substate, exitCode, haveExitCode)
		i.Status = status
		i.terminatedPaneSubstate = substate
		i.setTerminatedPaneExitCode(exitCode, haveExitCode)
	}
}

// classifyCustomCommandExit uses tmux's retained pane status for an explicit
// shell command. A successful command is idle; a failed command needs attention.
func (i *Instance) tracksCommandExit() bool {
	return i.Tool == "shell" && i.Command != "" && i.TrackCommandExit
}

func (i *Instance) classifyCustomCommandExit(status Status, substate Substate, code int, known bool) (Status, Substate) {
	if !i.tracksCommandExit() || !known {
		return status, substate
	}
	if code == 0 {
		return StatusIdle, SubstateProcessExited
	}
	return StatusError, SubstateProcessExited
}

func (i *Instance) setTerminatedPaneExitCode(code int, known bool) {
	i.terminatedPaneExitCode = nil
	if known && i.tracksCommandExit() {
		i.terminatedPaneExitCode = &code
	}
}

// ExitCode returns the observed custom-command exit code, including zero.
// The result is absent while the command is running or the pane has no exit evidence.
func (i *Instance) ExitCode() *int {
	if i == nil {
		return nil
	}
	i.mu.RLock()
	defer i.mu.RUnlock()
	if i.terminatedPaneSubstate != SubstateProcessExited ||
		(i.Status != StatusIdle && i.Status != StatusError) || i.terminatedPaneExitCode == nil {
		return nil
	}
	code := *i.terminatedPaneExitCode
	return &code
}

// classifyTerminatedPane is the pure decision behind terminatedPaneStatus,
// split out so the clean-exit-vs-crash rule can be exercised without a live
// tmux server. See terminatedPaneStatus for the full rationale.
//
// hookStatus is the instance's last-known hook-reported status ("running",
// "waiting", "idle", "dead", or "" if no hook was ever observed). It is the
// fix for issue #2091: a hook-emitting tool (Claude, Codex, ...) that
// finishes its turn fires a Stop-edge hook BEFORE its pane necessarily goes
// away, so when the pane vanishes without a captured exit code (no
// remain-on-exit), that recorded hook status is the deciding evidence — not
// a blind per-tool guess:
//
//   - hookStatus "waiting"/"idle": the tool already reported a clean turn
//     completion. Losing the pane after that is not a crash signal — the
//     TOCTOU the issue describes is exactly the classifier ignoring this
//     already-recorded fact. → StatusStopped.
//   - hookStatus "running"/"starting"/"dead": a turn was still in flight (or
//     the hook itself reported a dead session) when the pane vanished — a
//     real crash. → StatusError.
//   - hookStatus "" (no hook record ever seen for this session): there is no
//     evidence either way. The historical default is preserved (StatusError)
//     for backward compatibility, but SubstateUnknownExit says this verdict
//     is a guess, not a fact (see pattern_unknown_as_first_class_state) —
//     callers must not treat it as a confirmed crash for e.g. auto-restart.
func classifyTerminatedPane(exitCode int, haveExitCode bool, tool string, hookStatus string) (Status, Substate) {
	if haveExitCode {
		if exitCode == 0 {
			return StatusStopped, SubstateNone
		}
		return StatusError, SubstateNone
	}
	if tool == "opencode" {
		return StatusStopped, SubstateNone
	}
	// Only a hook-emitting tool records a hookStatus that means anything here:
	// it is the turn-end-edge evidence the doc comment above describes.
	if HookStatusTool(tool) {
		switch hookStatus {
		case "waiting", "idle":
			return StatusStopped, SubstateNone
		case "":
			return StatusError, SubstateUnknownExit
		}
	}
	return StatusError, SubstateNone
}

func (i *Instance) UpdateStatus() error {
	return i.updateStatus(nil, true)
}

// probeTmuxExists is called with i.mu held and returns with it held. tmux can
// wait for a busy server, so status readers must not wait behind this probe.
// With a pass, the pass's listing of the session's socket can prove the
// session absent without a has-session per row (listedAbsent).
//
// Start() keeps the same tmux.Session and stopRevision, so its lastStartTime
// is part of 'current' too: a probe that answered from before Start's
// new-session (a listing, or a has-session) must not overwrite 'starting'.
func (i *Instance) probeTmuxExists(pass *StatusUpdatePass) (exists, listedAbsent, current bool) {
	s := i.tmuxSession
	stopRevision := i.stopRevision
	started := i.lastStartTime
	i.mu.Unlock()
	exists, listedAbsent = s.ExistsListed(func() map[string]struct{} { return pass.listedSessions(s.SocketName) })
	i.mu.Lock()
	return exists, listedAbsent, i.tmuxSession == s && i.stopRevision == stopRevision && i.lastStartTime.Equal(started)
}

// listingAloneProvesDead reports whether a pass listing's absence may stand
// for this row without its own has-session (i.mu held). Only a row already
// recorded dead (error, stopped) or never started: a listing is up to
// passSessionListMaxAge old, and a row last seen in any other status (running,
// waiting, idle, starting, queued) written error from it would page its
// parent if another process had just (re)created the session.
func (i *Instance) listingAloneProvesDead() bool {
	return i.Status == StatusError || i.Status == StatusStopped || i.neverStarted()
}

// passSessionListMaxAge bounds how long a pass trusts one listing of a socket.
// A long pass re-lists rather than judging late rows against an old listing.
//
// A listing is a snapshot (aged from before list-sessions ran): a session
// created after it reads as absent until the listing ages out, where a
// per-row has-session would have seen it. So only a row already recorded dead
// takes the listing's word (listingAloneProvesDead); a row last seen live
// confirms with its own has-session, and a Start() during the probe makes it
// stale (probeTmuxExists). For a dead row the window is this age, under the
// 1.5 s start grace.
// A long pass pays one more list-sessions per socket per age, not per row.
const passSessionListMaxAge = 250 * time.Millisecond

// passListSessionNames lists one socket's sessions for a pass; a variable so
// tests can interleave a start or a slow listing with it.
var passListSessionNames = tmux.ListSessionNamesOnSocket

type passSessionListing struct {
	at    time.Time
	names map[string]struct{} // nil: the listing failed; rows probe on their own
}

// listedSessions returns the session names on socket, listed at most once per
// passSessionListMaxAge for the whole pass (one list-sessions per socket
// instead of a has-session and a list-panes per dead row). Concurrent workers
// share one listing. Nil for a nil pass or a failed listing.
func (p *StatusUpdatePass) listedSessions(socket string) map[string]struct{} {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	for {
		if l, ok := p.listed[socket]; ok && time.Since(l.at) < passSessionListMaxAge {
			p.mu.Unlock()
			return l.names
		}
		ready := p.listing[socket]
		if ready == nil {
			break
		}
		p.mu.Unlock()
		<-ready
		p.mu.Lock()
	}
	if p.listing == nil {
		p.listing = make(map[string]chan struct{})
	}
	ready := make(chan struct{})
	p.listing[socket] = ready
	p.mu.Unlock()
	at := time.Now() // before listing: a session created while it runs is missing
	names, err := passListSessionNames(socket)
	if err != nil {
		names = nil
	}
	p.mu.Lock()
	if p.listed == nil {
		p.listed = make(map[string]passSessionListing)
	}
	p.listed[socket] = passSessionListing{at: at, names: names}
	delete(p.listing, socket)
	close(ready)
	p.mu.Unlock()
	return names
}

// ForceNextStatusCheck clears the idle polling optimization so the next
// UpdateStatus() performs a full check instead of short-circuiting.
// Call this before UpdateStatus() when a status-affecting change was made
// externally (e.g. the u key toggling acknowledged state).
func (i *Instance) ForceNextStatusCheck() {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.lastIdleCheck = time.Time{}
}

// Substate returns the additive Honest-Status-v2 refinement for this session
// (see Substate). It reads the live tmux pane and classifies it; SubstateNone
// when there is no tmux session, the pane is dead, or the tool has no substate
// heuristics. This is an enrichment of Status, not a replacement — it never
// changes the canonical status reported by GetStatus/UpdateStatus.
// An auth hold overrides both accessors: a session whose agent EXITED on a
// credential failure has no pane left to classify, so the tmux layer honestly
// reports SubstateNone — which is exactly the blindness that made a fleet-wide
// 401 look like anonymous mass death. The hold is the durable memory of the last
// live verdict, so surfacing it here gives every reporting layer (TUI glyph, CLI
// status --json, web, transition events) the auth-401 substate for a dead
// session without any of them changing.
func (i *Instance) Substate() Substate {
	if held, _ := i.IsAuthHeld(); held {
		i.setShownTranscriptLimit(limitVerdict{})
		return SubstateAuth401
	}
	// A RECENT quota rejection is checked before the pane verdict, including the
	// pane's auth banner: it is the latest assistant turn and a 429 proves the
	// request was authenticated, so a stale "Please run /login" line still in the
	// scrollback must not win on category order and mislabel a session whose
	// credentials demonstrably work.
	//
	// "Recent" is load-bearing rather than decorative. A 429 only proves
	// authentication AT ITS OWN TIMESTAMP, so an old one must not mask newer pane
	// evidence — a freshly rendered 401, or a dropped socket that never arms the
	// credential hold. usageLimited enforces that bound (usageLimitMaxAge), which
	// is what keeps this ordering honest (#1802).
	//
	// The same verdict also carries the other transcript-derived kinds,
	// model-limit and account-blocked (limit-guard bugs 2 and 4), at the same
	// precedence: each is the latest assistant turn and a structured rejection.
	limit := i.transcriptLimit()
	i.setShownTranscriptLimit(limit)
	if limit.Kind != SubstateNone {
		return limit.Kind
	}
	if s := i.getTerminatedPaneSubstate(); s != SubstateNone {
		return s
	}
	tmuxSess := i.GetTmuxSession()
	if tmuxSess == nil {
		return SubstateNone
	}
	sub, liveSpinner := tmuxSess.GetSubstateWithLiveSpinner()
	// The substate capture can be newer than the waiting-hook probe. Use its
	// narrow current-composer cue, never generic running text in scrollback.
	if sub == SubstateRunning && liveSpinner {
		i.mu.Lock()
		if IsClaudeCompatible(i.Tool) && (i.Status == StatusWaiting || i.Status == StatusIdle) &&
			!blockingHookInGrace(i.hookEvent, i.hookLastUpdate, time.Now()) {
			i.Status = StatusRunning
			tmuxSess.ResetAcknowledged()
		}
		i.mu.Unlock()
	}
	// The frame just read is hook-lag evidence too (see hook_lag.go); feed it
	// back so status and substate describe the same frame.
	i.absorbCompletedTurnSample()
	return i.reconcileSubstate(sub)
}

// SubstateDetail returns free-text detail for the substate the last
// classification produced (the codex usage-limit retry time; for
// background-work the in-flight task, e.g. "workflow comms-followon-round3
// 3/5 · 18m32s"; for a transcript-derived usage-limit / model-limit /
// account-blocked the reason the session waits, e.g. "weekly limit · resets
// 12pm (Europe/Berlin)", "Fable 5.1"), or "".
// Call after Substate/CachedSubstate; it reads the cached value and never
// captures the pane.
func (i *Instance) SubstateDetail() string {
	// The transcript verdict outranks every pane verdict in Substate, so its
	// detail outranks theirs here. Without it a session waiting out a quota
	// read as an anonymous stall (limit-guard bug 3).
	i.mu.RLock()
	shown := i.shownTranscriptLimit
	i.mu.RUnlock()
	if isTranscriptLimitSubstate(shown.Kind) {
		return shown.Detail
	}
	if work := i.BackgroundWork(); work.InFlight() {
		return work.Summary()
	}
	tmuxSess := i.GetTmuxSession()
	if tmuxSess == nil {
		return ""
	}
	if tmuxSess.CachedSubstate() == tmux.SubstateBackgroundWork && i.GetStatusThreadSafe() != StatusRunning {
		return "" // a vetoed stale workflow row (see reconcileBackgroundSubstate)
	}
	return tmuxSess.CachedSubstateDetail()
}

// setShownTranscriptLimit records the transcript verdict Substate is about to
// report (or the zero verdict when it reports something else) for
// SubstateDetail. Acquires its own lock; callers must not already hold i.mu.
func (i *Instance) setShownTranscriptLimit(v limitVerdict) {
	i.mu.Lock()
	i.shownTranscriptLimit = v
	i.mu.Unlock()
}

// getTerminatedPaneSubstate returns the terminated-pane substate recorded by
// classifyTerminatedPane, gated on the session still being in StatusError
// from that same terminated-pane path — any later real status change
// (restart, hook activity) must not keep surfacing a stale verdict. Acquires
// its own lock; callers must not already hold i.mu.
func (i *Instance) getTerminatedPaneSubstate() Substate {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if i.Status != StatusError && i.Status != StatusIdle {
		return SubstateNone
	}
	return i.terminatedPaneSubstate
}

// CachedSubstate returns the last substate computed by a prior status check
// WITHOUT capturing the pane. Use it on the TUI render hot path; the background
// status loop keeps the value fresh.
// Like Substate, an auth hold wins — read from the in-memory mirror so the
// render path stays filesystem-free.
func (i *Instance) CachedSubstate() Substate {
	if i.AuthHeldCached() {
		return SubstateAuth401
	}
	// Deliberately NOT wired for usage-limit: this path must stay
	// filesystem-free, and nothing in the background status pass populates a
	// usage-limit mirror, so a branch here would be dead on the TUI/Web/transition
	// surfaces while looking supported. usage-limit is a live-Substate signal in
	// this change (CLI/JSON/fleet); wiring the cached path belongs with whatever
	// populates it. See usagelimit.go (#1802).
	if s := i.getTerminatedPaneSubstate(); s != SubstateNone {
		return s
	}
	tmuxSess := i.GetTmuxSession()
	if tmuxSess == nil {
		return SubstateNone
	}
	return i.reconcileSubstate(tmuxSess.CachedSubstate())
}

// SetAcknowledgedFromShared applies an acknowledgment from another TUI instance
// (read from SQLite). This transitions a YELLOW (waiting) session to GRAY (idle)
// without requiring the user to interact with this specific TUI instance.
func (i *Instance) SetAcknowledgedFromShared(ack bool) {
	if !ack || i.tmuxSession == nil {
		return
	}

	// Running/starting is authoritative: don't let stale shared ack force
	// an active session back to idle.
	status := i.GetStatusThreadSafe()
	if status == StatusRunning || status == StatusStarting {
		return
	}

	i.tmuxSession.Acknowledge()
}
