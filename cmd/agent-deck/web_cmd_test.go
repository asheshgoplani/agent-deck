package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBuildWebServer_DefaultEnablesWebMutations(t *testing.T) {
	srv, err := buildWebServer("work", nil, nil)
	if err != nil {
		t.Fatalf("buildWebServer returned error: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}

	var resp struct {
		Profile      string `json:"profile"`
		ReadOnly     bool   `json:"readOnly"`
		WebMutations bool   `json:"webMutations"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode /api/settings response: %v", err)
	}

	if resp.Profile != "work" {
		t.Fatalf("expected profile %q, got %q", "work", resp.Profile)
	}
	if resp.ReadOnly {
		t.Fatalf("expected readOnly=false by default, got true")
	}
	if !resp.WebMutations {
		t.Fatalf("expected webMutations=true by default, got false")
	}

	postReq := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(`{"title":"Test","tool":"claude","projectPath":"/tmp"}`))
	postReq.Header.Set("Content-Type", "application/json")
	postRR := httptest.NewRecorder()
	srv.Handler().ServeHTTP(postRR, postReq)

	if postRR.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status %d when mutations are allowed but mutator is nil, got %d: %s", http.StatusServiceUnavailable, postRR.Code, postRR.Body.String())
	}
	if strings.Contains(postRR.Body.String(), "MUTATIONS_DISABLED") {
		t.Fatalf("expected mutations to be enabled by default, got forbidden response: %s", postRR.Body.String())
	}
}

func TestBuildWebServer_ReadOnlyDisablesWebMutations(t *testing.T) {
	srv, err := buildWebServer("work", []string{"--read-only"}, nil)
	if err != nil {
		t.Fatalf("buildWebServer returned error: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}

	var resp struct {
		ReadOnly     bool `json:"readOnly"`
		WebMutations bool `json:"webMutations"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode /api/settings response: %v", err)
	}

	if !resp.ReadOnly {
		t.Fatalf("expected readOnly=true with --read-only, got false")
	}
	if resp.WebMutations {
		t.Fatalf("expected webMutations=false with --read-only, got true")
	}

	postReq := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(`{"title":"Test","tool":"claude","projectPath":"/tmp"}`))
	postReq.Header.Set("Content-Type", "application/json")
	postRR := httptest.NewRecorder()
	srv.Handler().ServeHTTP(postRR, postReq)

	if postRR.Code != http.StatusForbidden {
		t.Fatalf("expected status %d when read-only disables mutations, got %d: %s", http.StatusForbidden, postRR.Code, postRR.Body.String())
	}
	if !strings.Contains(postRR.Body.String(), "web mutations are disabled") {
		t.Fatalf("expected mutations-disabled error, got: %s", postRR.Body.String())
	}
}
