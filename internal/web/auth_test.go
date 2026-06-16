package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// SSE streams (EventSource) cannot set an Authorization header, so the menu and
// command-center event streams must accept the token via the query string —
// exactly like the WS upgrade. A bad or missing token still 401s, and the JSON
// API endpoints stay header-only (query token rejected). Regression for the
// v1.9.68/69 headless --token bug: header-only SSE handlers 401'd EventSource,
// so the web menu + Command Center never loaded over Tailscale.
//
// For the *accepted* cases the handler passes the auth gate and then enters its
// long-lived streaming loop (for { select { <-ctx.Done() ... } }). We give those
// requests an already-cancelled context so the handler writes the initial
// snapshot and returns immediately instead of blocking the test. The only thing
// asserted for accepted cases is that the response is NOT 401.

// cancelledRequest builds a GET request whose context is already cancelled, so
// an authorized SSE handler exits its stream loop right after the first emit.
func cancelledRequest(target string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	ctx, cancel := context.WithCancel(req.Context())
	cancel()
	return req.WithContext(ctx)
}

func TestSSE_QueryTokenAcceptedOnMenuEvents(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", Token: "secret"})

	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, cancelledRequest("/events/menu?token=secret"))

	if rr.Code == http.StatusUnauthorized {
		t.Fatalf("SSE /events/menu with query-string token should be authorized (not 401), got %d", rr.Code)
	}
}

func TestSSE_QueryTokenAcceptedOnCommandCenterEvents(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", Token: "secret"})

	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, cancelledRequest("/events/command-center?token=secret"))

	if rr.Code == http.StatusUnauthorized {
		t.Fatalf("SSE /events/command-center with query-string token should be authorized (not 401), got %d", rr.Code)
	}
}

func TestSSE_HeaderTokenAcceptedOnMenuEvents(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", Token: "secret"})

	req := cancelledRequest("/events/menu")
	req.Header.Set("Authorization", "Bearer secret")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code == http.StatusUnauthorized {
		t.Fatalf("SSE /events/menu with a valid header token should be authorized (not 401), got %d", rr.Code)
	}
}

func TestSSE_BadQueryTokenRejectedOnMenuEvents(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", Token: "secret"})

	req := httptest.NewRequest(http.MethodGet, "/events/menu?token=wrong", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("SSE /events/menu with a bad query token must be rejected (401), got %d", rr.Code)
	}
}

func TestSSE_MissingTokenRejectedOnMenuEvents(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", Token: "secret"})

	req := httptest.NewRequest(http.MethodGet, "/events/menu", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("SSE /events/menu with no token must be rejected (401), got %d", rr.Code)
	}
}

// Guard: the SSE exception must NOT leak to the JSON API. The command-center
// JSON status endpoint stays header-only — a query-string token is rejected.
func TestSSE_CommandCenterJSONStaysHeaderOnly(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", Token: "secret"})

	req := httptest.NewRequest(http.MethodGet, "/api/command-center/status?token=secret", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("JSON /api/command-center/status with query-string token should be rejected (401), got %d", rr.Code)
	}
}
