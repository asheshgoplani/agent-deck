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
	transitionDeliveryFailed  = "failed"
	transitionDeliveryDropped = "dropped_no_target"
	// transitionDeliveryCommitted marks an event durably committed to the
	// per-parent outbox (issue #1225) — the pull-model replacement for the
	// old push results. The parent drains it at its own turn boundary.
	transitionDeliveryCommitted = "committed_inbox"

	// defaultOutputHashDedupTTL caps the (child, to_status, output_hash)
	// suppression window from issue #1142. A dormant child that re-emits
	// the same transition with the same pane content is silenced for the
	// TTL, then re-emits once as a liveness ping so the operator still
	// sees the child is alive. 2h matches the worst-case 20.2-min mean
	// interval observed in the 2026-05-21 self-improvement report (47
	// fires over 15.5 hours) while still giving the operator periodic
	// confirmation a child hasn't died silently.
	defaultOutputHashDedupTTL = 2 * time.Hour

	// shortWindowDedupSeconds preserves the pre-#1142 90-second
	// idempotency window. It catches duplicate polls inside one daemon
	// tick (e.g. when a hook fires the same transition that the
	// status-poll also observes). Independent of output-hash dedup so
	// callers that haven't been wired to populate LastOutputHash still
	// get the legacy guarantee.
	shortWindowDedupSeconds = 90
)

type TransitionNotificationEvent struct {
	ChildSessionID string    `json:"child_session_id"`
	ChildTitle     string    `json:"child_title"`
	Profile        string    `json:"profile"`
	FromStatus     string    `json:"from_status"`
	ToStatus       string    `json:"to_status"`
	Timestamp      time.Time `json:"timestamp"`

	// LastOutputHash is a cheap stable signal (e.g. SHA-1 of the last N
	// bytes of the child's tmux pane at transition time) used by the
	// notifier's #1142 dedup to suppress repeated [EVENT] notifications
	// for a dormant child whose pane content hasn't changed. Optional —
	// empty string disables hash-based dedup and falls back to the legacy
	// 90s short window.
	LastOutputHash string `json:"last_output_hash,omitempty"`

	TargetSessionID string `json:"target_session_id,omitempty"`
	TargetKind      string `json:"target_kind,omitempty"` // parent | conductor
	DeliveryResult  string `json:"delivery_result,omitempty"`

	// Kind distinguishes a finished event (issue #1186) from the default
	// status-transition event. Empty means the legacy transition event
	// ("[EVENT] Child is waiting"); transitionKindFinished means a
	// worker-asserted completion ("[DONE] Child finished: status=…").
	Kind string `json:"kind,omitempty"`

	// DoneStatus/DoneSummary carry the parsed completion sentinel for a
	// finished event. Unused for transition events.
	DoneStatus  string `json:"done_status,omitempty"`
	DoneSummary string `json:"done_summary,omitempty"`

	// TurnFingerprint identifies a child's completed turn for exactly-once
	// consumer effects (issue #1225). Coarser than EventFingerprint (which
	// keys on emit-instant so retries collapse): two emits of the SAME turn
	// share a TurnFingerprint, so a parent that drains the durable outbox acts
	// on the turn exactly once even across a daemon restart that re-stamps
	// Timestamp. Format: "<child_id>@<turn-signal-hash>".
	TurnFingerprint string `json:"turn_fingerprint,omitempty"`

	// Attempts counts producer commit attempts against an unresolvable target
	// before the record is moved to the dead-letter store (issue #1225). Bounds
	// the old dropped_no_target ~1/sec runaway to a terminal state.
	Attempts int `json:"attempts,omitempty"`

	// DeadLetterReason records WHY a record was terminally undeliverable (audit
	// B5): orphan, child_removed, parent_removed (incl. cross-profile),
	// no_notify, self_conductor, or unresolvable. Empty for delivered records.
	// Flows to the dead-letter record and the operator-visible missed-log line so
	// a misconfiguration is distinguishable from a benign suppression.
	DeadLetterReason string `json:"dead_letter_reason,omitempty"`
}

// transitionKindFinished marks a TransitionNotificationEvent as a worker-
// asserted task-completion signal rather than a status transition.
const transitionKindFinished = "finished"

type transitionNotifyRecord struct {
	From string `json:"from"`
	To   string `json:"to"`
	At   int64  `json:"at"`
	// OutputHash mirrors TransitionNotificationEvent.LastOutputHash at
	// the moment of the last accepted (non-deduped) emission. Used by
	// isDuplicate to suppress identical re-fires within the TTL.
	OutputHash string `json:"output_hash,omitempty"`
}

type transitionNotifyState struct {
	Records map[string]transitionNotifyRecord `json:"records"`
}

type TransitionNotifier struct {
	statePath  string
	logPath    string
	missedPath string
	orphanPath string

	mu    sync.Mutex
	state transitionNotifyState

	// orphanMu guards orphanWarned. The set tracks child session ids we have
	// already emitted a WARN for, so a long-lived orphan firing many
	// transitions does not flood notifier-orphans.log.
	orphanMu     sync.Mutex
	orphanWarned map[string]bool

	// missedMu guards missedSeen. The set tracks (fingerprint|reason) keys
	// already written to notifier-missed.log so the same exhausted event
	// firing repeatedly (issue #824) doesn't flood the log with identical
	// lines. Process-local — restart resets the dedup state, which is fine:
	// missed-log is operator signal, not durable replay.
	missedMu   sync.Mutex
	missedSeen map[string]bool

	// terminalMu guards terminalSeen, the (child|reason) set for synchronous
	// terminal drops (audit B5). Keyed per child+reason (not per event) so a
	// chatty child whose parent was removed logs ONCE per reason, not once per
	// transition — same anti-flood discipline as the orphan log.
	terminalMu   sync.Mutex
	terminalSeen map[string]bool

	// outputHashDedupTTLOverride lets tests shrink the issue #1142
	// output-hash dedup window without waiting hours of wall-clock time.
	// Zero means "use defaultOutputHashDedupTTL". Production never sets
	// it. Tests that need a deterministic boundary drive the TTL via
	// synthetic event.Timestamp values instead — this override exists
	// only for the rare suite that wants to assert TTL math directly.
	outputHashDedupTTLOverride time.Duration

	// dlSink is the bounded dead-letter sink (issue #1225) for terminally
	// undeliverable events (unresolvable parent / removed child). It replaces
	// the dropped_no_target ~1/sec runaway with a terminal state logged once.
	// Lazily initialized against missedPath via deadLetterSink().
	dlMu   sync.Mutex
	dlSink *DeadLetterSink

	// wake is the issue #1225 Tier-2 wake-nudge wiring: after a record durably
	// lands in a parent's inbox, commitEventToInbox fires a debounced, idle-only,
	// best-effort send-keys to wake that parent so it drains the MOMENT the
	// completion is committed instead of on its next ~14-min heartbeat. nil
	// disables nudging (correctness unaffected — the record still drains on the
	// next turn). Tests inject a spy wiring; production gets defaultWakeNudgeWiring.
	wake *wakeNudgeWiring
}

// deadLetterSink returns the notifier's bounded dead-letter sink, initialized
// lazily against the notifier-missed.log path.
func (n *TransitionNotifier) deadLetterSink() *DeadLetterSink {
	n.dlMu.Lock()
	defer n.dlMu.Unlock()
	if n.dlSink == nil {
		n.dlSink = NewDeadLetterSink(n.missedPath)
	}
	return n.dlSink
}

func NewTransitionNotifier() *TransitionNotifier {
	n := &TransitionNotifier{
		statePath:  transitionNotifyStatePath(),
		logPath:    transitionNotifyLogPath(),
		missedPath: transitionNotifierMissedPath(),
		orphanPath: transitionNotifierOrphanLogPath(),
		state: transitionNotifyState{
			Records: map[string]transitionNotifyRecord{},
		},
		orphanWarned: map[string]bool{},
		missedSeen:   map[string]bool{},
		terminalSeen: map[string]bool{},
		wake:         defaultWakeNudgeWiring(),
	}
	n.loadState()
	return n
}

// terminalDrop records a synchronously-determined terminal-undeliverable event
// (audit B5/B9). Intentional suppressions (no_notify, self_conductor) are silent
// and orphan is already logged once at resolve time, so those return early.
// Every other reason (child_removed, parent_removed/cross-profile, unresolvable)
// gets an operator-visible missed-log line AND a dead-letter record, deduped
// once per (child|reason) so a chatty child can't flood. This is what makes a
// dropped completion visible instead of silent.
func (n *TransitionNotifier) terminalDrop(event TransitionNotificationEvent, reason string) {
	switch reason {
	case "", deadLetterReasonNoNotify, deadLetterReasonSelfConductor, deadLetterReasonOrphan:
		return
	}
	key := strings.TrimSpace(event.ChildSessionID) + "|" + reason
	n.terminalMu.Lock()
	if n.terminalSeen == nil {
		n.terminalSeen = map[string]bool{}
	}
	if n.terminalSeen[key] {
		n.terminalMu.Unlock()
		return
	}
	n.terminalSeen[key] = true
	n.terminalMu.Unlock()

	event.DeadLetterReason = reason
	event.Attempts = MaxUnresolvedAttempts // terminal: not a transient retry
	n.logMissed(event, reason)
	_ = writeDeadLetter(event)
}

// Close is retained for API compatibility with callers that defer cleanup of a
// notifier. Issue #1225 made delivery a synchronous commit to the durable
// outbox, so there are no background goroutines to cancel; Close is a no-op.
// Idempotent.
func (n *TransitionNotifier) Close() {}

// Flush is retained for API compatibility with the bounded-lifetime callers
// (`notify-daemon --once`, graceful shutdown, deterministic tests). Issue #1225
// made delivery a synchronous commit to the durable outbox, so there is nothing
// in flight to wait for; Flush is a no-op.
func (n *TransitionNotifier) Flush() {}

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

// instanceAcceptsTransitionEvents is the centralized per-session predicate used
// at NEW-emission (transition_daemon.go) to decide whether a session is
// currently accepting transition events. All "is this session currently
// accepting transition events?" logic lives here so future per-session bypass
// conditions (paused, stopped, etc.) extend this predicate, not its callers.
func instanceAcceptsTransitionEvents(inst *Instance) bool {
	if inst == nil {
		return false
	}
	if inst.NoTransitionNotify {
		return false
	}
	return true
}

// NotifyTransition validates the event and commits it to the parent's durable
// outbox (issue #1225: PULL, not push). A busy conductor drains it at its own
// turn boundary, so delivery no longer depends on an idle window that never
// opens. Synchronous returns: committed_inbox / dropped_no_target / failed.
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

	// Issue #1225: commit the transition to the parent's durable outbox instead
	// of gating delivery on the parent being idle.
	committed, transient, reason := n.commitEventToInbox(event)
	if committed {
		n.markNotified(event)
		event.DeliveryResult = transitionDeliveryCommitted
		return event
	}
	if transient {
		// Storage/fs hiccup — leave undelivered; the next poll re-observes the
		// transition (lastStatus is only advanced after this returns) and the
		// daemon's completion replay covers the one-shot path.
		event.DeliveryResult = transitionDeliveryFailed
		return event
	}
	// Terminally undeliverable: surface WHY (audit B5) — child_removed,
	// parent_removed/cross-profile, etc. get an operator-visible log + dead-letter
	// once; orphan/no_notify/self_conductor are handled silently/at resolve time.
	n.terminalDrop(event, reason)
	event.DeliveryResult = transitionDeliveryDropped
	event.DeadLetterReason = reason
	return event
}

// NotifyFinished commits a worker-asserted completion event (issue #1186) to
// the child's parent's durable outbox. Unlike NotifyTransition it is not gated
// by ShouldNotifyTransition (a finished event has no from→to transition);
// per-task idempotency is the daemon's responsibility (it only calls this when
// the detected sentinel actually changed).
func (n *TransitionNotifier) NotifyFinished(event TransitionNotificationEvent) TransitionNotificationEvent {
	event.Kind = transitionKindFinished
	event.Profile = strings.TrimSpace(event.Profile)
	event.ChildTitle = strings.TrimSpace(event.ChildTitle)
	event.ChildSessionID = strings.TrimSpace(event.ChildSessionID)
	event.DoneStatus = strings.ToLower(strings.TrimSpace(event.DoneStatus))
	event.DoneSummary = strings.TrimSpace(event.DoneSummary)
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}

	if event.ChildSessionID == "" || event.Profile == "" {
		event.DeliveryResult = transitionDeliveryDropped
		return event
	}
	if isConductorSessionTitle(event.ChildTitle) {
		event.DeliveryResult = transitionDeliveryDropped
		return event
	}

	// Issue #1225: commit the finished event to the parent's durable outbox.
	committed, transient, reason := n.commitEventToInbox(event)
	if committed {
		event.DeliveryResult = transitionDeliveryCommitted
		return event
	}
	if transient {
		event.DeliveryResult = transitionDeliveryFailed
		return event
	}
	n.terminalDrop(event, reason)
	event.DeliveryResult = transitionDeliveryDropped
	event.DeadLetterReason = reason
	return event
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

// isDuplicate reports whether the event should be suppressed because the
// parent has already seen an equivalent [EVENT] line. Two layered checks:
//
//  1. Short-window (legacy): identical (from→to) within shortWindowDedupSeconds.
//     Catches duplicate polls inside one daemon tick and back-compat callers
//     that don't populate LastOutputHash.
//
//  2. Output-hash (issue #1142): identical to_status AND identical
//     LastOutputHash within outputHashDedupTTL. Suppresses a dormant child
//     re-emitting the same transition with no new pane content. After the
//     TTL elapses, the event re-emits once as a liveness ping and the
//     stored record resets via markNotified.
//
// Either layer matching is enough to dedup. From-status is intentionally
// ignored in layer 2 since ShouldNotifyTransition already pins from=running.
func (n *TransitionNotifier) isDuplicate(event TransitionNotificationEvent) bool {
	n.mu.Lock()
	defer n.mu.Unlock()

	record, ok := n.state.Records[event.ChildSessionID]
	if !ok {
		return false
	}

	elapsed := event.Timestamp.Unix() - record.At

	if record.From == event.FromStatus && record.To == event.ToStatus && elapsed <= shortWindowDedupSeconds {
		return true
	}

	if event.LastOutputHash != "" &&
		record.To == event.ToStatus &&
		record.OutputHash == event.LastOutputHash &&
		elapsed <= int64(n.outputHashTTL().Seconds()) {
		return true
	}

	return false
}

// outputHashTTL returns the active TTL for the output-hash dedup layer. The
// override field is reserved for tests; production callers get the default.
func (n *TransitionNotifier) outputHashTTL() time.Duration {
	if n.outputHashDedupTTLOverride > 0 {
		return n.outputHashDedupTTLOverride
	}
	return defaultOutputHashDedupTTL
}

// transitionEventOutputHash derives the stable content signal used by the
// issue #1142 output-hash dedup. The key must be IDENTICAL across polls while
// the child's logical state is unchanged, and MUST change on a genuine new
// turn.
//
// issue #1187: the previous implementation keyed on
// inst.GetLastActivityTime().UnixNano(), but that timestamp is re-stamped to
// time.Now() on every tmux window_activity tick (tmux.go:2956/3102/3303). A
// live Claude pane sitting at the prompt animates its footer/token-counter/
// cursor/hint lines, so window_activity bumped every poll → the key moved
// every poll → layer-2 dedup could never match → the same [EVENT] re-fired
// 10-40x. The signal was clock-derived, structurally incapable of matching a
// live pane.
//
// The fix derives the key from session CONTENT (the transcript), which the
// animated chrome never touches. See transitionContentSignal. Empty string for
// nil/missing/non-transcript tools — falls back to the legacy 90s dedup window,
// exactly as before.
func transitionEventOutputHash(inst *Instance) string {
	if inst == nil {
		return ""
	}
	return transitionContentSignal(inst)
}

// transitionContentSignal returns a dedup signal derived from the child's
// transcript size. A Claude-compatible JSONL transcript is append-only and
// grows ONLY when a real message is written (user prompt, assistant turn, tool
// call) — it is completely untouched when the pane merely redraws its animated
// chrome. So the signal stays identical across idle polls and strictly changes
// on a genuine new turn. Returns "" when no transcript is resolvable (e.g.
// non-Claude tools), which routes the caller to the legacy 90s window.
func transitionContentSignal(inst *Instance) string {
	path := inst.GetJSONLPath()
	if path == "" {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("jsonl:%d", info.Size())
}

func (n *TransitionNotifier) markNotified(event TransitionNotificationEvent) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.state.Records == nil {
		n.state.Records = map[string]transitionNotifyRecord{}
	}
	n.state.Records[event.ChildSessionID] = transitionNotifyRecord{
		From:       event.FromStatus,
		To:         event.ToStatus,
		At:         event.Timestamp.Unix(),
		OutputHash: event.LastOutputHash,
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
	// Dedup by (fingerprint|reason) so repeated exhaust calls for the same
	// logical event don't flood the log. A different reason for the same
	// event still records — operators want to see e.g. timeout AND expired
	// for the same transition, but not seven exhaust lines in a row.
	key := EventFingerprint(event) + "|" + reason
	n.missedMu.Lock()
	if n.missedSeen == nil {
		n.missedSeen = map[string]bool{}
	}
	if n.missedSeen[key] {
		n.missedMu.Unlock()
		return
	}
	n.missedSeen[key] = true
	n.missedMu.Unlock()

	if err := os.MkdirAll(filepath.Dir(n.missedPath), 0o755); err != nil {
		return
	}
	entry := map[string]any{
		"ts":     time.Now().Format(time.RFC3339Nano),
		"target": event.TargetSessionID,
		"event":  fmt.Sprintf("%s→%s", event.FromStatus, event.ToStatus),
		"child":  event.ChildSessionID,
		"reason": reason,
		"fp":     EventFingerprint(event),
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
