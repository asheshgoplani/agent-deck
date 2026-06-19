package session

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

const (
	notifyPollFast   = 1 * time.Second
	notifyPollMedium = 2 * time.Second
	notifyPollSlow   = 3 * time.Second
	hookFreshWindow  = 45 * time.Second

	// inboxTTLSweepInterval rate-limits the per-process TTL sweep over
	// every inbox file. Issue #962 variant: without a periodic sweep,
	// the cleanup-on-success path alone can't reach entries whose
	// children never transition again. One pass per hour keeps the
	// disk churn negligible while bounding inbox size to TTL+1h.
	inboxTTLSweepInterval = time.Hour
)

type hookTransitionCandidate struct {
	ToStatus  string
	Timestamp time.Time
}

type TransitionDaemon struct {
	notifier *TransitionNotifier

	hookWatcher *StatusFileWatcher

	storages map[string]*Storage

	lastStatus  map[string]map[string]string
	initialized map[string]bool

	// lastDone tracks the most recently emitted completion sentinel per
	// (profile, instance) so a finished event (issue #1186) is emitted once
	// per distinct completion. Re-reading the same done-bearing hook file
	// across polls — or a later identical Stop — does not re-fire.
	lastDone map[string]map[string]DoneSignal

	// lastDoneScan tracks, per (profile, instance), the hook-status timestamp
	// whose pending transcript rescan (issue #1186 flush race) reached a
	// conclusive answer — assistant record flushed, sentinel present or not.
	// It stops the daemon from re-reading the transcript tail every poll for
	// the rest of the freshness window once the scan has resolved; an
	// UNRESOLVED (still-unflushed) scan is deliberately not recorded so the
	// next poll retries.
	lastDoneScan map[string]map[string]time.Time

	// lastInboxTTLSweep tracks the most recent SweepInboxByTTL call so
	// the daemon runs it at most once per inboxTTLSweepInterval. Zero
	// means "never run" — the first SyncOnce pass will perform it.
	lastInboxTTLSweep time.Time

	// selfheal holds the per-profile observe-only self-heal engines (lazily
	// created). Driven by this poll loop — NOT a new daemon (F3: no watchdog
	// stacking). nil until the first enabled pass.
	selfheal *selfHealRegistry

	// lastProbeStall rate-limits the probe-stall breadcrumb per (profile|key)
	// so a permanently wedged instance — which times out again on every poll —
	// logs at most once per probeStallLogInterval instead of flooding the log
	// every few seconds. Accessed only from the single-threaded Run loop.
	lastProbeStall map[string]time.Time
}

func NewTransitionDaemon() *TransitionDaemon {
	return &TransitionDaemon{
		notifier:       NewTransitionNotifier(),
		storages:       map[string]*Storage{},
		lastStatus:     map[string]map[string]string{},
		initialized:    map[string]bool{},
		lastDone:       map[string]map[string]DoneSignal{},
		lastDoneScan:   map[string]map[string]time.Time{},
		lastProbeStall: map[string]time.Time{},
	}
}

func (d *TransitionDaemon) Run(ctx context.Context) error {
	d.ensureHookWatcher()
	defer d.shutdown()

	// Prime baseline once, then run adaptive loop.
	interval := d.SyncOnce(ctx)
	if interval <= 0 {
		interval = notifyPollSlow
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
			interval = d.SyncOnce(ctx)
			if interval <= 0 {
				interval = notifyPollSlow
			}
		}
	}
}

// SyncOnce performs one full monitoring pass and returns the recommended delay
// until the next pass.
func (d *TransitionDaemon) SyncOnce(_ context.Context) time.Duration {
	profiles := profilesForTransitionDaemon()
	if len(profiles) == 0 {
		return notifyPollSlow
	}

	nextInterval := notifyPollSlow
	for _, profile := range profiles {
		interval := d.syncProfile(profile)
		if interval < nextInterval {
			nextInterval = interval
		}
		// Issue #1214: replay any durable task-worker completion record whose
		// parent was down/busy when the worker exited. Restart-safe and
		// exactly-once via the record's Acked flag.
		d.ReplayUnackedCompletions(profile)
	}

	d.maybeSweepInboxTTL()

	return nextInterval
}

// ReplayUnackedCompletions re-delivers durable task-worker completion records
// (issue #1214) that have not yet been acknowledged — the wrapper wrote the
// record but no live parent was reachable to wake (conductor down/busy at exit).
// On a successful wake the record is acked so it never fires again. This is the
// restart-durability half of the kernel-exit mechanism: a completion that
// happened while the conductor was offline is delivered exactly once when it
// returns, with no double-wake.
func (d *TransitionDaemon) ReplayUnackedCompletions(profile string) {
	recs, err := LoadCompletionRecords(profile)
	if err != nil {
		return
	}
	for _, rec := range recs {
		if rec.Acked || strings.TrimSpace(rec.Status) == "" {
			continue
		}
		if d.notifier.DeliverCompletion(rec) {
			_ = AckCompletion(rec.Profile, rec.ChildID)
			continue
		}
		// Not committed: the parent is unresolvable (e.g. removed) or a
		// transient error. Count it against the bounded dead-letter budget so
		// an unresolvable completion is dead-lettered to a terminal state after
		// MaxUnresolvedAttempts polls instead of replaying ~1/sec forever
		// (issue #1225 — the dropped_no_target runaway). Acking after
		// dead-letter is safe: the record is durably parked, not lost.
		ev := TransitionNotificationEvent{
			ChildSessionID: rec.ChildID,
			ChildTitle:     rec.Title,
			Profile:        rec.Profile,
			Kind:           transitionKindFinished,
			DoneStatus:     rec.Status,
			DoneSummary:    rec.Summary,
			Timestamp:      time.Now(),
		}
		if d.notifier.deadLetterSink().RecordUnresolvable(ev) {
			_ = AckCompletion(rec.Profile, rec.ChildID)
		}
	}
}

// maybeSweepInboxTTL invokes SweepInboxByTTL when more than
// inboxTTLSweepInterval has elapsed since the last call. Issue #962
// variant: prevents inbox-file growth from children that never see a
// later transition (the cleanup-on-success path alone can't reach
// them).
func (d *TransitionDaemon) maybeSweepInboxTTL() {
	now := time.Now()
	if !d.lastInboxTTLSweep.IsZero() && now.Sub(d.lastInboxTTLSweep) < inboxTTLSweepInterval {
		return
	}
	d.lastInboxTTLSweep = now
	_, _ = SweepInboxByTTL(InboxTTL())
}

// statusProbeBudget bounds a single instance's status refresh in the
// no-live-TUI sync path. The notify-daemon recurring-freeze bug: Run is a
// single-threaded poll loop, so a status probe that never returns — a wedged
// tmux pane, a stuck tmux server, a session whose query hangs, or lock
// contention on inst.mu behind an earlier hung probe — froze the entire
// delivery loop and muted every profile until launchctl kickstart. The
// underlying tmux subprocesses are individually context-bounded (CapturePane
// 3s, Exists 2s, IsPaneDead 2s, WaitDelay 2s); this budget is the loop-level
// backstop that keeps the daemon alive even if some future call site is added
// without its own timeout, or several probes pile up at once.
var statusProbeBudget = 6 * time.Second

// syncPassBudget bounds the cumulative time one no-live-TUI pass spends probing
// instance status. Past it the remaining instances keep their last-known status
// for this pass and are retried next pass, so a burst of simultaneously wedged
// tmux servers can't stretch a single pass without bound (worst case otherwise
// is instanceCount × statusProbeBudget).
var syncPassBudget = 30 * time.Second

// probeStallLogInterval rate-limits the per-(profile,instance) probe-stall
// breadcrumb so a permanently wedged instance doesn't flood the log.
const probeStallLogInterval = time.Minute

// statusProbeFunc is the signature of the swappable status-probe seam.
type statusProbeFunc = func(inst *Instance) error

// updateInstanceStatus is the swappable status-probe seam used by the
// no-live-TUI sync path, held in an atomic.Value so the production read at
// probe-spawn time stays race-free against tests that swap it (a detached probe
// goroutine may still be parked when a test restores the seam). Production
// points it at (*Instance).UpdateStatus, set once in init; tests Store a
// controllable blocking probe to prove a hung tmux call can't freeze the loop.
var updateInstanceStatus atomic.Value

func init() {
	updateInstanceStatus.Store(statusProbeFunc(func(inst *Instance) error { return inst.UpdateStatus() }))
}

// refreshInstanceStatusBounded runs the status probe for inst under
// statusProbeBudget. It returns timedOut=true when the probe didn't finish in
// time, in which case a detached goroutine is still inside UpdateStatus —
// possibly holding inst.mu — and the caller must NOT read lock-guarded instance
// state (it would block on that same lock and re-freeze the loop).
//
// Why a goroutine budget and not only the per-subprocess timeouts: the tmux
// execs already use exec.CommandContext, but UpdateStatus also takes inst.mu and
// can serialize behind a previous hung probe on the same instance, and not every
// reachable call site (e.g. a future helper) is guaranteed to carry its own
// deadline. We deliberately accept a possibly-leaked goroutine over a leaked
// lock: we never block the loop waiting on inst.mu, a still-hung instance simply
// times out again next pass instead of wedging the daemon, and the leak is
// bounded in practice because the subprocess context timeouts let the detached
// probe return within a few seconds.
func (d *TransitionDaemon) refreshInstanceStatusBounded(profile string, inst *Instance) (timedOut bool) {
	probe := updateInstanceStatus.Load().(statusProbeFunc)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = probe(inst)
	}()
	select {
	case <-done:
		return false
	case <-time.After(statusProbeBudget):
		d.logProbeStall(profile, inst.ID, "probe_budget")
		return true
	}
}

// logProbeStall appends a breadcrumb to notifier-probe-stalls.log when a status
// probe (or a whole pass) exceeds its budget, mirroring the notifier-missed.log
// diagnostic idiom so a future hang is visible and the daemon's self-recovery is
// auditable. Rate-limited per (profile|key) to probeStallLogInterval.
func (d *TransitionDaemon) logProbeStall(profile, instanceID, reason string) {
	dedupKey := profile + "|" + instanceID + "|" + reason
	now := time.Now()
	if d.lastProbeStall == nil {
		d.lastProbeStall = map[string]time.Time{}
	}
	if last, ok := d.lastProbeStall[dedupKey]; ok && now.Sub(last) < probeStallLogInterval {
		return
	}
	d.lastProbeStall[dedupKey] = now

	path := notifierProbeStallLogPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	budget := statusProbeBudget
	if reason == "pass_budget" {
		budget = syncPassBudget
	}
	entry := map[string]any{
		"ts":       now.Format(time.RFC3339Nano),
		"profile":  profile,
		"instance": instanceID,
		"reason":   reason,
		"budget":   budget.String(),
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	_, _ = f.Write(append(line, '\n'))
	if err := f.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "logProbeStall: close %s: %v\n", path, err)
	}
}

func notifierProbeStallLogPath() string {
	path, err := logDataPath("notifier-probe-stalls.log")
	if err != nil {
		return tempAgentDeckPath("logs", "notifier-probe-stalls.log")
	}
	return path
}

func profilesForTransitionDaemon() []string {
	profiles, err := ListProfiles()
	if err != nil || len(profiles) == 0 {
		return nil
	}
	sort.Strings(profiles)
	return profiles
}

func (d *TransitionDaemon) syncProfile(profile string) time.Duration {
	storage := d.getStorage(profile)
	if storage == nil {
		return notifyPollSlow
	}

	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		return notifyPollSlow
	}

	byID := make(map[string]*Instance, len(instances))
	hookCandidates := make(map[string]hookTransitionCandidate, len(instances))
	hookStatuses := make(map[string]*HookStatus, len(instances))
	for _, inst := range instances {
		byID[inst.ID] = inst
		if IsClaudeCompatible(inst.Tool) || inst.Tool == "codex" || inst.Tool == "gemini" || inst.Tool == "cursor" {
			if hs := d.hookStatusForInstance(inst.ID); hs != nil {
				// Issue #1349: only let a hook status rebind the session id when
				// the instance is actually LIVE (running/waiting/idle with a real
				// tmux session). A stopped/removed session keeps a stale
				// SessionEnd hook file for up to 24h; without this gate the daemon
				// rebinds its session id every poll cycle from that stale record,
				// colliding two ids onto one session-id and corrupting routing
				// (wrong transcript, dropped completions, mis-delivered input).
				// Done-signal / transition-candidate handling stays unguarded so
				// terminal completions are still observed.
				if isLiveSessionStatus(inst.Status) && inst.Exists() {
					inst.UpdateHookStatus(hs)
				}
				hookStatuses[inst.ID] = hs
				if candidate, ok := terminalHookTransitionCandidate(inst.Tool, hs); ok {
					hookCandidates[inst.ID] = candidate
				}
			}
		}
	}

	db := storage.GetDB()
	tuiAlive := false
	if db != nil {
		if count, err := db.AliveInstanceCount(); err == nil && count > 0 {
			tuiAlive = true
		}
	}

	statuses := map[string]string{}
	if tuiAlive {
		if db != nil {
			if rows, err := db.ReadAllStatuses(); err == nil {
				for id, row := range rows {
					statuses[id] = normalizeStatusString(row.Status)
				}
			}
		}
		for _, inst := range instances {
			if _, ok := statuses[inst.ID]; !ok {
				statuses[inst.ID] = normalizeStatusString(string(inst.Status))
			}
		}
	} else {
		// No live TUI: the daemon is the only thing refreshing status, so it
		// must probe each instance via tmux itself. This loop is the freeze
		// surface — Run is single-threaded (SyncOnce → syncProfile →
		// UpdateStatus per instance), so any probe that blocks here mutes the
		// whole daemon for every profile until launchctl kickstart. Bound each
		// probe (statusProbeBudget) and the cumulative pass (syncPassBudget) so
		// one wedged tmux call — or a burst of them during a busy sprint — can't
		// wedge delivery. See refreshInstanceStatusBounded for the lock
		// reasoning.
		passStart := time.Now()
		passBudgetSpent := false
		for _, inst := range instances {
			previousStatus := normalizeStatusString(string(inst.Status))
			if passBudgetSpent || time.Since(passStart) > syncPassBudget {
				if !passBudgetSpent {
					passBudgetSpent = true
					d.logProbeStall(profile, "", "pass_budget")
				}
				// Out of pass budget: keep last-known status for the remaining
				// instances and retry them next pass rather than stretching this
				// one without bound.
				statuses[inst.ID] = previousStatus
				continue
			}
			if d.refreshInstanceStatusBounded(profile, inst) {
				// Probe exceeded its per-instance budget. A detached goroutine is
				// still inside UpdateStatus and may hold inst.mu, so reading
				// GetStatusThreadSafe would block on that same lock — fall back to
				// the last-known status and keep the loop delivering.
				statuses[inst.ID] = previousStatus
				continue
			}
			status := normalizeStatusString(string(inst.GetStatusThreadSafe()))
			statuses[inst.ID] = status
			if db != nil && status != previousStatus {
				_ = db.WriteStatus(inst.ID, status, inst.Tool)
			}
		}
	}

	// Self-heal Stage 1 (observe-only): evaluate every instance through the
	// profile's observe engine, logging what it WOULD do and taking ZERO action.
	// Runs every poll (including the first) so the dwell/confirm clocks start
	// immediately. Reuses the instances/hookStatuses already loaded above — no
	// extra capture, no new goroutine (F3). Disabled-by-config → cheap no-op.
	d.runSelfHealObservePass(profile, instances, statuses, hookStatuses, db, time.Now().UTC())

	if !d.initialized[profile] {
		// Cover fast transitions that completed before we observed a running snapshot.
		d.emitHookTransitionCandidates(profile, byID, nil, statuses, hookCandidates)
		d.emitDoneSignals(profile, byID, hookStatuses)
		d.lastStatus[profile] = copyStatusMap(statuses)
		d.initialized[profile] = true
		return choosePollInterval(statuses)
	}

	prev := d.lastStatus[profile]
	notifyEnabled := GetNotificationsSettings().GetTransitionEventsEnabled()
	for id, to := range statuses {
		from := normalizeStatusString(prev[id])
		if !ShouldNotifyTransition(from, to) {
			continue
		}
		inst := byID[id]
		if !notifyEnabled || !instanceAcceptsTransitionEvents(inst) {
			continue
		}
		event := TransitionNotificationEvent{
			ChildSessionID: id,
			ChildTitle:     inst.Title,
			Profile:        profile,
			FromStatus:     from,
			ToStatus:       to,
			Timestamp:      time.Now(),
			LastOutputHash: transitionEventOutputHash(inst),
			// Honest Status v2 observability hook: stamp the additive substate so
			// the emitted transition event is structured + substate-bearing. Use
			// the CACHED value (no pane capture) — the daemon's own status poll
			// just refreshed it, and an extra capture per transition would make
			// this hot path heavier than the transcript-stat dedup signal above.
			Substate: string(inst.CachedSubstate()),
		}
		_ = d.notifier.NotifyTransition(event)
	}
	d.emitHookTransitionCandidates(profile, byID, prev, statuses, hookCandidates)
	d.emitDoneSignals(profile, byID, hookStatuses)

	d.lastStatus[profile] = copyStatusMap(statuses)
	return choosePollInterval(statuses)
}

// emitDoneSignals turns a worker-printed completion sentinel (persisted into
// the hook status file by the Stop-hook handler, issue #1186) into a distinct
// "finished" event delivered to the parent. Per-task idempotency is enforced
// via d.lastDone: the same sentinel re-read across polls — or repeated on a
// later identical Stop — fires at most once. A genuinely new completion
// (different status/summary) fires again. Stale hook files (older than
// hookFreshWindow) are ignored so a daemon restart doesn't replay a long-dead
// completion. When the hook's own scan was inconclusive (transcript not
// flushed at Stop time), the hook file carries the transcript path instead of
// done fields and the daemon finishes the scan here — see doneSignalFor.
func (d *TransitionDaemon) emitDoneSignals(profile string, byID map[string]*Instance, hookStatuses map[string]*HookStatus) {
	if len(hookStatuses) == 0 {
		return
	}
	notifyEnabled := GetNotificationsSettings().GetTransitionEventsEnabled()
	for id, hs := range hookStatuses {
		if hs == nil {
			continue
		}
		sig, ok := d.doneSignalFor(profile, id, hs)
		if !ok {
			continue
		}
		if prev, ok := d.lastDone[profile][id]; ok && prev == sig {
			continue // already emitted this exact completion
		}

		inst := byID[id]
		if !notifyEnabled || !instanceAcceptsTransitionEvents(inst) {
			continue
		}

		event := TransitionNotificationEvent{
			ChildSessionID: id,
			ChildTitle:     inst.Title,
			Profile:        profile,
			DoneStatus:     sig.Status,
			DoneSummary:    sig.Summary,
			Timestamp:      hs.UpdatedAt,
		}
		_ = d.notifier.NotifyFinished(event)

		if d.lastDone[profile] == nil {
			d.lastDone[profile] = map[string]DoneSignal{}
		}
		d.lastDone[profile][id] = sig
	}
}

// doneSignalFor resolves a hook status into a completion sentinel, or reports
// none (ok=false). Two sources, in order:
//
//  1. Done fields persisted by the Stop hook's own scan — the common path.
//  2. A pending transcript rescan (issue #1186 flush race): Claude Code can
//     fire the Stop hook BEFORE appending the turn's final assistant record,
//     and the hook — synchronous since #1225, Claude blocks on its exit —
//     must not sleep waiting for the flush. The hook persists the validated
//     transcript path instead, and the daemon's poll loop is the retry: each
//     pass re-scans the tail until the record lands (typically the very next
//     poll) or the hook file ages out of hookFreshWindow.
//
// Both sources respect the #1214 completion-wrapper ownership gate and the
// freshness window exactly like the pre-existing done-fields path.
func (d *TransitionDaemon) doneSignalFor(profile, id string, hs *HookStatus) (DoneSignal, bool) {
	fresh := hs.UpdatedAt.IsZero() || time.Since(hs.UpdatedAt) <= hookFreshWindow

	if strings.TrimSpace(hs.DoneStatus) != "" {
		// Issue #1214: a task worker run one-shot under the completion wrapper
		// owns its own done signal via the kernel-exit path (cmd.Wait ->
		// durable record -> active wake). Stand down from poll-inference for it
		// — the freshness window + lastDone dedup that simulate exactly-once
		// over a polled file are exactly what the kernel exit replaces. The
		// claim record exists for the whole run, so this also wins the race
		// against the worker's own Stop hook. Interactive sessions (no record)
		// keep the path below unchanged.
		if CompletionRecordExists(profile, id) {
			return DoneSignal{}, false
		}
		if !fresh {
			return DoneSignal{}, false
		}
		return DoneSignal{
			Status:  strings.ToLower(strings.TrimSpace(hs.DoneStatus)),
			Summary: strings.TrimSpace(hs.DoneSummary),
		}, true
	}

	// Pending rescan path. Freshness uses a hard zero-check here (unlike the
	// done-fields path, which tolerates a zero UpdatedAt for legacy files):
	// the window is the only bound on the retry loop.
	if strings.TrimSpace(hs.TranscriptPath) == "" {
		return DoneSignal{}, false
	}
	if hs.UpdatedAt.IsZero() || !fresh {
		return DoneSignal{}, false
	}
	// Already reached a conclusive scan for this Stop edge — don't re-read
	// the transcript every poll for the rest of the freshness window. (Hook
	// timestamps have second granularity; two Stop edges inside the same
	// second could collide here, which degrades to the pre-#1186 waiting
	// transition — turns take seconds, so this is acceptable.)
	if resolved, ok := d.lastDoneScan[profile][id]; ok && !hs.UpdatedAt.After(resolved) {
		return DoneSignal{}, false
	}
	if CompletionRecordExists(profile, id) {
		return DoneSignal{}, false
	}
	cleanPath, ok := ValidateTranscriptPath(hs.TranscriptPath)
	if !ok {
		d.markDoneScanResolved(profile, id, hs.UpdatedAt)
		return DoneSignal{}, false
	}
	sig, found, pending := ScanTranscriptTailForDone(cleanPath)
	if pending {
		return DoneSignal{}, false // record still unflushed: retry next poll
	}
	d.markDoneScanResolved(profile, id, hs.UpdatedAt)
	return sig, found
}

func (d *TransitionDaemon) markDoneScanResolved(profile, id string, at time.Time) {
	if d.lastDoneScan[profile] == nil {
		d.lastDoneScan[profile] = map[string]time.Time{}
	}
	d.lastDoneScan[profile][id] = at
}

func (d *TransitionDaemon) getStorage(profile string) *Storage {
	if s, ok := d.storages[profile]; ok && s != nil {
		return s
	}
	s, err := NewStorageWithProfile(profile)
	if err != nil {
		return nil
	}
	d.storages[profile] = s
	return s
}

func (d *TransitionDaemon) ensureHookWatcher() {
	if d.hookWatcher != nil {
		return
	}
	watcher, err := NewStatusFileWatcher(nil)
	if err != nil {
		return
	}
	d.hookWatcher = watcher
	go watcher.Start()
}

func (d *TransitionDaemon) shutdown() {
	if d.hookWatcher != nil {
		d.hookWatcher.Stop()
	}
	// Flush any in-flight async dispatches before closing storage so their
	// logEvent/logMissed writes aren't lost when the process exits.
	if d.notifier != nil {
		d.notifier.Flush()
	}
	for _, s := range d.storages {
		if s != nil {
			_ = s.Close()
		}
	}
}

// Flush exposes the notifier's in-flight-dispatch wait for callers of
// SyncOnce that need deterministic log output before returning (e.g., the
// `agent-deck notify-daemon --once` CLI path).
func (d *TransitionDaemon) Flush() {
	if d.notifier != nil {
		d.notifier.Flush()
	}
}

func choosePollInterval(statuses map[string]string) time.Duration {
	anyRunning := false
	anyWaiting := false
	for _, status := range statuses {
		s := normalizeStatusString(status)
		if s == string(StatusRunning) {
			anyRunning = true
			break
		}
		if s == string(StatusWaiting) {
			anyWaiting = true
		}
	}
	if anyRunning {
		return notifyPollFast
	}
	if anyWaiting {
		return notifyPollMedium
	}
	return notifyPollSlow
}

func normalizeStatusString(status string) string {
	return strings.ToLower(strings.TrimSpace(status))
}

func copyStatusMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func (d *TransitionDaemon) hookStatusForInstance(instanceID string) *HookStatus {
	var best *HookStatus
	if d.hookWatcher != nil {
		if hs := d.hookWatcher.GetHookStatus(instanceID); hs != nil {
			best = hs
		}
	}
	if hs := readHookStatusFile(instanceID); hs != nil {
		if best == nil || hs.UpdatedAt.After(best.UpdatedAt) {
			best = hs
		}
	}
	return best
}

// hookStatusFilePath resolves the on-disk status file for an instance.
// Sandboxed sessions bridge a PER-INSTANCE scoped subdir from the container, so
// their status lands at …/hooks/sandbox/<id>/<id>.json; non-sandbox sessions
// write the flat …/hooks/<id>.json. Prefer the scoped path when it exists, else
// fall back to flat. Robust to a missing sandbox subtree (Lstat just errors and
// we fall through to flat).
//
// We Lstat (not Stat) the scoped path so a container-planted SYMLINK at
// <id>.json is NOT preferred: Lstat reports the link itself, and the subsequent
// no-follow read (readStatusFileNoFollow) refuses to follow it. A symlinked
// scoped path therefore neither gets selected over the flat path nor gets read
// through, closing the exfiltration/DoS vector at the read site too.
func hookStatusFilePath(instanceID string) string {
	hooksDir := GetHooksDir()
	scoped := filepath.Join(hooksDir, "sandbox", instanceID, instanceID+".json")
	if info, err := os.Lstat(scoped); err == nil && info.Mode()&os.ModeSymlink == 0 {
		return scoped
	}
	return filepath.Join(hooksDir, instanceID+".json")
}

func readHookStatusFile(instanceID string) *HookStatus {
	if strings.TrimSpace(instanceID) == "" {
		return nil
	}
	// No-follow + size-bounded read for both the scoped (sandbox) and flat
	// (non-sandbox) paths: a container could symlink or oversize its <id>.json
	// to read a host file or OOM the shared notify-daemon that polls this.
	data, err := readStatusFileNoFollow(hookStatusFilePath(instanceID))
	if err != nil || len(data) == 0 {
		return nil
	}
	var raw struct {
		Status         string `json:"status"`
		SessionID      string `json:"session_id"`
		Event          string `json:"event"`
		Timestamp      int64  `json:"ts"`
		DoneStatus     string `json:"done_status"`
		DoneSummary    string `json:"done_summary"`
		TranscriptPath string `json:"transcript_path"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}
	if strings.TrimSpace(raw.Status) == "" {
		return nil
	}
	updatedAt := time.Now()
	if raw.Timestamp > 0 {
		updatedAt = time.Unix(raw.Timestamp, 0)
	}
	return &HookStatus{
		Status:         raw.Status,
		SessionID:      raw.SessionID,
		Event:          raw.Event,
		UpdatedAt:      updatedAt,
		DoneStatus:     raw.DoneStatus,
		DoneSummary:    raw.DoneSummary,
		TranscriptPath: raw.TranscriptPath,
	}
}

func (d *TransitionDaemon) emitHookTransitionCandidates(
	profile string,
	byID map[string]*Instance,
	prev map[string]string,
	current map[string]string,
	candidates map[string]hookTransitionCandidate,
) {
	if len(candidates) == 0 {
		return
	}
	notifyEnabled := GetNotificationsSettings().GetTransitionEventsEnabled()
	for id, candidate := range candidates {
		inst := byID[id]
		if !notifyEnabled || !instanceAcceptsTransitionEvents(inst) {
			continue
		}
		// Issue #1214: the completion wrapper owns a task worker's terminal
		// signal; suppress poll-inferred candidates for it. Interactive
		// sessions (no completion record) are unaffected.
		if CompletionRecordExists(profile, id) {
			continue
		}

		to := normalizeStatusString(candidate.ToStatus)
		// A live TUI heartbeat routes `current` through DB status rows. A TUI
		// that holds the heartbeat without refreshing its rows (orphaned tab,
		// or sessions created after it loaded its list) leaves rows frozen at
		// `running`, and letting that stale row override a FRESH terminal hook
		// status drops the child's completion entirely — no transition event,
		// no log line. The hook file is the child's own runtime asserting its
		// state; only defer to the row when the row itself is notify-terminal
		// (it may be MORE final, e.g. error). A non-terminal row never vetoes
		// a fresh terminal hook status.
		if curr := normalizeStatusString(current[id]); curr != "" && isNotifyTerminalStatus(curr) {
			to = curr
		}
		if !isNotifyTerminalStatus(to) {
			continue
		}

		fromSnapshot := ""
		if prev != nil {
			fromSnapshot = normalizeStatusString(prev[id])
		}
		// Snapshot transition path already handled this case.
		if ShouldNotifyTransition(fromSnapshot, normalizeStatusString(current[id])) {
			continue
		}

		event := TransitionNotificationEvent{
			ChildSessionID: id,
			ChildTitle:     inst.Title,
			Profile:        profile,
			FromStatus:     string(StatusRunning),
			ToStatus:       to,
			Timestamp:      candidate.Timestamp,
			LastOutputHash: transitionEventOutputHash(inst),
		}
		_ = d.notifier.NotifyTransition(event)
	}
}

func isNotifyTerminalStatus(status string) bool {
	s := normalizeStatusString(status)
	return s == string(StatusWaiting) || s == string(StatusError) || s == string(StatusIdle) || s == string(StatusStopped)
}

func terminalHookTransitionCandidate(tool string, hs *HookStatus) (hookTransitionCandidate, bool) {
	if hs == nil || hs.UpdatedAt.IsZero() {
		return hookTransitionCandidate{}, false
	}
	if time.Since(hs.UpdatedAt) > hookFreshWindow {
		return hookTransitionCandidate{}, false
	}

	to := normalizeStatusString(hs.Status)
	if !isNotifyTerminalStatus(to) {
		return hookTransitionCandidate{}, false
	}

	event := strings.ToLower(strings.TrimSpace(hs.Event))
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "claude":
		// SessionStart is intentionally excluded (initial prompt isn't task completion).
		if event == "stop" || event == "permissionrequest" || event == "notification" {
			return hookTransitionCandidate{ToStatus: to, Timestamp: hs.UpdatedAt}, true
		}
	case "codex":
		if isCodexTerminalHookEvent(event) {
			return hookTransitionCandidate{ToStatus: to, Timestamp: hs.UpdatedAt}, true
		}
	case "cursor":
		// sessionStart is intentionally excluded (initial prompt isn't task completion).
		if event == "stop" {
			return hookTransitionCandidate{ToStatus: to, Timestamp: hs.UpdatedAt}, true
		}
	}
	return hookTransitionCandidate{}, false
}

// isTerminalHookEvent reports whether a hook event name denotes session/thread
// termination (issue #1349). It mirrors the allowlist in
// cmd/agent-deck/hook_handler.go:isTerminalHookEvent (kept in the main package
// for the hook writer); this copy lets the session package refuse to bind a
// session id from a terminal payload. A SessionEnd record must never be a bind
// source — by the time it fires the session is gone, so its session_id is at
// best stale and at worst belongs to a different live session after id reuse.
func isTerminalHookEvent(event string) bool {
	norm := strings.ToLower(strings.TrimSpace(event))
	if norm == "" {
		return false
	}
	norm = strings.NewReplacer(".", "", "-", "", "_", "", "/", "", " ", "").Replace(norm)
	switch norm {
	case "sessionend", "sessionended", "sessionclose", "sessionclosed", "sessiondone", "sessionexit", "sessionexited",
		"onsessionend",
		"threadend", "threadended", "threadterminate", "threadterminated", "threadclose", "threadclosed",
		"threaddone", "threadexit", "threadexited":
		return true
	default:
		return false
	}
}

func isCodexTerminalHookEvent(event string) bool {
	e := strings.ToLower(strings.TrimSpace(event))
	if e == "" {
		return false
	}
	canon := strings.NewReplacer(".", "/", "-", "/", "_", "/").Replace(e)
	if !strings.Contains(canon, "turn") {
		return false
	}
	return strings.Contains(canon, "complete") ||
		strings.Contains(canon, "fail") ||
		strings.Contains(canon, "abort") ||
		strings.Contains(canon, "cancel")
}
