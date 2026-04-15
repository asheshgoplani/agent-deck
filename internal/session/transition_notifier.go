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
	transitionDeliverySent     = "sent"
	transitionDeliveryFailed   = "failed"
	transitionDeliveryDropped  = "dropped_no_target"
	transitionDeliveryDeferred = "deferred_target_busy"
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

type TransitionNotifier struct {
	statePath string
	logPath   string

	mu    sync.Mutex
	state transitionNotifyState
}

func NewTransitionNotifier() *TransitionNotifier {
	statePath := transitionNotifyStatePath()
	logPath := transitionNotifyLogPath()

	n := &TransitionNotifier{
		statePath: statePath,
		logPath:   logPath,
		state: transitionNotifyState{
			Records: map[string]transitionNotifyRecord{},
		},
	}
	n.loadState()
	return n
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
		// Avoid notification loops from conductor orchestration sessions.
		event.DeliveryResult = transitionDeliveryDropped
		return event
	}
	if n.isDuplicate(event) {
		event.DeliveryResult = transitionDeliveryDropped
		return event
	}

	result := n.dispatch(event)
	// Don't mark deferred events as notified so the next poll cycle retries delivery.
	if result.DeliveryResult != transitionDeliveryDeferred {
		n.markNotified(result)
	}
	n.logEvent(result)
	return result
}

func (n *TransitionNotifier) dispatch(event TransitionNotificationEvent) TransitionNotificationEvent {
	storage, err := NewStorageWithProfile(event.Profile)
	if err != nil {
		event.DeliveryResult = transitionDeliveryFailed
		return event
	}
	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		event.DeliveryResult = transitionDeliveryFailed
		return event
	}

	byID := make(map[string]*Instance, len(instances))
	for _, inst := range instances {
		byID[inst.ID] = inst
	}

	child := byID[event.ChildSessionID]
	if child == nil {
		event.DeliveryResult = transitionDeliveryDropped
		return event
	}

	if child.NoTransitionNotify {
		event.DeliveryResult = transitionDeliveryDropped
		return event
	}

	parent := resolveParentNotificationTarget(child, byID)
	if parent == nil {
		event.DeliveryResult = transitionDeliveryDropped
		return event
	}

	// Defer delivery when the target session is busy (running) to avoid
	// interrupting its in-progress response. The event will be retried on
	// the next daemon poll cycle since deferred events are not marked as notified.
	_ = parent.UpdateStatus()
	if parent.GetStatusThreadSafe() == StatusRunning {
		event.TargetSessionID = parent.ID
		event.TargetKind = "parent"
		event.DeliveryResult = transitionDeliveryDeferred
		return event
	}

	if err := SendSessionMessageReliable(event.Profile, parent.ID, buildTransitionMessage(event)); err != nil {
		event.TargetSessionID = parent.ID
		event.TargetKind = "parent"
		event.DeliveryResult = transitionDeliveryFailed
		return event
	}

	event.TargetSessionID = parent.ID
	event.TargetKind = "parent"
	event.DeliveryResult = transitionDeliverySent
	return event
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
	// Deduplicate identical transition replays for a short window.
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
