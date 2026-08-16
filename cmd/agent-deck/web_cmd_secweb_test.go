package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// secweb #1: `agent-deck web` must refuse to start on a non-loopback bind
// when no auth token is set, before the TUI boots. See /tmp/sec-web-REPORT.md.

func TestBuildWebServer_RefusesNonLoopbackWithoutToken(t *testing.T) {
	_, err := buildWebServer("test-profile", []string{"--listen", "0.0.0.0:8420"}, nil, noopMutator{})
	if err == nil {
		t.Fatal("expected buildWebServer to refuse non-loopback bind without a token")
	}
	if !strings.Contains(err.Error(), "--token") || !strings.Contains(err.Error(), "--token-file") || !strings.Contains(err.Error(), "--insecure-bind") {
		t.Fatalf("refusal error should be actionable (mention --token, --token-file, and --insecure-bind), got: %v", err)
	}
}

func TestBuildWebServer_AllowsNonLoopbackWithToken(t *testing.T) {
	srv, err := buildWebServer("test-profile", []string{"--listen", "0.0.0.0:0", "--token", "secret"}, nil, noopMutator{})
	if err != nil {
		t.Fatalf("non-loopback bind with token should be allowed, got %v", err)
	}
	if srv == nil {
		t.Fatal("expected a server")
	}
	if !srv.HasMCPManager() {
		t.Fatal("authenticated server should wire the MCP manager")
	}
}

func TestBuildWebServer_AllowsNonLoopbackWithTokenFile(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "web-token")
	if err := os.WriteFile(tokenPath, []byte("secret-from-file\n"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	srv, err := buildWebServer("test-profile", []string{"--listen", "0.0.0.0:0", "--token-file", tokenPath}, nil, noopMutator{})
	if err != nil {
		t.Fatalf("non-loopback bind with token file should be allowed, got %v", err)
	}
	if srv == nil {
		t.Fatal("expected a server")
	}
	if !srv.HasMCPManager() {
		t.Fatal("token-file authenticated server should wire the MCP manager")
	}

	validReq := httptest.NewRequest(http.MethodGet, "/api/mcps", nil)
	validReq.Header.Set("Authorization", "Bearer secret-from-file")
	validResponse := httptest.NewRecorder()
	srv.Handler().ServeHTTP(validResponse, validReq)
	if validResponse.Code != http.StatusOK {
		t.Fatalf("token read from --token-file should expose the authenticated MCP route with 200, got %d", validResponse.Code)
	}

	invalidReq := httptest.NewRequest(http.MethodGet, "/api/mcps", nil)
	invalidReq.Header.Set("Authorization", "Bearer wrong-token")
	invalidResponse := httptest.NewRecorder()
	srv.Handler().ServeHTTP(invalidResponse, invalidReq)
	if invalidResponse.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token should be rejected with 401, got %d", invalidResponse.Code)
	}
}

func TestBuildWebServer_TokenAndTokenFileMutuallyExclusive(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "web-token")
	if err := os.WriteFile(tokenPath, []byte("secret-from-file\n"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	_, err := buildWebServer("test-profile", []string{"--listen", "127.0.0.1:0", "--token", "secret", "--token-file", tokenPath}, nil, noopMutator{})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected mutually exclusive token error, got %v", err)
	}
}

func TestBuildWebServer_EmptyTokenFileRejected(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "web-token")
	if err := os.WriteFile(tokenPath, []byte("\n"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	_, err := buildWebServer("test-profile", []string{"--listen", "127.0.0.1:0", "--token-file", tokenPath}, nil, noopMutator{})
	if err == nil || !strings.Contains(err.Error(), "is empty") {
		t.Fatalf("expected empty token file error, got %v", err)
	}
}

func TestBuildWebServer_AllowsNonLoopbackWithInsecureBind(t *testing.T) {
	srv, err := buildWebServer("test-profile", []string{"--listen", "0.0.0.0:0", "--insecure-bind"}, nil, noopMutator{})
	if err != nil {
		t.Fatalf("non-loopback bind with --insecure-bind should be allowed, got %v", err)
	}
	if srv == nil {
		t.Fatal("expected a server")
	}
	assertMCPManagerUnavailable(t, srv)
}

func TestBuildWebServer_DefaultLoopbackUnchanged(t *testing.T) {
	srv, err := buildWebServer("test-profile", []string{"--listen", "127.0.0.1:0"}, nil, noopMutator{})
	if err != nil {
		t.Fatalf("default loopback bind without token should still work, got %v", err)
	}
	if srv == nil {
		t.Fatal("expected a server")
	}
	assertMCPManagerUnavailable(t, srv)
}

func assertMCPManagerUnavailable(t *testing.T, srv interface {
	HasMCPManager() bool
	Handler() http.Handler
}) {
	t.Helper()
	if srv.HasMCPManager() {
		t.Fatal("tokenless server must not wire the MCP manager")
	}

	req := httptest.NewRequest(http.MethodGet, "/api/mcps", nil)
	response := httptest.NewRecorder()
	srv.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("tokenless MCP route should remain unavailable with 503, got %d", response.Code)
	}
}
