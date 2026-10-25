package web

import (
	"context"
	"net/http"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/provider"
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
	writeJSON(w, http.StatusOK, labelRemoteFleet(s.remoteFleet.Snapshot(), loadRemoteKinds()))
}

// remoteFleetRemoteView is one fleet remote plus its kind label
// ("sandbox (provider docker)"; omitted for a plain remote).
type remoteFleetRemoteView struct {
	session.RemoteFleetRemote
	Kind string `json:"kind,omitempty"`
}

// remoteFleetView is the /api/remotes body: the fleet snapshot with each
// remote's kind label.
type remoteFleetView struct {
	ObservedAt time.Time                 `json:"observedAt"`
	Remotes    []remoteFleetRemoteView   `json:"remotes"`
	Counts     session.RemoteFleetCounts `json:"counts"`
}

func labelRemoteFleet(snap session.RemoteFleetSnapshot, kinds map[string]string) remoteFleetView {
	view := remoteFleetView{ObservedAt: snap.ObservedAt, Counts: snap.Counts, Remotes: make([]remoteFleetRemoteView, 0, len(snap.Remotes))}
	for _, r := range snap.Remotes {
		view.Remotes = append(view.Remotes, remoteFleetRemoteView{RemoteFleetRemote: r, Kind: kinds[r.Name]})
	}
	return view
}

// remoteKindsFromConfig maps each sandbox remote to provider.DisplayKind;
// plain remotes are absent.
func remoteKindsFromConfig(cfg *session.UserConfig) map[string]string {
	kinds := map[string]string{}
	if cfg == nil {
		return kinds
	}
	for name, rc := range cfg.Remotes {
		if kind := provider.DisplayKind(rc); kind != "" {
			kinds[name] = kind
		}
	}
	return kinds
}

// loadRemoteKinds reads the kind labels from the user config (cached by
// session.LoadUserConfig); a test substitutes it.
var loadRemoteKinds = func() map[string]string {
	cfg, err := session.LoadUserConfig()
	if err != nil {
		return map[string]string{}
	}
	return remoteKindsFromConfig(cfg)
}
