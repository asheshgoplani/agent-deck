package web

import (
	"fmt"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// MenuSessionState is a lightweight status/tool update for one session.
type MenuSessionState struct {
	Status session.Status
	Tool   string
}

// MemoryMenuData is an in-memory menu snapshot store used by web mode.
// It can optionally fall back to a loader (e.g. storage-backed) until the
// first in-memory snapshot is published.
type MemoryMenuData struct {
	mu       sync.RWMutex
	snapshot *MenuSnapshot
	fallback MenuDataLoader
}

// NewMemoryMenuData creates an in-memory menu data store.
func NewMemoryMenuData(fallback MenuDataLoader) *MemoryMenuData {
	return &MemoryMenuData{
		fallback: fallback,
	}
}

// LoadMenuSnapshot returns the latest in-memory snapshot.
// If no snapshot exists yet, it falls back once to the configured loader.
func (m *MemoryMenuData) LoadMenuSnapshot() (*MenuSnapshot, error) {
	m.mu.RLock()
	current := cloneMenuSnapshot(m.snapshot)
	m.mu.RUnlock()
	if current != nil {
		return current, nil
	}
	if m.fallback == nil {
		return nil, fmt.Errorf("menu snapshot is unavailable")
	}

	snapshot, err := m.fallback.LoadMenuSnapshot()
	if err != nil {
		return nil, err
	}
	m.SetSnapshot(snapshot)
	return cloneMenuSnapshot(snapshot), nil
}

// SetSnapshot replaces the stored menu snapshot.
func (m *MemoryMenuData) SetSnapshot(snapshot *MenuSnapshot) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.snapshot = cloneMenuSnapshot(snapshot)
	m.mu.Unlock()
}

// UpdateSessionStates updates status/tool fields in-place for existing sessions.
func (m *MemoryMenuData) UpdateSessionStates(states map[string]MenuSessionState, generatedAt time.Time) {
	if m == nil || len(states) == 0 {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.snapshot == nil {
		return
	}

	for i := range m.snapshot.Items {
		item := &m.snapshot.Items[i]
		if item.Type != MenuItemTypeSession || item.Session == nil {
			continue
		}
		state, ok := states[item.Session.ID]
		if !ok {
			continue
		}

		item.Session.Status = state.Status
		if state.Tool != "" {
			item.Session.Tool = state.Tool
		}
	}

	if generatedAt.IsZero() {
		generatedAt = time.Now()
	}
	m.snapshot.GeneratedAt = generatedAt.UTC()
}

func cloneMenuSnapshot(snapshot *MenuSnapshot) *MenuSnapshot {
	if snapshot == nil {
		return nil
	}

	cloned := *snapshot
	cloned.Items = make([]MenuItem, len(snapshot.Items))

	for i, item := range snapshot.Items {
		cloned.Items[i] = item
		if item.Group != nil {
			groupCopy := *item.Group
			cloned.Items[i].Group = &groupCopy
		}
		if item.Session != nil {
			sessionCopy := *item.Session
			cloned.Items[i].Session = &sessionCopy
		}
	}

	return &cloned
}
