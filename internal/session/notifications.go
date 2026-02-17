package session

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// NotificationEntry represents a waiting session in the notification bar
type NotificationEntry struct {
	SessionID    string
	TmuxName     string
	Title        string
	AssignedKey  string
	WaitingSince time.Time
	Status       Status // For icon rendering when show_all enabled
}

// NotificationManager tracks waiting sessions for the notification bar
type NotificationManager struct {
	entries  []*NotificationEntry // Ordered: newest first
	maxShown int
	showAll  bool // Show all sessions vs only waiting
	mu       sync.RWMutex
}

// NewNotificationManager creates a new notification manager
func NewNotificationManager(maxShown int, showAll bool) *NotificationManager {
	if maxShown <= 0 {
		maxShown = 6
	}
	return &NotificationManager{
		entries:  make([]*NotificationEntry, 0),
		maxShown: maxShown,
		showAll:  showAll,
	}
}

// Add registers a session as waiting (newest goes to position [0])
func (nm *NotificationManager) Add(inst *Instance) error {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	// Already tracked?
	for _, e := range nm.entries {
		if e.SessionID == inst.ID {
			return nil
		}
	}

	// Create entry
	tmuxName := ""
	if ts := inst.GetTmuxSession(); ts != nil {
		tmuxName = ts.Name
	}
	entry := &NotificationEntry{
		SessionID:    inst.ID,
		TmuxName:     tmuxName,
		Title:        inst.Title,
		WaitingSince: time.Now(),
	}

	// Prepend (newest first)
	nm.entries = append([]*NotificationEntry{entry}, nm.entries...)

	// Trim to max
	if len(nm.entries) > nm.maxShown {
		nm.entries = nm.entries[:nm.maxShown]
	}

	// Reassign keys (1, 2, 3, ...)
	nm.reassignKeys()

	return nil
}

// Remove removes a session from notifications
func (nm *NotificationManager) Remove(sessionID string) {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	for i, e := range nm.entries {
		if e.SessionID == sessionID {
			nm.entries = append(nm.entries[:i], nm.entries[i+1:]...)
			break
		}
	}

	// Reassign keys
	nm.reassignKeys()
}

// reassignKeys assigns keys 1-6 based on position
func (nm *NotificationManager) reassignKeys() {
	for i, e := range nm.entries {
		e.AssignedKey = fmt.Sprintf("%d", i+1)
	}
}

// GetEntries returns a copy of current entries (newest first)
func (nm *NotificationManager) GetEntries() []*NotificationEntry {
	nm.mu.RLock()
	defer nm.mu.RUnlock()

	result := make([]*NotificationEntry, len(nm.entries))
	copy(result, nm.entries)
	return result
}

// GetSessionByKey returns the entry for a given key (1-6)
func (nm *NotificationManager) GetSessionByKey(key string) *NotificationEntry {
	nm.mu.RLock()
	defer nm.mu.RUnlock()

	for _, e := range nm.entries {
		if e.AssignedKey == key {
			return e
		}
	}
	return nil
}

// Count returns number of notifications
func (nm *NotificationManager) Count() int {
	nm.mu.RLock()
	defer nm.mu.RUnlock()
	return len(nm.entries)
}

// Clear removes all notifications
func (nm *NotificationManager) Clear() {
	nm.mu.Lock()
	defer nm.mu.Unlock()
	nm.entries = make([]*NotificationEntry, 0)
}

// Has checks if a session is in notifications
func (nm *NotificationManager) Has(sessionID string) bool {
	nm.mu.RLock()
	defer nm.mu.RUnlock()

	for _, e := range nm.entries {
		if e.SessionID == sessionID {
			return true
		}
	}
	return false
}

// FormatBar returns the formatted status bar text
func (nm *NotificationManager) FormatBar() string {
	nm.mu.RLock()
	defer nm.mu.RUnlock()

	if len(nm.entries) == 0 {
		return ""
	}

	var parts []string
	for _, e := range nm.entries {
		var formatted string
		if nm.showAll {
			// Show status icon when in show_all mode
			icon := statusIcon(e.Status)
			formatted = fmt.Sprintf("[%s] %s %s", e.AssignedKey, icon, e.Title)
		} else {
			// Original format without icons (backward compatible)
			formatted = fmt.Sprintf("[%s] %s", e.AssignedKey, e.Title)
		}
		parts = append(parts, formatted)
	}

	return "⚡ " + strings.Join(parts, " ")
}

// statusIcon returns the Unicode icon for a given session status
func statusIcon(status Status) string {
	switch status {
	case StatusRunning:
		return "●"
	case StatusWaiting:
		return "◐"
	case StatusIdle:
		return "○"
	case StatusError:
		return "✕"
	default:
		return "○"
	}
}

// SyncFromInstances updates notifications based on current instance states
// Call this periodically to sync with actual session statuses
func (nm *NotificationManager) SyncFromInstances(instances []*Instance, currentSessionID string) (added, removed []string) {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	// Build set of sessions to show (based on showAll mode)
	var sessionSet map[string]*Instance
	if nm.showAll {
		// Show all sessions (excluding current)
		sessionSet = make(map[string]*Instance)
		for _, inst := range instances {
			if inst.ID != currentSessionID {
				sessionSet[inst.ID] = inst
			}
		}
	} else {
		// Show only waiting sessions (backward compatible)
		sessionSet = make(map[string]*Instance)
		for _, inst := range instances {
			if inst.GetStatusThreadSafe() == StatusWaiting && inst.ID != currentSessionID {
				sessionSet[inst.ID] = inst
			}
		}
	}

	// Remove entries that are no longer in the session set
	newEntries := make([]*NotificationEntry, 0)
	for _, e := range nm.entries {
		if inst, stillPresent := sessionSet[e.SessionID]; stillPresent {
			// Update status for existing entries
			e.Status = inst.GetStatusThreadSafe()
			newEntries = append(newEntries, e)
			delete(sessionSet, e.SessionID) // Don't re-add
		} else {
			removed = append(removed, e.SessionID)
		}
	}
	nm.entries = newEntries

	// Add new sessions to entries
	for _, inst := range sessionSet {
		tmuxName := ""
		if ts := inst.GetTmuxSession(); ts != nil {
			tmuxName = ts.Name
		}
		entry := &NotificationEntry{
			SessionID:    inst.ID,
			TmuxName:     tmuxName,
			Title:        inst.Title,
			WaitingSince: inst.GetWaitingSince(),
			Status:       inst.GetStatusThreadSafe(),
		}
		nm.entries = append(nm.entries, entry)
		added = append(added, inst.ID)
	}

	// Sort ALL entries by WaitingSince (newest first)
	// This ensures correct ordering regardless of how entries were added
	sort.Slice(nm.entries, func(i, j int) bool {
		return nm.entries[i].WaitingSince.After(nm.entries[j].WaitingSince)
	})

	// Trim to maxShown (keeps the newest sessions)
	if len(nm.entries) > nm.maxShown {
		nm.entries = nm.entries[:nm.maxShown]
	}

	// Reassign keys
	nm.reassignKeys()

	return added, removed
}
