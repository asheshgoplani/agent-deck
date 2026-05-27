package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// #5 MEDIUM: the auth token must not be accepted via the query string on the
// HTTP API (it leaks to logs/history/proxies). Header-only for the API; the
// WS upgrade is the one documented exception (browsers can't set WS headers).

func TestAuth_QueryTokenRejectedOnAPI(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", Token: "secret"})

	req := httptest.NewRequest(http.MethodGet, "/api/menu?token=secret", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("query-string token on API should be rejected (401), got %d", rr.Code)
	}
}

func TestAuth_HeaderTokenAcceptedOnAPI(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", Token: "secret"})

	req := httptest.NewRequest(http.MethodGet, "/api/menu", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code == http.StatusUnauthorized {
		t.Fatalf("header bearer token on API should be accepted, got 401")
	}
}

func TestAuth_QueryTokenAcceptedOnWS(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", Token: "secret"})

	// A WS request carrying the token in the query string must pass the
	// authorization gate (browsers cannot set headers on the WS handshake).
	// It then fails later for an unrelated reason (no real upgrade / unknown
	// session) — so the one thing we assert is that it is NOT 401.
	req := httptest.NewRequest(http.MethodGet, "/ws/session/sess-1?token=secret", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code == http.StatusUnauthorized {
		t.Fatalf("WS with query-string token should be authorized (not 401), got %d", rr.Code)
	}
}

// #4 MEDIUM: when a token is configured (the exposed mode #1 enforces for any
// non-loopback bind), CSRF fails closed — a mutation carrying NEITHER Origin
// NOR Referer is rejected. Default loopback no-token behavior is unchanged
// (covered by TestCSRF_AllowsNoOriginNoReferer).

func TestCSRF_FailsClosedWhenTokenSet_NoOriginNoReferer(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "0.0.0.0:8420", Token: "secret", WebMutations: true})

	body := strings.NewReader(`{"title":"x","projectPath":"/tmp","tool":"shell"}`)
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8420/api/sessions", body)
	req.Header.Set("Authorization", "Bearer secret") // authenticated, but no Origin/Referer
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("authenticated mutation with neither Origin nor Referer should be CSRF-rejected (403) when token set, got %d", rr.Code)
	}
}

// #6 LOW: when a token is configured, /healthz must not disclose
// profile/version/etc to anonymous (unauthenticated) callers.

func TestHealthz_TokenSet_AnonymousMinimal(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", Profile: "secret-profile", Token: "secret"})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("/healthz should still return 200 to anonymous callers, got %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"ok":true`) {
		t.Fatalf("/healthz should report ok=true, got %s", body)
	}
	for _, leaked := range []string{"secret-profile", `"version"`, `"profile"`} {
		if strings.Contains(body, leaked) {
			t.Fatalf("/healthz leaked %q to anonymous caller when token set: %s", leaked, body)
		}
	}
}

func TestHealthz_TokenSet_AuthorizedFullDetail(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", Profile: "p1", Token: "secret"})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	body := rr.Body.String()
	if !strings.Contains(body, `"profile":"p1"`) {
		t.Fatalf("authorized /healthz should include profile detail, got %s", body)
	}
}
