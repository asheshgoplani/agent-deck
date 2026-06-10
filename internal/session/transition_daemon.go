package session

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
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

	// lastInboxTTLSweep tracks the most recent SweepInboxByTTL call so
	// the daemon runs it at most once per inboxTTLSweepInterval. Zero
	// means "never run" — the first SyncOnce pass will perform it.
	lastInboxTTLSweep time.Time
}

func NewTransitionDaemon() *TransitionDaemon {
	return &TransitionDaemon{
		notifier:    NewTransitionNotifier(),
		storages:    map[string]*Storage{},
		lastStatus:  map[string]map[string]string{},
		initialized: map[string]bool{},
		lastDone:    map[string]map[string]DoneSignal{},
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
		if IsClaudeCompatible(inst.Tool) || inst.Tool == "codex" || inst.Tool == "gemini" {
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
		for _, inst := range instances {
			previousStatus := normalizeStatusString(string(inst.Status))
			_ = inst.UpdateStatus()
			status := normalizeStatusString(string(inst.GetStatusThreadSafe()))
			statuses[inst.ID] = status
			if db != nil && status != previousStatus {
				_ = db.WriteStatus(inst.ID, status, inst.Tool)
			}
		}
	}

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
// completion.
func (d *TransitionDaemon) emitDoneSignals(profile string, byID map[string]*Instance, hookStatuses map[string]*HookStatus) {
	if len(hookStatuses) == 0 {
		return
	}
	notifyEnabled := GetNotificationsSettings().GetTransitionEventsEnabled()
	for id, hs := range hookStatuses {
		if hs == nil || strings.TrimSpace(hs.DoneStatus) == "" {
			continue
		}
		// Issue #1214: a task worker run one-shot under the completion wrapper
		// owns its own done signal via the kernel-exit path (cmd.Wait ->
		// durable record -> active wake). Stand down from poll-inference for it
		// — the freshness window + lastDone dedup that simulate exactly-once
		// over a polled file are exactly what the kernel exit replaces. The
		// claim record exists for the whole run, so this also wins the race
		// against the worker's own Stop hook. Interactive sessions (no record)
		// keep the path below unchanged.
		if CompletionRecordExists(profile, id) {
			continue
		}
		if !hs.UpdatedAt.IsZero() && time.Since(hs.UpdatedAt) > hookFreshWindow {
			continue
		}
		sig := DoneSignal{
			Status:  strings.ToLower(strings.TrimSpace(hs.DoneStatus)),
			Summary: strings.TrimSpace(hs.DoneSummary),
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

func readHookStatusFile(instanceID string) *HookStatus {
	if strings.TrimSpace(instanceID) == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(GetHooksDir(), instanceID+".json"))
	if err != nil || len(data) == 0 {
		return nil
	}
	var raw struct {
		Status      string `json:"status"`
		SessionID   string `json:"session_id"`
		Event       string `json:"event"`
		Timestamp   int64  `json:"ts"`
		DoneStatus  string `json:"done_status"`
		DoneSummary string `json:"done_summary"`
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
		Status:      raw.Status,
		SessionID:   raw.SessionID,
		Event:       raw.Event,
		UpdatedAt:   updatedAt,
		DoneStatus:  raw.DoneStatus,
		DoneSummary: raw.DoneSummary,
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
		if curr := normalizeStatusString(current[id]); curr != "" {
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
