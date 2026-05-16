package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Phase 1 v1.9 regression coverage — case route-001.
//
// TestSessionEndpointNotFound (handlers_menu_test.go:211) covers the
// no-overlay 404 path. The #867 fix added a hookStatusLoader that runs in
// the same handler before the not-found scan; nothing today asserts that
// an overlay containing a session that is NOT in the snapshot does not
// somehow surface as a 200 response. A sloppy refactor that swapped the
// scan order (apply overlay → iterate by overlay key → write 200) would
// invent sessions out of thin air; this test catches that class.
//
// The negative invariant: hookStatusLoader is a STATUS overlay, never a
// session-existence source. The snapshot is the source of truth for what
// sessions exist; the overlay only re-paints their Status field.

func TestSessionEndpoint_NotFound_StaysNotFoundWithOverlay(t *testing.T) {
	t.Parallel()
	fx := newParityFixture()
	// Overlay claims a session id the snapshot has never heard of.
	installLoaderOnServer(fx.server, map[string]*session.HookStatus{
		"sess-ghost-001": {
			Status:    "running",
			Event:     "UserPromptSubmit",
			UpdatedAt: time.Now().Add(-5 * time.Second),
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/session/sess-ghost-001", nil)
	w := httptest.NewRecorder()
	fx.server.handleSessionByID(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("ghost session in overlay must produce 404, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"code":"NOT_FOUND"`) {
		t.Fatalf("expected NOT_FOUND body for ghost id, got: %s", w.Body.String())
	}

	// Cross-check: the existing legitimate sess-001 must still succeed
	// alongside the ghost overlay entry. Without this, a regression that
	// short-circuited the whole handler on any overlay miss would slip.
	req2 := httptest.NewRequest(http.MethodGet, "/api/session/sess-001", nil)
	w2 := httptest.NewRecorder()
	fx.server.handleSessionByID(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("legitimate session must still resolve when overlay has ghost entries: got %d body=%s",
			w2.Code, w2.Body.String())
	}
}

// Sister check: /api/menu must NOT include the ghost session either. The
// overlay is a re-paint, not an inject. A regression that iterated by
// overlay map keys would surface phantom rows in the sidebar.
func TestMenuEndpoint_OverlayDoesNotInjectPhantomSessions(t *testing.T) {
	t.Parallel()
	fx := newParityFixture()
	installLoaderOnServer(fx.server, map[string]*session.HookStatus{
		"sess-ghost-002": {
			Status:    "waiting",
			UpdatedAt: time.Now().Add(-5 * time.Second),
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/menu", nil)
	w := httptest.NewRecorder()
	fx.server.handleMenu(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/menu: status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Contains(body, "sess-ghost-002") {
		t.Fatalf("/api/menu must not contain overlay-only ghost sessions; body: %s", body)
	}
}
