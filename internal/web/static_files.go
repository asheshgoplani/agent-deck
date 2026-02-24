package web

import (
	"embed"
	"fmt"
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

// handleIndex serves the dashboard as the default landing page.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}

	path := r.URL.Path
	if path != "/" && !strings.HasPrefix(path, "/s/") {
		http.NotFound(w, r)
		return
	}

	dashboard, err := embeddedStaticFiles.ReadFile("static/dashboard.html")
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(dashboard)
}

// handleTerminal serves the existing terminal-focused UI.
func (s *Server) handleTerminal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}

	if r.URL.Path != "/terminal" {
		http.NotFound(w, r)
		return
	}

	index, err := embeddedStaticFiles.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, "terminal unavailable", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(index)
}

func (s *Server) handleManifest(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/manifest.webmanifest" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	if err := serveEmbeddedFile(
		w,
		"static/manifest.webmanifest",
		"application/manifest+json; charset=utf-8",
		map[string]string{
			"Cache-Control": "no-cache",
		},
	); err != nil {
		http.Error(w, "manifest unavailable", http.StatusInternalServerError)
	}
}

func (s *Server) handleServiceWorker(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/sw.js" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	if err := serveEmbeddedFile(
		w,
		"static/sw.js",
		"application/javascript; charset=utf-8",
		map[string]string{
			"Cache-Control":          "no-cache",
			"Service-Worker-Allowed": "/",
		},
	); err != nil {
		http.Error(w, "service worker unavailable", http.StatusInternalServerError)
	}
}

func serveEmbeddedFile(w http.ResponseWriter, path, contentType string, headers map[string]string) error {
	body, err := embeddedStaticFiles.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read embedded file %q: %w", path, err)
	}

	for key, value := range headers {
		if value == "" {
			continue
		}
		w.Header().Set(key, value)
	}
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
	return nil
}
