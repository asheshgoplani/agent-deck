package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

type groupsListResponse struct {
	Groups []*MenuGroup `json:"groups"`
}

// isGroupNotFound folds the two sentinels that mean "no such group" at this
// layer: SetGroupExpanded reports the mutator-contract ErrGroupNotFound, while
// RenameGroup passes GroupTree's own session.ErrGroupNotFound straight
// through. Either way the group is gone, and that is a 404 — a stale browser
// tab acting on a group the TUI already deleted is a client error, not an
// internal one. Without this the same endpoint answered two different ways
// depending on which field the request carried.
func isGroupNotFound(err error) bool {
	return errors.Is(err, ErrGroupNotFound) || errors.Is(err, session.ErrGroupNotFound)
}

func (s *Server) handleGroupsCollection(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, ErrCodeUnauthorized, "unauthorized")
		return
	}

	switch r.Method {
	case http.MethodGet:
		snapshot, err := s.menuData.LoadMenuSnapshot()
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to load group data")
			return
		}
		resp := groupsListResponse{
			Groups: make([]*MenuGroup, 0),
		}
		for _, item := range snapshot.Items {
			if item.Type == MenuItemTypeGroup && item.Group != nil {
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
		var req CreateGroupRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body")
			return
		}
		if req.Name == "" {
			writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, "name is required")
			return
		}
		if s.mutator == nil {
			writeAPIError(w, http.StatusServiceUnavailable, ErrCodeNotImplemented, "mutations not available")
			return
		}
		groupPath, err := s.mutator.CreateGroup(req.Name, req.ParentPath)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error())
			return
		}
		s.notifyMenuChanged()
		writeJSON(w, http.StatusCreated, map[string]string{"path": groupPath})

	default:
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleGroupByPath(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, ErrCodeUnauthorized, "unauthorized")
		return
	}

	const groupPrefix = "/api/groups/"
	groupPath := strings.TrimPrefix(r.URL.Path, groupPrefix)
	if groupPath == "" {
		writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, "group path is required")
		return
	}

	switch r.Method {
	case http.MethodPatch:
		if !s.checkMutationsAllowed(w) {
			return
		}
		if !s.checkMutationRateLimit(w) {
			return
		}
		var req UpdateGroupRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request body")
			return
		}
		if req.Name == "" && req.Expanded == nil {
			writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, "name or expanded is required")
			return
		}
		if s.mutator == nil {
			writeAPIError(w, http.StatusServiceUnavailable, ErrCodeNotImplemented, "mutations not available")
			return
		}
		resp := map[string]any{"path": groupPath}
		// Collapse first: a rename in the same request moves the group to a new
		// path, which this write would then miss.
		if req.Expanded != nil {
			if err := s.mutator.SetGroupExpanded(groupPath, *req.Expanded); err != nil {
				if isGroupNotFound(err) {
					writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, "group not found")
					return
				}
				writeAPIError(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error())
				return
			}
			resp["expanded"] = *req.Expanded
		}
		if req.Name != "" {
			if err := s.mutator.RenameGroup(groupPath, req.Name); err != nil {
				if isGroupNotFound(err) {
					writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, "group not found")
					return
				}
				writeAPIError(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error())
				return
			}
			resp["name"] = req.Name
		}
		s.notifyMenuChanged()
		writeJSON(w, http.StatusOK, resp)

	case http.MethodDelete:
		if !s.checkMutationsAllowed(w) {
			return
		}
		if !s.checkMutationRateLimit(w) {
			return
		}
		if groupPath == session.DefaultGroupPath {
			writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, "cannot delete default group")
			return
		}
		if s.mutator == nil {
			writeAPIError(w, http.StatusServiceUnavailable, ErrCodeNotImplemented, "mutations not available")
			return
		}
		if err := s.mutator.DeleteGroup(groupPath); err != nil {
			writeAPIError(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error())
			return
		}
		s.notifyMenuChanged()
		writeJSON(w, http.StatusOK, map[string]string{"deleted": groupPath})

	default:
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed")
	}
}
