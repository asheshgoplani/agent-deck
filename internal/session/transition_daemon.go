package session

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/desknotify"
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

	// deskNotifier alerts the OPERATOR (not a parent session) when a session
	// needs input. Distinct from notifier above, which is parent-keyed and so
	// cannot reach a top-level session. Opt-in via [notifications] desktop.
	deskNotifier *desknotify.Notifier

	hookWatcher *StatusFileWatcher

	storages map[string]*Storage

	lastStatus  map[string]map[string]string
	initialized map[string]bool

	// lastDone tracks the most recently emitted completion sentinel per
	// (profile, instance) so a finished event (issue #1186) is emitted once
	// per distinct completion. Re-reading the same done-bearing hook file
	// across polls — or a later identical Stop — does not re-fire.
	lastDone map[string]map[string]DoneSignal

	// lastTurn tracks, per (profile, instance), the last COMPLETED TURN recorded
	// into the drainable ledgers. Keyed by status + the transcript signal, so a
	// session parked at `waiting` records once and a genuinely new turn records
	// again. This is what makes recording independent of whether the daemon
	// happened to observe the session mid-`running`: see recordTerminalTurns.
	lastTurn map[string]map[string]string

	// turnLiveCheck decides whether an instance is a live session or a stale
	// registry row. A seam because the real check probes tmux, which a unit test
	// cannot and should not do — and testing this logic is the whole point after a
	// field failure that a passing suite failed to catch.
	turnLiveCheck func(inst *Instance) bool

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

	// lastDesktopNotify records the status a desktop notification was last
	// raised for, per (profile, instance), so the notification is EDGE
	// triggered rather than level triggered.
	//
	// The snapshot path is naturally edge-triggered: ShouldNotifyTransition
	// requires from==running and prev is refreshed each pass, so a session that
	// stays waiting is skipped. The hook-candidate path is NOT.
	// terminalHookTransitionCandidate accepts any hook file updated within
	// hookFreshWindow (45s), so a session that stops and stays waiting yields a
	// candidate on every poll for that whole window. The transition notifier's
	// own 90s dedup sits DOWNSTREAM in NotifyTransition and cannot cover a
	// desktop call placed before it. Without this map one answered prompt would
	// raise roughly 15-22 banners at a 2-3s poll interval.
	//
	// Accessed only from the single-threaded Run loop, like lastProbeStall.
	lastDesktopNotify map[string]string

	// desktopWG tracks in-flight desktop notifications, which are dispatched
	// off the poll loop so a wedged notifier binary cannot stall session
	// monitoring. Only tests wait on it.
	desktopWG sync.WaitGroup
}

func NewTransitionDaemon() *TransitionDaemon {
	return &TransitionDaemon{
		notifier:       NewTransitionNotifier(),
		deskNotifier:   desknotify.New(),
		storages:       map[string]*Storage{},
		lastStatus:     map[string]map[string]string{},
		initialized:    map[string]bool{},
		lastDone:       map[string]map[string]DoneSignal{},
		lastTurn:       map[string]map[string]string{},
		turnLiveCheck:  func(inst *Instance) bool { return inst.Exists() },
		lastDoneScan:   map[string]map[string]time.Time{},
		lastProbeStall: map[string]time.Time{},

		lastDesktopNotify: map[string]string{},
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

	// Stamp liveness BEFORE the work: the question a drain asks is "is a writer
	// alive", and a daemon wedged inside a probe should still look alive for one
	// staleness window rather than flipping to "absent" the moment it stalls.
	WriteNotifyHeartbeat()

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
		committed, parked := d.notifier.deliverCompletion(rec)
		if committed {
			_ = AckCompletion(rec.Profile, rec.ChildID)
			continue
		}
		// _unowned is a discovery copy, never an acknowledgement. Keep the
		// completion record replayable across daemon/parent restart, but do not
		// spend its dead-letter budget merely because the parent is absent.
		if parked {
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
		if IsClaudeCompatible(inst.Tool) || inst.Tool == "codex" || inst.Tool == "gemini" || inst.Tool == "cursor" || inst.Tool == "hermes" {
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

	// Runs on EVERY pass, the first scan included — see the FIRST SCAN note on
	// recordTerminalTurns for why suppressing it would recreate the field bug.
	d.recordTerminalTurns(profile, byID, statuses, hookStatuses)

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
		d.clearDesktopEdgeIfRunning(profile, id, to)

		from := normalizeStatusString(prev[id])
		if !ShouldNotifyTransition(from, to) {
			continue
		}
		inst := byID[id]

		// Desktop notification runs BEFORE (and independently of) the
		// parent-routing gate below. That gate drops a session with no
		// ParentSessionID, which is exactly the top-level session whose
		// operator has no other out-of-TUI signal. Gating the two together
		// would reproduce the hole this exists to close.
		d.notifyDesktop(profile, inst, to)

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

// recordTerminalTurns records EVERY completed turn into the drainable ledgers,
// whether or not a completion sentinel was printed and whether or not the daemon
// happened to observe the session mid-`running`.
//
// Field failure (2026-08-20, Mac↔agentbox, reproduced on g14 with the same
// binary): a fresh claude session completed a turn, went to `waiting`, and
// NOTHING was written anywhere. Two independent causes, both closed here.
//
//  1. DETECTION. The snapshot loop fires only on an observed edge, and
//     ShouldNotifyTransition returns false when `from` is empty. A session first
//     seen already at `waiting` — any turn that finishes between two polls, which
//     is most short turns — therefore has no edge to fire on, and is then seeded
//     into lastStatus so it can never fire for that turn afterwards. The
//     compensating hook path (emitHookTransitionCandidates) covers exactly this
//     case but needs Claude Stop-hooks wired into ~/.claude/settings.json; on a
//     plain box none are, so hookStatuses is empty and it never runs.
//
//  2. RECORDING. WriteLedgerEntry was reachable ONLY from emitDoneSignals, which
//     returns immediately when len(hookStatuses) == 0 and otherwise requires a
//     ===AGENTDECK_DONE=== sentinel. Ordinary sessions — every interactive one —
//     wrote no ledger entry, ever.
//
// The turn key is status + the transcript signal rather than a status edge. The
// transcript is append-only and grows only on a real message, so the key is
// stable while a session sits parked and changes on a genuine new turn. That
// also catches waiting→running→waiting entirely between two polls, which no
// edge-based rule can see. A tool with no resolvable transcript yields an empty
// signal, so it records once per status change — the honest limit of what is
// observable without a transcript, and the reason a sentinel still helps.
//
// FIRST SCAN. This runs on the daemon's first pass too, deliberately. The
// 2026-08-20 field round 3 lost its turn to exactly that window: the daemon was
// started, a session was launched, and its turn finished before any pass had
// seen it running — a first-scan race. Seeding a silent baseline instead would
// suppress precisely the turns the field test proved are being lost. The cost of
// not seeding is re-publishing turns that are still parked when a daemon
// restarts, and that costs nothing in practice: the record carries the same
// transcript signal, so it produces an identical EventFingerprint and the inbox
// collapses it. Across a consumption boundary the #1225 turn_fingerprint ledger
// collapses the consumer effect instead. Both layers already existed; this
// leans on them rather than adding a third.
//
// This does NOT emit desktop notifications: the operator-facing alert stays on
// the observed-edge rule above, deliberately, so closing the ledger gap cannot
// change notification volume.
//
// Duplicates are not a concern by construction. A turn the snapshot loop already
// emitted produces an identical EventFingerprint here (same child, same
// from=running, same to, same transcript signal), so WriteInboxEventIfNew
// collapses the two — in the parent inbox and in the _unowned ledger alike.
// Ordinary terminal observations are deliberately NOT completion-ledger entries:
// only a sentinel asserts that a task finished. Mirroring a waiting transition
// into that ledger changes its kind to "finished" during export.
func (d *TransitionDaemon) recordTerminalTurns(
	profile string,
	byID map[string]*Instance,
	statuses map[string]string,
	hookStatuses map[string]*HookStatus,
) {
	notifyEnabled := GetNotificationsSettings().GetTransitionEventsEnabled()
	if d.lastTurn == nil {
		d.lastTurn = map[string]map[string]string{}
	}
	if d.lastTurn[profile] == nil {
		d.lastTurn[profile] = map[string]string{}
	}
	seen := d.lastTurn[profile]

	for id, to := range statuses {
		if !isRecordableTurnStatus(to) {
			continue
		}
		inst := byID[id]
		if inst == nil {
			continue
		}
		signal := transitionEventOutputHash(inst)
		key := to + "|" + signal
		previous, known := seen[id]
		if known && previous == key {
			continue
		}
		// NOTE ON A SUPPRESSION THAT WAS TRIED AND REMOVED. A first observation
		// with no transcript signal looks like a launch rather than a completion
		// — a pane that has just come up sits at `idle` with nothing behind it,
		// and an early version skipped that case to avoid reporting a launch as a
		// finished turn. It was wrong: a tool that never writes a transcript at
		// all (a bash one-shot, field round 1) has an empty signal for its REAL
		// completion too, so the rule silenced exactly the sessions the field
		// test was trying to see. A spurious `idle` record is honest — the
		// session is idle — while a missed completion is the bug this exists to
		// fix. No transition is skipped here.
		_ = known

		// Liveness is checked only for a new candidate, so the tmux probe costs
		// nothing in steady state after an eligible turn has been recorded. A
		// registry row for a long-dead session sits at `error`
		// indefinitely; recording that as a completed turn published 34 stale
		// sessions on the first scan when this was first run against a real
		// profile, which is how the check came to be here.
		if d.turnLiveCheck != nil && !d.turnLiveCheck(inst) {
			continue
		}

		if !notifyEnabled || !instanceAcceptsTransitionEvents(inst) {
			continue
		}
		// Commit the dedup key only after the observation is eligible. A registry
		// row can appear before its tmux session, and notification settings can be
		// enabled while a turn remains parked; neither temporary rejection may
		// permanently suppress that unchanged turn.
		seen[id] = key

		// FromStatus is stamped `running` rather than the observed previous
		// status, matching what emitHookTransitionCandidates already does for
		// turns too fast to observe: a turn that reached a terminal status ran,
		// whether or not any poll caught it doing so. It also makes the
		// fingerprint identical to the snapshot loop's for the same turn, which
		// is what lets the inbox collapse the pair.
		event := TransitionNotificationEvent{
			ChildSessionID: id,
			ChildTitle:     inst.Title,
			Profile:        profile,
			FromStatus:     string(StatusRunning),
			ToStatus:       to,
			Timestamp:      time.Now(),
			LastOutputHash: signal,
			Substate:       string(inst.CachedSubstate()),
		}
		_ = d.notifier.NotifyTransition(event)
	}

	// Instances that disappeared (stopped, removed) must not keep an entry, or a
	// long-lived daemon accumulates one per session ever seen.
	for id := range seen {
		if _, ok := statuses[id]; !ok {
			delete(seen, id)
		}
	}
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

		// Record the completion to the non-destructive ledger so a parent can
		// query `session children` without consuming the delivery event.
		// Best-effort: a ledger failure must never block notification.
		_ = WriteLedgerEntry(CompletionLedgerEntry{
			ChildID:    id,
			Profile:    profile,
			Title:      inst.Title,
			Status:     sig.Status,
			Summary:    sig.Summary,
			FinishedAt: hs.UpdatedAt,
		})
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
	statusPath := hookStatusFilePath(instanceID)
	data, err := readStatusFileNoFollow(statusPath)
	if err != nil || len(data) == 0 {
		return nil
	}
	var raw struct {
		Status                   string `json:"status"`
		SessionID                string `json:"session_id"`
		Event                    string `json:"event"`
		Timestamp                int64  `json:"ts"`
		DoneStatus               string `json:"done_status"`
		DoneSummary              string `json:"done_summary"`
		TranscriptPath           string `json:"transcript_path"`
		Cwd                      string `json:"cwd"`
		CodexStartedGeneration   string `json:"codex_started_generation"`
		CodexCompletedGeneration string `json:"codex_completed_generation"`
		CodexStartedSessionID    string `json:"codex_started_session_id"`
		CodexCompletedSessionID  string `json:"codex_completed_session_id"`
		HookGeneration           string `json:"hook_generation"`
		Sequence                 uint64 `json:"sequence"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}
	if strings.TrimSpace(raw.Status) == "" {
		return nil
	}
	if generation, authority := hookGenerationForInstance(instanceID); !hookGenerationRecordAccepted(raw.HookGeneration, generation, authority) {
		return nil
	}
	updatedAt := time.Now()
	if raw.Timestamp > 0 {
		updatedAt = time.Unix(raw.Timestamp, 0)
	}
	hookStatus := &HookStatus{
		Status:                   raw.Status,
		SessionID:                raw.SessionID,
		Event:                    raw.Event,
		UpdatedAt:                updatedAt,
		DoneStatus:               raw.DoneStatus,
		DoneSummary:              raw.DoneSummary,
		TranscriptPath:           raw.TranscriptPath,
		Cwd:                      raw.Cwd,
		CodexStartedGeneration:   raw.CodexStartedGeneration,
		CodexCompletedGeneration: raw.CodexCompletedGeneration,
		CodexStartedSessionID:    raw.CodexStartedSessionID,
		CodexCompletedSessionID:  raw.CodexCompletedSessionID,
		HookGeneration:           raw.HookGeneration,
		Sequence:                 raw.Sequence,
	}
	maskConsumedCodexCompletion(instanceID, hookStatus)
	return hookStatus
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

		// Desktop-notify here too, AFTER the veto above so the two emission
		// paths cannot double-fire for one transition. This path covers a
		// transition fast enough that no `running` snapshot was ever observed
		// (a short prompt answered between polls), which is the common case
		// for an interactive agent, so omitting it would miss most alerts.
		d.notifyDesktop(profile, inst, to)

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

// isRecordableTurnStatus is the set of statuses that mean "a turn finished":
// waiting, idle, error. Deliberately NARROWER than isNotifyTerminalStatus, which
// also includes `stopped` — a stopped session did not complete a turn, it was
// shut down, and recording that as a completion tells a conductor something
// untrue.
func isRecordableTurnStatus(status string) bool {
	s := normalizeStatusString(status)
	return s == string(StatusWaiting) || s == string(StatusIdle) || s == string(StatusError)
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
	case "hermes":
		if event == "post_llm_call" || event == "postllmcall" || event == "onsessionend" || event == "on_session_end" {
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
		"onsessionfinalize",
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

// clearDesktopEdgeIfRunning drops a session's desktop edge record once it has
// moved on from the state the operator was alerted about, so its NEXT prompt
// notifies again. See releasesDesktopEdge for which statuses count.
//
// Without this the edge record set by notifyDesktop would persist for the life
// of the process and the FIRST prompt in a session would be the only one that
// ever alerted, which is a worse failure than the banner spam the edge record
// exists to prevent.
func (d *TransitionDaemon) clearDesktopEdgeIfRunning(profile, instanceID, toStatus string) {
	if d == nil || d.lastDesktopNotify == nil {
		return
	}
	if !releasesDesktopEdge(toStatus) {
		return
	}
	delete(d.lastDesktopNotify, profile+"|"+instanceID)
}

// releasesDesktopEdge reports whether a status means the session has moved on
// from whatever the operator was last alerted about, so its next prompt should
// alert again.
//
// Re-arming on running alone is not enough. The hook-candidate dispatch site
// exists precisely for turns too fast for a running snapshot to be observed, so
// the sequence waiting -> (answered, agent finishes fast) -> idle -> waiting can
// occur with the daemon never seeing running. Keyed on running only, the edge
// record stays pinned at "waiting" and the SECOND genuine prompt is suppressed:
// a silently dropped alert, which is the exact failure this feature exists to
// remove, and a worse outcome than the banner spam the edge prevents.
//
// idle and starting are therefore releases too: both mean "not currently
// asking the operator for anything". The attention statuses (waiting, error)
// are deliberately NOT releases, because holding the edge while a session sits
// in one of them is what stops per-poll spam.
func releasesDesktopEdge(status string) bool {
	switch normalizeStatusString(status) {
	case string(StatusRunning), string(StatusIdle), string(StatusStarting):
		return true
	default:
		return false
	}
}

// notifyDesktop raises an OS notification for a session that needs the
// operator. No-op unless [notifications] desktop is enabled, and best-effort
// after that: a status poll must never fail or stall because a notifier did.
func (d *TransitionDaemon) notifyDesktop(profile string, inst *Instance, toStatus string) {
	if d == nil || d.deskNotifier == nil || inst == nil {
		return
	}
	if !GetNotificationsSettings().GetDesktopEnabled() {
		return
	}
	// Narrower than the parent-routing status set: see desknotify.ShouldNotify.
	if !desknotify.ShouldNotify(toStatus) {
		return
	}
	// Honour the per-session opt-out the parent path already respects, so one
	// noisy session can be silenced without disabling notifications globally.
	if inst.NoTransitionNotify {
		return
	}
	// Edge-trigger: notify once per entry into a status, not once per poll
	// while the session sits in it. Required because the hook-candidate call
	// site re-derives a candidate for the whole 45s hookFreshWindow. Keyed on
	// the status too, so waiting -> error still alerts.
	key := profile + "|" + inst.ID
	if d.lastDesktopNotify[key] == toStatus {
		return
	}
	if d.lastDesktopNotify == nil {
		d.lastDesktopNotify = map[string]string{}
	}
	d.lastDesktopNotify[key] = toStatus

	// Dispatch OFF the poll loop. Notify shells out to a notifier binary and
	// bounds itself at 3s, but that bound is per invocation and nothing wraps
	// this call: statusProbeBudget and syncPassBudget cover status probes, not
	// this. N sessions transitioning in one pass would otherwise serialize into
	// N x 3s of blocking in a single-threaded loop that has a documented freeze
	// history (see statusProbeBudget). Fire-and-forget is safe because the
	// result is already advisory: Notify never returns an error, and no caller
	// branches on whether a banner appeared. The edge record above is written
	// BEFORE the goroutine starts, so suppression does not depend on it.
	notif := desknotify.Notification{
		SessionTitle: inst.Title,
		Profile:      profile,
		ToStatus:     toStatus,
	}
	instanceID := inst.ID
	d.desktopWG.Add(1)
	go func() {
		defer d.desktopWG.Done()
		if backend := d.deskNotifier.Notify(notif); backend != "" {
			sessionLog.Debug("desktop_notification_sent",
				slog.String("instance_id", instanceID),
				slog.String("to_status", toStatus),
				slog.String("backend", backend))
		}
	}()
}

// waitDesktopNotifications blocks until every in-flight desktop notification
// has finished. Exists so tests can assert on delivery without racing the
// fire-and-forget dispatch; production never needs to wait.
func (d *TransitionDaemon) waitDesktopNotifications() {
	if d == nil {
		return
	}
	d.desktopWG.Wait()
}
