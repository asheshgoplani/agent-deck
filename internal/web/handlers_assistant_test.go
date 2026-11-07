package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/assistant"
)

type fakeAssistantExec struct{ calls int }

func (f *fakeAssistantExec) Run(_ context.Context, _ string, args []string, _ []byte) ([]byte, error) {
	f.calls++
	if len(args) > 0 && args[0] == "list" {
		return []byte(`[{"id":"s1","title":"demo","tool":"shell","status":"idle","group":"g","path":"/tmp/p"}]`), nil
	}
	return []byte(`{"success":true}`), nil
}

// newAssistantServer returns a server with the assistant service wired over
// a fake executor, and a read-scope assistant token.
func newAssistantServer(t *testing.T, cfg Config) (*Server, string, *fakeAssistantExec) {
	t.Helper()
	cfg.ListenAddr = "127.0.0.1:0"
	srv := NewServer(cfg)
	fx := &fakeAssistantExec{}
	store := assistant.NewStore(t.TempDir())
	srv.SetAssistantService(assistant.NewService(store, fx, "test"), "/usr/local/bin/agent-deck")
	tok, _, err := store.Create("bot", "mcp", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	return srv, tok, fx
}

func serve(srv *Server, method, path, auth, body string) *httptest.ResponseRecorder {
	req := newLocalRequest(method, path, strings.NewReader(body))
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	return rr
}

const initializeBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`

// The web server with NO owner token must still demand an assistant token on
// /mcp and /api/assistant/v1/.
func TestAssistantRoutes_TokenlessServerStillRequiresAssistantToken(t *testing.T) {
	srv, tok, fx := newAssistantServer(t, Config{})
	if rr := serve(srv, http.MethodPost, "/mcp", "", initializeBody); rr.Code != http.StatusUnauthorized {
		t.Fatalf("tokenless POST /mcp = %d, want 401", rr.Code)
	}
	if rr := serve(srv, http.MethodGet, "/api/assistant/v1/sessions", "", ""); rr.Code != http.StatusUnauthorized {
		t.Fatalf("tokenless GET sessions = %d, want 401", rr.Code)
	}
	if fx.calls != 0 {
		t.Fatal("unauthenticated request ran a command")
	}
	rr := serve(srv, http.MethodPost, "/mcp", "Bearer "+tok, initializeBody)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"protocolVersion":"2025-06-18"`) {
		t.Fatalf("initialize with token: %d %s", rr.Code, rr.Body)
	}
	// No Origin/Referer and no cookie: still not blocked by the CSRF check.
	rr = serve(srv, http.MethodPost, "/mcp", "Bearer "+tok, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_sessions","arguments":{}}}`)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `\"count\": 1`) {
		t.Fatalf("tools/call: %d %s", rr.Code, rr.Body)
	}
	if rr := serve(srv, http.MethodGet, "/mcp", "Bearer "+tok, ""); rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /mcp = %d, want 405", rr.Code)
	}
}

func TestAssistantRoutes_OwnerTokenRejectedOnMCP(t *testing.T) {
	srv, _, _ := newAssistantServer(t, Config{Token: "owner-secret"})
	if rr := serve(srv, http.MethodPost, "/mcp", "Bearer owner-secret", initializeBody); rr.Code != http.StatusUnauthorized {
		t.Fatalf("owner token on /mcp = %d, want 401", rr.Code)
	}
	if rr := serve(srv, http.MethodGet, "/api/assistant/v1/whoami", "Bearer owner-secret", ""); rr.Code != http.StatusUnauthorized {
		t.Fatalf("owner token on assistant API = %d, want 401", rr.Code)
	}
	// The owner cookie is not a credential there either.
	req := newLocalRequest(http.MethodPost, "/mcp", strings.NewReader(initializeBody))
	req.AddCookie(&http.Cookie{Name: tokenCookieName, Value: "owner-secret"})
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("owner cookie on /mcp = %d, want 401", rr.Code)
	}
}

func TestAssistantRoutes_AssistantTokenRejectedOnOwnerRoutes(t *testing.T) {
	for _, cfg := range []Config{{Token: "owner-secret"}, {}} {
		srv, tok, _ := newAssistantServer(t, cfg)
		for _, path := range []string{"/api/menu", "/api/connect/assistants", "/api/system/stats", "/api/settings"} {
			if rr := serve(srv, http.MethodGet, path, "Bearer "+tok, ""); rr.Code != http.StatusUnauthorized {
				t.Errorf("owner token %q: assistant token on %s = %d, want 401", cfg.Token, path, rr.Code)
			}
		}
		// Same-origin, so the CSRF check passes and auth is what refuses it.
		req := newLocalRequest(http.MethodPost, "/api/connect/tokens", strings.NewReader(`{"scopes":["read","send"]}`))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Origin", "http://localhost")
		rr := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("owner token %q: assistant token on POST /api/connect/tokens = %d, want 401", cfg.Token, rr.Code)
		}
	}
}

func TestConnectOwnerEndpoints(t *testing.T) {
	srv, _, _ := newAssistantServer(t, Config{Token: "owner-secret", WebMutations: true})
	if rr := serve(srv, http.MethodGet, "/api/connect/assistants", "", ""); rr.Code != http.StatusUnauthorized {
		t.Fatalf("no owner auth: %d", rr.Code)
	}
	rr := serve(srv, http.MethodGet, "/api/connect/assistants", "Bearer owner-secret", "")
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"hermes"`) {
		t.Fatalf("assistants: %d %s", rr.Code, rr.Body)
	}
	req := newLocalRequest(http.MethodPost, "/api/connect/tokens", strings.NewReader(`{"assistant":"telegram","scopes":["read"],"name":"tg"}`))
	req.Header.Set("Authorization", "Bearer owner-secret")
	req.Header.Set("Origin", "http://localhost")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("create token: %d %s", rec.Code, rec.Body)
	}
	var conn assistant.Connection
	if err := json.Unmarshal(rec.Body.Bytes(), &conn); err != nil {
		t.Fatal(err)
	}
	if conn.Name != "tg" || !strings.HasPrefix(conn.Token, assistant.TokenPrefix) || conn.HTTPAPI.Base != "http://localhost/api/assistant/v1" || !strings.Contains(conn.Block, conn.Token) {
		t.Fatalf("connection: %+v", conn)
	}
	req = newLocalRequest(http.MethodPost, "/api/connect/test", strings.NewReader(`{"token":"`+conn.Token+`"}`))
	req.Header.Set("Authorization", "Bearer owner-secret")
	req.Header.Set("Origin", "http://localhost")
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":true`) || !strings.Contains(rec.Body.String(), `"sessions":1`) {
		t.Fatalf("test: %d %s", rec.Code, rec.Body)
	}
	if rr := serve(srv, http.MethodGet, "/connect", "", ""); rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "/static/connect.js") {
		t.Fatalf("/connect page: %d", rr.Code)
	}
}

func TestConnectTokens_ReadOnlyServerGrantsReadOnly(t *testing.T) {
	srv, _, _ := newAssistantServer(t, Config{Token: "owner-secret", ReadOnly: true})
	req := newLocalRequest(http.MethodPost, "/api/connect/tokens", strings.NewReader(`{"scopes":["read","send"]}`))
	req.Header.Set("Authorization", "Bearer owner-secret")
	req.Header.Set("Origin", "http://localhost")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("send scope on read-only server: %d %s", rec.Code, rec.Body)
	}
}

func TestAssistantRoutes_UnwiredAnswer503(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0"})
	if rr := serve(srv, http.MethodPost, "/mcp", "Bearer adk_x", initializeBody); rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("unwired /mcp = %d", rr.Code)
	}
}

// Only canonical assistant paths skip the CSRF check.
func TestAssistantCSRFExemptionNeedsCleanPath(t *testing.T) {
	srv, _, _ := newAssistantServer(t, Config{Token: "owner-secret"})
	req := newLocalRequest(http.MethodPost, "/api/assistant/v1/../../api/sessions", strings.NewReader(`{}`))
	req.URL.Path = "/api/assistant/v1/../../api/sessions"
	req.Header.Set("Authorization", "Bearer owner-secret")
	req.Header.Set("Origin", "https://evil.example")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("unclean assistant path = %d, want 403 from the CSRF check", rr.Code)
	}
}
