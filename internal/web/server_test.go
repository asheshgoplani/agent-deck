package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHealthzEndpoint(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
		Profile:    "test",
		ReadOnly:   true,
	})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
	}

	body := rr.Body.String()
	if !strings.Contains(body, `"ok":true`) {
		t.Fatalf("expected health response to contain ok=true, got: %s", body)
	}
	if !strings.Contains(body, `"profile":"test"`) {
		t.Fatalf("expected health response to contain profile, got: %s", body)
	}
}

func TestHealthzMethodNotAllowed(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
	})

	req := httptest.NewRequest(http.MethodPost, "/healthz", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status %d, got %d", http.StatusMethodNotAllowed, rr.Code)
	}
}

func TestIndexServed(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
	}

	contentType := rr.Header().Get("Content-Type")
	if !strings.Contains(contentType, "text/html") {
		t.Fatalf("expected html content-type, got: %s", contentType)
	}
	if !strings.Contains(rr.Body.String(), "Agent Deck") {
		t.Fatalf("expected Agent Deck in html body, got: %s", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "filter-status") {
		t.Fatalf("expected status filter in dashboard html, got: %s", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "manifest.webmanifest") {
		t.Fatalf("expected pwa manifest link in html, got: %s", rr.Body.String())
	}
}

func TestSessionRouteServed(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
	})

	// /s/ routes serve the dashboard (SPA catch-all)
	req := httptest.NewRequest(http.MethodGet, "/s/sess-123", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
	}

	contentType := rr.Header().Get("Content-Type")
	if !strings.Contains(contentType, "text/html") {
		t.Fatalf("expected html content-type, got: %s", contentType)
	}
	if !strings.Contains(rr.Body.String(), "Agent Deck") {
		t.Fatalf("expected Agent Deck in html body, got: %s", rr.Body.String())
	}
}

func TestTerminalRouteServed(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
	})

	req := httptest.NewRequest(http.MethodGet, "/terminal", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
	}

	contentType := rr.Header().Get("Content-Type")
	if !strings.Contains(contentType, "text/html") {
		t.Fatalf("expected html content-type, got: %s", contentType)
	}
	if !strings.Contains(rr.Body.String(), "Agent Deck Web") {
		t.Fatalf("expected terminal html body with Agent Deck Web, got: %s", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "menu-filter") {
		t.Fatalf("expected menu-filter in terminal html, got: %s", rr.Body.String())
	}
}

func TestStaticCSSServed(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
	})

	req := httptest.NewRequest(http.MethodGet, "/static/styles.css", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
	}

	if !strings.Contains(rr.Body.String(), "--accent") {
		t.Fatalf("expected css payload, got: %s", rr.Body.String())
	}
}

func TestManifestServed(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
	})

	req := httptest.NewRequest(http.MethodGet, "/manifest.webmanifest", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
	}

	contentType := rr.Header().Get("Content-Type")
	if !strings.Contains(contentType, "application/manifest+json") {
		t.Fatalf("expected manifest content-type, got: %s", contentType)
	}
	if !strings.Contains(rr.Body.String(), "\"name\": \"Agent Deck Web\"") {
		t.Fatalf("expected manifest payload, got: %s", rr.Body.String())
	}
}

func TestServiceWorkerServed(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
	})

	req := httptest.NewRequest(http.MethodGet, "/sw.js", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
	}

	contentType := rr.Header().Get("Content-Type")
	if !strings.Contains(contentType, "application/javascript") {
		t.Fatalf("expected javascript content-type, got: %s", contentType)
	}

	swScope := rr.Header().Get("Service-Worker-Allowed")
	if swScope != "/" {
		t.Fatalf("expected Service-Worker-Allowed=/, got: %q", swScope)
	}

	if !strings.Contains(rr.Body.String(), "CACHE_VERSION") {
		t.Fatalf("expected service worker payload, got: %s", rr.Body.String())
	}
}

func TestMenuChangeBroadcastNotifiesAllSubscribers(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
	})

	subA := srv.subscribeMenuChanges()
	subB := srv.subscribeMenuChanges()
	defer srv.unsubscribeMenuChanges(subA)
	defer srv.unsubscribeMenuChanges(subB)

	srv.notifyMenuChanged()

	select {
	case <-subA:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("expected subscriber A to receive menu change signal")
	}

	select {
	case <-subB:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("expected subscriber B to receive menu change signal")
	}
}
