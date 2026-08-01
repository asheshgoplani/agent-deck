package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSettingsGET(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		Profile:      "work",
		ReadOnly:     true,
		WebMutations: false,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}

	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"profile"`) {
		t.Errorf("expected 'profile' key, got: %s", body)
	}
	if !strings.Contains(body, `"readOnly"`) {
		t.Errorf("expected 'readOnly' key, got: %s", body)
	}
	if !strings.Contains(body, `"webMutations"`) {
		t.Errorf("expected 'webMutations' key, got: %s", body)
	}
	if !strings.Contains(body, `"version"`) {
		t.Errorf("expected 'version' key, got: %s", body)
	}
	if !strings.Contains(body, `"work"`) {
		t.Errorf("expected profile value 'work', got: %s", body)
	}
}

func TestSettingsMethodNotAllowed(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}

	req := httptest.NewRequest(http.MethodPost, "/api/settings", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status %d, got %d: %s", http.StatusMethodNotAllowed, rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), ErrCodeMethodNotAllowed) {
		t.Errorf("expected METHOD_NOT_ALLOWED error, got: %s", rr.Body.String())
	}
}

func TestSettingsUnauthorized(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
		Token:      "secret-token",
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}

	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d: %s", http.StatusUnauthorized, rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), ErrCodeUnauthorized) {
		t.Errorf("expected UNAUTHORIZED error, got: %s", rr.Body.String())
	}
}

func TestSettingsWebMutationsTrue(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: true,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}

	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"webMutations":true`) {
		t.Errorf("expected webMutations:true, got: %s", rr.Body.String())
	}
}

func TestProfilesGET(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
		Profile:    "work",
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}

	req := httptest.NewRequest(http.MethodGet, "/api/profiles", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"current"`) {
		t.Errorf("expected 'current' key, got: %s", body)
	}
	if !strings.Contains(body, `"profiles"`) {
		t.Errorf("expected 'profiles' key, got: %s", body)
	}
	if !strings.Contains(body, `"work"`) {
		t.Errorf("expected profile value 'work', got: %s", body)
	}
}

func TestProfilesMethodNotAllowed(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}

	req := httptest.NewRequest(http.MethodPost, "/api/profiles", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status %d, got %d: %s", http.StatusMethodNotAllowed, rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), ErrCodeMethodNotAllowed) {
		t.Errorf("expected METHOD_NOT_ALLOWED error, got: %s", rr.Body.String())
	}
}

func TestProfilesUnauthorized(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
		Token:      "secret-token",
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}

	req := httptest.NewRequest(http.MethodGet, "/api/profiles", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d: %s", http.StatusUnauthorized, rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), ErrCodeUnauthorized) {
		t.Errorf("expected UNAUTHORIZED error, got: %s", rr.Body.String())
	}
}

// Issue #1682: GET /api/settings carries the terminal link-open policy so the
// web UI can skip the confirm for trusted hosts. A Config that never set
// ConfirmLinkOpen must still report the confirm ON — a false there would
// silently disable the prompt for every host.
func TestSettingsLinkPolicy_DefaultsToConfirmOn(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", Profile: "default"})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}

	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}
	var got SettingsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	if !got.ConfirmLinkOpen {
		t.Error("confirmLinkOpen = false with ConfirmLinkOpen unset, want true (confirm stays on)")
	}
	if len(got.TrustedDomains) != 0 {
		t.Errorf("trustedDomains = %v, want empty when unconfigured", got.TrustedDomains)
	}
}

func TestSettingsLinkPolicy_ServesConfiguredAllowlistAndToggle(t *testing.T) {
	off := false
	srv := NewServer(Config{
		ListenAddr:      "127.0.0.1:0",
		Profile:         "default",
		TrustedDomains:  []string{"gitlab.corp.example", "*.ci.corp.example"},
		ConfirmLinkOpen: &off,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}

	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}
	var got SettingsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	if got.ConfirmLinkOpen {
		t.Error("confirmLinkOpen = true, want false when ConfirmLinkOpen is explicitly false")
	}
	want := []string{"gitlab.corp.example", "*.ci.corp.example"}
	if len(got.TrustedDomains) != len(want) {
		t.Fatalf("trustedDomains = %v, want %v", got.TrustedDomains, want)
	}
	for i := range want {
		if got.TrustedDomains[i] != want[i] {
			t.Errorf("trustedDomains[%d] = %q, want %q", i, got.TrustedDomains[i], want[i])
		}
	}
}
