package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSessionDirectoryPreview(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	t.Setenv("HOME", root)
	t.Setenv("SESSION_DIR_ROOT", root)
	if err := os.WriteFile(filepath.Join(root, "file"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path string
		status     int
		missing    []string
	}{
		{"existing", root, 200, []string{}},
		{"parents", root + "/parent/child", 200, []string{root + "/parent", root + "/parent/child"}},
		{"relative", "parent/child", 200, []string{root + "/parent", root + "/parent/child"}},
		{"home", "~/parent/child", 200, []string{root + "/parent", root + "/parent/child"}},
		{"env", "$SESSION_DIR_ROOT/parent/child", 200, []string{root + "/parent", root + "/parent/child"}},
		{"symlink", root + "/link/child", 200, []string{root + "/link/child"}},
		{"file", root + "/file", 400, nil},
		{"file parent", root + "/file/child", 400, nil},
		{"empty", "", 400, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := NewServer(Config{ListenAddr: "127.0.0.1:0", Token: "test-token"})
			req := newLocalRequest(http.MethodGet, "/api/session-directory?path="+url.QueryEscape(tc.path), nil)
			req.Header.Set("Authorization", "Bearer test-token")
			rr := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rr, req)
			if rr.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rr.Code, tc.status, rr.Body.String())
			}
			if tc.status == 200 {
				var got struct {
					Path    string   `json:"path"`
					Missing []string `json:"missing"`
				}
				if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if !filepath.IsAbs(got.Path) || !reflect.DeepEqual(got.Missing, tc.missing) {
					t.Fatalf("preview = %+v, want missing %v", got, tc.missing)
				}
			}
		})
	}
	if _, err := os.Stat(root + "/parent"); !os.IsNotExist(err) {
		t.Fatalf("preview created a directory: %v", err)
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		srv := NewServer(Config{ListenAddr: "127.0.0.1:0", Token: "test-token"})
		req := newLocalRequest(method, "/api/session-directory?path="+url.QueryEscape(root+"/new"), nil)
		req.Header.Set("Origin", "http://localhost")
		rr := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated %s: %d", method, rr.Code)
		}
		req.Header.Set("Authorization", "Bearer test-token")
		rr = httptest.NewRecorder()
		srv.Handler().ServeHTTP(rr, req)
		if method == http.MethodPost && rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("POST: %d", rr.Code)
		}
	}
}

func TestSessionsCreateDirectory(t *testing.T) {
	for _, tc := range []struct {
		name                                                                       string
		optIn, disabled, unauthorized, crossOrigin, blocked, denied, literalDollar bool
		status                                                                     int
	}{
		{name: "opt in", optIn: true, status: 201},
		{name: "literal dollar after expansion", optIn: true, literalDollar: true, status: 201},
		{name: "default", status: 500},
		{name: "mutations disabled", optIn: true, disabled: true, status: 403},
		{name: "unauthorized", optIn: true, unauthorized: true, status: 401},
		{name: "cross origin", optIn: true, crossOrigin: true, status: 403},
		{name: "file in way", optIn: true, blocked: true, status: 400},
		{name: "permission denied", optIn: true, denied: true, status: 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.literalDollar {
				t.Setenv("UNSET", "")
				root = filepath.Join(root, "cash$UNSET")
				if err := os.Mkdir(root, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("SESSION_DIR_ROOT", root)
			path := root + "/parent/child"
			if tc.blocked {
				if err := os.WriteFile(root+"/parent", nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.denied {
				if os.Getuid() == 0 {
					t.Skip("requires non-root user")
				}
				if err := os.Chmod(root, 0o500); err != nil {
					t.Fatal(err)
				}
				defer os.Chmod(root, 0o700)
			}
			srv := NewServer(Config{ListenAddr: "127.0.0.1:0", WebMutations: !tc.disabled, Token: "test-token"})
			srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
			called := false
			srv.mutator = &fakeMutator{createSessionFn: func(_, _, projectPath, _, _, _ string) (string, error) {
				called = true
				// Resolve as the real WebMutator does; legacy requests retain their input.
				projectPath = os.ExpandEnv(projectPath)
				info, err := os.Stat(projectPath)
				if err != nil {
					return "", fmt.Errorf("path does not exist: %s", projectPath)
				}
				if !info.IsDir() || projectPath != path {
					t.Fatalf("session path: %s", projectPath)
				}
				return "new-id", nil
			}}
			body, err := json.Marshal(map[string]any{"title": "Test", "tool": "shell", "projectPath": "$SESSION_DIR_ROOT/parent/child", "createDir": tc.optIn})
			if err != nil {
				t.Fatal(err)
			}
			req := newLocalRequest(http.MethodPost, "/api/sessions", bytes.NewReader(body))
			req.Header.Set("Origin", "http://localhost")
			if !tc.unauthorized {
				req.Header.Set("Authorization", "Bearer test-token")
			}
			if tc.crossOrigin {
				req.Header.Set("Origin", "http://other.invalid")
			}
			rr := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rr, req)
			if rr.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rr.Code, tc.status, rr.Body.String())
			}
			if tc.status == 201 {
				entries, err := os.ReadDir(path)
				if err != nil || len(entries) != 0 {
					t.Fatalf("created folder contains side effects: %v, %v", entries, err)
				}
			} else {
				if _, err := os.Stat(path); err == nil {
					t.Fatal("created without successful opt-in")
				}
				if tc.optIn && called {
					t.Fatal("started session after rejected creation")
				}
				if tc.blocked && !strings.Contains(rr.Body.String(), "directory") {
					t.Fatalf("unclear error: %s", rr.Body.String())
				}
				if tc.denied && !strings.Contains(rr.Body.String(), "permission denied") {
					t.Fatalf("unclear error: %s", rr.Body.String())
				}
			}
		})
	}
}

func TestSessionsCreateDirectoryRejectsAdditionalParents(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "parent", "child")
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", WebMutations: true})
	srv.mutator = &fakeMutator{createSessionFn: func(_, _, _, _, _, _ string) (string, error) {
		t.Fatal("started session with stale directory consent")
		return "", nil
	}}
	// Only child was missing at preview time; parent has since disappeared.
	body, err := json.Marshal(map[string]any{"title": "Test", "projectPath": path, "createDir": true, "createDirPaths": []string{path}})
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, newLocalRequest(http.MethodPost, "/api/sessions", bytes.NewReader(body)))
	if rr.Code != http.StatusConflict {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "parent")); !os.IsNotExist(err) {
		t.Fatalf("created an unapproved parent: %v", err)
	}
}
