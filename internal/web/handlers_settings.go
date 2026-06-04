package web

import (
	"net/http"
	"runtime/debug"

	"github.com/asheshgoplani/agent-deck/internal/buildinfo"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed")
		return
	}
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, ErrCodeUnauthorized, "unauthorized")
		return
	}

	// Tool-visibility filter (issue #1259) is read from the process registry at
	// request time, so it reflects the current config (re-probed only when config
	// changes — see currentRegistry). It is a display filter only.
	writeJSON(w, http.StatusOK, SettingsResponse{
		Profile:            s.cfg.Profile,
		ReadOnly:           s.cfg.ReadOnly,
		WebMutations:       s.cfg.WebMutations,
		Version:            buildVersion(),
		Commit:             buildinfo.Commit(s.cfg.Commit),
		ToolFilter:         session.ToolFilterActive(),
		VisibleTools:       session.VisibleToolNames(),
		ToolFilterFallback: session.ToolFilterFallbackActive(),
	})
}

func (s *Server) handleProfiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed")
		return
	}
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, ErrCodeUnauthorized, "unauthorized")
		return
	}
	profiles, err := session.ListProfiles()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, ErrCodeInternalError, err.Error())
		return
	}
	// Ensure current profile appears in list even if its directory lacks state.db.
	current := s.cfg.Profile
	found := false
	for _, p := range profiles {
		if p == current {
			found = true
			break
		}
	}
	if !found {
		profiles = append([]string{current}, profiles...)
	}
	writeJSON(w, http.StatusOK, ProfilesResponse{
		Current:  current,
		Profiles: profiles,
	})
}

// buildVersion returns the binary version from embedded build info.
// Falls back to "dev" when build info is unavailable (e.g. during tests).
func buildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok || info.Main.Version == "" || info.Main.Version == "(devel)" {
		return "dev"
	}
	return info.Main.Version
}
