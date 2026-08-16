package web

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"slices"
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
		// vendor/tailwind.js was deleted in Phase 1 / Plan 03 (PERF-01).
		// The Tailwind Play CDN runtime is replaced by build-time compiled
		// /static/styles.css (see internal/web/static_files.go //go:generate).
		"/static/vendor/xterm.mjs",
		"/static/vendor/xterm.css",
		"/static/vendor/addon-fit.mjs",
		"/static/vendor/addon-webgl.mjs",
		// vendor/addon-canvas.js was deleted in Phase 8 / Plan 03 (PERF-C).
		// xterm v6 does not reference the canvas renderer anywhere in the
		// first-party code. The TerminalPanel.js fallback path still has a
		// `typeof window.CanvasAddon !== 'undefined'` guard which is now
		// always false, making the canvas fallback inert. See the 404 gate
		// in TestAddonCanvasDeleted below.
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

// TestAuthTokenIsNeverPersistedToWebStorage guards the browser half of the
// bearer-token threat boundary. main.js reads the token from the URL, holds it
// in authTokenSignal, and strips it from the address bar; persisting it to Web
// Storage or a cookie would survive that strip and leave the credential
// readable by anything that later runs on the origin (an XSS, a shared kiosk
// browser profile, a synced cookie jar).
//
// The app legitimately persists theme, sidebar and toast state, so this cannot
// ban the storage APIs outright. Instead it scans every served app script for
// a line that mentions both a persistence sink and an auth-token identifier —
// the shape any regression here would take.
func TestAuthTokenIsNeverPersistedToWebStorage(t *testing.T) {
	sinks := []string{"localStorage", "sessionStorage", "indexedDB", "document.cookie"}
	tokenIdents := []string{"authTokenSignal", "webToken", "authToken", "bearer"}

	var scripts []string
	err := fs.WalkDir(embeddedStaticFiles, "static/app", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".js") {
			scripts = append(scripts, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk app scripts: %v", err)
	}
	if len(scripts) == 0 {
		t.Fatal("no app scripts found; the guard would pass vacuously")
	}

	// main.js is where the token is read, so it must be in the scanned set.
	if !slices.Contains(scripts, "static/app/main.js") {
		t.Fatalf("static/app/main.js missing from scanned scripts: %v", scripts)
	}

	for _, path := range scripts {
		body, err := embeddedStaticFiles.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for i, line := range strings.Split(string(body), "\n") {
			lower := strings.ToLower(line)
			sink := ""
			for _, s := range sinks {
				if strings.Contains(lower, strings.ToLower(s)) {
					sink = s
					break
				}
			}
			if sink == "" {
				continue
			}
			for _, ident := range tokenIdents {
				if strings.Contains(lower, strings.ToLower(ident)) {
					t.Errorf("%s:%d writes the bearer token to %s (%q); the token must stay in memory only",
						path, i+1, sink, strings.TrimSpace(line))
				}
			}
		}
	}
}

// TestAppScriptsUseTheSharedAuthenticatedFetch is the regression gate for the
// MCP pane breaking against an authenticated server.
//
// The bearer token exists only in authTokenSignal, so a call site that
// hand-rolls fetch() silently omits Authorization and 401s the moment the
// server runs with --token or --token-file. McpPane.js shipped exactly that:
// a private copy of the fetch helper that never grew the header, which made
// the whole MCP pane unusable through the UI while every server-side test
// passed. AppShell.js and SettingsPanel.js carried the same shape.
//
// Rule: inside static/app, only api.js may call fetch() without authHeaders().
// The check reads the whole balanced call expression rather than one line,
// because the call that actually broke was `fetch(path, {` — a variable URL
// spread over several lines, which a line-oriented or URL-matching check walks
// straight past. (sw.js is a service worker outside static/app; it proxies
// requests it did not originate and is correctly out of scope.)
func TestAppScriptsUseTheSharedAuthenticatedFetch(t *testing.T) {
	var scripts []string
	err := fs.WalkDir(embeddedStaticFiles, "static/app", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".js") {
			scripts = append(scripts, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk app scripts: %v", err)
	}
	if len(scripts) == 0 {
		t.Fatal("no app scripts found; the guard would pass vacuously")
	}

	checked := 0
	for _, path := range scripts {
		// api.js is the shared helper itself: the one place allowed to call
		// fetch() directly, because it is what adds the header.
		if path == "static/app/api.js" {
			continue
		}
		body, err := embeddedStaticFiles.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		src := string(body)
		for _, call := range fetchCallExpressions(src) {
			checked++
			if !strings.Contains(call.args, "authHeaders") {
				t.Errorf("%s:%d calls fetch() without authHeaders(); it will 401 against a "+
					"server started with --token/--token-file:\n\t%s",
					path, 1+strings.Count(src[:call.offset], "\n"), strings.TrimSpace(call.args))
			}
		}
	}
	if checked == 0 {
		t.Fatal("no fetch() call sites found outside api.js; the guard has gone stale")
	}
}

type fetchCall struct {
	offset int
	args   string
}

// fetchCallExpressions returns the balanced argument text of every fetch(...)
// call in src, skipping `apiFetch(`/`jsonFetch(`-style wrappers whose name
// merely ends in "fetch".
func fetchCallExpressions(src string) []fetchCall {
	var out []fetchCall
	for i := 0; i+6 <= len(src); i++ {
		if src[i:i+6] != "fetch(" {
			continue
		}
		// Require a non-identifier character before "fetch" so apiFetch( and
		// jsonFetch( (which do carry the header, or are the helper itself)
		// are not double-counted.
		if i > 0 && isJSIdentByte(src[i-1]) {
			continue
		}
		depth, j := 0, i+5
		for ; j < len(src); j++ {
			switch src[j] {
			case '(':
				depth++
			case ')':
				depth--
			}
			if depth == 0 {
				break
			}
		}
		if j >= len(src) {
			break
		}
		out = append(out, fetchCall{offset: i, args: src[i : j+1]})
		i = j
	}
	return out
}

func isJSIdentByte(b byte) bool {
	return b == '_' || b == '$' ||
		(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// TestAddonCanvasDeleted is the regression gate for Phase 8 / Plan 03
// (PERF-C). xterm v6 never references the canvas renderer, so the 94 KB
// vendor file was dead weight. This test ensures the file stays deleted:
//  1. /static/vendor/addon-canvas.js returns 404 via the static file server
//  2. index.html does not <script src> the file
func TestAddonCanvasDeleted(t *testing.T) {
	s := NewServer(Config{Token: "test-token"})

	// Index must not reference addon-canvas.js
	req := httptest.NewRequest(http.MethodGet, "/?token=test-token", nil)
	w := httptest.NewRecorder()
	s.handleIndex(w, req)
	body := w.Body.String()
	if strings.Contains(body, "/static/vendor/addon-canvas.js") {
		t.Error("index.html must NOT reference /static/vendor/addon-canvas.js (deleted in plan 08-03)")
	}

	// Static file server must 404 on the deleted path
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", s.staticFileServer()))
	req2 := httptest.NewRequest(http.MethodGet, "/static/vendor/addon-canvas.js", nil)
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)
	if w2.Code != 404 {
		t.Errorf("GET /static/vendor/addon-canvas.js: expected 404, got %d", w2.Code)
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

func TestCreateSessionDialogUsesModelIDCatalog(t *testing.T) {
	data, err := embeddedStaticFiles.ReadFile("static/app/CreateSessionDialog.js")
	if err != nil {
		t.Fatalf("read CreateSessionDialog.js: %v", err)
	}
	body := string(data)

	for _, want := range []string{
		"MODEL_ID_CATALOG",
		"<label>MODEL ID</label>",
		`<option value="">Tool default</option>`,
		"gpt-5.6-sol",
		"gpt-5.6-terra",
		"gpt-5.6-luna",
		"gpt-5.5",
		"gpt-5.4",
		"gpt-5.4-mini",
		"gpt-5.3-codex",
		"o3-pro",
		"claude-opus-5",
		"claude-sonnet-5",
		"claude-fable-5",
		"claude-sonnet-4-6",
		"claude-opus-4-8",
		"claude-opus-4-7",
		"claude-haiku-4-5-20251001",
		"gemini-3.1-pro-preview",
		"gemini-3-flash-preview",
		"gemini-2.5-flash-lite",
		"openai/gpt-5.5",
		"anthropic/claude-opus-5",
		"anthropic/claude-sonnet-5",
		"anthropic/claude-fable-5",
		"anthropic/claude-sonnet-4-6",
		"anthropic/claude-opus-4-8",
		"Custom model ID",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("CreateSessionDialog.js missing %q", want)
		}
	}
	if strings.Contains(body, "<label>VERSION</label>") {
		t.Fatal("web session creation should use model IDs directly, not a separate version selector")
	}
}

func TestCreateSessionDialogUsesToolSpecificReasoningEffortCatalog(t *testing.T) {
	data, err := embeddedStaticFiles.ReadFile("static/app/CreateSessionDialog.js")
	if err != nil {
		t.Fatalf("read CreateSessionDialog.js: %v", err)
	}
	source := string(data)
	for _, want := range []string{
		"REASONING EFFORT",
		"reasoningEffort",
		"minimal",
		"xhigh",
		"max",
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("CreateSessionDialog.js missing expected content %q", want)
		}
	}
}

// TestNoTailwindPlayCDN is the regression gate for Phase 1 / Plan 03 (PERF-01).
// The Tailwind Play CDN runtime (vendor/tailwind.js, 397 KB) was deleted in
// favor of a build-time compiled /static/styles.css file (~8 KB gzipped).
// This test ensures:
//  1. internal/web/static/index.html does NOT load /static/vendor/tailwind.js
//  2. internal/web/static/index.html does NOT carry an inline tailwind.config block
//  3. The static file server does NOT serve /static/vendor/tailwind.js (404 expected)
//  4. The compiled /static/styles.css IS linked from index.html
//
// If any of these fail, someone has either re-introduced the Play CDN or
// regressed the cascade swap. See .planning/research/PITFALLS.md Pitfall #2.
func TestNoTailwindPlayCDN(t *testing.T) {
	s := NewServer(Config{Token: "test-token"})
	req := httptest.NewRequest(http.MethodGet, "/?token=test-token", nil)
	w := httptest.NewRecorder()
	s.handleIndex(w, req)
	body := w.Body.String()
	if strings.Contains(body, `/static/vendor/tailwind.js`) {
		t.Error("index.html must NOT reference /static/vendor/tailwind.js (Play CDN was deleted in plan 03)")
	}
	if strings.Contains(body, `tailwind.config = {`) {
		t.Error("index.html must NOT contain inline tailwind.config (palette is now in styles.src.css @theme)")
	}
	if !strings.Contains(body, `href="/static/styles.css"`) {
		t.Error("index.html missing compiled /static/styles.css link")
	}

	// The static file server should now 404 on /static/vendor/tailwind.js.
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", s.staticFileServer()))
	req2 := httptest.NewRequest(http.MethodGet, "/static/vendor/tailwind.js", nil)
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)
	if w2.Code != 404 {
		t.Errorf("GET /static/vendor/tailwind.js: expected 404, got %d", w2.Code)
	}
}
