package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed static/*
var embeddedStaticFiles embed.FS

func (s *Server) staticFileServer() http.Handler {
	sub, err := fs.Sub(embeddedStaticFiles, "static")
	if err != nil {
		// This should never happen with embedded files present at build time.
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "static assets unavailable", http.StatusInternalServerError)
		})
	}
	return http.FileServer(http.FS(sub))
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}

	path := r.URL.Path
	if path != "/" && !strings.HasPrefix(path, "/s/") {
		http.NotFound(w, r)
		return
	}

	index, err := embeddedStaticFiles.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, "index unavailable", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(index)
}
