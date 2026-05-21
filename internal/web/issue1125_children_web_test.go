package web

// issue1125_children_web_test.go — coverage for the
// GET /api/sessions/{id}/children endpoint added in PR
// "feat(web): Children panel for conductor sessions".
//
// Per agent-deck-tdd-feature SKILL.md: happy / failure / boundary case per
// surface. This file covers the Go handler surface; the corresponding
// browser-driven coverage is in tests/web/e2e/children-panel.spec.js.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Build a snapshot that mimics a conductor session with two direct children
// and one grandchild via a nested conductor (boundary: deep nesting).
//
//	conductor-x (sess-cond)
//	  ├── child-a   (sess-a, claude)
//	  ├── child-b   (sess-b, conductor)
//	  │     └── grandchild-b1 (sess-b1, claude)
//
//	standalone (sess-solo) — no parent, no children
func childrenFixtureSnapshot() *MenuSnapshot {
	mk := func(id, title, parent string) MenuItem {
		return MenuItem{
			Type: MenuItemTypeSession,
			Session: &MenuSession{
				ID:              id,
				Title:           title,
				ParentSessionID: parent,
				Status:          session.StatusRunning,
				Tool:            "claude",
			},
		}
	}
	return &MenuSnapshot{
		Profile: "test",
		Items: []MenuItem{
			mk("sess-cond", "conductor-x", ""),
			mk("sess-a", "child-a", "sess-cond"),
			mk("sess-b", "child-b", "sess-cond"),
			mk("sess-b1", "grandchild-b1", "sess-b"),
			mk("sess-solo", "standalone", ""),
		},
	}
}

func newChildrenTestServer(snapshot *MenuSnapshot) *Server {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", Profile: "test"})
	srv.menuData = &fakeMenuDataLoader{snapshot: snapshot}
	return srv
}

type childNodeJSON struct {
	ID       string           `json:"id"`
	Title    string           `json:"title"`
	Children []*childNodeJSON `json:"children"`
}

type childrenRespJSON struct {
	SessionID string           `json:"sessionId"`
	Children  []*childNodeJSON `json:"children"`
}

func getChildren(t *testing.T, srv *Server, sessionID string) (*httptest.ResponseRecorder, *childrenRespJSON) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sessionID+"/children", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		return rr, nil
	}
	var body childrenRespJSON
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode children response: %v\nbody=%s", err, rr.Body.String())
	}
	return rr, &body
}

// Happy path — a conductor with two direct children returns a tree with two
// nodes at the top level.
func TestSessionChildren_HappyPath_TwoDirectChildren(t *testing.T) {
	srv := newChildrenTestServer(childrenFixtureSnapshot())

	rr, body := getChildren(t, srv, "sess-cond")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if body.SessionID != "sess-cond" {
		t.Errorf("sessionId = %q, want sess-cond", body.SessionID)
	}
	if len(body.Children) != 2 {
		t.Fatalf("expected 2 children, got %d", len(body.Children))
	}
	ids := []string{body.Children[0].ID, body.Children[1].ID}
	gotA, gotB := false, false
	for _, id := range ids {
		switch id {
		case "sess-a":
			gotA = true
		case "sess-b":
			gotB = true
		}
	}
	if !gotA || !gotB {
		t.Errorf("expected children {sess-a, sess-b}, got %v", ids)
	}
}

// Boundary — deep nesting (conductor → conductor → claude) renders
// recursively. The grandchild MUST appear under its parent, not at the
// top level.
func TestSessionChildren_Boundary_DeepNesting(t *testing.T) {
	srv := newChildrenTestServer(childrenFixtureSnapshot())

	_, body := getChildren(t, srv, "sess-cond")
	if body == nil {
		t.Fatal("nil body")
	}

	var bNode *childNodeJSON
	for _, c := range body.Children {
		if c.ID == "sess-b" {
			bNode = c
			break
		}
	}
	if bNode == nil {
		t.Fatal("sess-b child node missing")
	}
	if len(bNode.Children) != 1 {
		t.Fatalf("expected 1 grandchild under sess-b, got %d", len(bNode.Children))
	}
	if bNode.Children[0].ID != "sess-b1" {
		t.Errorf("grandchild id = %q, want sess-b1", bNode.Children[0].ID)
	}
	// Grandchild has no further descendants — children array must be empty,
	// not nil, so JSON consumers always see `"children":[]`.
	if bNode.Children[0].Children == nil {
		t.Error("leaf children must be an empty array, not nil/missing")
	}
}

// Failure mode — a non-conductor session returns an empty children tree,
// NOT a 404. Per prompt: "Non-conductor session → empty tree (not 404)."
func TestSessionChildren_NonConductor_EmptyTreeNot404(t *testing.T) {
	srv := newChildrenTestServer(childrenFixtureSnapshot())

	rr, body := getChildren(t, srv, "sess-solo")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 (empty tree), got %d: %s", rr.Code, rr.Body.String())
	}
	if body.SessionID != "sess-solo" {
		t.Errorf("sessionId = %q, want sess-solo", body.SessionID)
	}
	if len(body.Children) != 0 {
		t.Errorf("non-conductor must have 0 children, got %d", len(body.Children))
	}
}

// Failure mode — unknown session id returns 404 with NOT_FOUND code.
func TestSessionChildren_UnknownSession_404(t *testing.T) {
	srv := newChildrenTestServer(childrenFixtureSnapshot())

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/does-not-exist/children", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rr.Code, rr.Body.String())
	}
	if !contains(rr.Body.String(), ErrCodeNotFound) {
		t.Errorf("expected NOT_FOUND code, got %s", rr.Body.String())
	}
}

// Failure mode — only GET is allowed; POST/PUT/DELETE return 405.
func TestSessionChildren_MethodNotAllowed(t *testing.T) {
	srv := newChildrenTestServer(childrenFixtureSnapshot())

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(method, "/api/sessions/sess-cond/children", nil)
		rr := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s expected 405, got %d", method, rr.Code)
		}
	}
}

// Boundary — a conductor with no children returns an empty array (not nil).
func TestSessionChildren_ConductorWithNoChildren_EmptyArray(t *testing.T) {
	srv := newChildrenTestServer(&MenuSnapshot{
		Profile: "test",
		Items: []MenuItem{
			{
				Type: MenuItemTypeSession,
				Session: &MenuSession{
					ID:    "lonely-cond",
					Title: "conductor-empty",
				},
			},
		},
	})

	rr, body := getChildren(t, srv, "lonely-cond")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if body.Children == nil {
		t.Fatal("children must be [] not nil for empty conductor")
	}
	// Verify the raw JSON contains "children":[] not "children":null.
	if !contains(rr.Body.String(), `"children":[]`) {
		t.Errorf("expected JSON to contain \"children\":[], got: %s", rr.Body.String())
	}
}

// Boundary — cyclic parent pointers (defensive). If a corrupted snapshot
// produces a cycle, the handler must not loop forever and must not panic.
func TestSessionChildren_CycleSafe(t *testing.T) {
	mk := func(id, parent string) MenuItem {
		return MenuItem{
			Type: MenuItemTypeSession,
			Session: &MenuSession{
				ID:              id,
				Title:           id,
				ParentSessionID: parent,
			},
		}
	}
	// a → b → a cycle (data corruption scenario).
	srv := newChildrenTestServer(&MenuSnapshot{
		Profile: "test",
		Items: []MenuItem{
			mk("a", "b"),
			mk("b", "a"),
		},
	})

	rr, _ := getChildren(t, srv, "a")
	if rr.Code != http.StatusOK {
		t.Fatalf("cycle-safety: expected 200 (handler must not panic/timeout), got %d", rr.Code)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	if len(needle) == 0 {
		return 0
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
