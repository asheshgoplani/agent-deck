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

// MemoryMenuData stores one menu snapshot for web mode.
// SetSnapshot gives snapshot ownership to the in-process publisher.
// A fallback-owned snapshot uses a storage revision to find external changes.
type MemoryMenuData struct {
	// mu guards the snapshot and all fields that describe its storage revision.
	// A fallback load holds this mutex to give all callers one complete snapshot.
	mu                   sync.Mutex
	snapshot             *MenuSnapshot
	fallback             MenuDataLoader
	snapshotFromFallback bool
	fallbackRevision     int64
	lastRevisionCheck    time.Time
	lastFallbackError    error
}

const (
	memoryMenuRevisionCheckInterval  = time.Second
	memoryMenuConsistentLoadAttempts = 3
)

// menuDataRevisionLoader gives the revision of persisted menu data.
// Each persisted menu change must update this revision.
type menuDataRevisionLoader interface {
	MenuDataRevision() (int64, error)
}

// NewMemoryMenuData creates an in-memory menu data store.
func NewMemoryMenuData(fallback MenuDataLoader) *MemoryMenuData {
	return &MemoryMenuData{
		fallback: fallback,
	}
}

// LoadMenuSnapshot returns the latest complete snapshot.
// It rate-limits storage checks for fallback-owned snapshots.
func (m *MemoryMenuData) LoadMenuSnapshot() (snapshot *MenuSnapshot, err error) {
	if m == nil {
		return nil, fmt.Errorf("menu snapshot is unavailable")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Coalesce failed reads as well as successful revision checks. Keep the
	// error visible during the retry interval instead of serving stale data
	// as a successful refresh. Explicit invalidation or publishing resets it.
	if m.lastFallbackError != nil && time.Since(m.lastRevisionCheck) < memoryMenuRevisionCheckInterval {
		return nil, m.lastFallbackError
	}
	defer func() {
		m.lastFallbackError = err
		if err != nil {
			m.lastRevisionCheck = time.Now()
		}
	}()

	if m.snapshot == nil {
		return m.loadFallbackSnapshotLocked()
	}
	if !m.snapshotFromFallback {
		return cloneMenuSnapshot(m.snapshot), nil
	}

	revisionLoader, ok := m.fallback.(menuDataRevisionLoader)
	if !ok || time.Since(m.lastRevisionCheck) < memoryMenuRevisionCheckInterval {
		return cloneMenuSnapshot(m.snapshot), nil
	}

	revision, err := revisionLoader.MenuDataRevision()
	if err != nil {
		return nil, fmt.Errorf("load menu data revision: %w", err)
	}
	m.lastRevisionCheck = time.Now()
	if revision == m.fallbackRevision {
		return cloneMenuSnapshot(m.snapshot), nil
	}

	return m.loadFallbackSnapshotLocked()
}

// loadFallbackSnapshotLocked loads one snapshot with a stable storage revision.
// The caller must hold m.mu.
func (m *MemoryMenuData) loadFallbackSnapshotLocked() (*MenuSnapshot, error) {
	if m.fallback == nil {
		return nil, fmt.Errorf("menu snapshot is unavailable")
	}

	revisionLoader, versioned := m.fallback.(menuDataRevisionLoader)
	if !versioned {
		snapshot, err := m.fallback.LoadMenuSnapshot()
		if err != nil {
			return nil, err
		}
		m.setFallbackSnapshotLocked(snapshot, 0)
		return cloneMenuSnapshot(m.snapshot), nil
	}

	revision, err := revisionLoader.MenuDataRevision()
	if err != nil {
		return nil, fmt.Errorf("load menu data revision: %w", err)
	}
	for attempt := 0; attempt < memoryMenuConsistentLoadAttempts; attempt++ {
		snapshot, err := m.fallback.LoadMenuSnapshot()
		if err != nil {
			return nil, err
		}
		after, err := revisionLoader.MenuDataRevision()
		if err != nil {
			return nil, fmt.Errorf("load menu data revision: %w", err)
		}
		if revision == after {
			m.setFallbackSnapshotLocked(snapshot, after)
			return cloneMenuSnapshot(m.snapshot), nil
		}
		revision = after
	}

	return nil, fmt.Errorf("menu data changed during %d consecutive snapshot loads", memoryMenuConsistentLoadAttempts)
}

func (m *MemoryMenuData) setFallbackSnapshotLocked(snapshot *MenuSnapshot, revision int64) {
	m.snapshot = cloneMenuSnapshot(snapshot)
	m.snapshotFromFallback = true
	m.fallbackRevision = revision
	m.lastRevisionCheck = time.Now()
}

// LoadArchivedMenuSnapshot returns archived sessions from the storage fallback.
func (m *MemoryMenuData) LoadArchivedMenuSnapshot() (*MenuSnapshot, error) {
	if m == nil || m.fallback == nil {
		return nil, fmt.Errorf("menu snapshot is unavailable")
	}
	if loader, ok := m.fallback.(interface {
		LoadArchivedMenuSnapshot() (*MenuSnapshot, error)
	}); ok {
		return loader.LoadArchivedMenuSnapshot()
	}
	return nil, fmt.Errorf("archived session list is not available")
}

// InvalidateCache clears the cached snapshot so the next call to
// LoadMenuSnapshot reloads from the fallback storage-backed loader.
// Used after mutations in headless (--no-tui) mode to ensure the menu
// reflects the current persisted state on the next fetch.
func (m *MemoryMenuData) InvalidateCache() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.snapshot = nil
	m.snapshotFromFallback = false
	m.fallbackRevision = 0
	m.lastRevisionCheck = time.Time{}
	m.lastFallbackError = nil
	m.mu.Unlock()
}

// SetSnapshot replaces the stored menu snapshot.
func (m *MemoryMenuData) SetSnapshot(snapshot *MenuSnapshot) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.snapshot = cloneMenuSnapshot(snapshot)
	m.snapshotFromFallback = false
	m.fallbackRevision = 0
	m.lastRevisionCheck = time.Time{}
	m.lastFallbackError = nil
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
