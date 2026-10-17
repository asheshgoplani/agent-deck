package web

import (
	"net/http"
	"os"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// handlePathSuggestions serves filesystem directory completions using the same
// helper as the TUI. It returns paths only, never file contents or metadata.
func (s *Server) handlePathSuggestions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed")
		return
	}
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, ErrCodeUnauthorized, "unauthorized")
		return
	}
	// Suggestions only feed the New Session dialog, which is hidden when web
	// mutations are off. Do not expose directory listing in read-only mode.
	if s.cfg.ReadOnly {
		writeAPIError(w, http.StatusForbidden, ErrCodeForbidden, "web mutations are disabled")
		return
	}
	if !s.checkMutationsAllowed(w) {
		return
	}

	const maxSuggestions = 20
	paths, err := session.GetDirectoryCompletions(r.URL.Query().Get("prefix"))
	if err != nil || paths == nil {
		paths = []string{}
	}
	// Filesystem names are literal, but session creation expands variables.
	// Never offer a completion that expansion would redirect elsewhere.
	suggestions := make([]string, 0, maxSuggestions)
	for _, path := range paths {
		if os.ExpandEnv(path) != path {
			continue
		}
		suggestions = append(suggestions, path)
		if len(suggestions) == maxSuggestions {
			break
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, struct {
		Suggestions []string `json:"suggestions"`
	}{Suggestions: suggestions})
}
