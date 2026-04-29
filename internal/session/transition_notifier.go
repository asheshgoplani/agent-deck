package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	transitionDeliverySent        = "sent"
	transitionDeliveryFailed      = "failed"
	transitionDeliveryDropped     = "dropped_no_target"
	transitionDeliveryDeferred    = "deferred_target_busy"
	transitionDeliveryDispatching = "dispatching"

	defaultSendTimeout      = 30 * time.Second
	defaultQueueMaxAge      = 10 * time.Minute
	defaultQueueMaxAttempts = 20
)

type TransitionNotificationEvent struct {
	ChildSessionID string    `json:"child_session_id"`
	ChildTitle     string    `json:"child_title"`
	Profile        string    `json:"profile"`
	FromStatus     string    `json:"from_status"`
	ToStatus       string    `json:"to_status"`
	Timestamp      time.Time `json:"timestamp"`

	TargetSessionID string `json:"target_session_id,omitempty"`
	TargetKind      string `json:"target_kind,omitempty"` // parent | conductor
	DeliveryResult  string `json:"delivery_result,omitempty"`
}

type transitionNotifyRecord struct {
	From string `json:"from"`
	To   string `json:"to"`
	At   int64  `json:"at"`
}

type transitionNotifyState struct {
	Records map[string]transitionNotifyRecord `json:"records"`
}

type deferredQueueEntry struct {
	Event           TransitionNotificationEvent `json:"event"`
	FirstDeferredAt time.Time                   `json:"first_deferred_at"`
	Attempts        int                         `json:"attempts"`
}

type deferredQueue struct {
	Entries []deferredQueueEntry `json:"entries"`
}

// transitionSender is the function the notifier calls to push an event into
// a target's tmux pane. In production it's SendSessionMessageReliable; tests
// swap it for a controllable fake to exercise timeout/busy/success paths
// without a live tmux server.
type transitionSender func(profile, sessionID, message string) error

type TransitionNotifier struct {
	statePath  string
	logPath    string
	missedPath string
	queuePath  string
	orphanPath string

	sender      transitionSender
	sendTimeout time.Duration

	// busyBackoff is the in-process retry schedule used when the parent is
	// StatusRunning at dispatch time. After the last entry is exhausted the
	// event is persisted to the per-conductor inbox. Defaults to
	// {5s,15s,45s} via NewTransitionNotifier; tests override with shorter
	// durations.
	busyBackoff []time.Duration

	// availability decides whether a target session is free to receive a
	// send. Defaults to liveTargetAvailability (real tmux state); tests
	// inject a deterministic stub.
	availability targetAvailabilityResolver

	mu    sync.Mutex
	state transitionNotifyState

	queueMu sync.Mutex
	queue   deferredQueue

	slotsMu     sync.Mutex
	targetSlots map[string]chan struct{}

	// orphanMu guards orphanWarned. The set tracks child session ids we have
	// already emitted a WARN for, so a long-lived orphan firing many
	// transitions does not flood notifier-orphans.log.
	orphanMu     sync.Mutex
	orphanWarned map[string]bool

	// stopCh closes when Close() is invoked. scheduleBusyRetry's sleep loops
	// select on it so test cleanups can cancel pending retries instead of
	// letting them write inbox files into the post-cleanup environment.
	stopMu sync.Mutex
	stopCh chan struct{}

	// watchersWG tracks the short-lived goroutine that waits on a send's
	// completion or timeout. Tests use it to synchronize before asserting
	// on log file contents. sendersWG (not exposed) tracks the possibly
	// long-lived send goroutine itself, which may leak when the tmux pane
	// hangs past sendTimeout.
	watchersWG sync.WaitGroup
	sendersWG  sync.WaitGroup
}

func NewTransitionNotifier() *TransitionNotifier {
	n := &TransitionNotifier{
		statePath:   transitionNotifyStatePath(),
		logPath:     transitionNotifyLogPath(),
		missedPath:  transitionNotifierMissedPath(),
		queuePath:   transitionNotifierQueuePath(),
		orphanPath:  transitionNotifierOrphanLogPath(),
		sender:      SendSessionMessageReliable,
		sendTimeout: defaultSendTimeout,
		busyBackoff: []time.Duration{5 * time.Second, 15 * time.Second, 45 * time.Second},
		state: transitionNotifyState{
			Records: map[string]transitionNotifyRecord{},
		},
		targetSlots:  map[string]chan struct{}{},
		orphanWarned: map[string]bool{},
		stopCh:       make(chan struct{}),
	}
	n.loadState()
	n.loadQueue()
	return n
}

// Close cancels any pending in-process busy retries. Production callers do
// not need it because the daemon process owns the notifier for its
// lifetime; tests use it to stop scheduleBusyRetry goroutines from writing
// to inbox files after t.TempDir cleanup. Idempotent.
func (n *TransitionNotifier) Close() {
	n.stopMu.Lock()
	defer n.stopMu.Unlock()
	if n.stopCh == nil {
		return
	}
	select {
	case <-n.stopCh:
		// already closed
	default:
		close(n.stopCh)
	}
}

func (n *TransitionNotifier) getStopCh() <-chan struct{} {
	n.stopMu.Lock()
	defer n.stopMu.Unlock()
	if n.stopCh == nil {
		n.stopCh = make(chan struct{})
	}
	return n.stopCh
}

func ShouldNotifyTransition(fromStatus, toStatus string) bool {
	from := strings.ToLower(strings.TrimSpace(fromStatus))
	to := strings.ToLower(strings.TrimSpace(toStatus))
	if from == "" || to == "" || from == to {
		return false
	}
	if from != string(StatusRunning) {
		return false
	}
	return isTerminalAttentionStatus(to)
}

func isTerminalAttentionStatus(status string) bool {
	s := strings.ToLower(strings.TrimSpace(status))
	return s == string(StatusWaiting) || s == string(StatusError) || s == string(StatusIdle)
}

func isConductorSessionTitle(title string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(title)), "conductor-")
}

// NotifyTransition validates the event, resolves the delivery target, and
// schedules an async send. Synchronous returns: dropped / deferred. Async
// returns: dispatching (final sent/failed/timeout is written to logs from
// the send goroutine). Deferred events are persisted to the retry queue so
// the next daemon poll can try again when the target is free — this is the
// v1.7.45 fix for the silent-loss bug where the daemon's lastStatus update
// permanently masked deferred transitions.
func (n *TransitionNotifier) NotifyTransition(event TransitionNotificationEvent) TransitionNotificationEvent {
	event.FromStatus = strings.ToLower(strings.TrimSpace(event.FromStatus))
	event.ToStatus = strings.ToLower(strings.TrimSpace(event.ToStatus))
	event.Profile = strings.TrimSpace(event.Profile)
	event.ChildTitle = strings.TrimSpace(event.ChildTitle)
	event.ChildSessionID = strings.TrimSpace(event.ChildSessionID)
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}

	if !ShouldNotifyTransition(event.FromStatus, event.ToStatus) {
		event.DeliveryResult = transitionDeliveryDropped
		return event
	}
	if event.ChildSessionID == "" || event.Profile == "" {
		event.DeliveryResult = transitionDeliveryDropped
		return event
	}
	if isConductorSessionTitle(event.ChildTitle) {
		event.DeliveryResult = transitionDeliveryDropped
		return event
	}
	if n.isDuplicate(event) {
		event.DeliveryResult = transitionDeliveryDropped
		return event
	}

	plan := n.prepareDispatch(event)
	if plan.finalized {
		n.logEvent(plan.event)
		return plan.event
	}

	// Ready to send: mark notified synchronously so subsequent polls don't
	// redispatch while the async send is in flight, then fire-and-forget.
	n.markNotified(plan.event)
	n.dispatchAsync(plan.event.TargetSessionID, plan.message, plan.event)

	plan.event.DeliveryResult = transitionDeliveryDispatching
	return plan.event
}

type dispatchPlan struct {
	event     TransitionNotificationEvent
	message   string
	finalized bool // true = sync short-circuit; false = continue to async send
}

func (n *TransitionNotifier) prepareDispatch(event TransitionNotificationEvent) dispatchPlan {
	plan := dispatchPlan{event: event}

	storage, err := NewStorageWithProfile(event.Profile)
	if err != nil {
		plan.event.DeliveryResult = transitionDeliveryFailed
		plan.finalized = true
		return plan
	}
	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		plan.event.DeliveryResult = transitionDeliveryFailed
		plan.finalized = true
		return plan
	}

	byID := make(map[string]*Instance, len(instances))
	for _, inst := range instances {
		byID[inst.ID] = inst
	}

	child := byID[event.ChildSessionID]
	if child == nil {
		plan.event.DeliveryResult = transitionDeliveryDropped
		plan.finalized = true
		return plan
	}
	if child.NoTransitionNotify {
		plan.event.DeliveryResult = transitionDeliveryDropped
		plan.finalized = true
		return plan
	}

	// Orphan-on-creation guard (issue #805 cause A). When a child is born
	// without a parent linkage — typically because a worktree-setup hook
	// or sandboxed shell dropped $AGENTDECK_INSTANCE_ID — every transition
	// it fires resolves to nil parent and drops silently. Log a single WARN
	// per orphan child so the operator gets actionable signal pointing at
	// the documented `agent-deck session set-parent` workflow.
	if strings.TrimSpace(child.ParentSessionID) == "" {
		n.logOrphanOnce(plan.event, child.ID)
		plan.event.DeliveryResult = transitionDeliveryDropped
		plan.finalized = true
		return plan
	}

	parent := resolveParentNotificationTarget(child, byID)
	if parent == nil {
		plan.event.DeliveryResult = transitionDeliveryDropped
		plan.finalized = true
		return plan
	}

	plan.event.TargetSessionID = parent.ID
	plan.event.TargetKind = "parent"

	// Defer + enqueue when the target is busy. The daemon's lastStatus update
	// would otherwise permanently lose this transition; the queue drain on
	// the next poll picks it up once the target is free.
	_ = parent.UpdateStatus()
	if parent.GetStatusThreadSafe() == StatusRunning {
		plan.event.DeliveryResult = transitionDeliveryDeferred
		plan.finalized = true
		n.EnqueueDeferred(plan.event)
		// In-process retry-with-backoff (issue #805 cause B). The disk
		// queue + daemon poll path is the long-term retry; this is the
		// fast path that catches the common case where the parent is
		// busy for seconds, not minutes. After exhaustion the event is
		// persisted to the per-conductor inbox so the conductor's next
		// idle drain still sees it.
		n.scheduleBusyRetry(plan.event)
		return plan
	}

	plan.message = buildTransitionMessage(plan.event)
	return plan
}

// dispatchAsync runs the send in a goroutine with a per-target semaphore so
// a slow tmux pane on one target doesn't head-of-line-block others, and a
// timeout so a permanently wedged target doesn't leak a zombie waiter.
// Three terminal states land in logs:
//   - sent/failed → transition-notifier.log (existing delivery stream)
//   - timeout/busy → notifier-missed.log (new actionable stream)
func (n *TransitionNotifier) dispatchAsync(targetID, message string, event TransitionNotificationEvent) {
	slot := n.getTargetSlot(targetID)
	select {
	case slot <- struct{}{}:
		// acquired
	default:
		n.logMissed(event, "busy")
		return
	}

	doneCh := make(chan TransitionNotificationEvent, 1)

	n.sendersWG.Add(1)
	go func() {
		defer n.sendersWG.Done()
		e := event
		e.TargetSessionID = targetID
		if e.TargetKind == "" {
			e.TargetKind = "parent"
		}
		if err := n.sender(event.Profile, targetID, message); err != nil {
			e.DeliveryResult = transitionDeliveryFailed
		} else {
			e.DeliveryResult = transitionDeliverySent
		}
		doneCh <- e
		// Slot is only released once the send really returns, which prevents
		// a timeout+retry from racing a second tmux send-keys call to the
		// same pane while the first is still blocked in the kernel.
		<-slot
	}()

	timeout := n.sendTimeout
	if timeout <= 0 {
		timeout = defaultSendTimeout
	}

	n.watchersWG.Add(1)
	go func() {
		defer n.watchersWG.Done()
		select {
		case result := <-doneCh:
			n.logEvent(result)
		case <-time.After(timeout):
			n.logMissed(event, "timeout")
		}
	}()
}

func (n *TransitionNotifier) getTargetSlot(targetID string) chan struct{} {
	n.slotsMu.Lock()
	defer n.slotsMu.Unlock()
	if n.targetSlots == nil {
		n.targetSlots = map[string]chan struct{}{}
	}
	slot, ok := n.targetSlots[targetID]
	if !ok {
		slot = make(chan struct{}, 1)
		n.targetSlots[targetID] = slot
	}
	return slot
}

// waitWatchers blocks until every short-lived watcher goroutine started by
// dispatchAsync has returned. Intended for tests: production callers do not
// need it because the daemon's poll loop naturally overlaps with in-flight
// sends. Bounded by sendTimeout — sender goroutines that leak past that
// deadline are tracked separately in sendersWG.
func (n *TransitionNotifier) waitWatchers() {
	n.watchersWG.Wait()
}

// Flush waits for every pending async dispatch to resolve (sent, failed, or
// timed out) so that callers with a bounded lifetime — the `notify-daemon
// --once` CLI entry point, the graceful-shutdown path of Run, and any test
// that needs deterministic log contents — can observe the real delivery
// outcome before exiting. Bounded by sendTimeout for watchers plus any
// outstanding sender goroutines that finish within the same window.
func (n *TransitionNotifier) Flush() {
	n.watchersWG.Wait()
	n.sendersWG.Wait()
}

func buildTransitionMessage(event TransitionNotificationEvent) string {
	return fmt.Sprintf(
		"[EVENT] Child '%s' (%s) is %s.\nCheck: agent-deck -p %s session output %s -q",
		event.ChildTitle,
		event.ChildSessionID,
		event.ToStatus,
		event.Profile,
		event.ChildSessionID,
	)
}

func resolveParentNotificationTarget(child *Instance, byID map[string]*Instance) *Instance {
	if child == nil {
		return nil
	}
	parentID := strings.TrimSpace(child.ParentSessionID)
	if parentID == "" || parentID == child.ID {
		return nil
	}
	parent := byID[parentID]
	if parent == nil {
		return nil
	}
	if parent.ID == child.ID {
		return nil
	}
	if isConductorSessionTitle(parent.Title) {
		_ = parent.UpdateStatus()
		if !isLiveSessionStatus(parent.Status) {
			return nil
		}
	}
	return parent
}

func isLiveSessionStatus(status Status) bool {
	switch status {
	case StatusRunning, StatusWaiting, StatusIdle:
		return true
	default:
		return false
	}
}

func (n *TransitionNotifier) isDuplicate(event TransitionNotificationEvent) bool {
	n.mu.Lock()
	defer n.mu.Unlock()

	record, ok := n.state.Records[event.ChildSessionID]
	if !ok {
		return false
	}
	if record.From != event.FromStatus || record.To != event.ToStatus {
		return false
	}
	return event.Timestamp.Unix()-record.At <= 90
}

func (n *TransitionNotifier) markNotified(event TransitionNotificationEvent) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.state.Records == nil {
		n.state.Records = map[string]transitionNotifyRecord{}
	}
	n.state.Records[event.ChildSessionID] = transitionNotifyRecord{
		From: event.FromStatus,
		To:   event.ToStatus,
		At:   event.Timestamp.Unix(),
	}
	_ = n.saveStateLocked()
}

func (n *TransitionNotifier) loadState() {
	n.mu.Lock()
	defer n.mu.Unlock()

	data, err := os.ReadFile(n.statePath)
	if err != nil {
		return
	}
	var state transitionNotifyState
	if err := json.Unmarshal(data, &state); err != nil {
		return
	}
	if state.Records == nil {
		state.Records = map[string]transitionNotifyRecord{}
	}
	n.state = state
}

func (n *TransitionNotifier) saveStateLocked() error {
	if err := os.MkdirAll(filepath.Dir(n.statePath), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(n.state, "", "  ")
	if err != nil {
		return err
	}
	tmp := n.statePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, n.statePath)
}

func (n *TransitionNotifier) logEvent(event TransitionNotificationEvent) {
	if err := os.MkdirAll(filepath.Dir(n.logPath), 0o755); err != nil {
		return
	}
	line, err := json.Marshal(event)
	if err != nil {
		return
	}
	f, err := os.OpenFile(n.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

func (n *TransitionNotifier) logMissed(event TransitionNotificationEvent, reason string) {
	if err := os.MkdirAll(filepath.Dir(n.missedPath), 0o755); err != nil {
		return
	}
	entry := map[string]any{
		"ts":     time.Now().Format(time.RFC3339Nano),
		"target": event.TargetSessionID,
		"event":  fmt.Sprintf("%s→%s", event.FromStatus, event.ToStatus),
		"child":  event.ChildSessionID,
		"reason": reason,
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return
	}
	f, err := os.OpenFile(n.missedPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

// --- deferred retry queue ----------------------------------------------------

// EnqueueDeferred persists a deferred event so the next DrainRetryQueue pass
// can try delivery again once the target is free. Events keyed by
// (child, from, to) de-duplicate: a repeat defer for the same transition
// refreshes the event but keeps FirstDeferredAt so the age-out timer is
// honest.
func (n *TransitionNotifier) EnqueueDeferred(event TransitionNotificationEvent) {
	n.enqueueDeferredAt(event, time.Now())
}

func (n *TransitionNotifier) enqueueDeferredAt(event TransitionNotificationEvent, firstDeferredAt time.Time) {
	n.queueMu.Lock()
	defer n.queueMu.Unlock()

	key := deferredKey(event)
	for i, entry := range n.queue.Entries {
		if deferredKey(entry.Event) == key {
			n.queue.Entries[i].Event = event
			_ = n.saveQueueLocked()
			return
		}
	}
	n.queue.Entries = append(n.queue.Entries, deferredQueueEntry{
		Event:           event,
		FirstDeferredAt: firstDeferredAt,
		Attempts:        0,
	})
	_ = n.saveQueueLocked()
}

// enqueueDeferredAtForTest is a test-only hook that lets tests backdate the
// FirstDeferredAt timestamp to exercise the age-out path without sleeping.
func (n *TransitionNotifier) enqueueDeferredAtForTest(event TransitionNotificationEvent, firstDeferredAt time.Time) {
	n.enqueueDeferredAt(event, firstDeferredAt)
}

func deferredKey(event TransitionNotificationEvent) string {
	return event.ChildSessionID + "|" + event.FromStatus + "|" + event.ToStatus
}

// targetAvailabilityResolver reports whether the given target session is
// currently idle enough to accept a send. Production wires this to the
// live instance's status; tests pass a canned function.
type targetAvailabilityResolver func(profile, targetID string) bool

// DrainRetryQueue is the production entry point used by the daemon's poll
// loop. It resolves target availability by reading the live session state.
func (n *TransitionNotifier) DrainRetryQueue(profile string) {
	n.DrainRetryQueueWithResolver(profile, n.liveTargetAvailability)
}

// DrainRetryQueueWithResolver is the test seam. It walks the queue,
// dispatching entries whose target is now available and expiring entries
// older than defaultQueueMaxAge or past defaultQueueMaxAttempts.
func (n *TransitionNotifier) DrainRetryQueueWithResolver(profile string, isAvailable targetAvailabilityResolver) {
	now := time.Now()

	n.queueMu.Lock()
	snapshot := append([]deferredQueueEntry(nil), n.queue.Entries...)
	n.queue.Entries = nil
	n.queueMu.Unlock()

	var keep []deferredQueueEntry
	var toDispatch []deferredQueueEntry
	var toExpire []deferredQueueEntry

	for _, entry := range snapshot {
		if entry.Event.Profile != profile {
			keep = append(keep, entry)
			continue
		}
		expired := now.Sub(entry.FirstDeferredAt) > defaultQueueMaxAge ||
			entry.Attempts >= defaultQueueMaxAttempts
		if expired {
			toExpire = append(toExpire, entry)
			continue
		}
		if !isAvailable(profile, entry.Event.TargetSessionID) {
			keep = append(keep, entry)
			continue
		}
		entry.Attempts++
		toDispatch = append(toDispatch, entry)
	}

	for _, entry := range toExpire {
		n.logMissed(entry.Event, "expired")
	}

	n.queueMu.Lock()
	n.queue.Entries = keep
	_ = n.saveQueueLocked()
	n.queueMu.Unlock()

	for _, entry := range toDispatch {
		n.markNotified(entry.Event)
		message := buildTransitionMessage(entry.Event)
		n.dispatchAsync(entry.Event.TargetSessionID, message, entry.Event)
	}
}

func (n *TransitionNotifier) liveTargetAvailability(profile, targetID string) bool {
	if strings.TrimSpace(targetID) == "" {
		return false
	}
	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		return false
	}
	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		return false
	}
	for _, inst := range instances {
		if inst.ID != targetID {
			continue
		}
		_ = inst.UpdateStatus()
		return inst.GetStatusThreadSafe() != StatusRunning
	}
	return false
}

func (n *TransitionNotifier) snapshotQueueForTest() []deferredQueueEntry {
	n.queueMu.Lock()
	defer n.queueMu.Unlock()
	if len(n.queue.Entries) == 0 {
		// Re-read from disk so tests that reload the notifier see persisted
		// entries without having to drop in-memory state first.
		n.loadQueueLocked()
	}
	out := make([]deferredQueueEntry, len(n.queue.Entries))
	copy(out, n.queue.Entries)
	return out
}

func (n *TransitionNotifier) loadQueue() {
	n.queueMu.Lock()
	defer n.queueMu.Unlock()
	n.loadQueueLocked()
}

func (n *TransitionNotifier) loadQueueLocked() {
	data, err := os.ReadFile(n.queuePath)
	if err != nil {
		return
	}
	var q deferredQueue
	if err := json.Unmarshal(data, &q); err != nil {
		return
	}
	n.queue = q
}

func (n *TransitionNotifier) saveQueueLocked() error {
	if err := os.MkdirAll(filepath.Dir(n.queuePath), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(n.queue, "", "  ")
	if err != nil {
		return err
	}
	tmp := n.queuePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, n.queuePath)
}

// --- paths -------------------------------------------------------------------

func transitionNotifyStatePath() string {
	dir, err := GetAgentDeckDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".agent-deck", "runtime", "transition-notify-state.json")
	}
	return filepath.Join(dir, "runtime", "transition-notify-state.json")
}

func transitionNotifyLogPath() string {
	dir, err := GetAgentDeckDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".agent-deck", "logs", "transition-notifier.log")
	}
	return filepath.Join(dir, "logs", "transition-notifier.log")
}

func transitionNotifierMissedPath() string {
	dir, err := GetAgentDeckDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".agent-deck", "logs", "notifier-missed.log")
	}
	return filepath.Join(dir, "logs", "notifier-missed.log")
}

func transitionNotifierQueuePath() string {
	dir, err := GetAgentDeckDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".agent-deck", "runtime", "transition-deferred-queue.json")
	}
	return filepath.Join(dir, "runtime", "transition-deferred-queue.json")
}

func transitionNotifierOrphanLogPath() string {
	dir, err := GetAgentDeckDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".agent-deck", "logs", "notifier-orphans.log")
	}
	return filepath.Join(dir, "logs", "notifier-orphans.log")
}

// --- orphan WARN -------------------------------------------------------------

// logOrphanOnce writes a single WARN line per child id to
// notifier-orphans.log. Subsequent transitions for the same child are
// silently dropped from this stream so a long-lived orphan does not flood
// logs. The hint string is stable so operators can grep + redirect to the
// documented `agent-deck session set-parent` workflow.
func (n *TransitionNotifier) logOrphanOnce(event TransitionNotificationEvent, childID string) {
	n.orphanMu.Lock()
	if n.orphanWarned == nil {
		n.orphanWarned = map[string]bool{}
	}
	if n.orphanWarned[childID] {
		n.orphanMu.Unlock()
		return
	}
	n.orphanWarned[childID] = true
	n.orphanMu.Unlock()

	path := n.orphanPath
	if path == "" {
		path = transitionNotifierOrphanLogPath()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	entry := map[string]any{
		"ts":      time.Now().Format(time.RFC3339Nano),
		"level":   "WARN",
		"child":   childID,
		"title":   event.ChildTitle,
		"profile": event.Profile,
		"event":   fmt.Sprintf("%s→%s", event.FromStatus, event.ToStatus),
		"message": "orphan child detected; run orphan sweep: agent-deck session set-parent <child> <conductor>",
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

// --- in-process retry-with-backoff ------------------------------------------

// scheduleBusyRetry kicks off a goroutine that retries delivery on a fixed
// backoff schedule (n.busyBackoff, default {5s,15s,45s}). On each tick:
//   - check availability(profile,target); if free, send via n.sender
//   - on send success: log to delivery stream, mark dedup, drain queue entry
//   - on availability false: continue to the next backoff entry
//
// After the last entry without a successful send, persist the event to the
// per-conductor inbox and write notifier-missed.log{reason=exhausted_busy_retries}
// so the conductor's next idle drain still sees the transition.
//
// Bounded by len(busyBackoff). Cancellable via Close() — the select on
// stopCh releases pending sleeps so test cleanups don't leak retries past
// t.TempDir teardown.
func (n *TransitionNotifier) scheduleBusyRetry(event TransitionNotificationEvent) {
	delays := n.busyBackoff
	if len(delays) == 0 {
		return
	}
	stop := n.getStopCh()

	n.sendersWG.Add(1)
	go func() {
		defer n.sendersWG.Done()

		for _, d := range delays {
			select {
			case <-time.After(d):
			case <-stop:
				return
			}

			isAvail := n.availability
			if isAvail == nil {
				isAvail = n.liveTargetAvailability
			}
			if !isAvail(event.Profile, event.TargetSessionID) {
				continue
			}

			send := n.sender
			if send == nil {
				send = SendSessionMessageReliable
			}
			err := send(event.Profile, event.TargetSessionID, buildTransitionMessage(event))
			if err == nil {
				e := event
				e.DeliveryResult = transitionDeliverySent
				n.markNotified(e)
				n.logEvent(e)
				n.removeFromQueue(event)
				return
			}
			// Send failed: try the next backoff entry.
		}

		// Exhausted — persist to the parent's inbox and signal via missed log.
		if event.TargetSessionID != "" {
			_ = WriteInboxEvent(event.TargetSessionID, event)
		}
		n.logMissed(event, "exhausted_busy_retries")
		n.removeFromQueue(event)
	}()
}

func (n *TransitionNotifier) removeFromQueue(event TransitionNotificationEvent) {
	n.queueMu.Lock()
	defer n.queueMu.Unlock()
	key := deferredKey(event)
	keep := n.queue.Entries[:0]
	dropped := false
	for _, entry := range n.queue.Entries {
		if deferredKey(entry.Event) == key {
			dropped = true
			continue
		}
		keep = append(keep, entry)
	}
	if !dropped {
		return
	}
	n.queue.Entries = keep
	_ = n.saveQueueLocked()
}
