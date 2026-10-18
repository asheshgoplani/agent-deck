package web

import (
	"errors"
	"mime"
	"net/http"
	"path/filepath"

	"github.com/asheshgoplani/agent-deck/internal/filelinks"
)

// fileLinkCSP stops a served file from running anything on the deck's origin,
// even if a browser ignored the Content-Type.
const fileLinkCSP = "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; sandbox"

// handleFiles serves a file:// link from the web terminal (issue #2676):
// GET /api/files?path=/abs/path, read-only, limited to [web] file_link_roots.
// internal/filelinks decides what may be read and how it is typed.
func (s *Server) handleFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed")
		return
	}
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, ErrCodeUnauthorized, "unauthorized")
		return
	}
	f, err := filelinks.Open(s.cfg.FileLinkRoots, r.URL.Query().Get("path"))
	if err != nil {
		status, code := fileLinkErrorStatus(err)
		writeAPIError(w, status, code, err.Error())
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, filelinks.ErrNotFound.Error())
		return
	}
	disposition := "attachment"
	if f.Inline {
		disposition = "inline"
	}
	h := w.Header()
	h.Set("Content-Type", f.ContentType)
	h.Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": filepath.Base(f.Path)}))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", fileLinkCSP)
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "", info.ModTime(), f.Reader())
}

func fileLinkErrorStatus(err error) (int, string) {
	switch {
	case errors.Is(err, filelinks.ErrDisabled), errors.Is(err, filelinks.ErrNotFound):
		return http.StatusNotFound, ErrCodeNotFound
	case errors.Is(err, filelinks.ErrNotAbsolute):
		return http.StatusBadRequest, ErrCodeBadRequest
	case errors.Is(err, filelinks.ErrTooLarge):
		return http.StatusRequestEntityTooLarge, ErrCodeFileLinkRefused
	case errors.Is(err, filelinks.ErrOutside), errors.Is(err, filelinks.ErrNotRegular):
		return http.StatusForbidden, ErrCodeFileLinkRefused
	}
	return http.StatusInternalServerError, ErrCodeInternalError
}

func init() {
	registerFeatureRoutes(func(s *Server, mux *http.ServeMux) {
		mux.HandleFunc("/api/files", s.handleFiles)
	})
}
