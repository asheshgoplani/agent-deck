package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
	if !strings.Contains(rr.Body.String(), "Agent Deck Web") {
		t.Fatalf("expected shell html body, got: %s", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "menu-filter") {
		t.Fatalf("expected filter input in shell html, got: %s", rr.Body.String())
	}
}

func TestSessionRouteServed(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
	})

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
	if !strings.Contains(rr.Body.String(), "Agent Deck Web") {
		t.Fatalf("expected shell html body, got: %s", rr.Body.String())
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
