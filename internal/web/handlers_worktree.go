// POST /api/sessions/{id}/worktree/finish — Web parity for the TUI's
// W/shift+w hotkey and the `agent-deck worktree finish` CLI. Closes the
// "Finish worktree" MISSING row in tests/web/PARITY_MATRIX.md (issue
// #1126).
package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// handleSessionWorktreeFinish implements POST /api/sessions/{id}/worktree/finish.
// Behavior contract is pinned by issue1126_worktree_finish_test.go.
func (s *Server) handleSessionWorktreeFinish(w http.ResponseWriter, r *http.Request, sessionID string) {
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, ErrCodeUnauthorized, "unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed")
		return
	}
	if !s.checkMutationsAllowed(w) {
		return
	}
	if !s.checkMutationRateLimit(w) {
		return
	}
	if s.mutator == nil {
		writeAPIError(w, http.StatusServiceUnavailable, ErrCodeNotImplemented, "mutations not available")
		return
	}

	var req WorktreeFinishRequest
	if r.Body != nil {
		raw, _ := io.ReadAll(r.Body)
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &req); err != nil {
				writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body")
				return
			}
		}
	}

	result, err := s.mutator.FinishWorktree(sessionID, WorktreeFinishOptions{
		Into:       req.Into,
		NoMerge:    req.NoMerge,
		KeepBranch: req.KeepBranch,
		Force:      req.Force,
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrSessionNotFound):
			writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, err.Error())
		case errors.Is(err, ErrNotAWorktree):
			writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error())
		default:
			writeAPIError(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error())
		}
		return
	}

	s.notifyMenuChanged()
	writeJSON(w, http.StatusOK, WorktreeFinishResponse{
		SessionID:     result.SessionID,
		Branch:        result.Branch,
		MergedInto:    result.MergedInto,
		Merged:        result.Merged,
		BranchDeleted: result.BranchDeleted,
	})
}
