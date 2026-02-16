package web

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeMenuDataLoader struct {
	snapshot *MenuSnapshot
	err      error
}

func (f *fakeMenuDataLoader) LoadMenuSnapshot() (*MenuSnapshot, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.snapshot, nil
}

func TestMenuEndpointSuccess(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
		Profile:    "test-profile",
	})
	srv.menuData = &fakeMenuDataLoader{
		snapshot: &MenuSnapshot{
			Profile:       "test-profile",
			GeneratedAt:   time.Date(2026, time.February, 16, 0, 0, 0, 0, time.UTC),
			TotalGroups:   1,
			TotalSessions: 1,
			Items: []MenuItem{
				{
					Index: 0,
					Type:  MenuItemTypeSession,
					Level: 1,
					Path:  "work",
					Session: &MenuSession{
						ID:     "sess-1",
						Title:  "demo",
						Tool:   "claude",
						Status: "running",
					},
				},
			},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/menu", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
	}

	body := rr.Body.String()
	if !strings.Contains(body, `"profile":"test-profile"`) {
		t.Fatalf("expected profile in response, got: %s", body)
	}
	if !strings.Contains(body, `"id":"sess-1"`) {
		t.Fatalf("expected session id in response, got: %s", body)
	}
}

func TestMenuEndpointMethodNotAllowed(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}

	req := httptest.NewRequest(http.MethodPost, "/api/menu", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status %d, got %d", http.StatusMethodNotAllowed, rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"code":"METHOD_NOT_ALLOWED"`) {
		t.Fatalf("expected METHOD_NOT_ALLOWED body, got: %s", rr.Body.String())
	}
}

func TestMenuEndpointUnauthorizedWhenTokenEnabled(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
		Token:      "secret-token",
	})
	srv.menuData = &fakeMenuDataLoader{
		snapshot: &MenuSnapshot{
			Profile: "default",
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/menu", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"code":"UNAUTHORIZED"`) {
		t.Fatalf("expected UNAUTHORIZED body, got: %s", rr.Body.String())
	}
}

func TestMenuEndpointAuthorizedWithBearerToken(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
		Token:      "secret-token",
		Profile:    "test-profile",
	})
	srv.menuData = &fakeMenuDataLoader{
		snapshot: &MenuSnapshot{
			Profile: "test-profile",
			Items: []MenuItem{
				{
					Type: MenuItemTypeSession,
					Session: &MenuSession{
						ID: "sess-1",
					},
				},
			},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/menu", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
	}
}

func TestSessionEndpointAuthorizedWithQueryToken(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
		Token:      "secret-token",
		Profile:    "work",
	})
	srv.menuData = &fakeMenuDataLoader{
		snapshot: &MenuSnapshot{
			Profile: "work",
			Items: []MenuItem{
				{
					Type: MenuItemTypeSession,
					Session: &MenuSession{
						ID: "sess-123",
					},
				},
			},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/session/sess-123?token=secret-token", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
	}
}

func TestSessionEndpointSuccess(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
		Profile:    "work",
	})
	srv.menuData = &fakeMenuDataLoader{
		snapshot: &MenuSnapshot{
			Profile: "work",
			Items: []MenuItem{
				{Index: 0, Type: MenuItemTypeGroup, Path: "work"},
				{
					Index: 1,
					Type:  MenuItemTypeSession,
					Level: 1,
					Path:  "work",
					Session: &MenuSession{
						ID:     "sess-123",
						Title:  "project-a",
						Tool:   "claude",
						Status: "running",
					},
				},
			},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/session/sess-123", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"profile":"work"`) {
		t.Fatalf("expected profile in response, got: %s", body)
	}
	if !strings.Contains(body, `"id":"sess-123"`) {
		t.Fatalf("expected session id in response, got: %s", body)
	}
}

func TestSessionEndpointNotFound(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
	})
	srv.menuData = &fakeMenuDataLoader{
		snapshot: &MenuSnapshot{
			Profile: "default",
			Items: []MenuItem{
				{
					Index: 0,
					Type:  MenuItemTypeSession,
					Session: &MenuSession{
						ID:    "sess-existing",
						Title: "existing",
					},
				},
			},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/session/sess-missing", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"code":"NOT_FOUND"`) {
		t.Fatalf("expected NOT_FOUND body, got: %s", rr.Body.String())
	}
}

func TestSessionEndpointMissingID(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
	})
	srv.menuData = &fakeMenuDataLoader{
		snapshot: &MenuSnapshot{Profile: "default"},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/session/", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"code":"INVALID_REQUEST"`) {
		t.Fatalf("expected INVALID_REQUEST body, got: %s", rr.Body.String())
	}
}

func TestMenuEndpointInternalError(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
	})
	srv.menuData = &fakeMenuDataLoader{
		err: errors.New("storage failed"),
	}

	req := httptest.NewRequest(http.MethodGet, "/api/menu", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected status %d, got %d", http.StatusInternalServerError, rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"code":"INTERNAL_ERROR"`) {
		t.Fatalf("expected INTERNAL_ERROR body, got: %s", rr.Body.String())
	}
}

func TestSessionEndpointInternalError(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
	})
	srv.menuData = &fakeMenuDataLoader{
		err: errors.New("storage failed"),
	}

	req := httptest.NewRequest(http.MethodGet, "/api/session/sess-1", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected status %d, got %d", http.StatusInternalServerError, rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"code":"INTERNAL_ERROR"`) {
		t.Fatalf("expected INTERNAL_ERROR body, got: %s", rr.Body.String())
	}
}
