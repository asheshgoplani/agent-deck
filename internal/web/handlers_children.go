package web

// Web UI Children-panel handler.
//
// Surfaces the conductor → child session topology that the TUI shows
// natively (internal/ui Home pane indents children under their parent).
// Before this endpoint, the Web UI right-rail "Children (conductor)"
// card was an empty stub — see PARITY_MATRIX.md state-fields section.
//
// Tree is built from the same MenuSnapshot the menu/session list uses,
// so child status / hook refresh stay consistent with the rest of the
// web view.

import (
	"net/http"
	"strings"
)

// SessionChildNode is one node in the children-tree response. It inlines
// the standard MenuSession fields and adds a recursive `children` array.
// `children` is always non-nil — even leaves render as `"children":[]` so
// JS consumers don't have to null-check.
type SessionChildNode struct {
	*MenuSession
	Children []*SessionChildNode `json:"children"`
}

// SessionChildrenResponse is the body of GET /api/sessions/{id}/children.
type SessionChildrenResponse struct {
	SessionID string              `json:"sessionId"`
	Children  []*SessionChildNode `json:"children"`
}

// handleSessionChildren serves GET /api/sessions/{id}/children. Non-GET
// returns 405. Unknown session id returns 404. A session with no
// descendants (whether or not it is a conductor) returns 200 with
// `"children":[]` — this is intentional, so the UI can ask any session
// for its tree without special-casing non-conductors.
func (s *Server) handleSessionChildren(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed")
		return
	}

	snapshot, err := s.menuData.LoadMenuSnapshot()
	if err != nil || snapshot == nil {
		writeAPIError(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to load session data")
		return
	}

	// Index sessions by id and build parent→[child ids] adjacency.
	byID := make(map[string]*MenuSession, len(snapshot.Items))
	kids := make(map[string][]string)
	for _, item := range snapshot.Items {
		if item.Type != MenuItemTypeSession || item.Session == nil {
			continue
		}
		byID[item.Session.ID] = item.Session
		if item.Session.ParentSessionID != "" {
			kids[item.Session.ParentSessionID] = append(
				kids[item.Session.ParentSessionID], item.Session.ID)
		}
	}

	if _, ok := byID[sessionID]; !ok {
		writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, "session not found")
		return
	}

	// DFS with a visited set — defends against corrupt snapshots that
	// contain a cycle (a→b→a). Without this guard the recursion would
	// run forever; the test TestSessionChildren_CycleSafe locks the
	// invariant in.
	visited := make(map[string]bool, len(byID))
	var build func(id string) []*SessionChildNode
	build = func(id string) []*SessionChildNode {
		children := kids[id]
		out := make([]*SessionChildNode, 0, len(children))
		for _, cid := range children {
			if visited[cid] {
				continue
			}
			visited[cid] = true
			node := &SessionChildNode{
				MenuSession: byID[cid],
				Children:    build(cid),
			}
			out = append(out, node)
		}
		return out
	}
	visited[sessionID] = true

	writeJSON(w, http.StatusOK, SessionChildrenResponse{
		SessionID: sessionID,
		Children:  build(sessionID),
	})
}

// childrenSubpathFromAction returns true if the action suffix from
// handleSessionByAction is the "children" route, optionally with empty
// trailing path. Kept here so the routing match in handlers_sessions.go
// stays alongside its handler.
func isChildrenAction(action string) bool {
	return action == "children" || strings.HasPrefix(action, "children/")
}
