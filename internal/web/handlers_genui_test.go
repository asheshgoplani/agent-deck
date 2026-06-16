package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/genui"
)

func TestGenuiViews_ListsHandAuthoredViews(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0"})
	req := httptest.NewRequest(http.MethodGet, "/api/command-center/genui/views", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("views: status %d", rr.Code)
	}
	var body struct {
		Views []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"views"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Views) != len(genui.ViewIDs) {
		t.Fatalf("expected %d views, got %d", len(genui.ViewIDs), len(body.Views))
	}
}

func TestGenuiSpec_ServesValidatedSpec(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0"})
	for _, id := range genui.ViewIDs {
		req := httptest.NewRequest(http.MethodGet, "/api/command-center/genui/spec/"+id, nil)
		rr := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("spec %q: status %d", id, rr.Code)
		}
		// Re-validate the served bytes — the wire payload must be a valid spec.
		spec, err := genui.ValidateBytes(rr.Body.Bytes())
		if err != nil {
			t.Fatalf("served spec %q failed validation: %v", id, err)
		}
		if spec.SpecID != id {
			t.Errorf("spec %q: specId = %q", id, spec.SpecID)
		}
	}
}

func TestGenuiSpec_UnknownViewIs404(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0"})
	req := httptest.NewRequest(http.MethodGet, "/api/command-center/genui/spec/does-not-exist", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("unknown view: status %d (want 404)", rr.Code)
	}
}

// --- genui-1: the compose endpoint ----------------------------------------

func postCompose(t *testing.T, srv *Server, intent string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"intent":` + jsonString(intent) + `}`
	req := httptest.NewRequest(http.MethodPost, "/api/command-center/genui/compose", strings.NewReader(body))
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	return rr
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestGenuiCompose_StubProducesValidatedSpec(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0"}) // default: StubComposer
	rr := postCompose(t, srv, "show me what's blocked")
	if rr.Code != http.StatusOK {
		t.Fatalf("compose: status %d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Spec  json.RawMessage `json:"spec"`
		Trace struct {
			Composer string `json:"composer"`
			Tries    int    `json:"tries"`
			Repaired bool   `json:"repaired"`
		} `json:"trace"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The returned spec must itself clear the validator (defense in depth).
	spec, err := genui.ValidateBytes(body.Spec)
	if err != nil {
		t.Fatalf("composed spec failed validation: %v", err)
	}
	if spec.SpecID != "composed-blocked" {
		t.Errorf("specId = %q, want composed-blocked", spec.SpecID)
	}
	if body.Trace.Composer != "stub" || body.Trace.Tries != 1 {
		t.Errorf("trace = %+v", body.Trace)
	}
}

func TestGenuiCompose_InvalidIntentSurfacesCleanError(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0"})
	rr := postCompose(t, srv, "please use an unknown widget") // stub emits unknown-widget spec
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Error string          `json:"error"`
		Code  string          `json:"code"`
		Spec  json.RawMessage `json:"spec"`
		Trace struct {
			Tries    int              `json:"tries"`
			Attempts []map[string]any `json:"attempts"`
		} `json:"trace"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Code != "COMPOSE_FAILED" || body.Error == "" {
		t.Errorf("expected clean error, got %+v", body)
	}
	// CRITICAL: no spec is ever returned on failure — the renderer never sees it.
	if len(body.Spec) != 0 {
		t.Errorf("a rejected compose must NOT return a spec, got %s", body.Spec)
	}
	if body.Trace.Tries != genui.DefaultMaxTries {
		t.Errorf("expected %d tries in trace, got %d", genui.DefaultMaxTries, body.Trace.Tries)
	}
}

func TestGenuiCompose_EmptyIntentIs400(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0"})
	rr := postCompose(t, srv, "   ")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty intent, got %d", rr.Code)
	}
}

func TestGenuiCompose_GETIsNotAllowed(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0"})
	req := httptest.NewRequest(http.MethodGet, "/api/command-center/genui/compose", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code == http.StatusOK {
		t.Fatalf("GET should not be allowed on compose, got %d", rr.Code)
	}
}

func TestGenuiCompose_RequiresAuthWhenTokenSet(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", Token: "secret-token"})
	body := `{"intent":"what's blocked"}`
	// Same-origin so the CSRF gate passes; no Bearer token so the AUTH gate
	// (token set) rejects with 401.
	req := httptest.NewRequest(http.MethodPost, "/api/command-center/genui/compose", strings.NewReader(body))
	req.RemoteAddr = "10.0.0.5:1234" // non-loopback
	req.Header.Set("Origin", "http://"+req.Host)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d body=%s", rr.Code, rr.Body.String())
	}
	// With the token -> ok.
	req2 := httptest.NewRequest(http.MethodPost, "/api/command-center/genui/compose", strings.NewReader(body))
	req2.RemoteAddr = "10.0.0.5:1234"
	req2.Header.Set("Origin", "http://"+req2.Host)
	req2.Header.Set("Authorization", "Bearer secret-token")
	rr2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("expected 200 with token, got %d body=%s", rr2.Code, rr2.Body.String())
	}
}

func TestGenuiCompose_BlocksCrossOrigin(t *testing.T) {
	// A POST from a different origin must be blocked by the CSRF gate (the
	// compose endpoint is behind the same envelope as every other write).
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", Token: "secret-token"})
	req := httptest.NewRequest(http.MethodPost, "/api/command-center/genui/compose", strings.NewReader(`{"intent":"x"}`))
	req.RemoteAddr = "10.0.0.5:1234"
	req.Header.Set("Origin", "http://evil.example.com")
	req.Header.Set("Authorization", "Bearer secret-token")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 cross-origin, got %d", rr.Code)
	}
}

// fakeRunnerComposer mutates (it is a *genui.SessionComposer), so the compose
// endpoint must gate it as a mutation — 403 in read-only, 200 when mutations
// are enabled. The injected runner returns a valid spec with NO live session.
func newFakeSessionComposer() *genui.SessionComposer {
	return &genui.SessionComposer{
		Session: "conductor-test",
		Run: func(_ context.Context, _ string, _ string) (string, error) {
			return `{"schema":1,"specId":"composed-blocked","title":"x","version":1,"root":{"type":"col","children":[{"type":"decision-list","bind":"decisionsWaiting"}]}}`, nil
		},
	}
}

func TestGenuiCompose_SessionComposerGatedInReadOnly(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", WebMutations: false})
	srv.genuiComposer = newFakeSessionComposer()
	rr := postCompose(t, srv, "what's blocked")
	if rr.Code != http.StatusForbidden {
		t.Fatalf("session composer in read-only must be 403, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestGenuiCompose_SessionComposerAllowedWhenMutationsEnabled(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", WebMutations: true})
	srv.genuiComposer = newFakeSessionComposer()
	rr := postCompose(t, srv, "what's blocked")
	if rr.Code != http.StatusOK {
		t.Fatalf("session composer with mutations enabled must be 200, got %d body=%s", rr.Code, rr.Body.String())
	}
}

// unknownComposer implements SpecComposer but NOT the optional Mutates()
// method — the endpoint must fail closed and gate it as a mutation.
type unknownComposer struct{}

func (unknownComposer) Name() string { return "unknown" }
func (unknownComposer) Compose(_ context.Context, _ genui.ComposeRequest) ([]byte, error) {
	return []byte(`{"schema":1,"specId":"x","title":"x","version":1,"root":{"type":"col","children":[{"type":"stat","bind":"totals.running"}]}}`), nil
}

func TestGenuiCompose_UnknownComposerGatedAsMutationByDefault(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", WebMutations: false})
	srv.genuiComposer = unknownComposer{}
	rr := postCompose(t, srv, "anything")
	if rr.Code != http.StatusForbidden {
		t.Fatalf("an unknown composer must fail closed (403 in read-only), got %d", rr.Code)
	}
}

func TestGenuiSpec_RequiresAuthWhenTokenSet(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", Token: "secret-token"})
	// No auth header -> unauthorized (non-loopback gate via token).
	req := httptest.NewRequest(http.MethodGet, "/api/command-center/genui/spec/status-board", nil)
	req.RemoteAddr = "10.0.0.5:1234" // non-loopback
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token from non-loopback, got %d", rr.Code)
	}
	// With the token -> ok.
	req2 := httptest.NewRequest(http.MethodGet, "/api/command-center/genui/spec/status-board", nil)
	req2.RemoteAddr = "10.0.0.5:1234"
	req2.Header.Set("Authorization", "Bearer secret-token")
	rr2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("expected 200 with token, got %d", rr2.Code)
	}
}
