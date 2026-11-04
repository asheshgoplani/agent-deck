package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The 30-day token cookie (#3031) makes a logged-in deck framable by a
// same-site page (another port on the host, a sibling tailnet machine) for
// much longer, so every response must forbid framing: the app shell, the
// static copy of index.html, error pages and file links. Nothing in the
// product frames the web UI.
func TestFrameProtection_AllResponsesDenyFraming(t *testing.T) {
	root := t.TempDir()
	htmlFile := filepath.Join(root, "page.html")
	if err := os.WriteFile(htmlFile, []byte("<p>x</p>"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewServer(Config{ListenAddr: "127.0.0.1:8420", Token: issue3031Token, FileLinkRoots: []string{root}})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	for _, path := range []string{
		"/",
		"/?token=" + issue3031Token,
		"/s/some-session",
		"/static/index.html",
		"/manifest.webmanifest",
		"/no-such-page",
		"/api/sessions",
		"/api/files?path=" + htmlFile,
	} {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+issue3031Token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		_ = resp.Body.Close()
		if got := resp.Header.Get("X-Frame-Options"); got != "DENY" {
			t.Errorf("%s (%d): X-Frame-Options = %q, want DENY", path, resp.StatusCode, got)
		}
		found := false
		for _, csp := range resp.Header.Values("Content-Security-Policy") {
			if strings.Contains(csp, "frame-ancestors 'none'") {
				found = true
			}
		}
		if !found {
			t.Errorf("%s (%d): no CSP frame-ancestors 'none' in %q", path, resp.StatusCode, resp.Header.Values("Content-Security-Policy"))
		}
	}

	// A request for a Host the server refuses still gets the headers.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/", nil)
	req.Host = "evil.example"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.Header.Get("X-Frame-Options") != "DENY" {
		t.Errorf("refused-host response (%d) lacks X-Frame-Options", resp.StatusCode)
	}
}
