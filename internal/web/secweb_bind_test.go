package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// secweb fixes — see /tmp/sec-web-REPORT.md.
//
// #1 CRITICAL: refuse an unauthenticated non-loopback bind.

func TestCheckBindSecurity_NonLoopbackEmptyToken_Refused(t *testing.T) {
	for _, addr := range []string{
		":9000",          // all interfaces, no host
		"0.0.0.0:8420",   // explicit wildcard
		"192.168.1.5:80", // LAN IP
		"[::]:8420",      // IPv6 wildcard
	} {
		srv := NewServer(Config{ListenAddr: addr, Token: ""})
		if err := srv.checkBindSecurity(); err == nil {
			t.Errorf("addr %q with empty token: expected refusal, got nil error", addr)
		}
	}
}

func TestCheckBindSecurity_LoopbackEmptyToken_Allowed(t *testing.T) {
	for _, addr := range []string{
		"127.0.0.1:8420",
		"127.0.0.1:0",
		"localhost:8420",
		"[::1]:8420",
	} {
		srv := NewServer(Config{ListenAddr: addr, Token: ""})
		if err := srv.checkBindSecurity(); err != nil {
			t.Errorf("addr %q with empty token (loopback): expected allowed, got %v", addr, err)
		}
	}
}

func TestCheckBindSecurity_NonLoopbackWithToken_Allowed(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "0.0.0.0:8420", Token: "secret"})
	if err := srv.checkBindSecurity(); err != nil {
		t.Errorf("non-loopback bind with token set: expected allowed, got %v", err)
	}
}

func TestCheckBindSecurity_NonLoopbackInsecureBindOverride_Allowed(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "0.0.0.0:8420", Token: "", InsecureBind: true})
	if err := srv.checkBindSecurity(); err != nil {
		t.Errorf("non-loopback bind with --insecure-bind override: expected allowed, got %v", err)
	}
}

// #2 CRITICAL: the terminal-bridge WS and mutation endpoints require the token
// whenever a token is configured (which #1 forces for any non-loopback bind).

func TestWS_TokenSet_NoToken_Unauthorized(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "0.0.0.0:8420", Token: "secret"})

	req := httptest.NewRequest(http.MethodGet, "/ws/session/sess-1", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("WS bridge without token (token configured) should be 401, got %d", rr.Code)
	}
}

func TestMutation_TokenSet_NoToken_Unauthorized(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "0.0.0.0:8420", Token: "secret", WebMutations: true})

	body := strings.NewReader(`{"title":"x","projectPath":"/tmp","tool":"shell"}`)
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8420/api/sessions", body)
	// Same-origin so the request clears CSRF and reaches the auth check.
	req.Header.Set("Origin", "http://127.0.0.1:8420")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("mutation without token (token configured) should be 401, got %d", rr.Code)
	}
}
