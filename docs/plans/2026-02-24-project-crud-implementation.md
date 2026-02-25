# Project CRUD Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add project CRUD (create, read, update, delete) to the hub dashboard so users can manage projects via the UI, unblocking task creation.

**Architecture:** Replace the read-only YAML `ProjectRegistry` with a JSON filesystem `ProjectStore` (matching the existing `TaskStore` pattern). Add REST endpoints for project CRUD. Update the dashboard UI with an "Add Project" modal and a "Manage Projects" view.

**Tech Stack:** Go 1.24, vanilla JavaScript, HTML/CSS, JSON filesystem storage.

---

### Task 1: Create ProjectStore with Tests

**Files:**
- Create: `internal/hub/project_store.go`
- Create: `internal/hub/project_store_test.go`
- Modify: `internal/hub/models.go:43-49` (add CreatedAt/UpdatedAt to Project)

**Step 1: Add timestamps to Project model**

In `internal/hub/models.go`, add `CreatedAt` and `UpdatedAt` fields to the `Project` struct. Add a `Repo` field to store the original GitHub repo identifier.

```go
// Project defines a workspace that tasks can be routed to.
type Project struct {
	Name        string   `json:"name"        yaml:"name"`
	Repo        string   `json:"repo,omitempty" yaml:"repo,omitempty"`
	Path        string   `json:"path"        yaml:"path"`
	Keywords    []string `json:"keywords"    yaml:"keywords"`
	Container   string   `json:"container,omitempty"   yaml:"container,omitempty"`
	DefaultMCPs []string `json:"defaultMcps,omitempty" yaml:"default_mcps,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}
```

**Step 2: Write ProjectStore tests**

Create `internal/hub/project_store_test.go`. These tests mirror the patterns in `store_test.go`:

```go
package hub

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestProjectStore(t *testing.T) *ProjectStore {
	t.Helper()
	store, err := NewProjectStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewProjectStore: %v", err)
	}
	return store
}

func TestNewProjectStoreCreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	basePath := filepath.Join(dir, "hub")

	store, err := NewProjectStore(basePath)
	if err != nil {
		t.Fatalf("NewProjectStore: %v", err)
	}

	info, err := os.Stat(filepath.Join(basePath, "projects"))
	if err != nil {
		t.Fatalf("projects directory not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("projects path is not a directory")
	}
	_ = store
}

func TestProjectStoreSaveAndGet(t *testing.T) {
	store := newTestProjectStore(t)

	project := &Project{
		Name:     "agent-deck",
		Repo:     "C0ntr0lledCha0s/agent-deck",
		Path:     "/home/user/projects/agent-deck",
		Keywords: []string{"cli", "agents"},
	}

	if err := store.Save(project); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if project.CreatedAt.IsZero() {
		t.Fatal("expected CreatedAt to be set")
	}
	if project.UpdatedAt.IsZero() {
		t.Fatal("expected UpdatedAt to be set")
	}

	got, err := store.Get("agent-deck")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "agent-deck" {
		t.Fatalf("expected name agent-deck, got %s", got.Name)
	}
	if got.Repo != "C0ntr0lledCha0s/agent-deck" {
		t.Fatalf("expected repo C0ntr0lledCha0s/agent-deck, got %s", got.Repo)
	}
	if got.Path != "/home/user/projects/agent-deck" {
		t.Fatalf("expected path /home/user/projects/agent-deck, got %s", got.Path)
	}
}

func TestProjectStoreSaveRequiresName(t *testing.T) {
	store := newTestProjectStore(t)

	project := &Project{Path: "/some/path"}
	if err := store.Save(project); err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestProjectStoreSaveRejectsInvalidName(t *testing.T) {
	store := newTestProjectStore(t)

	for _, name := range []string{".", "..", "foo/bar", "foo\\bar"} {
		project := &Project{Name: name, Path: "/some/path"}
		if err := store.Save(project); err == nil {
			t.Fatalf("expected error for invalid name %q", name)
		}
	}
}

func TestProjectStoreUpdate(t *testing.T) {
	store := newTestProjectStore(t)

	project := &Project{
		Name:     "my-project",
		Path:     "/original/path",
		Keywords: []string{"api"},
	}
	if err := store.Save(project); err != nil {
		t.Fatalf("Save: %v", err)
	}

	firstUpdated := project.UpdatedAt
	time.Sleep(time.Millisecond)

	project.Path = "/updated/path"
	project.Keywords = []string{"api", "backend"}
	if err := store.Save(project); err != nil {
		t.Fatalf("Save update: %v", err)
	}

	got, err := store.Get("my-project")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Path != "/updated/path" {
		t.Fatalf("expected path /updated/path, got %s", got.Path)
	}
	if len(got.Keywords) != 2 {
		t.Fatalf("expected 2 keywords, got %d", len(got.Keywords))
	}
	if !got.UpdatedAt.After(firstUpdated) {
		t.Fatal("expected UpdatedAt to advance on update")
	}
}

func TestProjectStoreList(t *testing.T) {
	store := newTestProjectStore(t)

	for _, name := range []string{"alpha", "beta", "gamma"} {
		project := &Project{Name: name, Path: "/path/" + name}
		if err := store.Save(project); err != nil {
			t.Fatalf("Save %s: %v", name, err)
		}
		time.Sleep(time.Millisecond)
	}

	projects, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(projects) != 3 {
		t.Fatalf("expected 3 projects, got %d", len(projects))
	}
	// Sorted by creation time (oldest first)
	if projects[0].Name != "alpha" {
		t.Fatalf("expected first project 'alpha', got %s", projects[0].Name)
	}
	if projects[2].Name != "gamma" {
		t.Fatalf("expected last project 'gamma', got %s", projects[2].Name)
	}
}

func TestProjectStoreListEmpty(t *testing.T) {
	store := newTestProjectStore(t)

	projects, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(projects) != 0 {
		t.Fatalf("expected 0 projects, got %d", len(projects))
	}
}

func TestProjectStoreDelete(t *testing.T) {
	store := newTestProjectStore(t)

	project := &Project{Name: "to-delete", Path: "/path/to-delete"}
	if err := store.Save(project); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := store.Delete("to-delete"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err := store.Get("to-delete")
	if err == nil {
		t.Fatal("expected error after delete, got nil")
	}
}

func TestProjectStoreDeleteNotFound(t *testing.T) {
	store := newTestProjectStore(t)

	err := store.Delete("nonexistent")
	if err == nil {
		t.Fatal("expected error for non-existent project, got nil")
	}
}

func TestProjectStoreGetNotFound(t *testing.T) {
	store := newTestProjectStore(t)

	_, err := store.Get("nonexistent")
	if err == nil {
		t.Fatal("expected error for non-existent project, got nil")
	}
}
```

**Step 3: Run tests to verify they fail**

Run: `go test ./internal/hub/ -run TestProjectStore -v`
Expected: FAIL — `ProjectStore` type doesn't exist yet.

**Step 4: Write ProjectStore implementation**

Create `internal/hub/project_store.go`:

```go
package hub

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// validProjectName returns true if name is safe to use as a filename component.
func validProjectName(name string) bool {
	return name != "" && name != "." && name != ".." &&
		!strings.Contains(name, "/") && !strings.Contains(name, "\\")
}

// ProjectStore provides filesystem JSON-based CRUD for Project records.
// Each project is stored as an individual JSON file (e.g. agent-deck.json)
// under basePath/projects/.
type ProjectStore struct {
	mu         sync.RWMutex
	projectDir string
}

// NewProjectStore creates a ProjectStore backed by the given base directory.
// It creates the projects/ subdirectory if it does not exist.
func NewProjectStore(basePath string) (*ProjectStore, error) {
	projectDir := filepath.Join(basePath, "projects")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		return nil, fmt.Errorf("create project directory: %w", err)
	}
	return &ProjectStore{projectDir: projectDir}, nil
}

// List returns all projects sorted by creation time (oldest first).
func (s *ProjectStore) List() ([]*Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries, err := os.ReadDir(s.projectDir)
	if err != nil {
		return nil, fmt.Errorf("read project directory: %w", err)
	}

	var projects []*Project
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		project, err := s.readProjectFile(entry.Name())
		if err != nil {
			continue // skip corrupt files
		}
		projects = append(projects, project)
	}

	sort.Slice(projects, func(i, j int) bool {
		return projects[i].CreatedAt.Before(projects[j].CreatedAt)
	})

	return projects, nil
}

// Get retrieves a single project by name.
func (s *ProjectStore) Get(name string) (*Project, error) {
	if !validProjectName(name) {
		return nil, fmt.Errorf("invalid project name: %q", name)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.readProjectFile(name + ".json")
}

// Save persists a project. Name is required and used as the file key.
// UpdatedAt is always set to now. CreatedAt is set on first save.
func (s *ProjectStore) Save(project *Project) error {
	if !validProjectName(project.Name) {
		return fmt.Errorf("invalid project name: %q", project.Name)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if project.CreatedAt.IsZero() {
		project.CreatedAt = time.Now().UTC()
	}
	project.UpdatedAt = time.Now().UTC()

	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal project: %w", err)
	}

	path := filepath.Join(s.projectDir, project.Name+".json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write project file: %w", err)
	}

	return nil
}

// Delete removes a project by name.
func (s *ProjectStore) Delete(name string) error {
	if !validProjectName(name) {
		return fmt.Errorf("invalid project name: %q", name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(s.projectDir, name+".json")
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("project not found: %s", name)
		}
		return fmt.Errorf("delete project file: %w", err)
	}
	return nil
}

func (s *ProjectStore) readProjectFile(filename string) (*Project, error) {
	data, err := os.ReadFile(filepath.Join(s.projectDir, filename))
	if err != nil {
		return nil, fmt.Errorf("read project file %s: %w", filename, err)
	}
	var project Project
	if err := json.Unmarshal(data, &project); err != nil {
		return nil, fmt.Errorf("unmarshal project %s: %w", filename, err)
	}
	return &project, nil
}
```

**Step 5: Run tests to verify they pass**

Run: `go test ./internal/hub/ -run TestProjectStore -v`
Expected: All PASS.

**Step 6: Run all hub tests to make sure nothing broke**

Run: `go test ./internal/hub/ -v`
Expected: All PASS (including existing TaskStore and Router tests).

**Step 7: Commit**

```bash
git add internal/hub/project_store.go internal/hub/project_store_test.go internal/hub/models.go
git commit -m "feat(hub): add ProjectStore with JSON filesystem CRUD

Mirrors the existing TaskStore pattern. Stores projects as individual
JSON files under ~/.agent-deck/hub/projects/. Adds CreatedAt/UpdatedAt
and Repo fields to Project model."
```

---

### Task 2: Wire ProjectStore into Server and Update Handlers

**Files:**
- Modify: `internal/web/server.go:51` (change type from `*hub.ProjectRegistry` to `*hub.ProjectStore`)
- Modify: `internal/web/server.go:88-89` (change initialization)
- Modify: `internal/web/handlers_hub.go:515-541` (expand `handleProjects` to dispatch GET/POST)
- Modify: `internal/web/handlers_hub.go` (add project CRUD handlers and request/response types)

**Step 1: Write failing tests for project CRUD endpoints**

Add these tests to `internal/web/handlers_hub_test.go`. First, update `newTestServerWithHub` to use `ProjectStore`:

```go
func newTestServerWithHub(t *testing.T) *Server {
	t.Helper()
	hubDir := t.TempDir()

	taskStore, err := hub.NewTaskStore(hubDir)
	if err != nil {
		t.Fatalf("NewTaskStore: %v", err)
	}

	projectStore, err := hub.NewProjectStore(hubDir)
	if err != nil {
		t.Fatalf("NewProjectStore: %v", err)
	}

	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
		Profile:    "test-profile",
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{Profile: "test-profile"}}
	srv.hubTasks = taskStore
	srv.hubProjects = projectStore
	return srv
}
```

Then add project CRUD tests:

```go
func TestCreateProject(t *testing.T) {
	srv := newTestServerWithHub(t)

	body := `{"repo":"C0ntr0lledCha0s/agent-deck"}`
	req := httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected %d, got %d: %s", http.StatusCreated, rr.Code, rr.Body.String())
	}

	var resp projectDetailResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Project.Name != "agent-deck" {
		t.Fatalf("expected name agent-deck, got %s", resp.Project.Name)
	}
	if resp.Project.Repo != "C0ntr0lledCha0s/agent-deck" {
		t.Fatalf("expected repo C0ntr0lledCha0s/agent-deck, got %s", resp.Project.Repo)
	}
}

func TestCreateProjectWithOverrides(t *testing.T) {
	srv := newTestServerWithHub(t)

	body := `{"repo":"C0ntr0lledCha0s/agent-deck","name":"my-deck","path":"/custom/path","keywords":["cli","agents"]}`
	req := httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected %d, got %d: %s", http.StatusCreated, rr.Code, rr.Body.String())
	}

	var resp projectDetailResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Project.Name != "my-deck" {
		t.Fatalf("expected name my-deck, got %s", resp.Project.Name)
	}
	if resp.Project.Path != "/custom/path" {
		t.Fatalf("expected path /custom/path, got %s", resp.Project.Path)
	}
	if len(resp.Project.Keywords) != 2 {
		t.Fatalf("expected 2 keywords, got %d", len(resp.Project.Keywords))
	}
}

func TestCreateProjectMissingRepoAndName(t *testing.T) {
	srv := newTestServerWithHub(t)

	body := `{"path":"/some/path"}`
	req := httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d: %s", http.StatusBadRequest, rr.Code, rr.Body.String())
	}
}

func TestCreateProjectDuplicate(t *testing.T) {
	srv := newTestServerWithHub(t)

	body := `{"repo":"org/my-project"}`
	req := httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected %d on first create, got %d", http.StatusCreated, rr.Code)
	}

	// Try again — should conflict.
	req2 := httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	rr2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr2, req2)

	if rr2.Code != http.StatusConflict {
		t.Fatalf("expected %d on duplicate, got %d: %s", http.StatusConflict, rr2.Code, rr2.Body.String())
	}
}

func TestCreateProjectNameOnly(t *testing.T) {
	srv := newTestServerWithHub(t)

	body := `{"name":"standalone-project"}`
	req := httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected %d, got %d: %s", http.StatusCreated, rr.Code, rr.Body.String())
	}

	var resp projectDetailResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Project.Name != "standalone-project" {
		t.Fatalf("expected name standalone-project, got %s", resp.Project.Name)
	}
}

func TestGetProject(t *testing.T) {
	srv := newTestServerWithHub(t)

	project := &hub.Project{Name: "test-proj", Path: "/test/path", Keywords: []string{"test"}}
	if err := srv.hubProjects.Save(project); err != nil {
		t.Fatalf("Save: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/projects/test-proj", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"name":"test-proj"`) {
		t.Fatalf("expected project name in response, got: %s", rr.Body.String())
	}
}

func TestGetProjectNotFound(t *testing.T) {
	srv := newTestServerWithHub(t)

	req := httptest.NewRequest(http.MethodGet, "/api/projects/nonexistent", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected %d, got %d", http.StatusNotFound, rr.Code)
	}
}

func TestUpdateProject(t *testing.T) {
	srv := newTestServerWithHub(t)

	project := &hub.Project{Name: "test-proj", Path: "/original", Keywords: []string{"old"}}
	if err := srv.hubProjects.Save(project); err != nil {
		t.Fatalf("Save: %v", err)
	}

	body := `{"path":"/updated","keywords":["new","updated"]}`
	req := httptest.NewRequest(http.MethodPatch, "/api/projects/test-proj", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}

	var resp projectDetailResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Project.Path != "/updated" {
		t.Fatalf("expected path /updated, got %s", resp.Project.Path)
	}
	if len(resp.Project.Keywords) != 2 {
		t.Fatalf("expected 2 keywords, got %d", len(resp.Project.Keywords))
	}
}

func TestUpdateProjectNotFound(t *testing.T) {
	srv := newTestServerWithHub(t)

	body := `{"path":"/new"}`
	req := httptest.NewRequest(http.MethodPatch, "/api/projects/nonexistent", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected %d, got %d", http.StatusNotFound, rr.Code)
	}
}

func TestDeleteProject(t *testing.T) {
	srv := newTestServerWithHub(t)

	project := &hub.Project{Name: "to-delete", Path: "/path"}
	if err := srv.hubProjects.Save(project); err != nil {
		t.Fatalf("Save: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/projects/to-delete", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected %d, got %d: %s", http.StatusNoContent, rr.Code, rr.Body.String())
	}

	// Verify it's gone.
	getReq := httptest.NewRequest(http.MethodGet, "/api/projects/to-delete", nil)
	getRR := httptest.NewRecorder()
	srv.Handler().ServeHTTP(getRR, getReq)
	if getRR.Code != http.StatusNotFound {
		t.Fatalf("expected deleted project to return 404, got %d", getRR.Code)
	}
}

func TestDeleteProjectNotFound(t *testing.T) {
	srv := newTestServerWithHub(t)

	req := httptest.NewRequest(http.MethodDelete, "/api/projects/nonexistent", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected %d, got %d", http.StatusNotFound, rr.Code)
	}
}

func TestProjectsEndpointUnauthorized(t *testing.T) {
	srv := newTestServerWithHub(t)
	srv.cfg.Token = "secret"

	req := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected %d, got %d", http.StatusUnauthorized, rr.Code)
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./internal/web/ -run "TestCreateProject|TestGetProject|TestUpdateProject|TestDeleteProject|TestProjectsEndpointUnauthorized" -v`
Expected: FAIL — new types and handlers don't exist yet.

**Step 3: Update server.go — change hubProjects type**

In `internal/web/server.go`, change the `hubProjects` field from `*hub.ProjectRegistry` to `*hub.ProjectStore`, and update initialization:

Change line 52:
```go
hubProjects     *hub.ProjectStore
```

Change lines 88-89 (initialization):
```go
if ps, err := hub.NewProjectStore(hubDir); err != nil {
    webLog.Warn("hub_project_store_disabled", slog.String("error", err.Error()))
} else {
    s.hubProjects = ps
}
```

Add the route for `/api/projects/` (with trailing slash for individual project routes):
```go
mux.HandleFunc("/api/projects/", s.handleProjectByName)
```

**Step 4: Update handleProjects to dispatch GET/POST**

In `internal/web/handlers_hub.go`, replace the existing `handleProjects` function:

```go
// handleProjects dispatches GET /api/projects and POST /api/projects.
func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.handleProjectsList(w, r)
	case http.MethodPost:
		s.handleProjectsCreate(w, r)
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
	}
}

// handleProjectsList serves GET /api/projects.
func (s *Server) handleProjectsList(w http.ResponseWriter, r *http.Request) {
	if s.hubProjects == nil {
		writeJSON(w, http.StatusOK, projectsListResponse{Projects: []*hub.Project{}})
		return
	}

	projects, err := s.hubProjects.List()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to load projects")
		return
	}

	if projects == nil {
		projects = []*hub.Project{}
	}

	writeJSON(w, http.StatusOK, projectsListResponse{Projects: projects})
}
```

**Step 5: Add project create handler**

```go
// handleProjectsCreate serves POST /api/projects.
func (s *Server) handleProjectsCreate(w http.ResponseWriter, r *http.Request) {
	if s.hubProjects == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "hub not initialized")
		return
	}

	var req createProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body")
		return
	}

	// Derive name from repo if not provided.
	name := req.Name
	if name == "" && req.Repo != "" {
		parts := strings.Split(req.Repo, "/")
		name = parts[len(parts)-1]
	}
	if name == "" {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "repo or name is required")
		return
	}

	// Check for duplicates.
	if existing, _ := s.hubProjects.Get(name); existing != nil {
		writeAPIError(w, http.StatusConflict, "CONFLICT", "project already exists: "+name)
		return
	}

	// Derive path if not provided.
	path := req.Path
	if path == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			homeDir = "/tmp"
		}
		path = filepath.Join(homeDir, "projects", name)
	}

	project := &hub.Project{
		Name:        name,
		Repo:        req.Repo,
		Path:        path,
		Keywords:    req.Keywords,
		Container:   req.Container,
		DefaultMCPs: req.DefaultMCPs,
	}

	if err := s.hubProjects.Save(project); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to create project")
		return
	}

	writeJSON(w, http.StatusCreated, projectDetailResponse{Project: project})
}
```

**Step 6: Add handleProjectByName dispatcher**

```go
// handleProjectByName dispatches /api/projects/{name} for GET, PATCH, DELETE.
func (s *Server) handleProjectByName(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}

	const prefix = "/api/projects/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "route not found")
		return
	}

	name := strings.TrimPrefix(r.URL.Path, prefix)
	if name == "" {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "project name is required")
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.handleProjectGet(w, name)
	case http.MethodPatch:
		s.handleProjectUpdate(w, r, name)
	case http.MethodDelete:
		s.handleProjectDelete(w, name)
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
	}
}

// handleProjectGet serves GET /api/projects/{name}.
func (s *Server) handleProjectGet(w http.ResponseWriter, name string) {
	if s.hubProjects == nil {
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "project not found")
		return
	}

	project, err := s.hubProjects.Get(name)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "project not found")
		return
	}

	writeJSON(w, http.StatusOK, projectDetailResponse{Project: project})
}

// handleProjectUpdate serves PATCH /api/projects/{name}.
func (s *Server) handleProjectUpdate(w http.ResponseWriter, r *http.Request, name string) {
	if s.hubProjects == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "hub not initialized")
		return
	}

	project, err := s.hubProjects.Get(name)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "project not found")
		return
	}

	var req updateProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body")
		return
	}

	if req.Path != nil {
		project.Path = *req.Path
	}
	if req.Keywords != nil {
		project.Keywords = req.Keywords
	}
	if req.Container != nil {
		project.Container = *req.Container
	}
	if req.DefaultMCPs != nil {
		project.DefaultMCPs = req.DefaultMCPs
	}

	if err := s.hubProjects.Save(project); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to update project")
		return
	}

	writeJSON(w, http.StatusOK, projectDetailResponse{Project: project})
}

// handleProjectDelete serves DELETE /api/projects/{name}.
func (s *Server) handleProjectDelete(w http.ResponseWriter, name string) {
	if s.hubProjects == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "hub not initialized")
		return
	}

	if err := s.hubProjects.Delete(name); err != nil {
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "project not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
```

**Step 7: Add request/response types**

Add to `internal/web/handlers_hub.go` alongside the existing types:

```go
type projectDetailResponse struct {
	Project *hub.Project `json:"project"`
}

type createProjectRequest struct {
	Repo        string   `json:"repo"`
	Name        string   `json:"name,omitempty"`
	Path        string   `json:"path,omitempty"`
	Keywords    []string `json:"keywords,omitempty"`
	Container   string   `json:"container,omitempty"`
	DefaultMCPs []string `json:"defaultMcps,omitempty"`
}

type updateProjectRequest struct {
	Path        *string  `json:"path,omitempty"`
	Keywords    []string `json:"keywords,omitempty"`
	Container   *string  `json:"container,omitempty"`
	DefaultMCPs []string `json:"defaultMcps,omitempty"`
}
```

**Step 8: Add `os` and `path/filepath` imports to handlers_hub.go**

These are needed for `os.UserHomeDir()` and `filepath.Join()` in the create handler.

**Step 9: Update containerForProject to use ProjectStore.Get instead of List**

Replace the current `containerForProject` method (which iterates the full List) with a direct Get:

```go
func (s *Server) containerForProject(projectName string) string {
	if s.hubProjects == nil {
		return ""
	}
	project, err := s.hubProjects.Get(projectName)
	if err != nil {
		return ""
	}
	return project.Container
}
```

**Step 10: Fix existing tests that use YAML-based project setup**

Several existing tests write `projects.yaml` files. They need updating to use `ProjectStore.Save()` instead. Find these tests by searching for `projects.yaml` in the test file and update them to use `srv.hubProjects.Save()`:

- `TestProjectsEndpointWithData` — save projects via store instead of writing YAML
- `TestRouteEndpoint` — save projects via store
- `TestRouteEndpointNoMatch` — save projects via store
- `TestTaskHealthCheckHealthy` — save projects via store
- `TestCreateTaskLaunchesSession` — save projects via store
- `TestTaskInputSendsToContainer` — save projects via store

Example conversion pattern:
```go
// Old:
hubDir := filepath.Dir(srv.hubProjects.FilePath())
yaml := `projects: ...`
os.WriteFile(filepath.Join(hubDir, "projects.yaml"), []byte(yaml), 0o644)

// New:
srv.hubProjects.Save(&hub.Project{
    Name:     "api-service",
    Path:     "/home/user/code/api",
    Keywords: []string{"api", "backend", "auth"},
    Container: "sandbox-api",
})
```

**Step 11: Remove the TestProjectsEndpointMethodNotAllowed test**

This test checks that POST returns 405 on `/api/projects`. But now POST is allowed (it creates a project). Remove this test.

**Step 12: Run all tests**

Run: `go test ./internal/web/ -v`
Expected: All PASS.

Run: `go test ./internal/hub/ -v`
Expected: All PASS.

**Step 13: Commit**

```bash
git add internal/web/server.go internal/web/handlers_hub.go internal/web/handlers_hub_test.go
git commit -m "feat(api): add project CRUD endpoints

POST /api/projects - create project from repo name
GET /api/projects/{name} - get single project
PATCH /api/projects/{name} - update project fields
DELETE /api/projects/{name} - remove project

Replaces ProjectRegistry with ProjectStore in server wiring.
Updates all existing tests to use ProjectStore instead of YAML files."
```

---

### Task 3: Remove Old ProjectRegistry

**Files:**
- Delete: `internal/hub/projects.go`
- Delete: `internal/hub/projects_test.go`

**Step 1: Delete the old files**

```bash
rm internal/hub/projects.go internal/hub/projects_test.go
```

**Step 2: Run all tests to confirm nothing depends on deleted code**

Run: `go test ./internal/hub/ -v && go test ./internal/web/ -v`
Expected: All PASS.

**Step 3: Run go vet and build**

Run: `go vet ./... && go build ./...`
Expected: No errors. If `yaml.v3` is no longer directly imported, `go mod tidy` will clean it up if unused.

**Step 4: Tidy modules**

Run: `go mod tidy`

**Step 5: Commit**

```bash
git add -A
git commit -m "refactor(hub): remove YAML-based ProjectRegistry

Replaced by ProjectStore (JSON filesystem) in previous commit.
The yaml.v3 dependency remains as an indirect dep from other packages."
```

---

### Task 4: Add Project UI — HTML Modal and Manage View

**Files:**
- Modify: `internal/web/static/dashboard.html` (add project modal and manage view markup)
- Modify: `internal/web/static/dashboard.css` (add project-specific styles)

**Step 1: Add "Manage Projects" button to the toolbar**

In `internal/web/static/dashboard.html`, add a "Manage Projects" button next to the "New Task" button in the `hub-filters` div (line 46):

```html
<button id="manage-projects-btn" class="hub-btn" type="button">Manage Projects</button>
```

**Step 2: Add the Add Project modal**

After the New Task modal closing `</div>` (line 94), add:

```html
<!-- Add Project modal -->
<div class="modal-backdrop" id="add-project-backdrop"></div>
<div class="modal" id="add-project-modal" role="dialog" aria-label="Add project" aria-hidden="true">
  <div class="modal-header">
    <span class="modal-title">Add Project</span>
    <button class="modal-close" id="add-project-close" type="button" aria-label="Close">&times;</button>
  </div>
  <div class="modal-body">
    <label class="modal-label" for="add-project-repo">GitHub Repo</label>
    <input type="text" id="add-project-repo" class="modal-input modal-field" placeholder="owner/repo-name" />
    <label class="modal-label" for="add-project-name">Name</label>
    <input type="text" id="add-project-name" class="modal-input modal-field" placeholder="Auto-filled from repo" />
    <label class="modal-label" for="add-project-path">Path</label>
    <input type="text" id="add-project-path" class="modal-input modal-field" placeholder="~/projects/repo-name" />
    <label class="modal-label" for="add-project-keywords">Keywords</label>
    <input type="text" id="add-project-keywords" class="modal-input modal-field" placeholder="api, backend, auth (comma-separated)" />
    <label class="modal-label" for="add-project-container">Container</label>
    <input type="text" id="add-project-container" class="modal-input modal-field" placeholder="Optional container name" />
  </div>
  <div class="modal-footer">
    <button id="add-project-cancel" class="hub-btn" type="button">Cancel</button>
    <button id="add-project-submit" class="hub-btn-primary" type="button">Create</button>
  </div>
</div>

<!-- Manage Projects panel -->
<div class="modal-backdrop" id="manage-projects-backdrop"></div>
<div class="modal modal-wide" id="manage-projects-modal" role="dialog" aria-label="Manage projects" aria-hidden="true">
  <div class="modal-header">
    <span class="modal-title">Projects</span>
    <div class="modal-header-actions">
      <button id="manage-projects-add" class="hub-btn-primary" type="button">+ Add</button>
      <button class="modal-close" id="manage-projects-close" type="button" aria-label="Close">&times;</button>
    </div>
  </div>
  <div class="modal-body" id="manage-projects-body">
    <div class="project-list-empty">No projects yet.</div>
  </div>
</div>
```

**Step 3: Add CSS styles**

Add to `internal/web/static/dashboard.css`:

```css
/* ── Modal input fields ──────────────────────────────────────────── */

.modal-input {
  width: 100%;
  height: 36px;
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 0 10px;
  font: inherit;
  font-size: 0.9rem;
  color: var(--text);
  background: #fff;
}

.modal-input:focus {
  outline: 2px solid rgba(15, 118, 110, 0.2);
  border-color: var(--accent);
}

/* ── Wide modal (for project list) ───────────────────────────────── */

.modal-wide {
  width: min(600px, calc(100vw - 32px));
}

.modal-header-actions {
  display: flex;
  gap: 8px;
  align-items: center;
}

/* ── Project list ────────────────────────────────────────────────── */

.project-list-empty {
  padding: 24px 16px;
  text-align: center;
  color: var(--muted);
  font-size: 0.9rem;
}

.project-row {
  display: flex;
  align-items: center;
  padding: 10px 0;
  border-bottom: 1px solid var(--border);
  gap: 12px;
}

.project-row:last-child {
  border-bottom: none;
}

.project-row-info {
  flex: 1;
  min-width: 0;
}

.project-row-name {
  font-weight: 600;
  font-size: 0.92rem;
}

.project-row-path {
  font-size: 0.82rem;
  color: var(--muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.project-row-keywords {
  font-size: 0.78rem;
  color: var(--muted);
}

.project-row-actions {
  display: flex;
  gap: 6px;
  flex: 0 0 auto;
}

.project-row-actions button {
  background: none;
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 4px 10px;
  font: inherit;
  font-size: 0.8rem;
  color: var(--muted);
  cursor: pointer;
}

.project-row-actions button:hover {
  background: var(--bg);
  color: var(--text);
}

.project-row-actions .btn-danger:hover {
  background: #fef2f2;
  color: #dc2626;
  border-color: #fca5a5;
}
```

**Step 4: Commit HTML/CSS**

```bash
git add internal/web/static/dashboard.html internal/web/static/dashboard.css
git commit -m "feat(ui): add project modal and manage view markup

Adds Add Project modal (repo, name, path, keywords, container fields),
Manage Projects list panel, and supporting CSS styles."
```

---

### Task 5: Add Project UI — JavaScript Logic

**Files:**
- Modify: `internal/web/static/dashboard.js` (add project CRUD functions and event wiring)

**Step 1: Add DOM references for new elements**

Add after the existing DOM reference block (around line 26):

```javascript
var manageProjectsBtn = document.getElementById("manage-projects-btn")
var manageProjectsModal = document.getElementById("manage-projects-modal")
var manageProjectsBackdrop = document.getElementById("manage-projects-backdrop")
var manageProjectsClose = document.getElementById("manage-projects-close")
var manageProjectsAdd = document.getElementById("manage-projects-add")
var manageProjectsBody = document.getElementById("manage-projects-body")
var addProjectModal = document.getElementById("add-project-modal")
var addProjectBackdrop = document.getElementById("add-project-backdrop")
var addProjectClose = document.getElementById("add-project-close")
var addProjectCancel = document.getElementById("add-project-cancel")
var addProjectSubmit = document.getElementById("add-project-submit")
var addProjectRepo = document.getElementById("add-project-repo")
var addProjectName = document.getElementById("add-project-name")
var addProjectPath = document.getElementById("add-project-path")
var addProjectKeywords = document.getElementById("add-project-keywords")
var addProjectContainer = document.getElementById("add-project-container")
```

**Step 2: Add state for tracking context**

Add a field to `state` to track whether the add-project modal was opened from the new-task modal:

```javascript
// Add to state object:
addProjectFromNewTask: false,
editingProjectName: null,
```

**Step 3: Add project CRUD functions**

Add after the `suggestProject` function (around line 576):

```javascript
// ── Manage Projects modal ─────────────────────────────────────────
function openManageProjects() {
  renderProjectList()
  if (manageProjectsModal) manageProjectsModal.classList.add("open")
  if (manageProjectsBackdrop) manageProjectsBackdrop.classList.add("open")
  if (manageProjectsModal) manageProjectsModal.setAttribute("aria-hidden", "false")
}

function closeManageProjects() {
  if (manageProjectsModal) manageProjectsModal.classList.remove("open")
  if (manageProjectsBackdrop) manageProjectsBackdrop.classList.remove("open")
  if (manageProjectsModal) manageProjectsModal.setAttribute("aria-hidden", "true")
}

function renderProjectList() {
  if (!manageProjectsBody) return
  clearChildren(manageProjectsBody)

  if (state.projects.length === 0) {
    manageProjectsBody.appendChild(el("div", "project-list-empty", "No projects yet. Click + Add to create one."))
    return
  }

  for (var i = 0; i < state.projects.length; i++) {
    manageProjectsBody.appendChild(createProjectRow(state.projects[i]))
  }
}

function createProjectRow(project) {
  var row = el("div", "project-row")

  var info = el("div", "project-row-info")
  info.appendChild(el("div", "project-row-name", project.name))
  info.appendChild(el("div", "project-row-path", project.path || "\u2014"))
  if (project.keywords && project.keywords.length > 0) {
    info.appendChild(el("div", "project-row-keywords", project.keywords.join(", ")))
  }
  row.appendChild(info)

  var actions = el("div", "project-row-actions")

  var editBtn = el("button", "", "Edit")
  editBtn.addEventListener("click", function () {
    openEditProject(project)
  })
  actions.appendChild(editBtn)

  var deleteBtn = el("button", "btn-danger", "Delete")
  deleteBtn.addEventListener("click", function () {
    deleteProject(project.name)
  })
  actions.appendChild(deleteBtn)

  row.appendChild(actions)
  return row
}

// ── Add/Edit Project modal ──────────────────────────────────────────
function openAddProject(fromNewTask) {
  state.addProjectFromNewTask = !!fromNewTask
  state.editingProjectName = null
  if (addProjectRepo) addProjectRepo.value = ""
  if (addProjectName) addProjectName.value = ""
  if (addProjectPath) addProjectPath.value = ""
  if (addProjectKeywords) addProjectKeywords.value = ""
  if (addProjectContainer) addProjectContainer.value = ""
  if (addProjectRepo) addProjectRepo.disabled = false

  // Update modal title
  var titleEl = addProjectModal ? addProjectModal.querySelector(".modal-title") : null
  if (titleEl) titleEl.textContent = "Add Project"

  if (addProjectModal) addProjectModal.classList.add("open")
  if (addProjectBackdrop) addProjectBackdrop.classList.add("open")
  if (addProjectModal) addProjectModal.setAttribute("aria-hidden", "false")
  if (addProjectRepo) addProjectRepo.focus()
}

function openEditProject(project) {
  state.addProjectFromNewTask = false
  state.editingProjectName = project.name
  if (addProjectRepo) { addProjectRepo.value = project.repo || ""; addProjectRepo.disabled = true }
  if (addProjectName) addProjectName.value = project.name
  if (addProjectPath) addProjectPath.value = project.path || ""
  if (addProjectKeywords) addProjectKeywords.value = (project.keywords || []).join(", ")
  if (addProjectContainer) addProjectContainer.value = project.container || ""

  var titleEl = addProjectModal ? addProjectModal.querySelector(".modal-title") : null
  if (titleEl) titleEl.textContent = "Edit Project"

  if (addProjectModal) addProjectModal.classList.add("open")
  if (addProjectBackdrop) addProjectBackdrop.classList.add("open")
  if (addProjectModal) addProjectModal.setAttribute("aria-hidden", "false")
  if (addProjectPath) addProjectPath.focus()
}

function closeAddProject() {
  if (addProjectModal) addProjectModal.classList.remove("open")
  if (addProjectBackdrop) addProjectBackdrop.classList.remove("open")
  if (addProjectModal) addProjectModal.setAttribute("aria-hidden", "true")
}

function submitProject() {
  if (state.editingProjectName) {
    submitProjectUpdate()
  } else {
    submitProjectCreate()
  }
}

function submitProjectCreate() {
  var repo = addProjectRepo ? addProjectRepo.value.trim() : ""
  var name = addProjectName ? addProjectName.value.trim() : ""
  var path = addProjectPath ? addProjectPath.value.trim() : ""
  var keywordsStr = addProjectKeywords ? addProjectKeywords.value.trim() : ""
  var container = addProjectContainer ? addProjectContainer.value.trim() : ""

  if (!repo && !name) return

  var keywords = keywordsStr ? keywordsStr.split(",").map(function (k) { return k.trim() }).filter(Boolean) : []

  var payload = { repo: repo }
  if (name) payload.name = name
  if (path) payload.path = path
  if (keywords.length > 0) payload.keywords = keywords
  if (container) payload.container = container

  var headers = authHeaders()
  headers["Content-Type"] = "application/json"

  fetch(apiPathWithToken("/api/projects"), {
    method: "POST",
    headers: headers,
    body: JSON.stringify(payload),
  })
    .then(function (r) {
      if (!r.ok) return r.json().then(function (e) { throw new Error(e.message || "create failed") })
      return r.json()
    })
    .then(function (data) {
      closeAddProject()
      fetchProjects().then(function () {
        renderProjectList()
        if (state.addProjectFromNewTask && data.project) {
          // Re-open new task modal with the new project selected.
          openNewTaskModal()
          if (newTaskProject) {
            for (var i = 0; i < newTaskProject.options.length; i++) {
              if (newTaskProject.options[i].value === data.project.name) {
                newTaskProject.selectedIndex = i
                break
              }
            }
          }
        }
      })
    })
    .catch(function (err) {
      console.error("submitProjectCreate:", err)
      alert("Failed to create project: " + err.message)
    })
}

function submitProjectUpdate() {
  var path = addProjectPath ? addProjectPath.value.trim() : ""
  var keywordsStr = addProjectKeywords ? addProjectKeywords.value.trim() : ""
  var container = addProjectContainer ? addProjectContainer.value.trim() : ""

  var keywords = keywordsStr ? keywordsStr.split(",").map(function (k) { return k.trim() }).filter(Boolean) : []

  var payload = {}
  if (path) payload.path = path
  payload.keywords = keywords
  payload.container = container

  var headers = authHeaders()
  headers["Content-Type"] = "application/json"

  fetch(apiPathWithToken("/api/projects/" + state.editingProjectName), {
    method: "PATCH",
    headers: headers,
    body: JSON.stringify(payload),
  })
    .then(function (r) {
      if (!r.ok) return r.json().then(function (e) { throw new Error(e.message || "update failed") })
      return r.json()
    })
    .then(function () {
      closeAddProject()
      fetchProjects().then(function () {
        renderProjectList()
      })
    })
    .catch(function (err) {
      console.error("submitProjectUpdate:", err)
      alert("Failed to update project: " + err.message)
    })
}

function deleteProject(name) {
  if (!confirm("Delete project '" + name + "'? Tasks using this project will not be deleted.")) {
    return
  }

  var headers = authHeaders()

  fetch(apiPathWithToken("/api/projects/" + name), {
    method: "DELETE",
    headers: headers,
  })
    .then(function (r) {
      if (!r.ok) throw new Error("delete failed: " + r.status)
      fetchProjects().then(function () {
        renderProjectList()
      })
    })
    .catch(function (err) {
      console.error("deleteProject:", err)
    })
}
```

**Step 4: Add auto-fill logic for repo field**

Add an input listener on the repo field that auto-fills name and path:

```javascript
if (addProjectRepo) {
  addProjectRepo.addEventListener("input", function () {
    if (state.editingProjectName) return
    var repo = addProjectRepo.value.trim()
    if (!repo) return
    var parts = repo.split("/")
    var repoName = parts[parts.length - 1]
    if (addProjectName && !addProjectName.dataset.userEdited) {
      addProjectName.value = repoName
    }
    if (addProjectPath && !addProjectPath.dataset.userEdited) {
      addProjectPath.value = "~/projects/" + repoName
    }
  })
}

// Track user edits to name/path so auto-fill doesn't overwrite.
if (addProjectName) {
  addProjectName.addEventListener("input", function () {
    addProjectName.dataset.userEdited = "true"
  })
}
if (addProjectPath) {
  addProjectPath.addEventListener("input", function () {
    addProjectPath.dataset.userEdited = "true"
  })
}
```

**Step 5: Update openNewTaskModal to add "+ Add Project" option**

Modify the existing `openNewTaskModal` function. After the loop that adds project options, add:

```javascript
// Add "+ Add Project" option at the top.
var addOpt = document.createElement("option")
addOpt.value = "__add_project__"
addOpt.textContent = "+ Add Project..."
if (newTaskProject.options.length > 0) {
  newTaskProject.insertBefore(addOpt, newTaskProject.options[0])
} else {
  newTaskProject.appendChild(addOpt)
}
```

Add a change listener for the project dropdown to detect the "+ Add Project" selection. Add this in the event listeners section:

```javascript
if (newTaskProject) {
  newTaskProject.addEventListener("change", function () {
    if (newTaskProject.value === "__add_project__") {
      closeNewTaskModal()
      openAddProject(true)
    }
  })
}
```

**Step 6: Wire up event listeners for the new buttons/modals**

Add in the event listeners section:

```javascript
if (manageProjectsBtn) {
  manageProjectsBtn.addEventListener("click", openManageProjects)
}
if (manageProjectsClose) {
  manageProjectsClose.addEventListener("click", closeManageProjects)
}
if (manageProjectsBackdrop) {
  manageProjectsBackdrop.addEventListener("click", closeManageProjects)
}
if (manageProjectsAdd) {
  manageProjectsAdd.addEventListener("click", function () {
    openAddProject(false)
  })
}
if (addProjectClose) {
  addProjectClose.addEventListener("click", closeAddProject)
}
if (addProjectCancel) {
  addProjectCancel.addEventListener("click", closeAddProject)
}
if (addProjectBackdrop) {
  addProjectBackdrop.addEventListener("click", closeAddProject)
}
if (addProjectSubmit) {
  addProjectSubmit.addEventListener("click", submitProject)
}
```

**Step 7: Update Escape key handler**

Update the existing `document.addEventListener("keydown", ...)` to also handle the new modals:

```javascript
document.addEventListener("keydown", function (e) {
  if (e.key === "Escape") {
    if (addProjectModal && addProjectModal.classList.contains("open")) {
      closeAddProject()
    } else if (manageProjectsModal && manageProjectsModal.classList.contains("open")) {
      closeManageProjects()
    } else if (newTaskModal && newTaskModal.classList.contains("open")) {
      closeNewTaskModal()
    } else if (state.selectedTaskId) {
      closeDetail()
    }
  }
})
```

**Step 8: Reset auto-fill tracking when opening add project modal**

In `openAddProject`, clear the `dataset.userEdited` flags:

```javascript
if (addProjectName) delete addProjectName.dataset.userEdited
if (addProjectPath) delete addProjectPath.dataset.userEdited
```

**Step 9: Commit**

```bash
git add internal/web/static/dashboard.js
git commit -m "feat(ui): add project CRUD JavaScript logic

Adds manage projects modal, add/edit project modal, delete confirmation,
auto-fill name/path from GitHub repo, and '+Add Project' shortcut in
the new task dropdown."
```

---

### Task 6: Manual Testing and Final Verification

**Files:** None (testing only)

**Step 1: Run all Go tests**

Run: `go test ./... -v`
Expected: All PASS.

**Step 2: Build the project**

Run: `go build ./...`
Expected: Clean build, no errors.

**Step 3: Run go vet**

Run: `go vet ./...`
Expected: No issues.

**Step 4: Commit any fixes if needed**

If any issues found in steps 1-3, fix and commit with descriptive message.

**Step 5: Final commit (if all clean)**

No commit needed if everything passes.

---

### Summary of All Files Changed

| Action | File | Purpose |
|--------|------|---------|
| Create | `internal/hub/project_store.go` | JSON filesystem ProjectStore |
| Create | `internal/hub/project_store_test.go` | ProjectStore unit tests |
| Modify | `internal/hub/models.go` | Add CreatedAt/UpdatedAt/Repo to Project |
| Modify | `internal/web/server.go` | Swap ProjectRegistry for ProjectStore |
| Modify | `internal/web/handlers_hub.go` | Add project CRUD handlers |
| Modify | `internal/web/handlers_hub_test.go` | Add project handler tests, update existing |
| Delete | `internal/hub/projects.go` | Remove YAML ProjectRegistry |
| Delete | `internal/hub/projects_test.go` | Remove YAML registry tests |
| Modify | `internal/web/static/dashboard.html` | Add project modal/manage view markup |
| Modify | `internal/web/static/dashboard.css` | Add project-specific styles |
| Modify | `internal/web/static/dashboard.js` | Add project CRUD JS logic |
