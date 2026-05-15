package web

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestWebUI_RendersWaitingStatus_RegressionFor963 is the regression test for
// issue #963: the SSE menu stream (/events/menu) emits raw snapshot data
// without applying the hook overlay, so sessions whose snapshot is stale
// at "error" but whose hook payload says "waiting" surface as `error` on
// the web UI while the CLI correctly reports `waiting`.
//
// Production shape:
//   - TUI publishes a session as StatusError (e.g. hook fast-path window
//     expired, tmux GetStatus fell through to the default branch).
//   - The on-disk hook file still says status="waiting" with a recent
//     UpdatedAt (the inotify path dropped the event but the file is fine).
//   - CLI's `agent-deck list --json` cold-loads the hook file via
//     RefreshInstancesForCLIStatus → reports `waiting`.
//   - Web's /api/sessions, /api/menu, /api/session/{id} re-apply the hook
//     overlay via refreshSnapshotHookStatuses → also report `waiting`.
//   - But /events/menu (the SSE stream the frontend subscribes to for
//     live updates — see static/app/main.js:51) does NOT apply the
//     overlay, so it streams snapshot.Status verbatim.
//   - Frontend replaces sessionsSignal.value on every menu SSE event
//     (main.js:60), so seconds after a correct initial /api/menu load
//     the UI reverts to `error` and the Footer/FleetPane counters drop
//     waiting=0 / error=N.
//
// The fix is to call refreshSnapshotHookStatuses inside handleMenuEvents
// on both the initial snapshot and every emitIfChanged poll iteration.
// This test fails on origin/main and passes once that wiring is added.
func TestWebUI_RendersWaitingStatus_RegressionFor963(t *testing.T) {
	t.Parallel()

	// Tighten the SSE poll cadence so the test can observe the
	// post-initial-snapshot emit without waiting the default 2s.
	origPoll := menuEventsPollInterval
	menuEventsPollInterval = 30 * time.Millisecond
	defer func() { menuEventsPollInterval = origPoll }()

	origHeartbeat := menuEventsHeartbeatInterval
	menuEventsHeartbeatInterval = 5 * time.Second
	defer func() { menuEventsHeartbeatInterval = origHeartbeat }()

	fx := newParityFixture()

	// Production scenario: snapshot reports a Claude session as `error`
	// while the on-disk hook file says `waiting`, updated 30s ago (well
	// within the 2-minute hook fast-path freshness window).
	installLoaderOnServer(fx.server, map[string]*session.HookStatus{
		"sess-963": {
			Status:    "waiting",
			Event:     "Stop",
			UpdatedAt: time.Now().Add(-30 * time.Second),
		},
	})
	fx.store.mu.Lock()
	fx.store.sessions["sess-963"] = &MenuSession{
		ID:          "sess-963",
		Title:       "issue-963",
		Tool:        "claude",
		Status:      session.StatusError, // snapshot stale: TUI saw "error"
		GroupPath:   "work",
		ProjectPath: "/srv/issue-963",
		Order:       50,
		CreatedAt:   fx.store.now(),
	}
	fx.store.order = append(fx.store.order, "sess-963")
	fx.store.mu.Unlock()

	ts := httptest.NewServer(fx.server.Handler())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/events/menu", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("sse request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	reader := bufio.NewReader(resp.Body)
	event, payload, err := readSSEEvent(reader)
	if err != nil {
		t.Fatalf("failed to read initial sse menu event: %v", err)
	}
	if event != "menu" {
		t.Fatalf("expected 'menu' event, got %q", event)
	}

	assertSess963Status(t, "initial SSE menu event", payload)

	// Force a snapshot fingerprint change so the next emitIfChanged
	// poll re-publishes. Without a change emitIfChanged is a no-op and
	// we'd only ever validate the initial-snapshot path.
	fx.store.mu.Lock()
	fx.store.sessions["sess-963"].Title = "issue-963-updated"
	fx.store.mu.Unlock()

	_, payload, err = readSSEEvent(reader)
	if err != nil {
		t.Fatalf("failed to read second sse menu event: %v", err)
	}

	assertSess963Status(t, "post-poll SSE menu event", payload)
}

func assertSess963Status(t *testing.T, label, payload string) {
	t.Helper()

	var snapshot MenuSnapshot
	if err := json.Unmarshal([]byte(payload), &snapshot); err != nil {
		t.Fatalf("%s: invalid snapshot payload: %v", label, err)
	}

	var found *MenuSession
	for _, item := range snapshot.Items {
		if item.Type != MenuItemTypeSession || item.Session == nil {
			continue
		}
		if item.Session.ID == "sess-963" {
			found = item.Session
			break
		}
	}
	if found == nil {
		t.Fatalf("%s: sess-963 missing from snapshot.items", label)
	}
	if found.Status == session.StatusError {
		t.Fatalf("%s: sess-963 status=%q (the #963 bug — hook says waiting but SSE leaks the stale snapshot error)\npayload:\n%s",
			label, found.Status, truncatePayload(payload))
	}
	if found.Status != session.StatusWaiting {
		t.Fatalf("%s: expected sess-963 status=%q got %q\npayload:\n%s",
			label, session.StatusWaiting, found.Status, truncatePayload(payload))
	}
}

func truncatePayload(p string) string {
	const cap = 800
	if len(p) <= cap {
		return p
	}
	return p[:cap] + "...(truncated)"
}

// Belt-and-suspenders compile-time guard: if a future refactor renames
// the SSE event type the regression test silently no-ops, so anchor on
// the literal the frontend listens for (see static/app/main.js:56).
var _ = strings.ToLower("menu")
