package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIndexCacheControl(t *testing.T) {
	s := NewServer(Config{Token: "test-token"})
	req := httptest.NewRequest(http.MethodGet, "/?token=test-token", nil)
	w := httptest.NewRecorder()
	s.handleIndex(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	cc := w.Header().Get("Cache-Control")
	if !strings.Contains(cc, "no-cache") {
		t.Errorf("Cache-Control missing no-cache: %q", cc)
	}
}

func TestIndexImportMap(t *testing.T) {
	s := NewServer(Config{Token: "test-token"})
	req := httptest.NewRequest(http.MethodGet, "/?token=test-token", nil)
	w := httptest.NewRecorder()
	s.handleIndex(w, req)
	body := w.Body.String()
	for _, want := range []string{
		`"preact"`,
		`"preact/hooks"`,
		`"htm/preact"`,
		`"@preact/signals"`,
		`"@xterm/xterm"`,
		`"@xterm/addon-fit"`,
		`"@xterm/addon-webgl"`,
		`<script type="importmap">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("index.html missing %q", want)
		}
	}
}

func TestIndexThemeInit(t *testing.T) {
	s := NewServer(Config{Token: "test-token"})
	req := httptest.NewRequest(http.MethodGet, "/?token=test-token", nil)
	w := httptest.NewRecorder()
	s.handleIndex(w, req)
	body := w.Body.String()
	if !strings.Contains(body, "localStorage.getItem('theme')") {
		t.Error("index.html missing theme init script")
	}
	// theme init must appear before importmap
	themeIdx := strings.Index(body, "localStorage.getItem('theme')")
	importIdx := strings.Index(body, `<script type="importmap">`)
	if themeIdx > importIdx {
		t.Error("theme init script must appear before importmap")
	}
}

func TestIndexNoCDN(t *testing.T) {
	s := NewServer(Config{Token: "test-token"})
	req := httptest.NewRequest(http.MethodGet, "/?token=test-token", nil)
	w := httptest.NewRecorder()
	s.handleIndex(w, req)
	body := w.Body.String()
	if strings.Contains(body, "cdn.jsdelivr.net") {
		t.Error("index.html must not reference CDN URLs")
	}
}

func TestVendorFilesServed(t *testing.T) {
	s := NewServer(Config{})
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", s.staticFileServer()))
	for _, path := range []string{
		"/static/vendor/preact.mjs",
		"/static/vendor/tailwind.js",
		"/static/vendor/xterm.mjs",
		"/static/vendor/xterm.css",
		"/static/vendor/addon-fit.mjs",
		"/static/vendor/addon-webgl.mjs",
		"/static/vendor/addon-canvas.js",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Errorf("GET %s: expected 200, got %d", path, w.Code)
		}
		if w.Body.Len() == 0 {
			t.Errorf("GET %s: empty body", path)
		}
	}
}

func TestIndexXtermCSS(t *testing.T) {
	s := NewServer(Config{Token: "test-token"})
	req := httptest.NewRequest(http.MethodGet, "/?token=test-token", nil)
	w := httptest.NewRecorder()
	s.handleIndex(w, req)
	body := w.Body.String()
	if !strings.Contains(body, `href="/static/vendor/xterm.css"`) {
		t.Error("index.html missing xterm.css stylesheet link")
	}
}

func TestIndexAppRoot(t *testing.T) {
	s := NewServer(Config{Token: "test-token"})
	req := httptest.NewRequest(http.MethodGet, "/?token=test-token", nil)
	w := httptest.NewRecorder()
	s.handleIndex(w, req)
	body := w.Body.String()
	if !strings.Contains(body, `id="app-root"`) {
		t.Error("index.html missing app-root mount point")
	}
}
