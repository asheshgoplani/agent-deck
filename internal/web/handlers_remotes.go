package web

import (
	"context"
	"net/http"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// RemoteFleetLoader is a server-owned source of remote fleet snapshots.
// Start may perform background I/O; Snapshot must only read memory.
type RemoteFleetLoader interface {
	Start(context.Context)
	Snapshot() session.RemoteFleetSnapshot
}

func (s *Server) handleRemotes(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, ErrCodeUnauthorized, "unauthorized")
		return
	}
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed")
		return
	}
	if s.remoteFleet == nil {
		writeAPIError(w, http.StatusServiceUnavailable, ErrCodeNotImplemented, "remote fleet is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, s.remoteFleet.Snapshot())
}
