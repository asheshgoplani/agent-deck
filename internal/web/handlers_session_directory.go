package web

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

type sessionDirectoryPreview struct {
	Path    string   `json:"path"`
	Missing []string `json:"missing"`
}

// inspectSessionDirectory follows the same expansion and symlink rules as
// session creation. It only reads the filesystem, listing parents first.
func inspectSessionDirectory(path string) (sessionDirectoryPreview, error) {
	preview := sessionDirectoryPreview{Missing: []string{}}
	if path == "" {
		return preview, fmt.Errorf("path is required")
	}
	var err error
	preview.Path, err = session.ResolveProjectPath(path)
	if err != nil {
		return preview, err
	}
	for dir := preview.Path; ; dir = filepath.Dir(dir) {
		info, err := os.Stat(dir)
		if err == nil {
			if !info.IsDir() {
				return preview, fmt.Errorf("path is not a directory: %s", dir)
			}
			break
		}
		if !os.IsNotExist(err) {
			return preview, fmt.Errorf("cannot inspect directory %s: %w", dir, err)
		}
		// A dangling symlink exists but cannot be replaced by mkdir.
		if _, linkErr := os.Lstat(dir); linkErr == nil {
			return preview, fmt.Errorf("cannot use directory %s: %w", dir, err)
		}
		preview.Missing = append(preview.Missing, dir)
		if filepath.Dir(dir) == dir {
			return preview, fmt.Errorf("cannot inspect directory %s: %w", dir, err)
		}
	}
	slices.Reverse(preview.Missing)
	return preview, nil
}

func (s *Server) handleSessionDirectory(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, ErrCodeUnauthorized, "unauthorized")
		return
	}
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed")
		return
	}
	preview, err := inspectSessionDirectory(r.URL.Query().Get("path"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, ErrCodeBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

func init() {
	registerFeatureRoutes(func(s *Server, mux *http.ServeMux) {
		mux.HandleFunc("/api/session-directory", s.handleSessionDirectory)
	})
}
