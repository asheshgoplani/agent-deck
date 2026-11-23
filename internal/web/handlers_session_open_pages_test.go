package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/openpages"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestSessionOpenPagesRoutes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)

	project := t.TempDir()
	inst := session.NewInstanceWithTool("pages", project, "bash")
	inst.ID = "pages-session"
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", Token: "owner-token"})
	service := NewSessionDataService("")
	service.openStorage = func(string) (storageLoader, error) {
		return &fakeStorage{instances: []*session.Instance{inst}}, nil
	}
	srv.menuData = service

	if err := os.WriteFile(filepath.Join(project, "plan.md"), []byte("# Plan\n\n<script>x()</script>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "p.png"), []byte("\x89PNG\r\n\x1a\nZ"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "page.html"), []byte(`<title>Board</title><img src="p.png"><script>draw()</script>`), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret.html")
	if err := os.WriteFile(outside, []byte("<p>private</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	md, err := openpages.Record("", openpages.Page{SessionID: inst.ID, Path: filepath.Join(project, "plan.md"), Kind: openpages.KindMarkdown})
	if err != nil {
		t.Fatal(err)
	}
	page, err := openpages.Record("", openpages.Page{SessionID: inst.ID, Path: filepath.Join(project, "page.html"), Kind: openpages.KindHTML})
	if err != nil {
		t.Fatal(err)
	}
	// A dotfile in the project never reaches the phone, even when listed.
	if err := os.WriteFile(filepath.Join(project, ".env"), []byte("SECRET-VALUE-42"), 0o644); err != nil {
		t.Fatal(err)
	}
	envRec, err := openpages.Record("", openpages.Page{SessionID: inst.ID, Path: filepath.Join(project, ".env"), Kind: openpages.KindText})
	if err != nil {
		t.Fatal(err)
	}
	// A record pointing outside the project (a tampered index) is refused at serve time.
	bad, err := openpages.Record("", openpages.Page{SessionID: inst.ID, Path: outside, Kind: openpages.KindHTML})
	if err != nil {
		t.Fatal(err)
	}

	get := func(target string, auth bool) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		req := newLocalRequest(http.MethodGet, target, nil)
		if auth {
			req.Header.Set("Authorization", "Bearer owner-token")
		}
		srv.Handler().ServeHTTP(rr, req)
		return rr
	}
	base := "/api/sessions/" + inst.ID + "/open-pages"
	if rr := get(base, false); rr.Code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", rr.Code)
	}
	rr := get(base, true)
	var list struct {
		Session string           `json:"session"`
		Pages   []openpages.Page `json:"pages"`
	}
	if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &list) != nil || len(list.Pages) != 4 || list.Pages[2].ID != page.ID {
		t.Fatalf("list: %d %s", rr.Code, rr.Body.String())
	}
	rr = get(base+"/"+page.ID, true)
	body := rr.Body.String()
	csp := rr.Header().Get("Content-Security-Policy")
	if rr.Code != http.StatusOK || !strings.HasPrefix(rr.Header().Get("Content-Type"), "text/html") ||
		!strings.Contains(body, "data:image/png;base64,") || !strings.Contains(body, "<script>draw()</script>") ||
		!strings.Contains(csp, "sandbox allow-scripts;") || strings.Contains(csp, "allow-same-origin") || strings.Contains(csp, "allow-popups") || !strings.Contains(csp, "frame-ancestors 'none'") ||
		!strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "connect-src 'none'") || !strings.Contains(csp, "form-action 'none'") || strings.Contains(csp, "https:") ||
		rr.Header().Get("X-Content-Type-Options") != "nosniff" || rr.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("html page: %d %v %s", rr.Code, rr.Header(), body)
	}
	rr = get(base+"/"+md.ID+"?format=json", true)
	var shown map[string]any
	if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &shown) != nil || shown["title"] != "Plan" ||
		!strings.Contains(shown["html"].(string), "&lt;script&gt;") {
		t.Fatalf("md json: %d %s", rr.Code, rr.Body.String())
	}
	if rr = get(base+"/"+envRec.ID, true); rr.Code != http.StatusForbidden || strings.Contains(rr.Body.String(), "SECRET-VALUE-42") {
		t.Fatalf(".env: %d %s", rr.Code, rr.Body.String())
	}
	if rr = get(base+"/"+bad.ID, true); rr.Code != http.StatusForbidden || strings.Contains(rr.Body.String(), "private") {
		t.Fatalf("outside: %d %s", rr.Code, rr.Body.String())
	}
	for _, target := range []string{base + "/0123456789abcdef", base + "/..%2f..%2fetc", base + "/" + page.ID + "/x", "/api/sessions/nope/open-pages"} {
		if rr = get(target, true); rr.Code != http.StatusNotFound && rr.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", target, rr.Code, rr.Body.String())
		}
	}
}
