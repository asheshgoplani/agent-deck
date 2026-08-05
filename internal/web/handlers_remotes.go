package web

import (
	"context"
	"net/http"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// RemoteFleetLoader provides a read-only snapshot of configured SSH remotes.
// It is exported so embedders can supply a cached or otherwise customized
// scanner without coupling the web package to its concrete implementation.
type RemoteFleetLoader interface {
	Scan(context.Context) (session.RemoteFleetSnapshot, error)
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
	snapshot, err := s.remoteFleet.Scan(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, ErrCodeInternalError, "remote fleet scan failed")
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}
