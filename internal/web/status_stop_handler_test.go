package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Phase 1 v1.9 regression coverage — case status-stop-001.
//
// applyHookStatusToMenuSession (snapshot_hook_refresh.go:63) short-circuits
// when sess.Status == StatusStopped: "user-intentional state, never flip
// stopped sessions." TestRefreshSnapshotHookStatuses_StoppedNeverOverridden
// pins this at the helper level. NOTHING pins it at the HANDLER level —
// /api/menu and /api/session/{id} could (and historically did, before #867)
// drift if a refactor moved the stopped check inside a per-call branch in
// the handler instead of the helper.
//
// This test pins the invariant end-to-end through both GET handlers:
// a STOPPED session with a fresh "running" or "waiting" hook overlay must
// stay STOPPED in the response.
//
// Why it matters: a regression here makes the web UI's "I clicked stop
// 3 seconds ago" optimistic state revert to running on the next /api/menu
// poll, which is the exact failure mode users reported in the v1.8.0
// flicker incident before #867 was tightened.

func TestParity_HandleMenu_StoppedIsStickyAgainstFreshHook(t *testing.T) {
	t.Parallel()
	fx := newParityFixture()
	// Fresh "running" overlay on a session the user just stopped.
	installLoaderOnServer(fx.server, map[string]*session.HookStatus{
		"sess-001": {
			Status:    "running",
			Event:     "UserPromptSubmit",
			UpdatedAt: time.Now().Add(-1 * time.Second),
		},
	})
	fx.store.mu.Lock()
	fx.store.sessions["sess-001"].Status = session.StatusStopped
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
	if got.Status != session.StatusStopped {
		t.Fatalf("user-intentional stop must be sticky against fresh hook overlay: "+
			"want %q got %q. Regression here re-introduces the v1.8.0 flicker class "+
			"(\"I clicked stop, then it flipped back to running\").",
			session.StatusStopped, got.Status)
	}
}

func TestParity_HandleSessionByID_StoppedIsStickyAgainstFreshHook(t *testing.T) {
	t.Parallel()
	fx := newParityFixture()
	installLoaderOnServer(fx.server, map[string]*session.HookStatus{
		"sess-002": {
			Status:    "waiting",
			Event:     "Stop",
			UpdatedAt: time.Now().Add(-2 * time.Second),
		},
	})
	fx.store.mu.Lock()
	fx.store.sessions["sess-002"].Status = session.StatusStopped
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
	if resp.Session.Status != session.StatusStopped {
		t.Fatalf("user-intentional stop must be sticky on per-session endpoint: "+
			"want %q got %q", session.StatusStopped, resp.Session.Status)
	}
}
