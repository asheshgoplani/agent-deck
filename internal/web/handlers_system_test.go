package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleSystemStats_OK(t *testing.T) {
	s := &Server{cfg: Config{}}
	req := httptest.NewRequest(http.MethodGet, "/api/system/stats", nil)
	w := httptest.NewRecorder()

	s.handleSystemStats(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// On a real system, at least memory should be available
	if _, ok := resp["memory"]; !ok {
		t.Log("warning: memory not available in test environment")
	}
}

func TestHandleSystemStats_MethodNotAllowed(t *testing.T) {
	s := &Server{cfg: Config{}}
	req := httptest.NewRequest(http.MethodPost, "/api/system/stats", nil)
	w := httptest.NewRecorder()

	s.handleSystemStats(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

func TestHandleSystemStats_Unauthorized(t *testing.T) {
	s := &Server{cfg: Config{Token: "secret"}}
	req := httptest.NewRequest(http.MethodGet, "/api/system/stats", nil)
	w := httptest.NewRecorder()

	s.handleSystemStats(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}
