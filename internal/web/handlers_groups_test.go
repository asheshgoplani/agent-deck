package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
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

// Collapse state is shared with the TUI, so PATCH carries `expanded` alongside
// the existing rename. Both fields are optional; at least one is required.
func TestGroupPATCHExpandedPersists(t *testing.T) {
	for _, want := range []bool{false, true} {
		srv := NewServer(Config{
			ListenAddr:   "127.0.0.1:0",
			WebMutations: true,
		})
		srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}

		var gotPath string
		var gotExpanded bool
		calls := 0
		srv.mutator = &fakeMutator{
			setGroupExpandedFn: func(groupPath string, expanded bool) error {
				calls++
				gotPath, gotExpanded = groupPath, expanded
				return nil
			},
		}

		body := strings.NewReader(fmt.Sprintf(`{"expanded":%t}`, want))
		req := httptest.NewRequest(http.MethodPatch, "/api/groups/stride/ws1", body)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("expanded=%t: expected 200, got %d: %s", want, rr.Code, rr.Body.String())
		}
		if calls != 1 {
			t.Fatalf("expanded=%t: expected exactly one mutator call, got %d", want, calls)
		}
		// Nested paths must survive the /api/groups/ prefix trim intact.
		if gotPath != "stride/ws1" {
			t.Errorf("expanded=%t: group path = %q, want %q", want, gotPath, "stride/ws1")
		}
		if gotExpanded != want {
			t.Errorf("expanded=%t: mutator got %t", want, gotExpanded)
		}
	}
}

// A bare `{}` used to be rejected only because `name` was missing. Now that
// `expanded` is also accepted, the guard has to require at least one of them
// rather than silently succeeding as a no-op.
func TestGroupPATCHRequiresNameOrExpanded(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: true,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	srv.mutator = &fakeMutator{}

	body := strings.NewReader(`{}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/groups/mygroup", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), ErrCodeBadRequest) {
		t.Errorf("expected %s, got: %s", ErrCodeBadRequest, rr.Body.String())
	}
}

// A stale browser tab can PATCH a group the TUI already deleted. That must
// read as 404, not as a successful collapse the sidebar then trusts.
func TestGroupPATCHExpandedUnknownGroupReturns404(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: true,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	srv.mutator = &fakeMutator{
		setGroupExpandedFn: func(string, bool) error { return ErrGroupNotFound },
	}

	body := strings.NewReader(`{"expanded":false}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/groups/ghost", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rr.Code, rr.Body.String())
	}
}

// Omitting `expanded` must leave collapse state alone — the pointer exists
// precisely so a rename does not read as "expand this group".
func TestGroupPATCHRenameLeavesExpandedUntouched(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: true,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	srv.mutator = &fakeMutator{
		renameGroupFn: func(string, string) error { return nil },
		setGroupExpandedFn: func(string, bool) error {
			t.Error("rename-only PATCH must not touch collapse state")
			return nil
		},
	}

	body := strings.NewReader(`{"name":"renamed"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/groups/mygroup", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

// When both arrive together the collapse has to land FIRST: the rename moves
// the group to a new path, which a later collapse write would miss entirely.
func TestGroupPATCHAppliesExpandedBeforeRename(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: true,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}

	var order []string
	srv.mutator = &fakeMutator{
		setGroupExpandedFn: func(string, bool) error { order = append(order, "expanded"); return nil },
		renameGroupFn:      func(string, string) error { order = append(order, "rename"); return nil },
	}

	body := strings.NewReader(`{"name":"renamed","expanded":false}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/groups/mygroup", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if len(order) != 2 || order[0] != "expanded" || order[1] != "rename" {
		t.Errorf("expected [expanded rename], got %v", order)
	}
}

// One endpoint, one answer for "no such group". RenameGroup surfaces
// GroupTree's session.ErrGroupNotFound while SetGroupExpanded reports the
// mutator-contract web.ErrGroupNotFound; before isGroupNotFound folded them,
// PATCH answered 404 for `expanded` and 500 for `name` on the same dead group.
func TestGroupPATCHRenameUnknownGroupReturns404(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: true,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	srv.mutator = &fakeMutator{
		renameGroupFn: func(string, string) error { return session.ErrGroupNotFound },
	}

	body := strings.NewReader(`{"name":"renamed"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/groups/ghost", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rr.Code, rr.Body.String())
	}
}

// A real failure must still be a 500 — the 404 mapping is for the missing-group
// sentinels only, not a blanket downgrade of every rename error.
func TestGroupPATCHRenameRealErrorStays500(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr:   "127.0.0.1:0",
		WebMutations: true,
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	srv.mutator = &fakeMutator{
		renameGroupFn: func(string, string) error { return errors.New("disk on fire") },
	}

	body := strings.NewReader(`{"name":"renamed"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/groups/mygroup", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rr.Code, rr.Body.String())
	}
}
