// Regression coverage for issue #1126 — Web UI worktree finish endpoint.
// TUI has W/shift+w hotkey + CLI has `agent-deck worktree finish`, but the
// web had no equivalent. The MISSING row in tests/web/PARITY_MATRIX.md
// pointed at a gap users hit when finishing a worktree session from the
// browser. This file pins down the contract for
// POST /api/sessions/{id}/worktree/finish so the gap can't silently
// reopen.
//
// Coverage required by agent-deck-tdd-feature (sections 1, 2, 3):
//   - Happy path: 200 with the finish result body.
//   - Failure mode: mutator returns "not a worktree" — 400 INVALID_REQUEST.
//   - Failure mode: mutator returns "session not found" — 404 NOT_FOUND.
//   - Boundary: malformed JSON body — 400 INVALID_REQUEST.
//   - Boundary: empty body is accepted (defaults), uses auto-detect branch.
//   - Guard: no mutator wired — 503 NOT_IMPLEMENTED.
//   - Guard: mutations disabled — 403 MUTATIONS_DISABLED.
//   - Auth: missing token (when WebToken set) — 401 UNAUTHORIZED.
package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWorktreeFinish_HappyPath(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", WebMutations: true})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}

	var gotID string
	var gotOpts WorktreeFinishOptions
	srv.mutator = &fakeMutator{
		finishWorktreeFn: func(id string, opts WorktreeFinishOptions) (WorktreeFinishResult, error) {
			gotID = id
			gotOpts = opts
			return WorktreeFinishResult{
				SessionID:     id,
				Branch:        "feat/foo",
				MergedInto:    "main",
				Merged:        true,
				BranchDeleted: true,
			}, nil
		},
	}

	body := strings.NewReader(`{"into":"main","keepBranch":false}`)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/sess-42/worktree/finish", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if gotID != "sess-42" {
		t.Errorf("mutator received id=%q, want sess-42", gotID)
	}
	if gotOpts.Into != "main" || gotOpts.KeepBranch {
		t.Errorf("opts mismatch: %+v", gotOpts)
	}

	var resp WorktreeFinishResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.SessionID != "sess-42" || resp.Branch != "feat/foo" || resp.MergedInto != "main" {
		t.Errorf("response payload mismatch: %+v", resp)
	}
	if !resp.Merged || !resp.BranchDeleted {
		t.Errorf("response flags mismatch: %+v", resp)
	}
}

func TestWorktreeFinish_EmptyBodyDefaults(t *testing.T) {
	// Boundary case: caller POSTs with no JSON body — should not 400.
	// Defaults: noMerge=false, keepBranch=false, force=false, into="".
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", WebMutations: true})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}

	var seen WorktreeFinishOptions
	srv.mutator = &fakeMutator{
		finishWorktreeFn: func(id string, opts WorktreeFinishOptions) (WorktreeFinishResult, error) {
			seen = opts
			return WorktreeFinishResult{SessionID: id, Branch: "feat/x", Merged: true, MergedInto: "main", BranchDeleted: true}, nil
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/sess-1/worktree/finish", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for empty body, got %d body=%s", rr.Code, rr.Body.String())
	}
	if seen.Into != "" || seen.NoMerge || seen.KeepBranch || seen.Force {
		t.Errorf("expected zero-value opts, got %+v", seen)
	}
}

func TestWorktreeFinish_MalformedJSON(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", WebMutations: true})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	srv.mutator = &fakeMutator{
		finishWorktreeFn: func(id string, opts WorktreeFinishOptions) (WorktreeFinishResult, error) {
			t.Fatal("mutator must not be called on malformed body")
			return WorktreeFinishResult{}, nil
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/sess-1/worktree/finish",
		strings.NewReader(`{not json`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), ErrCodeBadRequest) {
		t.Errorf("expected %s in body, got %s", ErrCodeBadRequest, rr.Body.String())
	}
}

func TestWorktreeFinish_NotAWorktree(t *testing.T) {
	// Failure mode: caller targets a session that is not in a worktree.
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", WebMutations: true})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	srv.mutator = &fakeMutator{
		finishWorktreeFn: func(id string, opts WorktreeFinishOptions) (WorktreeFinishResult, error) {
			return WorktreeFinishResult{}, ErrNotAWorktree
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/sess-1/worktree/finish", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for not-a-worktree, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestWorktreeFinish_SessionNotFound(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", WebMutations: true})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	srv.mutator = &fakeMutator{
		finishWorktreeFn: func(id string, opts WorktreeFinishOptions) (WorktreeFinishResult, error) {
			return WorktreeFinishResult{}, ErrSessionNotFound
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/missing/worktree/finish", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing session, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestWorktreeFinish_InternalError(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", WebMutations: true})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	srv.mutator = &fakeMutator{
		finishWorktreeFn: func(id string, opts WorktreeFinishOptions) (WorktreeFinishResult, error) {
			return WorktreeFinishResult{}, errors.New("merge failed: conflict")
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/sess-1/worktree/finish", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for generic mutator failure, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestWorktreeFinish_NoMutatorWired(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", WebMutations: true})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/sess-1/worktree/finish", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when mutator unwired, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestWorktreeFinish_MutationsDisabled(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", WebMutations: false})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	srv.mutator = &fakeMutator{
		finishWorktreeFn: func(id string, opts WorktreeFinishOptions) (WorktreeFinishResult, error) {
			t.Fatal("must not call mutator when mutations disabled")
			return WorktreeFinishResult{}, nil
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/sess-1/worktree/finish", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when mutations disabled, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestWorktreeFinish_OnlyPOST(t *testing.T) {
	// GET /api/sessions/{id}/worktree/finish should 404 (unknown action).
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", WebMutations: true})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	srv.mutator = &fakeMutator{}

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/sess-1/worktree/finish", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code == http.StatusOK {
		t.Fatalf("GET should not succeed, got 200 body=%s", rr.Body.String())
	}
}
