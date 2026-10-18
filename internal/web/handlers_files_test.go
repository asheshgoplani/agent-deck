package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Issue #2676: GET /api/files serves a file:// link from the web terminal when
// the file is under `[web] file_link_roots`. These pin the route's half of the
// policy (the containment rules themselves are in internal/filelinks).

func fileLinkFixture(t *testing.T) (tmp, root string) {
	t.Helper()
	tmp = t.TempDir()
	root = filepath.Join(tmp, "root")
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"root/notes.txt":   "hello\n",
		"root/report.html": "<script>alert(1)</script>",
		"root/logo.svg":    `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`,
		"root/shot.png":    "\x89PNG\r\n\x1a\n",
		"root/blob.bin":    "a\x00b",
		"secret.txt":       "TOP SECRET",
	}
	for rel, body := range files {
		if err := os.WriteFile(filepath.Join(tmp, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("../secret.txt", filepath.Join(root, "link-out")); err != nil {
		t.Fatal(err)
	}
	return tmp, root
}

func fileLinkServer(roots []string, token string) *Server {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", Profile: "default", FileLinkRoots: roots, Token: token})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	return srv
}

func getFile(srv *Server, method, path string, header http.Header) *httptest.ResponseRecorder {
	req := newLocalRequest(method, "/api/files?path="+url.QueryEscape(path), nil)
	for k, v := range header {
		req.Header[k] = v
	}
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	return rr
}

func TestFilesRoute_ServesUnderARootWithSafeHeaders(t *testing.T) {
	_, root := fileLinkFixture(t)
	srv := fileLinkServer([]string{root}, "")
	cases := []struct {
		name, wantType, wantDisposition, wantBody string
	}{
		{"notes.txt", "text/plain; charset=utf-8", "inline", "hello\n"},
		// HTML and SVG come back as their source text, never rendered.
		{"report.html", "text/plain; charset=utf-8", "inline", "<script>alert(1)</script>"},
		{"logo.svg", "text/plain; charset=utf-8", "inline", ""},
		{"shot.png", "image/png", "inline", ""},
		{"blob.bin", "application/octet-stream", "attachment", "a\x00b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := getFile(srv, http.MethodGet, filepath.Join(root, tc.name), nil)
			if rr.Code != http.StatusOK {
				t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
			}
			h := rr.Header()
			if got := h.Get("Content-Type"); got != tc.wantType {
				t.Errorf("Content-Type %q, want %q", got, tc.wantType)
			}
			if got := h.Get("Content-Disposition"); !strings.HasPrefix(got, tc.wantDisposition+";") || !strings.Contains(got, tc.name) {
				t.Errorf("Content-Disposition %q, want %s with the file name", got, tc.wantDisposition)
			}
			if h.Get("X-Content-Type-Options") != "nosniff" {
				t.Error("missing X-Content-Type-Options: nosniff")
			}
			if csp := h.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "sandbox") {
				t.Errorf("Content-Security-Policy %q must forbid script (default-src 'none'; sandbox)", csp)
			}
			if h.Get("Cache-Control") != "no-store" {
				t.Errorf("Cache-Control %q, want no-store", h.Get("Cache-Control"))
			}
			if tc.wantBody != "" && rr.Body.String() != tc.wantBody {
				t.Errorf("body %q, want %q", rr.Body.String(), tc.wantBody)
			}
		})
	}
}

func TestFilesRoute_RefusesEscapesAndListings(t *testing.T) {
	tmp, root := fileLinkFixture(t)
	srv := fileLinkServer([]string{root}, "")
	cases := []struct {
		name, path string
		want       int
	}{
		{"dotdot", root + "/../secret.txt", http.StatusForbidden},
		{"symlink out", filepath.Join(root, "link-out"), http.StatusForbidden},
		{"outside", filepath.Join(tmp, "secret.txt"), http.StatusForbidden},
		{"missing outside reads as outside", filepath.Join(tmp, "nope.txt"), http.StatusForbidden},
		{"missing inside", filepath.Join(root, "nope.txt"), http.StatusNotFound},
		{"directory", filepath.Join(root, "sub"), http.StatusForbidden},
		{"relative", "root/notes.txt", http.StatusBadRequest},
		{"empty", "", http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := getFile(srv, http.MethodGet, tc.path, nil)
			if rr.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", rr.Code, tc.want, rr.Body.String())
			}
			if strings.Contains(rr.Body.String(), "TOP SECRET") {
				t.Fatal("served a file outside the root")
			}
			var body struct {
				Error struct{ Code string } `json:"error"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil || body.Error.Code == "" {
				t.Errorf("want a JSON API error, got %q", rr.Body.String())
			}
		})
	}
}

func TestFilesRoute_OffWithoutRoots(t *testing.T) {
	_, root := fileLinkFixture(t)
	rr := getFile(fileLinkServer(nil, ""), http.MethodGet, filepath.Join(root, "notes.txt"), nil)
	if rr.Code != http.StatusNotFound || strings.Contains(rr.Body.String(), "hello") {
		t.Fatalf("with no roots the route must be off: status %d body %q", rr.Code, rr.Body.String())
	}
}

func TestFilesRoute_RequiresAuth(t *testing.T) {
	_, root := fileLinkFixture(t)
	srv := fileLinkServer([]string{root}, "s3cret")
	path := filepath.Join(root, "notes.txt")

	if rr := getFile(srv, http.MethodGet, path, nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("no credentials: status %d, want 401", rr.Code)
	}
	// The query string is never a credential on an API route.
	req := newLocalRequest(http.MethodGet, "/api/files?token=s3cret&path="+url.QueryEscape(path), nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("query token: status %d, want 401", rr.Code)
	}
	if rr := getFile(srv, http.MethodGet, path, http.Header{"Authorization": {"Bearer s3cret"}}); rr.Code != http.StatusOK {
		t.Fatalf("bearer: status %d, want 200", rr.Code)
	}
	// A browser tab opened from the deck carries the token cookie.
	cookie := http.Header{"Cookie": {"agentdeck_token=s3cret"}}
	if rr := getFile(srv, http.MethodGet, path, cookie); rr.Code != http.StatusOK || rr.Body.String() != "hello\n" {
		t.Fatalf("cookie: status %d body %q", rr.Code, rr.Body.String())
	}
}

func TestFilesRoute_ReadOnlyMethods(t *testing.T) {
	_, root := fileLinkFixture(t)
	srv := fileLinkServer([]string{root}, "")
	path := filepath.Join(root, "notes.txt")
	if rr := getFile(srv, http.MethodHead, path, nil); rr.Code != http.StatusOK || rr.Body.Len() != 0 {
		t.Fatalf("HEAD: status %d body %q", rr.Code, rr.Body.String())
	}
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		if rr := getFile(srv, m, path, nil); rr.Code != http.StatusMethodNotAllowed && rr.Code != http.StatusForbidden {
			t.Errorf("%s: status %d, want 405 (or a CSRF 403)", m, rr.Code)
		}
	}
}
