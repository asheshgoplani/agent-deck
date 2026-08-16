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
	"slices"
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
	// targets records every MCPTarget the handler passed through, so tests
	// can assert the session's tool reaches the manager and not just its path.
	targets []MCPTarget
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

func (f *fakeMCPManager) ListAttached(target MCPTarget) (map[string][]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.targets = append(f.targets, target)
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make(map[string][]string, 3)
	for _, scope := range []string{"local", "global", "user"} {
		if names := f.attached[target.ProjectPath][scope]; names != nil {
			cp := append([]string(nil), names...)
			sort.Strings(cp)
			out[scope] = cp
		} else {
			out[scope] = []string{}
		}
	}
	return out, nil
}

func (f *fakeMCPManager) Attach(target MCPTarget, name, scope string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.targets = append(f.targets, target)
	if f.attachErr != nil {
		return f.attachErr
	}
	f.attachCalls = append(f.attachCalls, mcpAttachCall{target.ProjectPath, name, scope})
	if f.attached[target.ProjectPath] == nil {
		f.attached[target.ProjectPath] = map[string][]string{}
	}
	for _, existing := range f.attached[target.ProjectPath][scope] {
		if existing == name {
			return nil
		}
	}
	f.attached[target.ProjectPath][scope] = append(f.attached[target.ProjectPath][scope], name)
	return nil
}

func (f *fakeMCPManager) Detach(target MCPTarget, name, scope string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.targets = append(f.targets, target)
	if f.detachErr != nil {
		return f.detachErr
	}
	f.detachCalls = append(f.detachCalls, mcpAttachCall{target.ProjectPath, name, scope})
	names := f.attached[target.ProjectPath][scope]
	out := names[:0]
	for _, n := range names {
		if n != name {
			out = append(out, n)
		}
	}
	if f.attached[target.ProjectPath] == nil {
		f.attached[target.ProjectPath] = map[string][]string{}
	}
	f.attached[target.ProjectPath][scope] = out
	return nil
}

func (f *fakeMCPManager) Move(target MCPTarget, name, fromScope, toScope string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.moveErr != nil {
		return f.moveErr
	}
	f.moveCalls = append(f.moveCalls, mcpMoveCall{target.ProjectPath, name, fromScope, toScope})
	if f.attached[target.ProjectPath] == nil {
		f.attached[target.ProjectPath] = map[string][]string{}
	}
	from := f.attached[target.ProjectPath][fromScope]
	out := from[:0]
	for _, n := range from {
		if n != name {
			out = append(out, n)
		}
	}
	f.attached[target.ProjectPath][fromScope] = out
	for _, existing := range f.attached[target.ProjectPath][toScope] {
		if existing == name {
			return nil
		}
	}
	f.attached[target.ProjectPath][toScope] = append(f.attached[target.ProjectPath][toScope], name)
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
				// A tool with no MCP support at all, so the handler's gate has
				// something to refuse (see TestMCPRoutesRefuseUnsupportedTool).
				{Type: MenuItemTypeSession, Session: &MenuSession{
					ID: "sess-shell", Title: "scratch", Tool: "shell",
					Status: session.StatusRunning, ProjectPath: "/srv/scratch",
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

// TestMCPRoutesRefuseUnsupportedTool pins the honest gate. The MCP endpoints
// are exposed for every session, but a shell session has no MCP store; before
// the tool became part of the target, selecting one wrote Claude's config on
// its behalf. It must now be refused with a reason that names the tool.
func TestMCPRoutesRefuseUnsupportedTool(t *testing.T) {
	mgr := newFakeMCPManager()
	srv := newMCPTestServer(t, mgr, true)

	for _, tc := range []struct {
		name   string
		method string
		path   string
	}{
		{"list", http.MethodGet, "/api/sessions/sess-shell/mcps"},
		{"attach", http.MethodPost, "/api/sessions/sess-shell/mcps/exa"},
		{"detach", http.MethodDelete, "/api/sessions/sess-shell/mcps/exa"},
		{"move", http.MethodPatch, "/api/sessions/sess-shell/mcps/exa"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			rr := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rr, req)

			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s, want 400", rr.Code, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), "shell") {
				t.Errorf("refusal should name the unsupported tool, got %s", rr.Body.String())
			}
		})
	}

	if len(mgr.attachCalls) != 0 || len(mgr.detachCalls) != 0 || len(mgr.moveCalls) != 0 {
		t.Errorf("an unsupported tool reached the manager: attach=%v detach=%v move=%v",
			mgr.attachCalls, mgr.detachCalls, mgr.moveCalls)
	}
}

// TestMCPTargetCarriesTheSessionTool proves the tool reaches the manager, so a
// per-tool implementation can route to the right store.
func TestMCPTargetCarriesTheSessionTool(t *testing.T) {
	mgr := newFakeMCPManager()
	srv := newMCPTestServer(t, mgr, true)

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/sess-002/mcps/exa", nil)
	req.Header.Set("Origin", "http://"+req.Host)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(mgr.targets) == 0 {
		t.Fatal("manager saw no target")
	}
	last := mgr.targets[len(mgr.targets)-1]
	if last.Tool != "gemini" || last.ProjectPath != "/srv/beta" {
		t.Errorf("manager got target %+v, want tool=gemini path=/srv/beta", last)
	}
}

// TestAttachDefaultsToTheToolsOwnScope is the regression test for the pane
// hardcoding "local".
//
// Codex and Gemini are global-only, so a bodyless attach that defaulted to
// "local" was refused every time — the primary MCP workflow was unusable for
// both tools. The default now comes from the tool's capability, and the
// response reports the scope actually used so the client can show it.
func TestAttachDefaultsToTheToolsOwnScope(t *testing.T) {
	for _, tc := range []struct {
		sessionID string
		tool      string
		wantScope string
	}{
		{"sess-001", "claude", "local"},
		{"sess-002", "gemini", "global"},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			mgr := newFakeMCPManager()
			srv := newMCPTestServer(t, mgr, true)

			// No body at all: the server must pick the scope, not the caller.
			req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+tc.sessionID+"/mcps/exa", nil)
			req.Header.Set("Origin", "http://"+req.Host)
			rr := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			var resp map[string]string
			if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if resp["scope"] != tc.wantScope {
				t.Errorf("attach for %q used scope %q, want %q", tc.tool, resp["scope"], tc.wantScope)
			}
			if len(mgr.attachCalls) != 1 || mgr.attachCalls[0].Scope != tc.wantScope {
				t.Errorf("manager saw %+v, want a single attach in %q", mgr.attachCalls, tc.wantScope)
			}
		})
	}
}

// TestSessionMCPsResponseReportsScopesAndProject pins the contract the pane
// depends on: the server states which scopes exist, so the client never
// hardcodes one, and Claude's project map is its own field.
func TestSessionMCPsResponseReportsScopesAndProject(t *testing.T) {
	mgr := newFakeMCPManager()
	srv := newMCPTestServer(t, mgr, true)

	for _, tc := range []struct {
		sessionID  string
		tool       string
		wantScopes []string
	}{
		{"sess-001", "claude", []string{"local", "project", "global", "user"}},
		{"sess-002", "gemini", []string{"global"}},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+tc.sessionID+"/mcps", nil)
			rr := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			var resp SessionMCPsResponse
			if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if !slices.Equal(resp.Scopes, tc.wantScopes) {
				t.Errorf("scopes = %v, want %v", resp.Scopes, tc.wantScopes)
			}
			if resp.Project == nil {
				t.Error("project must be present (empty array, not null) so clients can render it")
			}
		})
	}
}
