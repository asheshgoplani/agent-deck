package web

import (
	"encoding/json"
	"net/http"
	"strings"
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
		sessionID, err := s.mutator.CreateSession(req.Title, req.Tool, req.ProjectPath, req.GroupPath)
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
		default:
			writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, "unknown session action")
		}
		return
	}

	writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, "route not found")
}
