// Tests for the Web UI MCP management endpoints. Covers the four PARITY_MATRIX
// rows that were MISSING before this PR: Attach MCP, Detach MCP, List MCPs,
// Toggle pooled ↔ local. Each surface has happy-path, failure-mode, and
// boundary-case coverage per agent-deck-tdd-feature SKILL.md.
package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// fakeMCPManager records every call and supports per-method injectable errors.
type fakeMCPManager struct {
	mu sync.Mutex

	catalog     []MCPCatalogEntry
	attached    map[string]map[string][]string
	listErr     error
	attachErr   error
	detachErr   error
	moveErr     error
	attachCalls []mcpAttachCall
	detachCalls []mcpAttachCall
	moveCalls   []mcpMoveCall
}

type mcpAttachCall struct {
	ProjectPath, Name, Scope string
}
type mcpMoveCall struct {
	ProjectPath, Name, FromScope, ToScope string
}

func newFakeMCPManager() *fakeMCPManager {
	return &fakeMCPManager{attached: map[string]map[string][]string{}}
}

func (f *fakeMCPManager) ListCatalog() []MCPCatalogEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]MCPCatalogEntry(nil), f.catalog...)
}

func (f *fakeMCPManager) ListAttached(projectPath string) (map[string][]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make(map[string][]string, 3)
	for _, scope := range []string{"local", "global", "user"} {
		if names := f.attached[projectPath][scope]; names != nil {
			cp := append([]string(nil), names...)
			sort.Strings(cp)
			out[scope] = cp
		} else {
			out[scope] = []string{}
		}
	}
	return out, nil
}

func (f *fakeMCPManager) Attach(projectPath, name, scope string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.attachErr != nil {
		return f.attachErr
	}
	f.attachCalls = append(f.attachCalls, mcpAttachCall{projectPath, name, scope})
	if f.attached[projectPath] == nil {
		f.attached[projectPath] = map[string][]string{}
	}
	for _, existing := range f.attached[projectPath][scope] {
		if existing == name {
			return nil
		}
	}
	f.attached[projectPath][scope] = append(f.attached[projectPath][scope], name)
	return nil
}

func (f *fakeMCPManager) Detach(projectPath, name, scope string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.detachErr != nil {
		return f.detachErr
	}
	f.detachCalls = append(f.detachCalls, mcpAttachCall{projectPath, name, scope})
	names := f.attached[projectPath][scope]
	out := names[:0]
	for _, n := range names {
		if n != name {
			out = append(out, n)
		}
	}
	if f.attached[projectPath] == nil {
		f.attached[projectPath] = map[string][]string{}
	}
	f.attached[projectPath][scope] = out
	return nil
}

func (f *fakeMCPManager) Move(projectPath, name, fromScope, toScope string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.moveErr != nil {
		return f.moveErr
	}
	f.moveCalls = append(f.moveCalls, mcpMoveCall{projectPath, name, fromScope, toScope})
	if f.attached[projectPath] == nil {
		f.attached[projectPath] = map[string][]string{}
	}
	from := f.attached[projectPath][fromScope]
	out := from[:0]
	for _, n := range from {
		if n != name {
			out = append(out, n)
		}
	}
	f.attached[projectPath][fromScope] = out
	for _, existing := range f.attached[projectPath][toScope] {
		if existing == name {
			return nil
		}
	}
	f.attached[projectPath][toScope] = append(f.attached[projectPath][toScope], name)
	return nil
}

func newMCPTestServer(t *testing.T, mgr MCPManager, mutationsAllowed bool) *Server {
	t.Helper()
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", WebMutations: mutationsAllowed})
	srv.menuData = &fakeMenuDataLoader{
		snapshot: &MenuSnapshot{
			Items: []MenuItem{
				{Type: MenuItemTypeSession, Session: &MenuSession{
					ID: "sess-001", Title: "alpha", Tool: "claude",
					Status: session.StatusRunning, ProjectPath: "/srv/alpha",
				}},
				{Type: MenuItemTypeSession, Session: &MenuSession{
					ID: "sess-002", Title: "beta", Tool: "gemini",
					Status: session.StatusRunning, ProjectPath: "/srv/beta",
				}},
			},
		},
	}
	srv.SetMCPManager(mgr)
	return srv
}

// ---- GET /api/mcps (catalog) ----

func TestMCPCatalog_HappyPath(t *testing.T) {
	mgr := newFakeMCPManager()
	mgr.catalog = []MCPCatalogEntry{
		{Name: "exa", Description: "search", Transport: "stdio"},
		{Name: "youtube", Description: "yt", Transport: "http"},
	}
	srv := newMCPTestServer(t, mgr, true)

	req := httptest.NewRequest(http.MethodGet, "/api/mcps", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp MCPCatalogResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.MCPs) != 2 || resp.MCPs[0].Name != "exa" {
		t.Fatalf("unexpected catalog: %+v", resp.MCPs)
	}
}

func TestMCPCatalog_EmptyBoundary(t *testing.T) {
	srv := newMCPTestServer(t, newFakeMCPManager(), true)
	req := httptest.NewRequest(http.MethodGet, "/api/mcps", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"mcps"`) {
		t.Fatalf("missing mcps key: %s", rr.Body.String())
	}
}

func TestMCPCatalog_NoManagerReturns503(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", WebMutations: true})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}

	req := httptest.NewRequest(http.MethodGet, "/api/mcps", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503", rr.Code)
	}
}

// ---- GET /api/sessions/{id}/mcps (attached) ----

func TestSessionMCPs_ListHappyPath(t *testing.T) {
	mgr := newFakeMCPManager()
	mgr.attached["/srv/alpha"] = map[string][]string{
		"local": {"exa"}, "global": {"youtube"}, "user": {},
	}
	srv := newMCPTestServer(t, mgr, true)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/sess-001/mcps", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp SessionMCPsResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !slicesEqual(resp.Local, []string{"exa"}) || !slicesEqual(resp.Global, []string{"youtube"}) {
		t.Fatalf("scopes: %+v", resp)
	}
}

func TestSessionMCPs_ListUnknownSession_404(t *testing.T) {
	srv := newMCPTestServer(t, newFakeMCPManager(), true)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/does-not-exist/mcps", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404", rr.Code)
	}
}

// ---- POST /api/sessions/{id}/mcps/{name} (attach) ----

func TestSessionMCPs_AttachHappyPath(t *testing.T) {
	mgr := newFakeMCPManager()
	srv := newMCPTestServer(t, mgr, true)

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/sess-001/mcps/exa", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(mgr.attachCalls) != 1 {
		t.Fatalf("attach calls=%d", len(mgr.attachCalls))
	}
	got := mgr.attachCalls[0]
	if got.ProjectPath != "/srv/alpha" || got.Name != "exa" || got.Scope != "local" {
		t.Fatalf("call=%+v", got)
	}
}

func TestSessionMCPs_AttachExplicitScope(t *testing.T) {
	mgr := newFakeMCPManager()
	srv := newMCPTestServer(t, mgr, true)

	body := strings.NewReader(`{"scope":"global"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/sess-001/mcps/exa", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if mgr.attachCalls[0].Scope != "global" {
		t.Fatalf("scope=%q", mgr.attachCalls[0].Scope)
	}
}

func TestSessionMCPs_AttachInvalidScope_400(t *testing.T) {
	srv := newMCPTestServer(t, newFakeMCPManager(), true)
	body := strings.NewReader(`{"scope":"bogus"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/sess-001/mcps/exa", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rr.Code)
	}
}

func TestSessionMCPs_AttachManagerError_500(t *testing.T) {
	mgr := newFakeMCPManager()
	mgr.attachErr = errors.New("disk full")
	srv := newMCPTestServer(t, mgr, true)

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/sess-001/mcps/exa", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500", rr.Code)
	}
}

func TestSessionMCPs_AttachMutationsDisabled_403(t *testing.T) {
	srv := newMCPTestServer(t, newFakeMCPManager(), false)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/sess-001/mcps/exa", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403", rr.Code)
	}
}

// ---- DELETE /api/sessions/{id}/mcps/{name} (detach) ----

func TestSessionMCPs_DetachHappyPath(t *testing.T) {
	mgr := newFakeMCPManager()
	mgr.attached["/srv/alpha"] = map[string][]string{"local": {"exa"}}
	srv := newMCPTestServer(t, mgr, true)

	req := httptest.NewRequest(http.MethodDelete, "/api/sessions/sess-001/mcps/exa", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(mgr.detachCalls) != 1 || mgr.detachCalls[0].Name != "exa" || mgr.detachCalls[0].Scope != "local" {
		t.Fatalf("call=%+v", mgr.detachCalls)
	}
}

func TestSessionMCPs_DetachUnknownSession_404(t *testing.T) {
	srv := newMCPTestServer(t, newFakeMCPManager(), true)
	req := httptest.NewRequest(http.MethodDelete, "/api/sessions/nope/mcps/exa", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404", rr.Code)
	}
}

// ---- PATCH /api/sessions/{id}/mcps/{name} (move/toggle pooled ↔ local) ----

func TestSessionMCPs_MoveHappyPath(t *testing.T) {
	mgr := newFakeMCPManager()
	mgr.attached["/srv/alpha"] = map[string][]string{"local": {"exa"}}
	srv := newMCPTestServer(t, mgr, true)

	body := strings.NewReader(`{"scope":"global"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/sessions/sess-001/mcps/exa", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(mgr.moveCalls) != 1 || mgr.moveCalls[0].FromScope != "local" || mgr.moveCalls[0].ToScope != "global" {
		t.Fatalf("calls=%+v", mgr.moveCalls)
	}
}

func TestSessionMCPs_MovePooledTrueToGlobal(t *testing.T) {
	mgr := newFakeMCPManager()
	mgr.attached["/srv/alpha"] = map[string][]string{"local": {"exa"}}
	srv := newMCPTestServer(t, mgr, true)

	body := strings.NewReader(`{"pooled":true}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/sessions/sess-001/mcps/exa", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(mgr.moveCalls) != 1 || mgr.moveCalls[0].ToScope != "global" {
		t.Fatalf("calls=%+v", mgr.moveCalls)
	}
}

func TestSessionMCPs_MoveNoTargetScope_400(t *testing.T) {
	srv := newMCPTestServer(t, newFakeMCPManager(), true)
	body := strings.NewReader(`{}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/sessions/sess-001/mcps/exa", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rr.Code)
	}
}

func TestSessionMCPs_MoveNotAttached_404(t *testing.T) {
	srv := newMCPTestServer(t, newFakeMCPManager(), true)
	body := strings.NewReader(`{"scope":"global"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/sessions/sess-001/mcps/exa", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404", rr.Code)
	}
}

// ---- Boundary: URL-encoded UTF-8 name ----

func TestSessionMCPs_AttachUTF8Name(t *testing.T) {
	mgr := newFakeMCPManager()
	srv := newMCPTestServer(t, mgr, true)

	encoded := "mcp-%E2%9C%93" // mcp-✓
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/sess-001/mcps/"+encoded, nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if mgr.attachCalls[0].Name != "mcp-✓" {
		t.Fatalf("name=%q want mcp-✓", mgr.attachCalls[0].Name)
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

var _ MCPManager = (*fakeMCPManager)(nil)
