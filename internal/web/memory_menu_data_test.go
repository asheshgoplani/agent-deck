package web

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

type staticMenuLoader struct {
	calls    int
	snapshot *MenuSnapshot
}

func (s *staticMenuLoader) LoadMenuSnapshot() (*MenuSnapshot, error) {
	s.calls++
	return s.snapshot, nil
}

func TestMemoryMenuData_LoadMenuSnapshotFallbackAndCache(t *testing.T) {
	loader := &staticMenuLoader{
		snapshot: &MenuSnapshot{
			Profile:       "default",
			GeneratedAt:   time.Now().UTC(),
			TotalGroups:   1,
			TotalSessions: 1,
			Items: []MenuItem{
				{
					Type: MenuItemTypeSession,
					Session: &MenuSession{
						ID:     "sess-1",
						Title:  "Session 1",
						Status: session.StatusIdle,
					},
				},
			},
		},
	}
	store := NewMemoryMenuData(loader)

	first, err := store.LoadMenuSnapshot()
	if err != nil {
		t.Fatalf("first LoadMenuSnapshot() error = %v", err)
	}
	if loader.calls != 1 {
		t.Fatalf("fallback loader calls = %d, want 1", loader.calls)
	}

	// Mutating the returned snapshot must not mutate internal store state.
	first.Items[0].Session.Title = "mutated"

	second, err := store.LoadMenuSnapshot()
	if err != nil {
		t.Fatalf("second LoadMenuSnapshot() error = %v", err)
	}
	if loader.calls != 1 {
		t.Fatalf("fallback loader calls after cache = %d, want 1", loader.calls)
	}
	if got := second.Items[0].Session.Title; got != "Session 1" {
		t.Fatalf("cached snapshot title = %q, want %q", got, "Session 1")
	}
}

func TestMemoryMenuData_InvalidateCacheForcesReload(t *testing.T) {
	loader := &staticMenuLoader{
		snapshot: &MenuSnapshot{
			Profile:       "default",
			GeneratedAt:   time.Now().UTC(),
			TotalGroups:   1,
			TotalSessions: 1,
			Items: []MenuItem{
				{
					Type: MenuItemTypeSession,
					Session: &MenuSession{
						ID: "sess-1", Title: "Original",
					},
				},
			},
		},
	}
	store := NewMemoryMenuData(loader)

	// First load populates the cache from the fallback loader.
	first, err := store.LoadMenuSnapshot()
	if err != nil {
		t.Fatalf("first LoadMenuSnapshot() error = %v", err)
	}
	if loader.calls != 1 {
		t.Fatalf("fallback calls after first load = %d, want 1", loader.calls)
	}

	// Second load returns the cached snapshot without calling the fallback.
	_, err = store.LoadMenuSnapshot()
	if err != nil {
		t.Fatalf("second LoadMenuSnapshot() error = %v", err)
	}
	if loader.calls != 1 {
		t.Fatalf("cached load triggered fallback: calls = %d, want 1", loader.calls)
	}

	// Verify the first snapshot content is correct
	if got := first.Items[0].Session.Title; got != "Original" {
		t.Fatalf("first load title = %q, want %q", got, "Original")
	}

	// Mutate the fallback data to simulate a storage-side change.
	loader.snapshot.Items[0].Session.Title = "Updated"

	// Invalidate the cache — next LoadMenuSnapshot must go back to fallback.
	store.InvalidateCache()

	// Third load must call the fallback and get the updated title.
	third, err := store.LoadMenuSnapshot()
	if err != nil {
		t.Fatalf("third LoadMenuSnapshot() error = %v", err)
	}
	if loader.calls != 2 {
		t.Fatalf("fallback calls after invalidate = %d, want 2", loader.calls)
	}
	if got := third.Items[0].Session.Title; got != "Updated" {
		t.Fatalf("after invalidation title = %q, want %q", got, "Updated")
	}
}

func TestMemoryMenuData_UpdateSessionStates(t *testing.T) {
	store := NewMemoryMenuData(nil)
	store.SetSnapshot(&MenuSnapshot{
		Profile:       "default",
		GeneratedAt:   time.Now().UTC(),
		TotalGroups:   1,
		TotalSessions: 1,
		Items: []MenuItem{
			{
				Type: MenuItemTypeSession,
				Session: &MenuSession{
					ID:     "sess-2",
					Tool:   "claude",
					Status: session.StatusIdle,
				},
			},
		},
	})

	ts := time.Date(2026, 2, 16, 12, 0, 0, 0, time.UTC)
	store.UpdateSessionStates(map[string]MenuSessionState{
		"sess-2": {
			Status: session.StatusWaiting,
			Tool:   "codex",
		},
	}, ts)

	snapshot, err := store.LoadMenuSnapshot()
	if err != nil {
		t.Fatalf("LoadMenuSnapshot() error = %v", err)
	}
	if got := snapshot.Items[0].Session.Status; got != session.StatusWaiting {
		t.Fatalf("session status = %q, want %q", got, session.StatusWaiting)
	}
	if got := snapshot.Items[0].Session.Tool; got != "codex" {
		t.Fatalf("session tool = %q, want %q", got, "codex")
	}
	if !snapshot.GeneratedAt.Equal(ts) {
		t.Fatalf("generatedAt = %s, want %s", snapshot.GeneratedAt, ts)
	}
}
