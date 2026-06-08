package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

type sessionsListResponse struct {
	Sessions []*MenuSession `json:"sessions"`
	Groups   []*MenuGroup   `json:"groups"`
	Profile  string         `json:"profile"`
}

func (s *Server) handleSessionsCollection(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, ErrCodeUnauthorized, "unauthorized")
		return
	}

	switch r.Method {
	case http.MethodGet:
		snapshot, err := s.menuData.LoadMenuSnapshot()
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to load session data")
			return
		}
		// Reapply hook fast-path Status mapping so the web matches the CLI
		// even when the TUI's inotify-driven snapshot has fallen out of date.
		// See snapshot_hook_refresh.go for the rationale.
		refreshSnapshotHookStatuses(snapshot, s.hookStatusLoader)
		resp := sessionsListResponse{
			Sessions: make([]*MenuSession, 0),
			Groups:   make([]*MenuGroup, 0),
			Profile:  snapshot.Profile,
		}
		for _, item := range snapshot.Items {
			if item.Type == MenuItemTypeSession && item.Session != nil {
				resp.Sessions = append(resp.Sessions, item.Session)
			} else if item.Type == MenuItemTypeGroup && item.Group != nil {
				resp.Groups = append(resp.Groups, item.Group)
			}
		}
		writeJSON(w, http.StatusOK, resp)

	case http.MethodPost:
		if !s.checkMutationsAllowed(w) {
			return
		}
		if !s.checkMutationRateLimit(w) {
			return
		}
		var req CreateSessionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body")
			return
		}
		if req.Title == "" {
			writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, "title is required")
			return
		}
		if req.ProjectPath == "" {
			writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, "projectPath is required")
			return
		}
		if s.mutator == nil {
			writeAPIError(w, http.StatusServiceUnavailable, ErrCodeNotImplemented, "mutations not available")
			return
		}
		sessionID, err := s.mutator.CreateSession(req.Title, req.Tool, req.ProjectPath, req.GroupPath, req.ModelID)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error())
			return
		}
		s.notifyMenuChanged()
		writeJSON(w, http.StatusCreated, SessionActionResponse{SessionID: sessionID})

	default:
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleSessionByAction(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, ErrCodeUnauthorized, "unauthorized")
		return
	}

	// Path: /api/sessions/{id} or /api/sessions/{id}/{action}
	const prefix = "/api/sessions/"
	rest := strings.TrimPrefix(r.URL.Path, prefix)
	parts := strings.SplitN(rest, "/", 2)
	sessionID := parts[0]
	if sessionID == "" {
		writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, "session id is required")
		return
	}

	action := ""
	if len(parts) == 2 {
		action = parts[1]
	}

	// Skills sub-routes: /api/sessions/{id}/skills            (GET)
	//                    /api/sessions/{id}/skills/{name}     (POST/DELETE)
	if action == "skills" || strings.HasPrefix(action, "skills/") {
		sub := strings.TrimPrefix(action, "skills")
		sub = strings.TrimPrefix(sub, "/")
		s.handleSessionSkills(w, r, sessionID, sub)
		return
	}

	// Children sub-route: GET /api/sessions/{id}/children
	if isChildrenAction(action) {
		s.handleSessionChildren(w, r, sessionID)
		return
	}

	// Worktree sub-route: POST /api/sessions/{id}/worktree/finish
	// (issue #1126 — closes the "Finish worktree" MISSING row in
	// tests/web/PARITY_MATRIX.md, mirrors TUI W/shift+w + CLI
	// `agent-deck worktree finish`).
	if action == "worktree/finish" {
		s.handleSessionWorktreeFinish(w, r, sessionID)
		return
	}

	// PATCH /api/sessions/{id} — partial field edit (matches TUI EditSessionDialog).
	if r.Method == http.MethodPatch && action == "" {
		s.handleSessionPatch(w, r, sessionID)
		return
	}

	// DELETE /api/sessions/{id}
	if r.Method == http.MethodDelete && action == "" {
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
		if err := s.mutator.DeleteSession(sessionID); err != nil {
			writeAPIError(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error())
			return
		}
		s.notifyMenuChanged()
		writeJSON(w, http.StatusOK, map[string]string{"deleted": sessionID})
		return
	}

	// POST /api/sessions/{id}/{action}
	if r.Method == http.MethodPost {
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
		switch action {
		case "stop":
			if err := s.mutator.StopSession(sessionID); err != nil {
				writeAPIError(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error())
				return
			}
			s.notifyMenuChanged()
			writeJSON(w, http.StatusOK, SessionActionResponse{SessionID: sessionID})
		case "close":
			// Non-destructive close: stop process, keep metadata. Mirrors
			// the TUI Shift+D handler (closes the "Close session" MISSING
			// row in tests/web/PARITY_MATRIX.md).
			if err := s.mutator.CloseSession(sessionID); err != nil {
				writeAPIError(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error())
				return
			}
			s.notifyMenuChanged()
			writeJSON(w, http.StatusOK, SessionActionResponse{SessionID: sessionID})
		case "start":
			if err := s.mutator.StartSession(sessionID); err != nil {
				writeAPIError(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error())
				return
			}
			s.notifyMenuChanged()
			writeJSON(w, http.StatusOK, SessionActionResponse{SessionID: sessionID})
		case "restart":
			if err := s.mutator.RestartSession(sessionID); err != nil {
				writeAPIError(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error())
				return
			}
			s.notifyMenuChanged()
			writeJSON(w, http.StatusOK, SessionActionResponse{SessionID: sessionID})
		case "fork":
			newID, err := s.mutator.ForkSession(sessionID)
			if err != nil {
				writeAPIError(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error())
				return
			}
			s.notifyMenuChanged()
			writeJSON(w, http.StatusOK, SessionActionResponse{SessionID: newID})
		case "archive":
			if err := s.mutator.ArchiveSession(sessionID); err != nil {
				if strings.Contains(err.Error(), "not found") {
					writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, err.Error())
					return
				}
				writeAPIError(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error())
				return
			}
			s.notifyMenuChanged()
			writeJSON(w, http.StatusOK, SessionActionResponse{SessionID: sessionID})
		case "unarchive":
			if err := s.mutator.UnarchiveSession(sessionID); err != nil {
				if strings.Contains(err.Error(), "not found") {
					writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, err.Error())
					return
				}
				if strings.Contains(err.Error(), "not archived") {
					writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error())
					return
				}
				writeAPIError(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error())
				return
			}
			s.notifyMenuChanged()
			writeJSON(w, http.StatusOK, SessionActionResponse{SessionID: sessionID})
		default:
			writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, "unknown session action")
		}
		return
	}

	writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, "route not found")
}

// undeleteResponse is the JSON body returned from POST /api/sessions/undelete.
type undeleteResponse struct {
	SessionID string `json:"sessionId"`
}

// handleSessionUndelete is POST /api/sessions/undelete — Chrome-style undo
// of the most recent delete. Mirrors the TUI's ctrl+z handler. Closes the
// "Undo delete" MISSING row in tests/web/PARITY_MATRIX.md.
//
//   - 401 if unauthorized
//   - 403 if mutations are disabled
//   - 503 if no mutator is wired
//   - 404 if the undo stack is empty OR the entry expired (the front-end
//     can surface either as "nothing to undo")
//   - 200 with the restored sessionId on success
func (s *Server) handleSessionUndelete(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, ErrCodeUnauthorized, "unauthorized")
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
	restoredID, err := s.mutator.UndoDelete()
	if err != nil {
		if errors.Is(err, ErrUndoNothing) || errors.Is(err, ErrUndoExpired) {
			writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, err.Error())
			return
		}
		writeAPIError(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error())
		return
	}
	s.notifyMenuChanged()
	writeJSON(w, http.StatusOK, undeleteResponse{SessionID: restoredID})
}

// handleSessionPatch implements PATCH /api/sessions/{id}. Decodes an
// UpdateSessionRequest into a session.SetField-compatible map and delegates to
// the mutator. Validation errors from SetField (unknown field, invalid color,
// claude-only field on a non-claude session) surface as 400, not 500 — they
// reflect bad client input, not server failure.
func (s *Server) handleSessionPatch(w http.ResponseWriter, r *http.Request, sessionID string) {
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

	var req UpdateSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body")
		return
	}

	updates := updatesFromRequest(req)
	if len(updates) == 0 {
		writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, "at least one field is required")
		return
	}
	// Empty title is rejected at the API boundary so the error is unambiguous;
	// session.SetField currently accepts any string including "" for Title.
	if t := req.Title; t != nil && strings.TrimSpace(*t) == "" {
		writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, "title cannot be empty")
		return
	}

	changed, restartRequired, err := s.mutator.UpdateSession(sessionID, updates)
	if err != nil {
		// session.MutationError signals client-side bad input; "not found"
		// signals an unknown id. Everything else is a 500.
		var mutErr *session.MutationError
		switch {
		case errors.As(err, &mutErr):
			writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error())
		case strings.HasPrefix(err.Error(), "session not found"):
			writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, err.Error())
		default:
			writeAPIError(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error())
		}
		return
	}

	if len(changed) > 0 {
		s.notifyMenuChanged()
	}
	writeJSON(w, http.StatusOK, UpdateSessionResponse{
		SessionID:       sessionID,
		UpdatedFields:   changed,
		RestartRequired: restartRequired,
	})
}

// updatesFromRequest maps the typed request struct to the field/value pairs
// session.SetField accepts. Only fields whose pointer is non-nil are included
// — this is how a client signals "leave this field alone" vs "set to empty".
func updatesFromRequest(req UpdateSessionRequest) map[string]string {
	out := make(map[string]string, 9)
	if req.Title != nil {
		out[session.FieldTitle] = *req.Title
	}
	if req.Notes != nil {
		out[session.FieldNotes] = *req.Notes
	}
	if req.Color != nil {
		out[session.FieldColor] = *req.Color
	}
	if req.Tool != nil {
		out[session.FieldTool] = *req.Tool
	}
	if req.ExtraArgs != nil {
		out[session.FieldExtraArgs] = *req.ExtraArgs
	}
	if req.Plugins != nil {
		out[session.FieldPlugins] = *req.Plugins
	}
	if req.Channels != nil {
		out[session.FieldChannels] = *req.Channels
	}
	if req.SkipPermissions != nil {
		out[session.FieldSkipPermissions] = strconv.FormatBool(*req.SkipPermissions)
	}
	if req.AutoMode != nil {
		out[session.FieldAutoMode] = strconv.FormatBool(*req.AutoMode)
	}
	return out
}

type archivedSessionsResponse struct {
	Sessions []*MenuSession `json:"sessions"`
	Profile  string         `json:"profile"`
}

// handleArchivedSessions is GET /api/sessions/archived — archived sessions only.
func (s *Server) handleArchivedSessions(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, ErrCodeUnauthorized, "unauthorized")
		return
	}
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed")
		return
	}
	snapshot, err := s.loadArchivedMenuSnapshot()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to load archived sessions")
		return
	}
	refreshSnapshotHookStatuses(snapshot, s.hookStatusLoader)
	resp := archivedSessionsResponse{
		Sessions: make([]*MenuSession, 0),
		Profile:  snapshot.Profile,
	}
	for _, item := range snapshot.Items {
		if item.Type == MenuItemTypeSession && item.Session != nil {
			resp.Sessions = append(resp.Sessions, item.Session)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) loadArchivedMenuSnapshot() (*MenuSnapshot, error) {
	if loader, ok := s.menuData.(interface {
		LoadArchivedMenuSnapshot() (*MenuSnapshot, error)
	}); ok {
		return loader.LoadArchivedMenuSnapshot()
	}
	return nil, fmt.Errorf("archived session list is not available")
}
