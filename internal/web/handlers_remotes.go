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
	writeJSON(w, http.StatusOK, labelRemoteFleet(s.remoteFleet.Snapshot(), loadRemoteLabels()))
}

// remoteLabels is what the config says about one remote beyond the fleet
// snapshot: its kind ("sandbox (provider docker)") and, for a sandbox, its
// owner. Both are empty for a plain remote.
type remoteLabels struct {
	Kind  string
	Owner string
}

// remoteFleetRemoteView is one fleet remote plus its kind label
// ("sandbox (provider docker)") and its owner; both omitted for a plain
// remote.
type remoteFleetRemoteView struct {
	session.RemoteFleetRemote
	Kind  string `json:"kind,omitempty"`
	Owner string `json:"owner,omitempty"`
}

// remoteFleetView is the /api/remotes body: the fleet snapshot with each
// remote's kind label and owner.
type remoteFleetView struct {
	ObservedAt time.Time                 `json:"observedAt"`
	Remotes    []remoteFleetRemoteView   `json:"remotes"`
	Counts     session.RemoteFleetCounts `json:"counts"`
}

func labelRemoteFleet(snap session.RemoteFleetSnapshot, labels map[string]remoteLabels) remoteFleetView {
	view := remoteFleetView{ObservedAt: snap.ObservedAt, Counts: snap.Counts, Remotes: make([]remoteFleetRemoteView, 0, len(snap.Remotes))}
	for _, r := range snap.Remotes {
		view.Remotes = append(view.Remotes, remoteFleetRemoteView{RemoteFleetRemote: r, Kind: labels[r.Name].Kind, Owner: labels[r.Name].Owner})
	}
	return view
}

// remoteLabelsFromConfig maps each sandbox remote to its
// provider.DisplayKind and owner; plain remotes are absent.
func remoteLabelsFromConfig(cfg *session.UserConfig) map[string]remoteLabels {
	labels := map[string]remoteLabels{}
	if cfg == nil {
		return labels
	}
	for name, rc := range cfg.Remotes {
		if kind := provider.DisplayKind(rc); kind != "" {
			labels[name] = remoteLabels{Kind: kind, Owner: session.PrintableOwner(rc.Owner)}
		}
	}
	return labels
}

// loadRemoteLabels reads the remote labels from the user config (cached by
// session.LoadUserConfig); a test substitutes it.
var loadRemoteLabels = func() map[string]remoteLabels {
	cfg, err := session.LoadUserConfig()
	if err != nil {
		return map[string]remoteLabels{}
	}
	return remoteLabelsFromConfig(cfg)
}
