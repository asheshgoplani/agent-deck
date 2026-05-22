package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCSRF_AllowsSameOriginPost(t *testing.T) {
	handler := csrfProtect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8420/api/sessions", strings.NewReader("{}"))
	req.Header.Set("Origin", "http://127.0.0.1:8420")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("same-origin POST should be allowed, got %d", rec.Code)
	}
}

func TestCSRF_BlocksCrossOriginPost(t *testing.T) {
	handler := csrfProtect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8420/api/sessions", strings.NewReader("{}"))
	req.Header.Set("Origin", "http://evil.com")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-origin POST should be blocked, got %d", rec.Code)
	}
}

func TestCSRF_BlocksTextPlainCrossOrigin(t *testing.T) {
	handler := csrfProtect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8420/api/sessions", strings.NewReader(`{"title":"x","tool":"id","projectPath":"/tmp"}`))
	req.Header.Set("Origin", "http://attacker.example.com")
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("text/plain cross-origin POST should be blocked, got %d", rec.Code)
	}
}

func TestCSRF_AllowsGetWithCrossOrigin(t *testing.T) {
	handler := csrfProtect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8420/api/sessions", nil)
	req.Header.Set("Origin", "http://evil.com")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("GET should be allowed regardless of origin, got %d", rec.Code)
	}
}

func TestCSRF_AllowsNoOriginNoReferer(t *testing.T) {
	handler := csrfProtect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8420/api/sessions", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("POST without Origin/Referer (non-browser client) should be allowed, got %d", rec.Code)
	}
}

func TestCSRF_AllowsSameOriginRefererFallback(t *testing.T) {
	handler := csrfProtect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8420/api/sessions", strings.NewReader("{}"))
	req.Header.Set("Referer", "http://127.0.0.1:8420/dashboard")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("same-origin Referer should be allowed, got %d", rec.Code)
	}
}

func TestCSRF_BlocksCrossOriginReferer(t *testing.T) {
	handler := csrfProtect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8420/api/sessions", strings.NewReader("{}"))
	req.Header.Set("Referer", "http://evil.com/exploit.html")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-origin Referer should be blocked, got %d", rec.Code)
	}
}

func TestCSRF_BlocksDeleteCrossOrigin(t *testing.T) {
	handler := csrfProtect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodDelete, "http://127.0.0.1:8420/api/session/abc123", nil)
	req.Header.Set("Origin", "http://evil.com")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-origin DELETE should be blocked, got %d", rec.Code)
	}
}

func TestCSRF_BlocksInvalidOriginURL(t *testing.T) {
	handler := csrfProtect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8420/api/sessions", strings.NewReader("{}"))
	req.Header.Set("Origin", "not-a-valid-url")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("invalid Origin URL should be blocked, got %d", rec.Code)
	}
}
