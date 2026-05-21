package web

// Tests for the Web UI Skills management endpoints.
//
// Closes the MISSING rows in tests/web/PARITY_MATRIX.md "SKILLS MANAGEMENT"
// section. Mirrors the TUI `s`-key dialog (internal/ui/home.go:6597,
// internal/ui/skill_dialog.go) so Web parity exists for:
//
//   GET    /api/skills                          -> catalog
//   GET    /api/sessions/{id}/skills            -> attached for session's project
//   POST   /api/sessions/{id}/skills/{name}     -> attach
//   DELETE /api/sessions/{id}/skills/{name}     -> detach
//
// Per ~/.agent-deck/skills/pool/agent-deck-tdd-feature/SKILL.md we cover
// per endpoint: 1 happy path, 1 failure mode, 1 boundary case.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// fakeSkillsService implements SkillsService for handler-level tests so we
// don't have to touch the user's real ~/.agent-deck/skills/ during the suite.
type fakeSkillsService struct {
	catalog        []session.SkillCandidate
	catalogErr     error
	attachedByPath map[string][]session.ProjectSkillAttachment
	attachedErr    error
	attachFn       func(projectPath, tool, skillRef, source string) (*session.ProjectSkillAttachment, error)
	detachFn       func(projectPath, skillRef, source string) (*session.ProjectSkillAttachment, error)
}

func (f *fakeSkillsService) ListCatalog() ([]session.SkillCandidate, error) {
	if f.catalogErr != nil {
		return nil, f.catalogErr
	}
	return f.catalog, nil
}

func (f *fakeSkillsService) ListAttached(projectPath string) ([]session.ProjectSkillAttachment, error) {
	if f.attachedErr != nil {
		return nil, f.attachedErr
	}
	if f.attachedByPath == nil {
		return nil, nil
	}
	return f.attachedByPath[projectPath], nil
}

func (f *fakeSkillsService) Attach(projectPath, tool, skillRef, source string) (*session.ProjectSkillAttachment, error) {
	if f.attachFn == nil {
		return nil, fmt.Errorf("attach not configured")
	}
	return f.attachFn(projectPath, tool, skillRef, source)
}

func (f *fakeSkillsService) Detach(projectPath, skillRef, source string) (*session.ProjectSkillAttachment, error) {
	if f.detachFn == nil {
		return nil, fmt.Errorf("detach not configured")
	}
	return f.detachFn(projectPath, skillRef, source)
}

// menuWithSession returns a snapshot containing a single session with the
// given id/projectPath/tool — enough for the handler to resolve the project.
func menuWithSession(id, projectPath, tool string) *MenuSnapshot {
	return &MenuSnapshot{
		Items: []MenuItem{
			{
				Type: MenuItemTypeSession,
				Session: &MenuSession{
					ID:          id,
					Title:       "skill-test",
					Tool:        tool,
					ProjectPath: projectPath,
					Status:      session.StatusRunning,
				},
			},
		},
	}
}

// --- /api/skills (catalog) ---------------------------------------------------

// Happy path: catalog returns the discovered skills as JSON.
func TestSkillsCatalogGET_Happy(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0"})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	srv.skills = &fakeSkillsService{
		catalog: []session.SkillCandidate{
			{ID: "pool/alpha", Name: "alpha", Source: "pool", EntryName: "alpha", Kind: "dir"},
			{ID: "pool/beta", Name: "beta", Source: "pool", EntryName: "beta", Kind: "dir"},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/skills", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Skills []session.SkillCandidate `json:"skills"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, rr.Body.String())
	}
	if len(resp.Skills) != 2 || resp.Skills[0].Name != "alpha" {
		t.Fatalf("skills = %#v, want alpha+beta", resp.Skills)
	}
}

// Failure mode: catalog source errors should surface as 500 with an
// INTERNAL_ERROR code (parity with other catalog handlers).
func TestSkillsCatalogGET_FailurePropagates(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0"})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	srv.skills = &fakeSkillsService{catalogErr: errors.New("disk read failed")}

	req := httptest.NewRequest(http.MethodGet, "/api/skills", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), ErrCodeInternalError) {
		t.Fatalf("expected INTERNAL_ERROR, got %s", rr.Body.String())
	}
}

// Boundary: method other than GET is rejected.
func TestSkillsCatalog_MethodNotAllowed(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0"})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	srv.skills = &fakeSkillsService{}

	req := httptest.NewRequest(http.MethodPut, "/api/skills", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
}

// Boundary: empty catalog returns an empty array, not null. JSON parity
// matters because the frontend checks `.skills.length`.
func TestSkillsCatalogGET_EmptyReturnsArray(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0"})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	srv.skills = &fakeSkillsService{catalog: nil}

	req := httptest.NewRequest(http.MethodGet, "/api/skills", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"skills":[]`) {
		t.Fatalf("expected empty array, got %s", rr.Body.String())
	}
}

// --- GET /api/sessions/{id}/skills (attached) --------------------------------

func TestSessionSkillsGET_Happy(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0"})
	srv.menuData = &fakeMenuDataLoader{snapshot: menuWithSession("sess-1", "/tmp/proj", "claude")}
	srv.skills = &fakeSkillsService{
		attachedByPath: map[string][]session.ProjectSkillAttachment{
			"/tmp/proj": {
				{ID: "pool/alpha", Name: "alpha", Source: "pool", EntryName: "alpha", TargetPath: ".claude/skills/alpha"},
			},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/sess-1/skills", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"alpha"`) {
		t.Fatalf("expected attached skill in response, got %s", rr.Body.String())
	}
}

// Failure: session not found -> 404 NOT_FOUND.
func TestSessionSkillsGET_SessionNotFound(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0"})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{}}
	srv.skills = &fakeSkillsService{}

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/missing/skills", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body=%s", rr.Code, rr.Body.String())
	}
}

// Boundary: session with no attached skills returns an empty array, not null.
func TestSessionSkillsGET_EmptyReturnsArray(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0"})
	srv.menuData = &fakeMenuDataLoader{snapshot: menuWithSession("sess-2", "/tmp/empty", "claude")}
	srv.skills = &fakeSkillsService{}

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/sess-2/skills", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"skills":[]`) {
		t.Fatalf("expected empty array, got %s", rr.Body.String())
	}
}

// --- POST /api/sessions/{id}/skills/{name} (attach) --------------------------

func TestSessionSkillsAttach_Happy(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", WebMutations: true})
	srv.menuData = &fakeMenuDataLoader{snapshot: menuWithSession("sess-1", "/tmp/proj", "claude")}

	var gotProj, gotTool, gotRef string
	srv.skills = &fakeSkillsService{
		attachFn: func(projectPath, tool, skillRef, source string) (*session.ProjectSkillAttachment, error) {
			gotProj, gotTool, gotRef = projectPath, tool, skillRef
			return &session.ProjectSkillAttachment{
				ID: "pool/alpha", Name: "alpha", Source: "pool", EntryName: "alpha",
				TargetPath: ".claude/skills/alpha",
			}, nil
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/sess-1/skills/alpha", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}
	if gotProj != "/tmp/proj" || gotTool != "claude" || gotRef != "alpha" {
		t.Fatalf("attach called with (%q,%q,%q), want (/tmp/proj, claude, alpha)", gotProj, gotTool, gotRef)
	}
	if !strings.Contains(rr.Body.String(), `"alpha"`) {
		t.Fatalf("expected attached skill in response, got %s", rr.Body.String())
	}
}

// Failure: mutations disabled -> 403.
func TestSessionSkillsAttach_MutationsDisabled(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", WebMutations: false})
	srv.menuData = &fakeMenuDataLoader{snapshot: menuWithSession("sess-1", "/tmp/proj", "claude")}
	srv.skills = &fakeSkillsService{}

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/sess-1/skills/alpha", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body=%s", rr.Code, rr.Body.String())
	}
}

// Failure: tool doesn't support project skills (e.g. plain `bash`).
func TestSessionSkillsAttach_UnsupportedTool(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", WebMutations: true})
	srv.menuData = &fakeMenuDataLoader{snapshot: menuWithSession("sess-1", "/tmp/proj", "bash")}
	srv.skills = &fakeSkillsService{}

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/sess-1/skills/alpha", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", rr.Code, rr.Body.String())
	}
}

// Boundary: attaching with a source qualifier preserves both segments.
func TestSessionSkillsAttach_SourceQualifiedName(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", WebMutations: true})
	srv.menuData = &fakeMenuDataLoader{snapshot: menuWithSession("sess-1", "/tmp/proj", "claude")}

	var gotRef, gotSource string
	srv.skills = &fakeSkillsService{
		attachFn: func(projectPath, tool, skillRef, source string) (*session.ProjectSkillAttachment, error) {
			gotRef, gotSource = skillRef, source
			return &session.ProjectSkillAttachment{ID: "pool/alpha", Name: "alpha", Source: "pool"}, nil
		},
	}

	// Source is passed via ?source= query (cannot be in path because of slashes).
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/sess-1/skills/alpha?source=pool", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}
	if gotRef != "alpha" || gotSource != "pool" {
		t.Fatalf("attach called with ref=%q source=%q, want alpha/pool", gotRef, gotSource)
	}
}

// --- DELETE /api/sessions/{id}/skills/{name} (detach) ------------------------

func TestSessionSkillsDetach_Happy(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", WebMutations: true})
	srv.menuData = &fakeMenuDataLoader{snapshot: menuWithSession("sess-1", "/tmp/proj", "claude")}

	var gotProj, gotRef string
	srv.skills = &fakeSkillsService{
		detachFn: func(projectPath, skillRef, source string) (*session.ProjectSkillAttachment, error) {
			gotProj, gotRef = projectPath, skillRef
			return &session.ProjectSkillAttachment{ID: "pool/alpha", Name: "alpha"}, nil
		},
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/sessions/sess-1/skills/alpha", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}
	if gotProj != "/tmp/proj" || gotRef != "alpha" {
		t.Fatalf("detach called with (%q,%q), want (/tmp/proj, alpha)", gotProj, gotRef)
	}
}

// Failure: skill not attached -> 404 NOT_FOUND.
func TestSessionSkillsDetach_NotAttached(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", WebMutations: true})
	srv.menuData = &fakeMenuDataLoader{snapshot: menuWithSession("sess-1", "/tmp/proj", "claude")}
	srv.skills = &fakeSkillsService{
		detachFn: func(projectPath, skillRef, source string) (*session.ProjectSkillAttachment, error) {
			return nil, session.ErrSkillNotAttached
		},
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/sessions/sess-1/skills/missing", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body=%s", rr.Code, rr.Body.String())
	}
}

// Boundary: empty skill name should return 400.
func TestSessionSkillsDetach_EmptyName(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", WebMutations: true})
	srv.menuData = &fakeMenuDataLoader{snapshot: menuWithSession("sess-1", "/tmp/proj", "claude")}
	srv.skills = &fakeSkillsService{}

	// Trailing slash but no name.
	req := httptest.NewRequest(http.MethodDelete, "/api/sessions/sess-1/skills/", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", rr.Code, rr.Body.String())
	}
}
