package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A session action on an unknown id answered 500 INTERNAL_ERROR for stop,
// close, start, restart, fork and DELETE, and 404 NOT_FOUND for archive,
// unarchive and PATCH. Every session route answers 404 with the same shape.
func TestSessionRoutes_UnknownSessionIs404(t *testing.T) {
	notFound := func(id string) error { return fmt.Errorf("%w: %s", ErrSessionNotFound, id) }
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", WebMutations: true})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	srv.mutator = &fakeMutator{
		startSessionFn:     notFound,
		stopSessionFn:      notFound,
		restartSessionFn:   notFound,
		deleteSessionFn:    notFound,
		closeSessionFn:     notFound,
		archiveSessionFn:   notFound,
		unarchiveSessionFn: notFound,
		forkSessionFn:      func(id string) (string, error) { return "", notFound(id) },
		updateSessionFn: func(id string, _ map[string]string) ([]string, bool, error) {
			return nil, false, notFound(id)
		},
	}

	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/sessions/nope/stop", ""},
		{http.MethodPost, "/api/sessions/nope/close", ""},
		{http.MethodPost, "/api/sessions/nope/start", ""},
		{http.MethodPost, "/api/sessions/nope/restart", ""},
		{http.MethodPost, "/api/sessions/nope/fork", ""},
		{http.MethodPost, "/api/sessions/nope/archive", ""},
		{http.MethodPost, "/api/sessions/nope/unarchive", ""},
		{http.MethodDelete, "/api/sessions/nope", ""},
		{http.MethodPatch, "/api/sessions/nope", `{"title":"x"}`},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			req := newLocalRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rr, req)
			if rr.Code != http.StatusNotFound {
				t.Fatalf("status %d, want 404: %s", rr.Code, rr.Body.String())
			}
			for _, want := range []string{`"code":"NOT_FOUND"`, `"message":"session not found: nope"`} {
				if !strings.Contains(rr.Body.String(), want) {
					t.Errorf("body %s lacks %s", rr.Body.String(), want)
				}
			}
		})
	}
}

// Other failures of the same routes stay 500.
func TestSessionRoutes_OtherFailureStays500(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", WebMutations: true})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	srv.mutator = &fakeMutator{stopSessionFn: func(string) error { return fmt.Errorf("tmux: command not found") }}

	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, newLocalRequest(http.MethodPost, "/api/sessions/s1/stop", nil))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500: %s", rr.Code, rr.Body.String())
	}
}

// DELETE /api/groups/{path} answered 200 {"deleted": path} for a group that
// does not exist. It is 404, as for group rename and collapse.
func TestGroupDelete_MissingGroupIs404(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", WebMutations: true})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	srv.mutator = &fakeMutator{deleteGroupFn: func(string) error { return ErrGroupNotFound }}

	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, newLocalRequest(http.MethodDelete, "/api/groups/totally-nope", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404: %s", rr.Code, rr.Body.String())
	}
	for _, want := range []string{`"code":"NOT_FOUND"`, `"message":"group not found"`} {
		if !strings.Contains(rr.Body.String(), want) {
			t.Errorf("body %s lacks %s", rr.Body.String(), want)
		}
	}
}

// Archiving an archived session is the caller's mistake (400), as unarchiving
// a session that is not archived already is; the CLI refuses it too.
func TestSessionArchive_AlreadyArchivedIs400(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", WebMutations: true})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	srv.mutator = &fakeMutator{archiveSessionFn: func(id string) error { return fmt.Errorf("session is already archived: %s", id) }}

	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, newLocalRequest(http.MethodPost, "/api/sessions/xi/archive", nil))
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "already archived") {
		t.Fatalf("status %d, want 400 already archived: %s", rr.Code, rr.Body.String())
	}
}
