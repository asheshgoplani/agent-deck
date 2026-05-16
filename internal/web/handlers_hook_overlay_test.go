package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Phase 1 v1.9 regression coverage for issue #867 (joeblubaugh's "TUI says
// running, web says error" divergence). The fix introduced
// refreshSnapshotHookStatuses + Server.hookStatusLoader and wired it into
// THREE GET handlers: handleSessionsCollection, handleMenu, handleSessionByID.
//
// Existing coverage: TestParity_WaitingStatusFlowsThroughHandler hits
// /api/sessions only. The other two call sites at handlers_menu.go:41 and
// handlers_menu.go:72 are wired but have no end-to-end assertion, so a
// future refactor that drops the call from one handler but not the other
// would slip — exactly the failure mode that #867 was filed for in the
// first place (web /api/sessions disagrees with /api/menu).
//
// Cases:
//   web-001: handleMenu (GET /api/menu) lifts stale-error → waiting.
//   web-002: handleSessionByID (GET /api/session/{id}) lifts stale-error → waiting.
//   web-003: refreshSnapshotHookStatuses defensive shape (nil snapshot,
//            nil session items, mixed group + session items, empty loader).
//   status-stop-001 (separate file, see status_stop_handler_test.go).

// web-001: handleMenu — fresh waiting hook overlay must lift the
// snapshot-stale error to waiting. End-to-end through the handler so the
// hookStatusLoader wiring at handlers_menu.go:41 is exercised.
func TestParity_HandleMenu_AppliesHookOverlay(t *testing.T) {
	t.Parallel()
	fx := newParityFixture()
	installLoaderOnServer(fx.server, map[string]*session.HookStatus{
		"sess-001": {
			Status:    "waiting",
			Event:     "Stop",
			UpdatedAt: time.Now().Add(-30 * time.Second),
		},
	})
	// sess-001 was seeded as Idle by parityStore.seed; force it to Error to
	// reproduce the divergence that #867 produced in production.
	fx.store.mu.Lock()
	fx.store.sessions["sess-001"].Status = session.StatusError
	fx.store.mu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/api/menu", nil)
	w := httptest.NewRecorder()
	fx.server.handleMenu(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/menu: status=%d body=%s", w.Code, w.Body.String())
	}

	var snap MenuSnapshot
	if err := json.NewDecoder(w.Body).Decode(&snap); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := findSessionByID(&snap, "sess-001")
	if got == nil {
		t.Fatalf("sess-001 missing from /api/menu response")
	}
	if got.Status != session.StatusWaiting {
		t.Fatalf("/api/menu must apply hook overlay for sess-001: want %q got %q "+
			"(handlers_menu.go:41 wiring regression)", session.StatusWaiting, got.Status)
	}
}

// web-002: handleSessionByID — same lift for the per-session endpoint so a
// link copied from the TUI to the web UI never shows "error" while the
// TUI shows "waiting".
func TestParity_HandleSessionByID_AppliesHookOverlay(t *testing.T) {
	t.Parallel()
	fx := newParityFixture()
	installLoaderOnServer(fx.server, map[string]*session.HookStatus{
		"sess-002": {
			Status:    "waiting",
			Event:     "Stop",
			UpdatedAt: time.Now().Add(-15 * time.Second),
		},
	})
	fx.store.mu.Lock()
	fx.store.sessions["sess-002"].Status = session.StatusError
	fx.store.mu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/api/session/sess-002", nil)
	w := httptest.NewRecorder()
	fx.server.handleSessionByID(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/session/sess-002: status=%d body=%s", w.Code, w.Body.String())
	}

	var resp sessionDetailsResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Session == nil {
		t.Fatalf("response.session is nil")
	}
	if resp.Session.Status != session.StatusWaiting {
		t.Fatalf("/api/session/{id} must apply hook overlay: want %q got %q "+
			"(handlers_menu.go:72 wiring regression)", session.StatusWaiting, resp.Session.Status)
	}
}

// web-003: refreshSnapshotHookStatuses defensive shape.
//
// The helper has guards for nil snapshot and nil loader (snapshot_hook_refresh.go:42)
// plus per-item nil-Session and group-Type filters (line 52). All three paths
// matter in production because LoadMenuSnapshot interleaves MenuItemTypeGroup
// and MenuItemTypeSession entries — a regression that drops any guard would
// crash the handler for anyone with a group-only profile or for the
// near-empty-cache early-boot window.
func TestRefreshSnapshotHookStatuses_DefensiveShape(t *testing.T) {
	t.Parallel()
	loader := makeFakeLoader(map[string]*session.HookStatus{
		"sess-A": {Status: "waiting", UpdatedAt: time.Now().Add(-10 * time.Second)},
	})

	t.Run("nil snapshot is a no-op", func(t *testing.T) {
		// Must not panic. Asserts the snapshot==nil guard at line 42.
		refreshSnapshotHookStatuses(nil, loader)
	})

	t.Run("nil loader is a no-op", func(t *testing.T) {
		snap := snapshotWithSession("sess-A", "claude", session.StatusError)
		refreshSnapshotHookStatuses(snap, nil)
		if got := snap.Items[0].Session.Status; got != session.StatusError {
			t.Fatalf("nil loader must leave snapshot untouched: got %q", got)
		}
	})

	t.Run("group items are skipped", func(t *testing.T) {
		snap := &MenuSnapshot{
			Profile:     "test",
			GeneratedAt: time.Now(),
			Items: []MenuItem{
				{Index: 0, Type: MenuItemTypeGroup, Path: "work", Group: &MenuGroup{Path: "work"}},
				{Index: 1, Type: MenuItemTypeSession, Session: &MenuSession{
					ID: "sess-A", Tool: "claude", Status: session.StatusError,
				}},
			},
		}
		// Must not panic on the group-typed item that has Session==nil.
		refreshSnapshotHookStatuses(snap, loader)
		if snap.Items[1].Session.Status != session.StatusWaiting {
			t.Fatalf("session item should still be lifted, group item not crashed")
		}
		if snap.Items[0].Group == nil {
			t.Fatalf("group item must not be mutated")
		}
	})

	t.Run("session-typed item with nil Session pointer", func(t *testing.T) {
		// Guard at snapshot_hook_refresh.go:52: `item.Session == nil` continues.
		// A bug that dereferenced Session unconditionally would NPE here.
		snap := &MenuSnapshot{
			Profile:     "test",
			GeneratedAt: time.Now(),
			Items: []MenuItem{
				{Index: 0, Type: MenuItemTypeSession, Session: nil},
			},
		}
		refreshSnapshotHookStatuses(snap, loader)
		// no panic = success; nothing else to assert.
	})

	t.Run("empty loader return is a no-op", func(t *testing.T) {
		snap := snapshotWithSession("sess-A", "claude", session.StatusError)
		empty := func() map[string]*session.HookStatus { return map[string]*session.HookStatus{} }
		refreshSnapshotHookStatuses(snap, empty)
		if got := snap.Items[0].Session.Status; got != session.StatusError {
			t.Fatalf("empty loader return must leave snapshot untouched: got %q", got)
		}
	})
}
