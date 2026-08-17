package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/web"
)

// Exact-tip auth matrix for the endpoints this change makes reachable.
//
// buildWebServer only wires the production MCP manager when a bearer token is
// configured, so /api/mcps and the session-scoped MCP routes go from
// permanently-503 to live the moment a token exists. Server.authorize()
// short-circuits to "allowed" when Config.Token is empty, which means the
// token-conditioned wiring in buildWebServer is load-bearing security, not a
// convenience. These tests pin both halves of that contract:
//
//   - authenticated server: every MCP endpoint answers 401 for a missing
//     token and 401 for a wrong token, and only lets a correct token through;
//   - tokenless server (loopback default and --insecure-bind alike): every MCP
//     endpoint stays unavailable, so the change adds no unauthenticated
//     network surface.
//
// The matrix runs for both credential sources (--token and --token-file) so
// the file-backed path cannot drift away from the flag-backed one.

// mcpEndpoint is one route the MCP manager makes live.
type mcpEndpoint struct {
	name   string
	method string
	path   string
}

// mcpEndpoints lists every route registered against the MCP manager in
// web.NewServer. Keep in sync with the mux registrations there; a new MCP
// route that is not listed here is a route with no auth-boundary test.
var mcpEndpoints = []mcpEndpoint{
	{"catalog", http.MethodGet, "/api/mcps"},
	{"session list", http.MethodGet, "/api/sessions/sess-1/mcps"},
	{"attach", http.MethodPost, "/api/sessions/sess-1/mcps/context7"},
	{"detach", http.MethodDelete, "/api/sessions/sess-1/mcps/context7"},
	{"move scope", http.MethodPatch, "/api/sessions/sess-1/mcps/context7"},
}

func serveMCP(t *testing.T, srv interface{ Handler() http.Handler }, ep mcpEndpoint, authHeader string) int {
	t.Helper()
	return serveMCPWithOrigin(t, srv, ep, authHeader, sameOrigin)
}

// sameOrigin asks serveMCPWithOrigin to derive an Origin header from the
// request's own Host, so a mutation clears csrfProtect and reaches the
// authorization gate under test.
const sameOrigin = "\x00same-origin"

func serveMCPWithOrigin(t *testing.T, srv interface{ Handler() http.Handler }, ep mcpEndpoint, authHeader, origin string) int {
	t.Helper()
	req := httptest.NewRequest(ep.method, ep.path, nil)
	// POST/DELETE/PATCH pass through csrfProtect before reaching any handler,
	// and that layer fails closed once a token is configured: a mutation with
	// no Origin and no Referer is rejected with 403 before authorization is
	// ever consulted. Present a same-origin header so these cases measure the
	// auth boundary rather than the CSRF boundary. The CSRF layer has its own
	// coverage in TestBuildWebServer_MCPMutationsRejectCrossOrigin.
	if isMutation(ep.method) && origin != "" {
		if origin == sameOrigin {
			req.Header.Set("Origin", "http://"+req.Host)
		} else {
			req.Header.Set("Origin", origin)
		}
	}
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec.Code
}

func isMutation(method string) bool {
	return method == http.MethodPost || method == http.MethodDelete || method == http.MethodPatch
}

// TestBuildWebServer_MCPRoutesAuth is the token-missing / token-wrong /
// token-right matrix over every MCP endpoint, for both credential sources.
func TestBuildWebServer_MCPRoutesAuth(t *testing.T) {
	for _, source := range []struct {
		name  string
		args  func(t *testing.T) []string
		token string
	}{
		{
			name:  "--token",
			token: "flag-secret",
			args: func(*testing.T) []string {
				return []string{"--listen", "127.0.0.1:0", "--token", "flag-secret"}
			},
		},
		{
			name:  "--token-file",
			token: "file-secret",
			args: func(t *testing.T) []string {
				path := filepath.Join(t.TempDir(), "web-token")
				if err := os.WriteFile(path, []byte("file-secret\n"), 0o600); err != nil {
					t.Fatalf("write token file: %v", err)
				}
				return []string{"--listen", "127.0.0.1:0", "--token-file", path}
			},
		},
	} {
		t.Run(source.name, func(t *testing.T) {
			withTempHomeAndConfig(t, "")
			srv, err := buildWebServer("test-profile", source.args(t), emptyMenuData{}, noopMutator{})
			if err != nil {
				t.Fatalf("buildWebServer: %v", err)
			}
			if !srv.HasMCPManager() {
				t.Fatal("an authenticated server must wire the MCP manager")
			}

			for _, ep := range mcpEndpoints {
				t.Run(ep.name, func(t *testing.T) {
					if code := serveMCP(t, srv, ep, ""); code != http.StatusUnauthorized {
						t.Errorf("token missing: %s %s = %d, want 401", ep.method, ep.path, code)
					}
					if code := serveMCP(t, srv, ep, "Bearer wrong-"+source.token); code != http.StatusUnauthorized {
						t.Errorf("token wrong: %s %s = %d, want 401", ep.method, ep.path, code)
					}
					// A correct token must clear the auth gate. The catalog
					// then answers 200; the session-scoped routes reach the
					// session lookup and 404 against the empty menu, which is
					// still proof the request was authorized rather than
					// rejected at the boundary.
					want := http.StatusNotFound
					if ep.path == "/api/mcps" {
						want = http.StatusOK
					}
					if code := serveMCP(t, srv, ep, "Bearer "+source.token); code != want {
						t.Errorf("token right: %s %s = %d, want %d", ep.method, ep.path, code, want)
					}
				})
			}
		})
	}
}

// TestBuildWebServer_MCPMutationsRejectCrossOrigin covers the layer in front of
// authorization. Once a token is configured, csrfProtect fails closed on
// state-changing methods, so an MCP mutation carrying a foreign Origin — or no
// Origin at all — is refused before the manager is reached, even when the
// bearer token is correct. A valid token is therefore not sufficient on its
// own to drive an MCP mutation from another origin.
func TestBuildWebServer_MCPMutationsRejectCrossOrigin(t *testing.T) {
	withTempHomeAndConfig(t, "")
	srv, err := buildWebServer("test-profile",
		[]string{"--listen", "127.0.0.1:0", "--token", "flag-secret"}, emptyMenuData{}, noopMutator{})
	if err != nil {
		t.Fatalf("buildWebServer: %v", err)
	}

	for _, ep := range mcpEndpoints {
		if !isMutation(ep.method) {
			continue
		}
		t.Run(ep.name, func(t *testing.T) {
			for _, origin := range []struct {
				name  string
				value string
			}{
				{"foreign origin", "http://evil.example"},
				{"no origin and no referer", ""},
			} {
				code := serveMCPWithOrigin(t, srv, ep, "Bearer flag-secret", origin.value)
				if code != http.StatusForbidden {
					t.Errorf("%s: %s %s with a valid token = %d, want 403", origin.name, ep.method, ep.path, code)
				}
			}
		})
	}
}

// TestBuildWebServer_MCPRoutesUnavailableWithoutToken is the fail-closed half:
// with no token there is no authorization to fail, so the guarantee has to be
// that the routes carry no production manager at all. Both tokenless binds are
// covered, including the --insecure-bind case that deliberately exposes the
// listener to the network.
func TestBuildWebServer_MCPRoutesUnavailableWithoutToken(t *testing.T) {
	for _, bind := range []struct {
		name string
		args []string
	}{
		{"loopback default", []string{"--listen", "127.0.0.1:0"}},
		{"non-loopback with --insecure-bind", []string{"--listen", "0.0.0.0:0", "--insecure-bind"}},
	} {
		t.Run(bind.name, func(t *testing.T) {
			withTempHomeAndConfig(t, "")
			srv, err := buildWebServer("test-profile", bind.args, emptyMenuData{}, noopMutator{})
			if err != nil {
				t.Fatalf("buildWebServer: %v", err)
			}
			if srv.HasMCPManager() {
				t.Fatal("a tokenless server must not wire the MCP manager")
			}

			for _, ep := range mcpEndpoints {
				// No credential is presented and none exists, so the only
				// thing standing between the network and the MCP config
				// writers is the unwired manager. Anything but 503 here is a
				// new unauthenticated surface.
				if code := serveMCP(t, srv, ep, ""); code != http.StatusServiceUnavailable {
					t.Errorf("%s %s = %d, want 503", ep.method, ep.path, code)
				}
			}
		})
	}
}

// TestResolveWebToken_FailsClosed pins the resolver's refusal paths. Every one
// of them must return an error: returning an empty token instead would turn
// Server.authorize() into a no-op and open every authenticated route.
func TestResolveWebToken_FailsClosed(t *testing.T) {
	writeToken := func(t *testing.T, contents string, mode os.FileMode) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "web-token")
		if err := os.WriteFile(path, []byte(contents), mode); err != nil {
			t.Fatalf("write token file: %v", err)
		}
		// os.WriteFile applies the process umask, which would quietly turn a
		// deliberately-loose 0640 fixture into 0600 and make the permission
		// cases pass for the wrong reason. Set the mode explicitly.
		if err := os.Chmod(path, mode); err != nil {
			t.Fatalf("chmod token file: %v", err)
		}
		return path
	}

	t.Run("group-readable file rejected", func(t *testing.T) {
		path := writeToken(t, "secret\n", 0o640)
		_, err := resolveWebToken("", path)
		if err == nil || !strings.Contains(err.Error(), "chmod 600") {
			t.Fatalf("expected an actionable permission refusal, got %v", err)
		}
	})

	t.Run("world-readable file rejected", func(t *testing.T) {
		path := writeToken(t, "secret\n", 0o644)
		if _, err := resolveWebToken("", path); err == nil {
			t.Fatal("expected a world-readable token file to be refused")
		}
	})

	t.Run("directory rejected", func(t *testing.T) {
		_, err := resolveWebToken("", t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("expected a non-regular-file refusal, got %v", err)
		}
	})

	t.Run("missing file rejected", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "absent")
		if _, err := resolveWebToken("", path); err == nil {
			t.Fatal("expected a missing token file to be refused")
		}
	})

	t.Run("oversized file rejected", func(t *testing.T) {
		path := writeToken(t, strings.Repeat("a", maxWebTokenFileSize+1), 0o600)
		_, err := resolveWebToken("", path)
		if err == nil || !strings.Contains(err.Error(), "single-line bearer token") {
			t.Fatalf("expected an oversized token file to be refused, got %v", err)
		}
	})

	t.Run("interior whitespace rejected", func(t *testing.T) {
		path := writeToken(t, "two words\n", 0o600)
		_, err := resolveWebToken("", path)
		if err == nil || !strings.Contains(err.Error(), "single line") {
			t.Fatalf("expected a multi-word token file to be refused, got %v", err)
		}
	})

	t.Run("multi-line file rejected", func(t *testing.T) {
		path := writeToken(t, "line-one\nline-two\n", 0o600)
		if _, err := resolveWebToken("", path); err == nil {
			t.Fatal("expected a multi-line token file to be refused")
		}
	})

	t.Run("control characters rejected", func(t *testing.T) {
		path := writeToken(t, "secret\x00tail\n", 0o600)
		if _, err := resolveWebToken("", path); err == nil {
			t.Fatal("expected a token containing control characters to be refused")
		}
	})

	t.Run("errors never echo the token", func(t *testing.T) {
		path := writeToken(t, "super-secret-value tail\n", 0o600)
		_, err := resolveWebToken("", path)
		if err == nil {
			t.Fatal("expected a refusal")
		}
		if strings.Contains(err.Error(), "super-secret-value") {
			t.Fatalf("refusal message leaked token contents: %v", err)
		}
	})

	t.Run("clean 0600 file accepted and trimmed", func(t *testing.T) {
		path := writeToken(t, "  secret-from-file\n", 0o600)
		got, err := resolveWebToken("", path)
		if err != nil {
			t.Fatalf("resolveWebToken: %v", err)
		}
		if got != "secret-from-file" {
			t.Fatalf("resolveWebToken = %q, want %q", got, "secret-from-file")
		}
	})
}

// emptyMenuData is a MenuDataLoader that reports no sessions, so the
// session-scoped MCP routes resolve deterministically to 404 without touching
// real session storage.
type emptyMenuData struct{}

func (emptyMenuData) LoadMenuSnapshot() (*web.MenuSnapshot, error) {
	return &web.MenuSnapshot{}, nil
}
