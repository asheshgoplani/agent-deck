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
}

func NewTransitionDaemon() *TransitionDaemon {
	return &TransitionDaemon{
		notifier:    NewTransitionNotifier(),
		storages:    map[string]*Storage{},
		lastStatus:  map[string]map[string]string{},
		initialized: map[string]bool{},
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
	}
	return nextInterval
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
	for _, inst := range instances {
		byID[inst.ID] = inst
		if inst.Tool == "claude" || inst.Tool == "codex" {
			if hs := d.hookStatusForInstance(inst.ID); hs != nil {
				inst.UpdateHookStatus(hs)
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
		d.lastStatus[profile] = copyStatusMap(statuses)
		d.initialized[profile] = true
		return choosePollInterval(statuses)
	}

	prev := d.lastStatus[profile]
	for id, to := range statuses {
		from := normalizeStatusString(prev[id])
		if !ShouldNotifyTransition(from, to) {
			continue
		}
		inst := byID[id]
		if inst == nil {
			continue
		}
		event := TransitionNotificationEvent{
			ChildSessionID: id,
			ChildTitle:     inst.Title,
			Profile:        profile,
			FromStatus:     from,
			ToStatus:       to,
			Timestamp:      time.Now(),
		}
		_ = d.notifier.NotifyTransition(event)
	}
	d.emitHookTransitionCandidates(profile, byID, prev, statuses, hookCandidates)

	d.lastStatus[profile] = copyStatusMap(statuses)
	return choosePollInterval(statuses)
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
	for _, s := range d.storages {
		if s != nil {
			_ = s.Close()
		}
	}
	// Clear maps to release memory
	d.storages = nil
	d.lastStatus = nil
	d.initialized = nil
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
		Status    string `json:"status"`
		SessionID string `json:"session_id"`
		Event     string `json:"event"`
		Timestamp int64  `json:"ts"`
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
		Status:    raw.Status,
		SessionID: raw.SessionID,
		Event:     raw.Event,
		UpdatedAt: updatedAt,
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
	for id, candidate := range candidates {
		inst := byID[id]
		if inst == nil {
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
		}
		_ = d.notifier.NotifyTransition(event)
	}
}

func isNotifyTerminalStatus(status string) bool {
	s := normalizeStatusString(status)
	return s == string(StatusWaiting) || s == string(StatusError) || s == string(StatusIdle)
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
