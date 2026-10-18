package web

import (
	"net/http"
	"os"
	"strings"

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
	w.Header().Set("Cache-Control", "no-store")
	prefix := r.URL.Query().Get("prefix")
	// The ".." check is inline so static analysis sees the guard directly.
	if !suggestablePrefix(prefix) || strings.Contains(prefix, "..") {
		writeJSON(w, http.StatusOK, struct {
			Suggestions []string `json:"suggestions"`
		}{Suggestions: []string{}})
		return
	}
	paths, err := session.GetDirectoryCompletions(prefix)
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
	writeJSON(w, http.StatusOK, struct {
		Suggestions []string `json:"suggestions"`
	}{Suggestions: suggestions})
}

// maxSuggestionPrefix is far above any real path the dialog needs (PATH_MAX is
// 4096 on Linux) and keeps hostile input from reaching the filesystem.
const maxSuggestionPrefix = 4096

// suggestablePrefix reports whether prefix may be completed over the web API.
// The caller also refuses any "..".
//
// Exposure model: the caller already passed the host allowlist, the token
// check and the mutations gate, so it may create a session (and run commands)
// in any directory the server user can reach. Listing directory names there
// discloses nothing that caller could not already obtain. The checks below
// are defense in depth: NUL bytes and oversized input are refused outright,
// and any ".." is refused so completion never walks out of the typed parent
// (including through a symlink followed by ".."). The TUI keeps its ".."
// support; the web dialog takes absolute or ~ paths.
func suggestablePrefix(prefix string) bool {
	if prefix == "" || len(prefix) > maxSuggestionPrefix {
		return false
	}
	return !strings.ContainsRune(prefix, 0)
}
