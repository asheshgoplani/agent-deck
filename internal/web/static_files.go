package web

import (
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
	"sync"
)

//go:generate tailwindcss -i ./static/styles.src.css -o ./static/styles.css --minify

//go:embed static/*
var embeddedStaticFiles embed.FS

// webAssets is the process-wide Assets instance used by handleIndex to
// substitute {{ASSET:...}} placeholders in index.html. Loaded lazily on
// first use; LoadAssetsFromFS tolerates a missing manifest and returns a
// dev-mode instance so the serving path never errors just because
// bundle.go has not been run. PERF-H rollback: set AGENTDECK_WEB_BUNDLE=0
// in the environment to force dev mode regardless of manifest presence.
var (
	webAssetsOnce sync.Once
	webAssets     *Assets
)

func getWebAssets() *Assets {
	webAssetsOnce.Do(func() {
		a, err := LoadAssetsFromFS(embeddedStaticFiles, "static/dist/manifest.json")
		if err != nil {
			// Manifest is malformed. Degrade to dev mode rather than
			// returning an error at serve time — the startup log is a
			// better place to flag this than a 500 on every page load.
			a = newDevAssets()
		}
		webAssets = a
	})
	return webAssets
}

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
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
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

	// PERF-H: substitute {{ASSET:logical}} placeholders with the resolved
	// URLs from the bundler manifest. In dev mode (no manifest) this is
	// a no-op string pass that rewrites placeholders to /static/<logical>
	// unchanged. In prod mode, each placeholder resolves to the hashed
	// output path emitted by bundle.go.
	substituted := getWebAssets().SubstitutePlaceholders(string(index))

	// Defense-in-depth: prevent the auth token from leaking via the Referer
	// header to any external resources loaded by the page. The JavaScript
	// token-stripping (history.replaceState) is the primary mitigation;
	// this header ensures no Referer is sent even if the script runs late.
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(substituted))
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
