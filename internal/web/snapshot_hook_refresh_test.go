package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestRefreshSnapshotHookStatuses_FreshHookOverridesStaleSnapshotError reproduces
// the v1.7.81 production bug: the web /api/sessions returns "error" for a
// session whose on-disk hook file says "waiting" because the TUI's inotify-fed
// snapshot is stale. The CLI `agent-deck list --json` reads the hook file
// directly per call and reports "waiting" for the same session — so until this
// helper runs, the two surfaces disagree.
//
// This test injects the exact divergence: snapshot status=error, disk hook
// status=waiting. Asserts the web read path lifts the snapshot to waiting.
func TestRefreshSnapshotHookStatuses_FreshHookOverridesStaleSnapshotError(t *testing.T) {
	t.Parallel()
	// fixed "now" so the freshness window is deterministic
	now := time.Date(2026, 5, 5, 19, 0, 0, 0, time.UTC)
	hooks := map[string]*session.HookStatus{
		"sess-A": {
			Status:    "waiting",
			Event:     "Stop",
			UpdatedAt: now.Add(-30 * time.Second),
		},
	}

	snap := snapshotWithSession("sess-A", "claude", session.StatusError)
	refreshSnapshotHookStatusesAt(snap, hooks, now)

	got := snap.Items[0].Session.Status
	if got != session.StatusWaiting {
		t.Fatalf("snapshot stale-error must be overridden by fresh waiting hook: got %q want %q", got, session.StatusWaiting)
	}
}

// TestRefreshSnapshotHookStatuses_WaitingHookOverridesAnyNonStopped covers the
// durability rule: a "waiting" hook record represents the latest hook event
// for the instance — no subsequent transition happened at the hook layer (the
// next UserPromptSubmit would have replaced the file with "running"). So even
// if the snapshot has latched at "running" because the TUI's inotify watcher
// missed the Stop event AND a stale tmux pane heuristic is now stuck on
// "active", the web should report "waiting" to match what `agent-deck list`
// shows for the same database state.
func TestRefreshSnapshotHookStatuses_WaitingHookOverridesAnyNonStopped(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 5, 19, 0, 0, 0, time.UTC)
	hooks := map[string]*session.HookStatus{
		"sess-A": {
			Status:    "waiting",
			UpdatedAt: now.Add(-2*time.Hour - time.Second),
		},
	}
	for _, snapshotState := range []session.Status{
		session.StatusRunning, session.StatusError, session.StatusIdle, session.StatusStarting,
	} {
		snapshotState := snapshotState
		t.Run(string(snapshotState), func(t *testing.T) {
			snap := snapshotWithSession("sess-A", "claude", snapshotState)
			refreshSnapshotHookStatusesAt(snap, hooks, now)
			if got := snap.Items[0].Session.Status; got != session.StatusWaiting {
				t.Fatalf("snapshot=%q + hook=waiting must yield waiting: got %q", snapshotState, got)
			}
		})
	}
}

// TestRefreshSnapshotHookStatuses_StaleWaitingOverridesSnapshotError covers the
// long-tail bug case: hook says waiting but is older than the fast-path window
// (e.g. Claude has been sitting at its prompt for 19 minutes). The TUI's
// snapshot has somehow latched at error (typical when inotify dropped the Stop
// event entirely), and the CLI still reports waiting because its tmux pane
// heuristic catches it. The web should agree with the CLI here, so the helper
// promotes the stale waiting to override the error specifically.
func TestRefreshSnapshotHookStatuses_StaleWaitingOverridesSnapshotError(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 5, 19, 0, 0, 0, time.UTC)
	hooks := map[string]*session.HookStatus{
		"sess-A": {
			Status:    "waiting",
			Event:     "Stop",
			UpdatedAt: now.Add(-30 * time.Minute), // way past fast-path window
		},
	}
	snap := snapshotWithSession("sess-A", "claude", session.StatusError)
	refreshSnapshotHookStatusesAt(snap, hooks, now)

	if got := snap.Items[0].Session.Status; got != session.StatusWaiting {
		t.Fatalf("stale waiting must override snapshot=error (mirrors CLI tmux fallback): got %q want %q", got, session.StatusWaiting)
	}
}

// TestRefreshSnapshotHookStatuses_StaleRunningDoesNotOverride asserts the
// asymmetry: only "waiting" is durable enough to override a stale-error
// snapshot. Stale "running" is transient and could be obsolete (Claude may
// have finished since), so don't override.
func TestRefreshSnapshotHookStatuses_StaleRunningDoesNotOverride(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 5, 19, 0, 0, 0, time.UTC)
	hooks := map[string]*session.HookStatus{
		"sess-A": {
			Status:    "running",
			UpdatedAt: now.Add(-30 * time.Minute),
		},
	}
	snap := snapshotWithSession("sess-A", "claude", session.StatusError)
	refreshSnapshotHookStatusesAt(snap, hooks, now)

	if got := snap.Items[0].Session.Status; got != session.StatusError {
		t.Fatalf("stale running must NOT override (transient): got %q want %q", got, session.StatusError)
	}
}

// TestRefreshSnapshotHookStatuses_StoppedNeverOverridden encodes the
// user-intentional rule from Instance.UpdateStatus: a stopped session is
// stopped no matter what the hook says, because the user explicitly stopped it.
func TestRefreshSnapshotHookStatuses_StoppedNeverOverridden(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 5, 19, 0, 0, 0, time.UTC)
	hooks := map[string]*session.HookStatus{
		"sess-A": {Status: "waiting", UpdatedAt: now.Add(-10 * time.Second)},
	}
	snap := snapshotWithSession("sess-A", "claude", session.StatusStopped)
	refreshSnapshotHookStatusesAt(snap, hooks, now)

	if got := snap.Items[0].Session.Status; got != session.StatusStopped {
		t.Fatalf("stopped must be sticky against hook overrides: got %q", got)
	}
}

// TestRefreshSnapshotHookStatuses_NonHookEmittingToolsUntouched asserts that
// tools without hook signals (shell, custom external) are not affected by a
// stray hook file matching their ID — the snapshot's status is the source of
// truth for them.
func TestRefreshSnapshotHookStatuses_NonHookEmittingToolsUntouched(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 5, 19, 0, 0, 0, time.UTC)
	hooks := map[string]*session.HookStatus{
		"sess-A": {Status: "waiting", UpdatedAt: now.Add(-10 * time.Second)},
	}
	snap := snapshotWithSession("sess-A", "shell", session.StatusError)
	refreshSnapshotHookStatusesAt(snap, hooks, now)

	if got := snap.Items[0].Session.Status; got != session.StatusError {
		t.Fatalf("shell tool snapshot must not be touched by hook overlay: got %q", got)
	}
}

// TestRefreshSnapshotHookStatuses_NoHookFilePreservesAllStatuses is the
// property test the user requested: any value of session.Status must round-trip
// through the helper unchanged when no fresh hook overlay applies. This pins
// the contract that adding a new Status enum value can only break the web at
// the override path (intentional), never silently — the snapshot is otherwise
// passed through.
func TestRefreshSnapshotHookStatuses_NoHookFilePreservesAllStatuses(t *testing.T) {
	t.Parallel()
	emptyLoader := makeFakeLoader(nil)

	for _, st := range allSessionStatuses() {
		st := st
		t.Run(string(st), func(t *testing.T) {
			snap := snapshotWithSession("sess-X", "claude", st)
			refreshSnapshotHookStatuses(snap, emptyLoader)
			if got := snap.Items[0].Session.Status; got != st {
				t.Fatalf("status %q changed to %q with no hook overlay", st, got)
			}
		})
	}
}

// TestParity_AllStatusesPreservedThroughGetSessions is the end-to-end version
// of the property test: every Status value seeded into the parity fixture must
// be preserved verbatim through `GET /api/sessions`. This locks the
// MenuSnapshot → JSON serialization contract and covers any future refactor
// that introduces an inadvertent status mapping in the read path.
func TestParity_AllStatusesPreservedThroughGetSessions(t *testing.T) {
	t.Parallel()
	statuses := allSessionStatuses()
	fx := newParityFixture()
	installLoaderOnServer(fx.server, nil) // no overlay
	fx.store.mu.Lock()
	for i, st := range statuses {
		id := "status-" + strconv.Itoa(i)
		fx.store.sessions[id] = &MenuSession{
			ID:          id,
			Title:       string(st),
			Tool:        "shell", // shell never gets hook-overlaid; preserves the seeded value
			Status:      st,
			GroupPath:   "work",
			ProjectPath: "/srv/x",
			Order:       i + 100,
			CreatedAt:   fx.store.now(),
		}
		fx.store.order = append(fx.store.order, id)
	}
	fx.store.mu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	w := httptest.NewRecorder()
	fx.server.handleSessionsCollection(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/sessions: status=%d body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		Sessions []*MenuSession `json:"sessions"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	gotByID := make(map[string]session.Status, len(resp.Sessions))
	for _, s := range resp.Sessions {
		gotByID[s.ID] = s.Status
	}
	for i, want := range statuses {
		id := "status-" + strconv.Itoa(i)
		got, ok := gotByID[id]
		if !ok {
			t.Errorf("session %q missing from API response", id)
			continue
		}
		if got != want {
			t.Errorf("session %q status drift: want %q got %q", id, want, got)
		}
	}
}

// TestParity_WaitingStatusFlowsThroughHandler is the simulation of the live
// production divergence the user reported: a session sitting in the snapshot
// at error has a fresh "waiting" hook on disk; the web's GET /api/sessions
// must report waiting (matching CLI), not the stale error.
//
// Without refreshSnapshotHookStatuses wired into handleSessionsCollection,
// this test fails (snapshot is returned unchanged).
func TestParity_WaitingStatusFlowsThroughHandler(t *testing.T) {
	t.Parallel()
	fx := newParityFixture()
	installLoaderOnServer(fx.server, map[string]*session.HookStatus{
		"sess-W": {
			Status:    "waiting",
			Event:     "Stop",
			UpdatedAt: time.Now().Add(-30 * time.Second),
		},
	})
	fx.store.mu.Lock()
	fx.store.sessions["sess-W"] = &MenuSession{
		ID:          "sess-W",
		Title:       "stale-error-session",
		Tool:        "claude",
		Status:      session.StatusError, // snapshot is stale
		GroupPath:   "work",
		ProjectPath: "/srv/w",
		Order:       50,
		CreatedAt:   fx.store.now(),
	}
	fx.store.order = append(fx.store.order, "sess-W")
	fx.store.mu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	w := httptest.NewRecorder()
	fx.server.handleSessionsCollection(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/sessions: status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Sessions []*MenuSession `json:"sessions"`
	}
	_ = json.NewDecoder(w.Body).Decode(&resp)

	var found *MenuSession
	for _, s := range resp.Sessions {
		if s.ID == "sess-W" {
			found = s
			break
		}
	}
	if found == nil {
		t.Fatalf("session sess-W not in API response")
	}
	if found.Status != session.StatusWaiting {
		t.Fatalf("expected web to lift stale error → waiting via fresh hook overlay, got %q", found.Status)
	}
}

// TestParity_DeadHookFlipsToError verifies the symmetric override: a snapshot
// stuck at running while the hook signals dead must show error. Mirrors
// Instance.UpdateStatus's "dead → StatusError" mapping.
func TestParity_DeadHookFlipsToError(t *testing.T) {
	t.Parallel()
	fx := newParityFixture()
	installLoaderOnServer(fx.server, map[string]*session.HookStatus{
		"sess-D": {
			Status:    "dead",
			Event:     "SessionEnd",
			UpdatedAt: time.Now().Add(-10 * time.Second),
		},
	})
	fx.store.mu.Lock()
	fx.store.sessions["sess-D"] = &MenuSession{
		ID: "sess-D", Title: "stuck-running", Tool: "claude",
		Status: session.StatusRunning, GroupPath: "work",
		ProjectPath: "/srv/d", Order: 60, CreatedAt: fx.store.now(),
	}
	fx.store.order = append(fx.store.order, "sess-D")
	fx.store.mu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	w := httptest.NewRecorder()
	fx.server.handleSessionsCollection(w, req)

	body := w.Body.Bytes()
	if !bytes.Contains(body, []byte(`"status":"error"`)) {
		t.Fatalf("expected dead-hook to flip running → error, body: %s", body)
	}
}

// --- helpers ---------------------------------------------------------------

func snapshotWithSession(id, tool string, status session.Status) *MenuSnapshot {
	return &MenuSnapshot{
		Profile:       "test",
		GeneratedAt:   time.Date(2026, 5, 5, 19, 0, 0, 0, time.UTC),
		TotalSessions: 1,
		Items: []MenuItem{
			{
				Index: 0,
				Type:  MenuItemTypeSession,
				Session: &MenuSession{
					ID: id, Title: id, Tool: tool, Status: status,
					GroupPath: "work", ProjectPath: "/srv/test",
				},
			},
		},
	}
}

// refreshSnapshotHookStatusesAt is the deterministic-clock variant used in
// helper-level tests so timestamps don't drift with wall clock. Helper-level
// tests call this directly instead of going through handlers, so they get a
// fixed `now` and don't need a Server.
func refreshSnapshotHookStatusesAt(snapshot *MenuSnapshot, hooks map[string]*session.HookStatus, now time.Time) {
	if snapshot == nil {
		return
	}
	for i := range snapshot.Items {
		it := &snapshot.Items[i]
		if it.Type != MenuItemTypeSession || it.Session == nil {
			continue
		}
		applyHookStatusToMenuSession(it.Session, hooks[it.Session.ID], now)
	}
}

// makeFakeLoader returns a loader closure that yields a fresh copy of `hooks`
// on each call (so helpers that mutate the result don't pollute the source).
func makeFakeLoader(hooks map[string]*session.HookStatus) func() map[string]*session.HookStatus {
	return func() map[string]*session.HookStatus {
		out := make(map[string]*session.HookStatus, len(hooks))
		for k, v := range hooks {
			cp := *v
			out[k] = &cp
		}
		return out
	}
}

// installLoaderOnServer points the parity fixture's Server at a per-test
// loader. Avoids globals so parallel tests can each have their own overlay.
func installLoaderOnServer(srv *Server, hooks map[string]*session.HookStatus) {
	srv.hookStatusLoader = makeFakeLoader(hooks)
}

// allSessionStatuses is the source of truth for the property test. If a new
// session.Status constant is added, append it here and decide whether the web
// needs explicit handling for it.
func allSessionStatuses() []session.Status {
	return []session.Status{
		session.StatusRunning,
		session.StatusWaiting,
		session.StatusIdle,
		session.StatusError,
		session.StatusStarting,
		session.StatusStopped,
	}
}
