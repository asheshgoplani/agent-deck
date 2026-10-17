package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPathSuggestions(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Chdir(dir)
	for _, name := range []string{"AirConnect", "SpotConnect", ".hidden"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "A-file"), []byte("private contents"), 0600); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", WebMutations: true})
	for _, tc := range []struct {
		name, prefix string
		want         []string
	}{
		{"children", dir + "/", []string{dir + "/AirConnect", dir + "/SpotConnect"}},
		{"partial case insensitive", dir + "/a", []string{dir + "/AirConnect"}},
		{"hidden segment", dir + "/.", []string{dir + "/.hidden"}},
		{"tilde", "~/", []string{"~/AirConnect", "~/SpotConnect"}},
		{"tilde partial", "~/sp", []string{"~/SpotConnect"}},
		{"tilde hidden", "~/.", []string{"~/.hidden"}},
		{"relative", "./sp", []string{"SpotConnect"}},
		{"exact directory", dir + "/AirConnect", []string{dir + "/AirConnect"}},
		{"missing parent", dir + "/missing/", []string{}},
		{"file", dir + "/A-file", []string{}},
		{"empty", "", []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rr, newLocalRequest(http.MethodGet, "/api/path-suggestions?prefix="+url.QueryEscape(tc.prefix), nil))
			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			var body struct {
				Suggestions []string `json:"suggestions"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(body.Suggestions, tc.want) {
				t.Fatalf("suggestions=%v want=%v", body.Suggestions, tc.want)
			}
		})
	}
}

func TestPathSuggestionsLimit(t *testing.T) {
	t.Setenv("PR_TRIAL_UNSET", "")
	dir := t.TempDir()
	for i := range 30 {
		if err := os.Mkdir(filepath.Join(dir, fmt.Sprintf("${PR_TRIAL_UNSET}%02d", i)), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(dir, fmt.Sprintf("dir-%02d", i)), 0755); err != nil {
			t.Fatal(err)
		}
	}
	rr := httptest.NewRecorder()
	NewServer(Config{ListenAddr: "127.0.0.1:0", WebMutations: true}).Handler().ServeHTTP(rr,
		newLocalRequest(http.MethodGet, "/api/path-suggestions?prefix="+url.QueryEscape(dir+"/"), nil))
	var body struct {
		Suggestions []string `json:"suggestions"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Suggestions) != 20 || body.Suggestions[19] != dir+"/dir-19" {
		t.Fatalf("expected first 20 sorted directories, got %v", body.Suggestions)
	}
}

func TestPathSuggestionsAuthAndMethod(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", Token: "test-token", WebMutations: true})
	for _, tc := range []struct {
		name, method, header, query, cookie string
		status                              int
	}{
		{"missing", "GET", "", "", "", http.StatusUnauthorized},
		{"wrong", "GET", "Bearer wrong", "", "", http.StatusUnauthorized},
		{"query is not auth", "GET", "", "&token=test-token", "", http.StatusUnauthorized},
		{"bearer", "GET", "Bearer test-token", "", "", http.StatusOK},
		{"cookie", "GET", "", "", "test-token", http.StatusOK},
		{"method", "PUT", "Bearer test-token", "", "", http.StatusMethodNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := newLocalRequest(tc.method, "/api/path-suggestions?prefix="+tc.query, nil)
			req.Header.Set("Authorization", tc.header)
			req.Header.Set("Origin", "http://localhost")
			if tc.cookie != "" {
				req.AddCookie(&http.Cookie{Name: "agentdeck_token", Value: tc.cookie})
			}
			rr := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rr, req)
			if rr.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", rr.Code, tc.status, rr.Body.String())
			}
		})
	}
}

// A completion must not become a different path when submitted to the resolver.
func TestPathSuggestionsOmitReinterpretedNames(t *testing.T) {
	t.Setenv("PR_TRIAL_UNSET", "")
	t.Setenv("PR_TRIAL_BOUND", "different")
	dir := t.TempDir()
	for _, name := range []string{"cash$PR_TRIAL_UNSET", "cash${PR_TRIAL_BOUND}", "cash$", "ordinary"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0755); err != nil {
			t.Fatal(err)
		}
	}
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", WebMutations: true})
	for _, prefix := range []string{dir + "/", dir + "/cash$PR_TRIAL_UNSET"} {
		rr := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rr, newLocalRequest(http.MethodGet, "/api/path-suggestions?prefix="+url.QueryEscape(prefix), nil))
		var body struct {
			Suggestions []string `json:"suggestions"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		want := []string{}
		if prefix == dir+"/" {
			want = []string{dir + "/cash$", dir + "/ordinary"}
		}
		if !reflect.DeepEqual(body.Suggestions, want) {
			t.Fatalf("prefix=%q suggestions=%v want=%v", prefix, body.Suggestions, want)
		}
	}
}

// Read-only servers must not enumerate directories: the only consumer is the
// New Session dialog, which is not shown when mutations are disabled.
func TestPathSuggestionsForbiddenWithoutMutations(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "secret"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		cfg  Config
	}{
		{"read-only", Config{ListenAddr: "127.0.0.1:0", ReadOnly: true}},
		{"read-only with mutations flag", Config{ListenAddr: "127.0.0.1:0", ReadOnly: true, WebMutations: true}},
		{"mutations disabled", Config{ListenAddr: "127.0.0.1:0", WebMutations: false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			NewServer(tc.cfg).Handler().ServeHTTP(rr,
				newLocalRequest(http.MethodGet, "/api/path-suggestions?prefix="+url.QueryEscape(dir+"/"), nil))
			if rr.Code != http.StatusForbidden {
				t.Fatalf("status=%d want=403 body=%s", rr.Code, rr.Body.String())
			}
		})
	}
}
