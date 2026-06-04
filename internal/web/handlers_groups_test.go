package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGroupsCollectionGET(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
		Profile:    "test",
	})
	srv.menuData = &fakeMenuDataLoader{
		snapshot: &MenuSnapshot{
			Profile: "test",
			Items: []MenuItem{
				{
					Type: MenuItemTypeGroup,
					Group: &MenuGroup{
						Name: "work",
						Path: "work",
					},
				},
				{
					Type: MenuItemTypeSession,
					Session: &MenuSession{
						ID:    "sess-1",
						Title: "alpha",
					},
				},
			},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/groups", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"groups"`) {
		t.Errorf("expected 'groups' key in response, got: %s", body)
	}
	if !strings.Contains(body, `"work"`) {
		t.Errorf("expected group name in response, got: %s", body)
	}
}

func TestGroupsCollectionPOSTCreatesGroup(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: true,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	srv.mutator = &fakeMutator{
		createGroupFn: func(name, parentPath string) (string, error) {
			return "new-group", nil
		},
	}

	body := strings.NewReader(`{"name":"newgroup"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/groups", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d: %s", http.StatusCreated, rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "new-group") {
		t.Errorf("expected group path in response, got: %s", rr.Body.String())
	}
}

func TestGroupCreateMissingName(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: true,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	srv.mutator = &fakeMutator{}

	body := strings.NewReader(`{"parentPath":""}`)
	req := httptest.NewRequest(http.MethodPost, "/api/groups", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d: %s", http.StatusBadRequest, rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), ErrCodeBadRequest) {
		t.Errorf("expected INVALID_REQUEST error, got: %s", rr.Body.String())
	}
}

func TestGroupRenamePATCHOK(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: true,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	srv.mutator = &fakeMutator{
		renameGroupFn: func(groupPath, newName string) error { return nil },
	}

	body := strings.NewReader(`{"name":"renamed"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/groups/mygroup", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}
}

func TestGroupDeleteOK(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: true,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	srv.mutator = &fakeMutator{
		deleteGroupFn: func(groupPath string) error { return nil },
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/groups/mygroup", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}
}

func TestGroupDeleteDefaultGroupReturns400(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: true,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	srv.mutator = &fakeMutator{}

	req := httptest.NewRequest(http.MethodDelete, "/api/groups/my-sessions", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d: %s", http.StatusBadRequest, rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "cannot delete default group") {
		t.Errorf("expected default group protection message, got: %s", rr.Body.String())
	}
}

// ----------------------------------------------------------------------------
// Group reparent (PATCH /api/groups/{path} with parentPath field).
// ----------------------------------------------------------------------------

func TestGroupReparentPATCHOK(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", WebMutations: true})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}

	var gotSrc, gotDest string
	var renameCalled bool
	srv.mutator = &fakeMutator{
		moveGroupFn: func(src, dest string) error {
			gotSrc = src
			gotDest = dest
			return nil
		},
		renameGroupFn: func(groupPath, newName string) error {
			renameCalled = true
			return nil
		},
	}

	body := strings.NewReader(`{"parentPath":"B"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/groups/A", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("reparent: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if gotSrc != "A" || gotDest != "B" {
		t.Errorf("reparent: MoveGroup called with (%q,%q), want (A,B)", gotSrc, gotDest)
	}
	if renameCalled {
		t.Errorf("reparent: RenameGroup should NOT be invoked when parentPath is set")
	}
	if !strings.Contains(rr.Body.String(), `"reparentedTo":"B"`) {
		t.Errorf("reparent: response missing reparentedTo: %s", rr.Body.String())
	}
}

func TestGroupReparentToRootPATCHOK(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", WebMutations: true})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}

	var gotSrc, gotDest string
	srv.mutator = &fakeMutator{
		moveGroupFn: func(src, dest string) error {
			gotSrc = src
			gotDest = dest
			return nil
		},
	}

	// parentPath:"" (empty string pointer) means move to root.
	body := strings.NewReader(`{"parentPath":""}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/groups/A", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("reparent to root: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if gotSrc != "A" || gotDest != "" {
		t.Errorf("reparent to root: MoveGroup called with (%q,%q), want (A,\"\")", gotSrc, gotDest)
	}
}

func TestGroupRenamePATCHRegressionAfterReparent(t *testing.T) {
	// Regression: name-only rename must still work when parentPath is absent.
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", WebMutations: true})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}

	var moveGroupCalled bool
	var gotNewName string
	srv.mutator = &fakeMutator{
		renameGroupFn: func(groupPath, newName string) error {
			gotNewName = newName
			return nil
		},
		moveGroupFn: func(src, dest string) error {
			moveGroupCalled = true
			return nil
		},
	}

	body := strings.NewReader(`{"name":"newname"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/groups/A", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("rename regression: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if gotNewName != "newname" {
		t.Errorf("rename regression: RenameGroup called with name=%q, want newname", gotNewName)
	}
	if moveGroupCalled {
		t.Errorf("rename regression: MoveGroup should NOT be invoked when only name is set")
	}
}

func TestGroupPatchNeitherNameNorParentPath400(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", WebMutations: true})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	srv.mutator = &fakeMutator{}

	body := strings.NewReader(`{}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/groups/A", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("neither field: expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), ErrCodeBadRequest) {
		t.Errorf("neither field: expected INVALID_REQUEST error, got: %s", rr.Body.String())
	}
}

func TestGroupReparentMoveGroupError500(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", WebMutations: true})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	srv.mutator = &fakeMutator{
		moveGroupFn: func(src, dest string) error {
			return fmt.Errorf("circular move not allowed")
		},
	}

	body := strings.NewReader(`{"parentPath":"X"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/groups/A", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("move error: expected 500, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), ErrCodeInternalError) {
		t.Errorf("move error: expected INTERNAL_ERROR, got: %s", rr.Body.String())
	}
}

func TestGroupReparentMutationsDisabled403(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", WebMutations: false})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}

	body := strings.NewReader(`{"parentPath":"B"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/groups/A", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("mutations disabled: expected 403, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestGroupReparentNilMutator503(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", WebMutations: true})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	// mutator intentionally nil

	body := strings.NewReader(`{"parentPath":"B"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/groups/A", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil mutator: expected 503, got %d: %s", rr.Code, rr.Body.String())
	}
}
