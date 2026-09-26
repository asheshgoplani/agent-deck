package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// #2368: POST /api/sessions/{id}/move is the web side of the TUI's M and
// `agent-deck group move`.

func newMoveTestServer(mutations bool, mutator SessionMutator) *Server {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", WebMutations: mutations})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	if mutator != nil {
		srv.mutator = mutator
	}
	return srv
}

func postMove(srv *Server, id, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+id+"/move", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	return rr
}

func TestSessionMoveForwardsToMutator(t *testing.T) {
	var gotID, gotGroup string
	srv := newMoveTestServer(true, &fakeMutator{
		moveSessionFn: func(id, groupPath string) (string, bool, error) {
			gotID, gotGroup = id, groupPath
			return "work/frontend", true, nil
		},
	})

	rr := postMove(srv, "sess-001", `{"groupPath":"  work/frontend "}`)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if gotID != "sess-001" || gotGroup != "work/frontend" {
		t.Fatalf("mutator got id=%q group=%q", gotID, gotGroup)
	}
	body := rr.Body.String()
	for _, want := range []string{`"sessionId":"sess-001"`, `"groupPath":"work/frontend"`, `"restartRequired":true`} {
		if !strings.Contains(body, want) {
			t.Errorf("response %s missing %s", body, want)
		}
	}
}

func TestSessionMoveEmptyGroupReachesMutator(t *testing.T) {
	gotGroup := "unset"
	srv := newMoveTestServer(true, &fakeMutator{
		moveSessionFn: func(id, groupPath string) (string, bool, error) {
			gotGroup = groupPath
			return "my-sessions", false, nil
		},
	})
	if rr := postMove(srv, "sess-001", `{}`); rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if gotGroup != "" {
		t.Fatalf(`missing groupPath must reach the mutator as "" (root), got %q`, gotGroup)
	}
}

func TestSessionMoveNotFoundReturns404(t *testing.T) {
	srv := newMoveTestServer(true, &fakeMutator{
		moveSessionFn: func(id, groupPath string) (string, bool, error) {
			return "", false, fmt.Errorf("move %s: %w", id, ErrSessionNotFound)
		},
	})
	if rr := postMove(srv, "nope", `{"groupPath":"work"}`); rr.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s, want 404", rr.Code, rr.Body.String())
	}
}

func TestSessionMoveMutatorErrorReturns500(t *testing.T) {
	srv := newMoveTestServer(true, &fakeMutator{
		moveSessionFn: func(id, groupPath string) (string, bool, error) {
			return "", false, fmt.Errorf("save session: disk full")
		},
	})
	if rr := postMove(srv, "sess-001", `{"groupPath":"work"}`); rr.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s, want 500", rr.Code, rr.Body.String())
	}
}

func TestSessionMoveMalformedJSONReturns400(t *testing.T) {
	called := false
	srv := newMoveTestServer(true, &fakeMutator{
		moveSessionFn: func(id, groupPath string) (string, bool, error) {
			called = true
			return "", false, nil
		},
	})
	if rr := postMove(srv, "sess-001", `{"groupPath":`); rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", rr.Code, rr.Body.String())
	}
	if called {
		t.Fatal("mutator must not run on a malformed body")
	}
}

func TestSessionMoveMutationsDisabledReturns403(t *testing.T) {
	called := false
	srv := newMoveTestServer(false, &fakeMutator{
		moveSessionFn: func(id, groupPath string) (string, bool, error) {
			called = true
			return "", false, nil
		},
	})
	if rr := postMove(srv, "sess-001", `{"groupPath":"work"}`); rr.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s, want 403", rr.Code, rr.Body.String())
	}
	if called {
		t.Fatal("mutator must not run when mutations are disabled")
	}
}

func TestSessionMoveNilMutatorReturns503(t *testing.T) {
	srv := newMoveTestServer(true, nil)
	if rr := postMove(srv, "sess-001", `{"groupPath":"work"}`); rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s, want 503", rr.Code, rr.Body.String())
	}
}

func TestSessionMoveNotifiesSSE(t *testing.T) {
	srv := newMoveTestServer(true, &fakeMutator{
		moveSessionFn: func(id, groupPath string) (string, bool, error) { return groupPath, false, nil },
	})
	ch := srv.subscribeMenuChanges()
	defer srv.unsubscribeMenuChanges(ch)

	if rr := postMove(srv, "sess-001", `{"groupPath":"work"}`); rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	select {
	case <-ch:
	case <-time.After(250 * time.Millisecond):
		t.Error("expected SSE notification on move within 250ms, got none")
	}
}
