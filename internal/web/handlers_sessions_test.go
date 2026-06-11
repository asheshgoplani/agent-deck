package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// fakeMutator is a test double for SessionMutator that delegates to function fields.
// If a function field is nil, the method returns an error indicating it is unconfigured.
type fakeMutator struct {
	createSessionFn    func(title, tool, projectPath, groupPath, modelID string) (string, error)
	startSessionFn     func(id string) error
	stopSessionFn      func(id string) error
	restartSessionFn   func(id string) error
	deleteSessionFn    func(id string) error
	closeSessionFn     func(id string) error
	archiveSessionFn   func(id string) error
	unarchiveSessionFn func(id string) error
	undoDeleteFn       func() (string, error)
	forkSessionFn      func(id string) (string, error)
	updateSessionFn    func(id string, updates map[string]string) ([]string, bool, error)
	createGroupFn      func(name, parentPath string) (string, error)
	renameGroupFn      func(groupPath, newName string) error
	deleteGroupFn      func(groupPath string) error
	finishWorktreeFn   func(id string, opts WorktreeFinishOptions) (WorktreeFinishResult, error)
}

func (f *fakeMutator) CreateSession(title, tool, projectPath, groupPath, modelID string) (string, error) {
	if f.createSessionFn == nil {
		return "", fmt.Errorf("createSession not configured")
	}
	return f.createSessionFn(title, tool, projectPath, groupPath, modelID)
}

func (f *fakeMutator) StartSession(id string) error {
	if f.startSessionFn == nil {
		return fmt.Errorf("startSession not configured")
	}
	return f.startSessionFn(id)
}

func (f *fakeMutator) StopSession(id string) error {
	if f.stopSessionFn == nil {
		return fmt.Errorf("stopSession not configured")
	}
	return f.stopSessionFn(id)
}

func (f *fakeMutator) RestartSession(id string) error {
	if f.restartSessionFn == nil {
		return fmt.Errorf("restartSession not configured")
	}
	return f.restartSessionFn(id)
}

func (f *fakeMutator) DeleteSession(id string) error {
	if f.deleteSessionFn == nil {
		return fmt.Errorf("deleteSession not configured")
	}
	return f.deleteSessionFn(id)
}

func (f *fakeMutator) CloseSession(id string) error {
	if f.closeSessionFn == nil {
		return fmt.Errorf("closeSession not configured")
	}
	return f.closeSessionFn(id)
}

func (f *fakeMutator) ArchiveSession(id string) error {
	if f.archiveSessionFn == nil {
		return fmt.Errorf("archiveSession not configured")
	}
	return f.archiveSessionFn(id)
}

func (f *fakeMutator) UnarchiveSession(id string) error {
	if f.unarchiveSessionFn == nil {
		return fmt.Errorf("unarchiveSession not configured")
	}
	return f.unarchiveSessionFn(id)
}

func (f *fakeMutator) UndoDelete() (string, error) {
	if f.undoDeleteFn == nil {
		return "", fmt.Errorf("undoDelete not configured")
	}
	return f.undoDeleteFn()
}

func (f *fakeMutator) ForkSession(id string) (string, error) {
	if f.forkSessionFn == nil {
		return "", fmt.Errorf("forkSession not configured")
	}
	return f.forkSessionFn(id)
}

func (f *fakeMutator) UpdateSession(id string, updates map[string]string) ([]string, bool, error) {
	if f.updateSessionFn == nil {
		return nil, false, fmt.Errorf("updateSession not configured")
	}
	return f.updateSessionFn(id, updates)
}

func (f *fakeMutator) CreateGroup(name, parentPath string) (string, error) {
	if f.createGroupFn == nil {
		return "", fmt.Errorf("createGroup not configured")
	}
	return f.createGroupFn(name, parentPath)
}

func (f *fakeMutator) RenameGroup(groupPath, newName string) error {
	if f.renameGroupFn == nil {
		return fmt.Errorf("renameGroup not configured")
	}
	return f.renameGroupFn(groupPath, newName)
}

func (f *fakeMutator) DeleteGroup(groupPath string) error {
	if f.deleteGroupFn == nil {
		return fmt.Errorf("deleteGroup not configured")
	}
	return f.deleteGroupFn(groupPath)
}

func (f *fakeMutator) FinishWorktree(id string, opts WorktreeFinishOptions) (WorktreeFinishResult, error) {
	if f.finishWorktreeFn == nil {
		return WorktreeFinishResult{}, fmt.Errorf("finishWorktree not configured")
	}
	return f.finishWorktreeFn(id, opts)
}

func TestSessionsCollectionGET(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
		Profile:    "test",
	})
	srv.menuData = &fakeMenuDataLoader{
		snapshot: &MenuSnapshot{
			Profile: "test",
			Items: []MenuItem{
				{
					Type: MenuItemTypeGroup,
					Group: &MenuGroup{
						Name: "work",
						Path: "work",
					},
				},
				{
					Type: MenuItemTypeSession,
					Session: &MenuSession{
						ID:     "sess-1",
						Title:  "alpha",
						Status: session.StatusRunning,
					},
				},
			},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"sessions"`) {
		t.Errorf("expected 'sessions' key in response, got: %s", body)
	}
	if !strings.Contains(body, `"groups"`) {
		t.Errorf("expected 'groups' key in response, got: %s", body)
	}
	if !strings.Contains(body, `"sess-1"`) {
		t.Errorf("expected session id in response, got: %s", body)
	}
}

func TestSessionsCollectionPOSTCreatesSession(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: true,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	srv.mutator = &fakeMutator{
		createSessionFn: func(title, tool, projectPath, groupPath, modelID string) (string, error) {
			return "new-id", nil
		},
	}

	body := strings.NewReader(`{"title":"Test","tool":"claude","projectPath":"/tmp"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d: %s", http.StatusCreated, rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "new-id") {
		t.Errorf("expected session id in response, got: %s", rr.Body.String())
	}
}

func TestSessionsCollectionPOSTForwardsModelID(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: true,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}

	var gotModel string
	srv.mutator = &fakeMutator{
		createSessionFn: func(title, tool, projectPath, groupPath, modelID string) (string, error) {
			gotModel = modelID
			return "new-id", nil
		},
	}

	body := strings.NewReader(`{"title":"Test","tool":"claude","projectPath":"/tmp","modelId":"claude-sonnet-4-6"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d: %s", http.StatusCreated, rr.Code, rr.Body.String())
	}
	if gotModel != "claude-sonnet-4-6" {
		t.Fatalf("modelID = %q, want %q", gotModel, "claude-sonnet-4-6")
	}
}

func TestSessionsCollectionPOSTNilMutatorReturns503(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: true,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	// mutator is nil

	body := strings.NewReader(`{"title":"Test","tool":"claude","projectPath":"/tmp"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status %d, got %d: %s", http.StatusServiceUnavailable, rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), ErrCodeNotImplemented) {
		t.Errorf("expected NOT_IMPLEMENTED error, got: %s", rr.Body.String())
	}
}

func TestSessionsCollectionPOSTMutationsDisabled(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: false,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}

	body := strings.NewReader(`{"title":"Test","tool":"claude","projectPath":"/tmp"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d: %s", http.StatusForbidden, rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), ErrCodeForbidden) {
		t.Errorf("expected MUTATIONS_DISABLED error, got: %s", rr.Body.String())
	}
}

func TestSessionCreateMissingTitle(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: true,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	srv.mutator = &fakeMutator{}

	body := strings.NewReader(`{"tool":"claude","projectPath":"/tmp"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d: %s", http.StatusBadRequest, rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), ErrCodeBadRequest) {
		t.Errorf("expected INVALID_REQUEST error, got: %s", rr.Body.String())
	}
}

func TestSessionCreateMissingPath(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: true,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	srv.mutator = &fakeMutator{}

	body := strings.NewReader(`{"title":"Test","tool":"claude"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d: %s", http.StatusBadRequest, rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), ErrCodeBadRequest) {
		t.Errorf("expected INVALID_REQUEST error, got: %s", rr.Body.String())
	}
}

func TestSessionStopOK(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: true,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	srv.mutator = &fakeMutator{
		stopSessionFn: func(id string) error { return nil },
	}

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/test-id/stop", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}
}

func TestSessionDeleteOK(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: true,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	srv.mutator = &fakeMutator{
		deleteSessionFn: func(id string) error { return nil },
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/sessions/test-id", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}
}

func TestSessionStartOK(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: true,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	srv.mutator = &fakeMutator{
		startSessionFn: func(id string) error { return nil },
	}

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/test-id/start", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}
}

func TestSessionRestartOK(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: true,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	srv.mutator = &fakeMutator{
		restartSessionFn: func(id string) error { return nil },
	}

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/test-id/restart", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}
}

func TestSessionForkOK(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: true,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	srv.mutator = &fakeMutator{
		forkSessionFn: func(id string) (string, error) { return "forked-id", nil },
	}

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/test-id/fork", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "forked-id") {
		t.Errorf("expected forked session id in response, got: %s", rr.Body.String())
	}
}

func TestSessionsUnauthorized(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
		Token:      "secret-token",
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}

	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d: %s", http.StatusUnauthorized, rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), ErrCodeUnauthorized) {
		t.Errorf("expected UNAUTHORIZED error, got: %s", rr.Body.String())
	}
}

func TestMutationNilMutatorReturns503(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: true,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	// mutator is nil

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/test-id/stop", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status %d, got %d: %s", http.StatusServiceUnavailable, rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), ErrCodeNotImplemented) {
		t.Errorf("expected NOT_IMPLEMENTED error, got: %s", rr.Body.String())
	}
}

// ----------------------------------------------------------------------------
// Non-destructive Close (POST /api/sessions/{id}/close) + Undo Delete
// (POST /api/sessions/undelete). Closes the two MISSING rows under
// "SESSION OPERATIONS" in tests/web/PARITY_MATRIX.md.
//
// Coverage per ~/.agent-deck/skills/pool/agent-deck-tdd-feature/SKILL.md:
//   - close: happy path, mutator wiring (mutations disabled / nil mutator
//     / underlying error), SSE notification.
//   - undo: happy path (roundtrip after delete), boundary (nothing on
//     stack → 404), boundary (entry expired → 404), nil mutator → 503.
// ----------------------------------------------------------------------------

func TestSessionCloseOK(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: true,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}

	var gotID string
	srv.mutator = &fakeMutator{
		closeSessionFn: func(id string) error {
			gotID = id
			return nil
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/test-id/close", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("close: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if gotID != "test-id" {
		t.Errorf("close: mutator saw id=%q, want %q", gotID, "test-id")
	}
	if !strings.Contains(rr.Body.String(), `"sessionId":"test-id"`) {
		t.Errorf("close: response missing sessionId field: %s", rr.Body.String())
	}
}

func TestSessionCloseMutationsDisabled(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: false,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/test-id/close", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("close (mutations disabled): expected 403, got %d", rr.Code)
	}
}

func TestSessionCloseNilMutatorReturns503(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: true,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/test-id/close", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("close (nil mutator): expected 503, got %d", rr.Code)
	}
}

func TestSessionCloseMutatorError(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: true,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	srv.mutator = &fakeMutator{
		closeSessionFn: func(id string) error { return fmt.Errorf("kill failed: signal 9") },
	}

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/test-id/close", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("close (mutator err): expected 500, got %d", rr.Code)
	}
}

func TestSessionCloseNotifiesSSE(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: true,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	srv.mutator = &fakeMutator{
		closeSessionFn: func(id string) error { return nil },
	}

	ch := srv.subscribeMenuChanges()
	defer srv.unsubscribeMenuChanges(ch)

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/whatever/close", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("close: status=%d", rr.Code)
	}

	select {
	case <-ch:
	case <-time.After(250 * time.Millisecond):
		t.Error("close: expected SSE notification within 250ms")
	}
}

// TestSessionDeleteUndoRoundtrip exercises the full delete → undelete
// flow against the fake mutator. Asserts both endpoints fire (with
// distinct paths) and that the restored id matches what undo returned.
func TestSessionDeleteUndoRoundtrip(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: true,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}

	var deletedID string
	var undoCalls int
	srv.mutator = &fakeMutator{
		deleteSessionFn: func(id string) error { deletedID = id; return nil },
		undoDeleteFn: func() (string, error) {
			undoCalls++
			return deletedID, nil
		},
	}

	// Delete.
	delReq := httptest.NewRequest(http.MethodDelete, "/api/sessions/sess-42", nil)
	delRR := httptest.NewRecorder()
	srv.Handler().ServeHTTP(delRR, delReq)
	if delRR.Code != http.StatusOK {
		t.Fatalf("delete: status=%d body=%s", delRR.Code, delRR.Body.String())
	}
	if deletedID != "sess-42" {
		t.Fatalf("delete: mutator saw id=%q, want sess-42", deletedID)
	}

	// Undo.
	undoReq := httptest.NewRequest(http.MethodPost, "/api/sessions/undelete", nil)
	undoRR := httptest.NewRecorder()
	srv.Handler().ServeHTTP(undoRR, undoReq)
	if undoRR.Code != http.StatusOK {
		t.Fatalf("undelete: status=%d body=%s", undoRR.Code, undoRR.Body.String())
	}
	if undoCalls != 1 {
		t.Fatalf("undelete: mutator called %d times, want 1", undoCalls)
	}
	if !strings.Contains(undoRR.Body.String(), `"sessionId":"sess-42"`) {
		t.Errorf("undelete: response missing restored sessionId: %s", undoRR.Body.String())
	}
}

func TestSessionUndoNothing(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: true,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	srv.mutator = &fakeMutator{
		undoDeleteFn: func() (string, error) { return "", ErrUndoNothing },
	}

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/undelete", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("undo nothing: expected 404, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), ErrCodeNotFound) {
		t.Errorf("undo nothing: expected NOT_FOUND code, got: %s", rr.Body.String())
	}
}

// TestSessionUndoExpiredReturns404 covers the boundary where the most
// recent delete is older than the undo window. Critical: tests the
// distinction between empty stack and stale stack — both must surface
// as 404 to the front-end (which then shows "nothing to undo").
func TestSessionUndoExpiredReturns404(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: true,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	srv.mutator = &fakeMutator{
		undoDeleteFn: func() (string, error) { return "", ErrUndoExpired },
	}

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/undelete", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("undo expired: expected 404, got %d", rr.Code)
	}
}

func TestSessionUndoMutatorError(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: true,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	srv.mutator = &fakeMutator{
		undoDeleteFn: func() (string, error) { return "", fmt.Errorf("restart failed: tmux missing") },
	}

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/undelete", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("undo internal err: expected 500, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestSessionUndoNilMutatorReturns503(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: true,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/undelete", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("undo nil mutator: expected 503, got %d", rr.Code)
	}
}

func TestSessionUndoMutationsDisabled(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: false,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/undelete", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("undo disabled: expected 403, got %d", rr.Code)
	}
}

func TestSessionUndoUnauthorized(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
		Token:      "secret-token",
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/undelete", nil)
	// Same-origin so the request clears CSRF (fail-closed when a token is set)
	// and reaches the auth check — the behavior under test.
	req.Header.Set("Origin", "http://example.com")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("undo unauthorized: expected 401, got %d", rr.Code)
	}
}

func TestMutationNotifiesSSE(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: true,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	srv.mutator = &fakeMutator{
		stopSessionFn: func(id string) error { return nil },
	}

	ch := srv.subscribeMenuChanges()
	defer srv.unsubscribeMenuChanges(ch)

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/test-id/stop", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}

	select {
	case <-ch:
		// notification received
	case <-time.After(250 * time.Millisecond):
		t.Error("expected SSE notification within 250ms, got none")
	}
}

// --- PATCH /api/sessions/{id} ---------------------------------------------
//
// Covers the surfaces in ~/.agent-deck/skills/pool/agent-deck-tdd-feature/
// SKILL.md per the matrix: happy path (title update), failure mode (validation
// error / not-found / unknown field), boundary case (empty body, empty title).
// Closes the "Edit session settings" MISSING row in tests/web/PARITY_MATRIX.md.

func TestSessionPatchUpdatesTitle(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: true,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}

	var gotID string
	var gotUpdates map[string]string
	srv.mutator = &fakeMutator{
		updateSessionFn: func(id string, updates map[string]string) ([]string, bool, error) {
			gotID = id
			gotUpdates = updates
			return []string{"title"}, false, nil
		},
	}

	body := strings.NewReader(`{"title":"renamed"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/sessions/sess-001", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}
	if gotID != "sess-001" {
		t.Errorf("expected id sess-001, got %q", gotID)
	}
	if gotUpdates["title"] != "renamed" {
		t.Errorf("expected updates[title]=renamed, got %q", gotUpdates["title"])
	}
	if !strings.Contains(rr.Body.String(), `"updatedFields":["title"]`) {
		t.Errorf("expected updatedFields in response, got: %s", rr.Body.String())
	}
}

func TestSessionPatchForwardsAllSupportedFields(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: true,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}

	var gotUpdates map[string]string
	srv.mutator = &fakeMutator{
		updateSessionFn: func(id string, updates map[string]string) ([]string, bool, error) {
			gotUpdates = updates
			// Echo back what we received as "changed" so the assertion can
			// verify field-name canonicalization (api → session.Field*).
			fields := make([]string, 0, len(updates))
			for k := range updates {
				fields = append(fields, k)
			}
			return fields, true, nil
		},
	}

	body := strings.NewReader(`{
	  "title": "x",
	  "notes": "hello",
	  "color": "#ff0000",
	  "tool": "claude",
	  "extraArgs": "--model opus",
	  "plugins": "octopus",
	  "channels": "telegram",
	  "skipPermissions": true,
	  "autoMode": false
	}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/sessions/sess-1", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}
	// All fields must be canonicalized to session.Field* constants.
	expected := map[string]string{
		session.FieldTitle:           "x",
		session.FieldNotes:           "hello",
		session.FieldColor:           "#ff0000",
		session.FieldTool:            "claude",
		session.FieldExtraArgs:       "--model opus",
		session.FieldPlugins:         "octopus",
		session.FieldChannels:        "telegram",
		session.FieldSkipPermissions: "true",
		session.FieldAutoMode:        "false",
	}
	for k, v := range expected {
		if gotUpdates[k] != v {
			t.Errorf("updates[%q] = %q, want %q", k, gotUpdates[k], v)
		}
	}
	if !strings.Contains(rr.Body.String(), `"restartRequired":true`) {
		t.Errorf("expected restartRequired=true in response, got: %s", rr.Body.String())
	}
}

func TestSessionPatchEmptyBodyRejected(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: true,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	srv.mutator = &fakeMutator{
		updateSessionFn: func(id string, updates map[string]string) ([]string, bool, error) {
			t.Fatal("mutator must not be called with no updates")
			return nil, false, nil
		},
	}

	body := strings.NewReader(`{}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/sessions/sess-1", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d: %s", http.StatusBadRequest, rr.Code, rr.Body.String())
	}
}

func TestSessionPatchEmptyTitleRejected(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: true,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	srv.mutator = &fakeMutator{
		updateSessionFn: func(id string, updates map[string]string) ([]string, bool, error) {
			t.Fatal("mutator must not be called for invalid input")
			return nil, false, nil
		},
	}

	body := strings.NewReader(`{"title":"   "}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/sessions/sess-1", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d: %s", http.StatusBadRequest, rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "title cannot be empty") {
		t.Errorf("expected title-empty error, got: %s", rr.Body.String())
	}
}

func TestSessionPatchMalformedJSONRejected(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: true,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	srv.mutator = &fakeMutator{}

	body := strings.NewReader(`{not-json`)
	req := httptest.NewRequest(http.MethodPatch, "/api/sessions/sess-1", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d: %s", http.StatusBadRequest, rr.Code, rr.Body.String())
	}
}

func TestSessionPatchMutationErrorReturns400(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: true,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	srv.mutator = &fakeMutator{
		updateSessionFn: func(id string, updates map[string]string) ([]string, bool, error) {
			return nil, false, &session.MutationError{Field: session.FieldColor, Msg: "invalid color \"bogus\""}
		},
	}

	body := strings.NewReader(`{"color":"bogus"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/sessions/sess-1", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d: %s", http.StatusBadRequest, rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "invalid color") {
		t.Errorf("expected mutation error message, got: %s", rr.Body.String())
	}
}

func TestSessionPatchNotFoundReturns404(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: true,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	srv.mutator = &fakeMutator{
		updateSessionFn: func(id string, updates map[string]string) ([]string, bool, error) {
			return nil, false, fmt.Errorf("session not found: %s", id)
		},
	}

	body := strings.NewReader(`{"title":"x"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/sessions/does-not-exist", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d: %s", http.StatusNotFound, rr.Code, rr.Body.String())
	}
}

func TestSessionPatchNilMutatorReturns503(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: true,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	// mutator is nil

	body := strings.NewReader(`{"title":"x"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/sessions/sess-1", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status %d, got %d: %s", http.StatusServiceUnavailable, rr.Code, rr.Body.String())
	}
}

func TestSessionPatchMutationsDisabledReturns403(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: false,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}

	body := strings.NewReader(`{"title":"x"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/sessions/sess-1", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d: %s", http.StatusForbidden, rr.Code, rr.Body.String())
	}
}

func TestSessionPatchNotifiesSSE(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: true,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	srv.mutator = &fakeMutator{
		updateSessionFn: func(id string, updates map[string]string) ([]string, bool, error) {
			return []string{"title"}, false, nil
		},
	}

	ch := srv.subscribeMenuChanges()
	defer srv.unsubscribeMenuChanges(ch)

	body := strings.NewReader(`{"title":"renamed"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/sessions/sess-1", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}
	select {
	case <-ch:
	case <-time.After(250 * time.Millisecond):
		t.Error("expected SSE notification on PATCH within 250ms, got none")
	}
}

// TestSessionPatchUnicodeAndLongTitle exercises the boundary case the
// SKILL.md mandates — emoji + multibyte chars + a long string. Verifies the
// JSON pipeline preserves bytes and the API doesn't truncate.
func TestSessionPatchUnicodeAndLongTitle(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: true,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}

	const newTitle = "🐙 重命名 — long unicode title with special chars: <>&\"'"
	var gotTitle string
	srv.mutator = &fakeMutator{
		updateSessionFn: func(id string, updates map[string]string) ([]string, bool, error) {
			gotTitle = updates[session.FieldTitle]
			return []string{session.FieldTitle}, false, nil
		},
	}

	payload, err := json.Marshal(map[string]string{"title": newTitle})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPatch, "/api/sessions/sess-1", strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}
	if gotTitle != newTitle {
		t.Errorf("title mangled: got %q want %q", gotTitle, newTitle)
	}
}

func TestSessionArchiveOK(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: true,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}

	var gotID string
	srv.mutator = &fakeMutator{
		archiveSessionFn: func(id string) error {
			gotID = id
			return nil
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/test-id/archive", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("archive: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if gotID != "test-id" {
		t.Errorf("archive: mutator saw id=%q, want %q", gotID, "test-id")
	}
}

func TestSessionUnarchiveOK(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: true,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}

	var gotID string
	srv.mutator = &fakeMutator{
		unarchiveSessionFn: func(id string) error {
			gotID = id
			return nil
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/test-id/unarchive", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("unarchive: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if gotID != "test-id" {
		t.Errorf("unarchive: mutator saw id=%q, want %q", gotID, "test-id")
	}
}

func TestArchivedSessionsList(t *testing.T) {
	archivedSnap := &MenuSnapshot{
		Profile:       "test",
		TotalSessions: 1,
		Items: []MenuItem{{
			Type: MenuItemTypeSession,
			Session: &MenuSession{
				ID:         "arch-1",
				Title:      "old",
				ArchivedAt: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
			},
		}},
	}
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0"})
	srv.menuData = &fakeMenuDataLoader{
		snapshot:         &MenuSnapshot{},
		archivedSnapshot: archivedSnap,
	}

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/archived", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("archived list: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"arch-1"`) {
		t.Errorf("archived list: expected arch-1 in body: %s", rr.Body.String())
	}
}
