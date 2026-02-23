# Hub Dashboard Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add a mobile-first dashboard view to agent-deck showing task cards with status badges, alongside the existing terminal view.

**Architecture:** Extend the existing `internal/web` package with Task and Project data models, new API endpoints (`/api/tasks`, `/api/projects`), and a dashboard HTML/JS/CSS frontend that reuses the existing SSE and xterm.js infrastructure.

**Tech Stack:** Go 1.21+, vanilla JS, CSS, xterm.js (existing), SSE (existing)

---

## Task 1: Project Data Model

**Files:**
- Create: `internal/web/project.go`
- Create: `internal/web/project_test.go`

**Step 1: Write the failing test**

```go
// internal/web/project_test.go
package web

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadProjects_ValidYAML(t *testing.T) {
	tmpDir := t.TempDir()
	yamlContent := `projects:
  - name: api-service
    path: /workspace/api
    keywords: [api, backend, auth]
  - name: web-app
    path: /workspace/web
    keywords: [frontend, ui, react]
`
	yamlPath := filepath.Join(tmpDir, "projects.yaml")
	if err := os.WriteFile(yamlPath, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	projects, err := LoadProjects(yamlPath)
	if err != nil {
		t.Fatalf("LoadProjects failed: %v", err)
	}
	if len(projects) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(projects))
	}
	if projects[0].Name != "api-service" {
		t.Errorf("expected name 'api-service', got %q", projects[0].Name)
	}
	if len(projects[0].Keywords) != 3 {
		t.Errorf("expected 3 keywords, got %d", len(projects[0].Keywords))
	}
}

func TestLoadProjects_FileNotFound(t *testing.T) {
	_, err := LoadProjects("/nonexistent/projects.yaml")
	if err == nil {
		t.Error("expected error for missing file")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/web -run TestLoadProjects -v`
Expected: FAIL with "undefined: LoadProjects"

**Step 3: Write minimal implementation**

```go
// internal/web/project.go
package web

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Project represents a workspace project for task routing.
type Project struct {
	Name        string   `json:"name" yaml:"name"`
	Path        string   `json:"path" yaml:"path"`
	Keywords    []string `json:"keywords" yaml:"keywords"`
	Container   string   `json:"container,omitempty" yaml:"container,omitempty"`
	DefaultMCPs []string `json:"defaultMcps,omitempty" yaml:"default_mcps,omitempty"`
}

type projectsFile struct {
	Projects []Project `yaml:"projects"`
}

// LoadProjects loads projects from a YAML file.
func LoadProjects(path string) ([]Project, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read projects file: %w", err)
	}

	var pf projectsFile
	if err := yaml.Unmarshal(data, &pf); err != nil {
		return nil, fmt.Errorf("parse projects yaml: %w", err)
	}

	return pf.Projects, nil
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/web -run TestLoadProjects -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/web/project.go internal/web/project_test.go
git commit -m "feat(web): add project data model and YAML loader"
```

---

## Task 2: Project Service with Caching

**Files:**
- Modify: `internal/web/project.go`
- Modify: `internal/web/project_test.go`

**Step 1: Write the failing test**

```go
// Add to internal/web/project_test.go

func TestProjectService_GetAll(t *testing.T) {
	tmpDir := t.TempDir()
	yamlContent := `projects:
  - name: test-project
    path: /test
    keywords: [test]
`
	yamlPath := filepath.Join(tmpDir, "projects.yaml")
	if err := os.WriteFile(yamlPath, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	svc := NewProjectService(yamlPath)
	projects, err := svc.GetAll()
	if err != nil {
		t.Fatalf("GetAll failed: %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("expected 1 project, got %d", len(projects))
	}
}

func TestProjectService_FindByKeyword(t *testing.T) {
	tmpDir := t.TempDir()
	yamlContent := `projects:
  - name: api-service
    path: /api
    keywords: [api, backend]
  - name: web-app
    path: /web
    keywords: [frontend, ui]
`
	yamlPath := filepath.Join(tmpDir, "projects.yaml")
	if err := os.WriteFile(yamlPath, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	svc := NewProjectService(yamlPath)

	// Test exact keyword match
	project, confidence := svc.FindByKeyword("api")
	if project == nil {
		t.Fatal("expected to find project")
	}
	if project.Name != "api-service" {
		t.Errorf("expected 'api-service', got %q", project.Name)
	}
	if confidence < 0.8 {
		t.Errorf("expected high confidence, got %f", confidence)
	}

	// Test no match
	project, _ = svc.FindByKeyword("database")
	if project != nil {
		t.Error("expected no match for 'database'")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/web -run TestProjectService -v`
Expected: FAIL with "undefined: NewProjectService"

**Step 3: Write minimal implementation**

```go
// Add to internal/web/project.go

import (
	"strings"
	"sync"
)

// ProjectService manages project configuration with caching.
type ProjectService struct {
	path     string
	mu       sync.RWMutex
	projects []Project
	loaded   bool
}

// NewProjectService creates a project service for a config file path.
func NewProjectService(path string) *ProjectService {
	return &ProjectService{path: path}
}

// GetAll returns all configured projects.
func (s *ProjectService) GetAll() ([]Project, error) {
	s.mu.RLock()
	if s.loaded {
		projects := s.projects
		s.mu.RUnlock()
		return projects, nil
	}
	s.mu.RUnlock()

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.loaded {
		return s.projects, nil
	}

	projects, err := LoadProjects(s.path)
	if err != nil {
		return nil, err
	}

	s.projects = projects
	s.loaded = true
	return projects, nil
}

// FindByKeyword finds the best matching project for keywords in a message.
// Returns nil if no match found. Confidence is 0.0-1.0.
func (s *ProjectService) FindByKeyword(message string) (*Project, float64) {
	projects, err := s.GetAll()
	if err != nil || len(projects) == 0 {
		return nil, 0
	}

	msgLower := strings.ToLower(message)
	var bestMatch *Project
	var bestScore float64

	for i := range projects {
		p := &projects[i]
		for _, kw := range p.Keywords {
			if strings.Contains(msgLower, strings.ToLower(kw)) {
				score := float64(len(kw)) / float64(len(message))
				if score > bestScore {
					bestScore = score
					bestMatch = p
				}
			}
		}
	}

	if bestMatch == nil {
		return nil, 0
	}

	// Normalize confidence to 0.5-1.0 range for keyword matches
	confidence := 0.5 + (bestScore * 0.5)
	if confidence > 1.0 {
		confidence = 1.0
	}

	return bestMatch, confidence
}

// Reload forces a reload of the projects file.
func (s *ProjectService) Reload() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	projects, err := LoadProjects(s.path)
	if err != nil {
		return err
	}

	s.projects = projects
	s.loaded = true
	return nil
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/web -run TestProjectService -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/web/project.go internal/web/project_test.go
git commit -m "feat(web): add ProjectService with keyword matching"
```

---

## Task 3: Task Data Model

**Files:**
- Create: `internal/web/task.go`
- Create: `internal/web/task_test.go`

**Step 1: Write the failing test**

```go
// internal/web/task_test.go
package web

import (
	"strings"
	"testing"
)

func TestTask_NewTask(t *testing.T) {
	task := NewTask("api-service", "Fix auth bug")

	if task.ID == "" {
		t.Error("expected non-empty ID")
	}
	if !strings.HasPrefix(task.ID, "t-") {
		t.Errorf("expected ID to start with 't-', got %q", task.ID)
	}
	if task.Project != "api-service" {
		t.Errorf("expected project 'api-service', got %q", task.Project)
	}
	if task.Description != "Fix auth bug" {
		t.Errorf("expected description 'Fix auth bug', got %q", task.Description)
	}
	if task.Phase != PhaseExecute {
		t.Errorf("expected phase 'execute', got %q", task.Phase)
	}
	if task.Status != TaskStatusPending {
		t.Errorf("expected status 'pending', got %q", task.Status)
	}
	if task.CreatedAt.IsZero() {
		t.Error("expected non-zero CreatedAt")
	}
}

func TestPhase_IsValid(t *testing.T) {
	tests := []struct {
		phase Phase
		valid bool
	}{
		{PhaseBrainstorm, true},
		{PhasePlan, true},
		{PhaseExecute, true},
		{PhaseReview, true},
		{Phase("invalid"), false},
	}

	for _, tt := range tests {
		if tt.phase.IsValid() != tt.valid {
			t.Errorf("Phase(%q).IsValid() = %v, want %v", tt.phase, tt.phase.IsValid(), tt.valid)
		}
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/web -run TestTask -v`
Expected: FAIL with "undefined: NewTask"

**Step 3: Write minimal implementation**

```go
// internal/web/task.go
package web

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// Phase represents a workflow phase.
type Phase string

const (
	PhaseBrainstorm Phase = "brainstorm"
	PhasePlan       Phase = "plan"
	PhaseExecute    Phase = "execute"
	PhaseReview     Phase = "review"
)

// IsValid returns true if the phase is a known value.
func (p Phase) IsValid() bool {
	switch p {
	case PhaseBrainstorm, PhasePlan, PhaseExecute, PhaseReview:
		return true
	}
	return false
}

// TaskStatus represents the current state of a task.
type TaskStatus string

const (
	TaskStatusPending  TaskStatus = "pending"
	TaskStatusActive   TaskStatus = "active"
	TaskStatusComplete TaskStatus = "complete"
	TaskStatusError    TaskStatus = "error"
)

// Task represents a work item that wraps one or more agent sessions.
type Task struct {
	ID           string     `json:"id"`
	Project      string     `json:"project"`
	Description  string     `json:"description"`
	Phase        Phase      `json:"phase"`
	Status       TaskStatus `json:"status"`
	SessionID    string     `json:"sessionId,omitempty"`
	TmuxSession  string     `json:"tmuxSession,omitempty"`
	AgentStatus  string     `json:"agentStatus,omitempty"` // from session: running, waiting, idle, error
	Branch       string     `json:"branch,omitempty"`
	ParentTaskID string     `json:"parentTaskId,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
}

// NewTask creates a new task with generated ID and defaults.
func NewTask(project, description string) *Task {
	now := time.Now()
	return &Task{
		ID:          generateTaskID(),
		Project:     project,
		Description: description,
		Phase:       PhaseExecute,
		Status:      TaskStatusPending,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

func generateTaskID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "t-" + hex.EncodeToString(b)
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/web -run TestTask -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/web/task.go internal/web/task_test.go
git commit -m "feat(web): add Task data model with phases"
```

---

## Task 4: Task Storage (Filesystem JSON)

**Files:**
- Create: `internal/web/task_store.go`
- Create: `internal/web/task_store_test.go`

**Step 1: Write the failing test**

```go
// internal/web/task_store_test.go
package web

import (
	"testing"
)

func TestTaskStore_CreateAndGet(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewTaskStore(tmpDir)

	task := NewTask("api-service", "Test task")
	if err := store.Save(task); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	loaded, err := store.Get(task.ID)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if loaded.ID != task.ID {
		t.Errorf("ID mismatch: got %q, want %q", loaded.ID, task.ID)
	}
	if loaded.Description != task.Description {
		t.Errorf("Description mismatch: got %q, want %q", loaded.Description, task.Description)
	}
}

func TestTaskStore_List(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewTaskStore(tmpDir)

	// Create 3 tasks
	for i := 0; i < 3; i++ {
		task := NewTask("project", "Task")
		if err := store.Save(task); err != nil {
			t.Fatalf("Save failed: %v", err)
		}
	}

	tasks, err := store.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(tasks) != 3 {
		t.Errorf("expected 3 tasks, got %d", len(tasks))
	}
}

func TestTaskStore_GetNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewTaskStore(tmpDir)

	_, err := store.Get("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent task")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/web -run TestTaskStore -v`
Expected: FAIL with "undefined: NewTaskStore"

**Step 3: Write minimal implementation**

```go
// internal/web/task_store.go
package web

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// TaskStore handles persistence of tasks to filesystem JSON.
type TaskStore struct {
	dir string
}

// NewTaskStore creates a task store at the given directory.
func NewTaskStore(dir string) *TaskStore {
	return &TaskStore{dir: dir}
}

// Save persists a task to disk.
func (s *TaskStore) Save(task *Task) error {
	if err := os.MkdirAll(s.dir, 0755); err != nil {
		return fmt.Errorf("create tasks dir: %w", err)
	}

	path := filepath.Join(s.dir, task.ID+".json")
	data, err := json.MarshalIndent(task, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal task: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write task file: %w", err)
	}

	return nil
}

// Get loads a task by ID.
func (s *TaskStore) Get(id string) (*Task, error) {
	path := filepath.Join(s.dir, id+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("task not found: %s", id)
		}
		return nil, fmt.Errorf("read task file: %w", err)
	}

	var task Task
	if err := json.Unmarshal(data, &task); err != nil {
		return nil, fmt.Errorf("unmarshal task: %w", err)
	}

	return &task, nil
}

// List returns all tasks, sorted by creation time (newest first).
func (s *TaskStore) List() ([]*Task, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []*Task{}, nil
		}
		return nil, fmt.Errorf("read tasks dir: %w", err)
	}

	var tasks []*Task
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		id := strings.TrimSuffix(entry.Name(), ".json")
		task, err := s.Get(id)
		if err != nil {
			continue // skip corrupt files
		}
		tasks = append(tasks, task)
	}

	// Sort by creation time, newest first
	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].CreatedAt.After(tasks[j].CreatedAt)
	})

	return tasks, nil
}

// Delete removes a task.
func (s *TaskStore) Delete(id string) error {
	path := filepath.Join(s.dir, id+".json")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete task file: %w", err)
	}
	return nil
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/web -run TestTaskStore -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/web/task_store.go internal/web/task_store_test.go
git commit -m "feat(web): add TaskStore for filesystem JSON persistence"
```

---

## Task 5: Task Service (Session Integration)

**Files:**
- Create: `internal/web/task_service.go`
- Create: `internal/web/task_service_test.go`

**Step 1: Write the failing test**

```go
// internal/web/task_service_test.go
package web

import (
	"testing"
)

func TestTaskService_CreateTask(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewTaskStore(tmpDir)
	svc := NewTaskService(store, nil) // nil session service for unit test

	task, err := svc.CreateTask("api-service", "Fix bug", PhaseExecute)
	if err != nil {
		t.Fatalf("CreateTask failed: %v", err)
	}
	if task.ID == "" {
		t.Error("expected non-empty ID")
	}
	if task.Project != "api-service" {
		t.Errorf("expected project 'api-service', got %q", task.Project)
	}

	// Verify persisted
	loaded, err := store.Get(task.ID)
	if err != nil {
		t.Fatalf("task not persisted: %v", err)
	}
	if loaded.Description != "Fix bug" {
		t.Errorf("description mismatch: got %q", loaded.Description)
	}
}

func TestTaskService_ListByStatus(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewTaskStore(tmpDir)
	svc := NewTaskService(store, nil)

	// Create tasks with different statuses
	task1, _ := svc.CreateTask("p1", "Active task", PhaseExecute)
	task1.Status = TaskStatusActive
	_ = store.Save(task1)

	task2, _ := svc.CreateTask("p2", "Complete task", PhaseExecute)
	task2.Status = TaskStatusComplete
	_ = store.Save(task2)

	// List active only
	active, err := svc.List(TaskStatusActive, "")
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(active) != 1 {
		t.Errorf("expected 1 active task, got %d", len(active))
	}

	// List all
	all, err := svc.List("", "")
	if err != nil {
		t.Fatalf("List all failed: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("expected 2 tasks, got %d", len(all))
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/web -run TestTaskService -v`
Expected: FAIL with "undefined: NewTaskService"

**Step 3: Write minimal implementation**

```go
// internal/web/task_service.go
package web

import (
	"fmt"
	"time"
)

// SessionLinker links tasks to sessions (implemented by SessionDataService).
type SessionLinker interface {
	LoadMenuSnapshot() (*MenuSnapshot, error)
}

// TaskService manages task lifecycle.
type TaskService struct {
	store    *TaskStore
	sessions SessionLinker
}

// NewTaskService creates a task service.
func NewTaskService(store *TaskStore, sessions SessionLinker) *TaskService {
	return &TaskService{
		store:    store,
		sessions: sessions,
	}
}

// CreateTask creates a new task and persists it.
func (s *TaskService) CreateTask(project, description string, phase Phase) (*Task, error) {
	task := NewTask(project, description)
	task.Phase = phase

	if err := s.store.Save(task); err != nil {
		return nil, fmt.Errorf("save task: %w", err)
	}

	return task, nil
}

// Get retrieves a task by ID, enriched with session status.
func (s *TaskService) Get(id string) (*Task, error) {
	task, err := s.store.Get(id)
	if err != nil {
		return nil, err
	}

	s.enrichWithSessionStatus(task)
	return task, nil
}

// List returns tasks filtered by status and/or project.
func (s *TaskService) List(status TaskStatus, project string) ([]*Task, error) {
	tasks, err := s.store.List()
	if err != nil {
		return nil, err
	}

	// Enrich with session status
	for _, task := range tasks {
		s.enrichWithSessionStatus(task)
	}

	// Filter
	if status == "" && project == "" {
		return tasks, nil
	}

	var filtered []*Task
	for _, task := range tasks {
		if status != "" && task.Status != status {
			continue
		}
		if project != "" && task.Project != project {
			continue
		}
		filtered = append(filtered, task)
	}

	return filtered, nil
}

// Update modifies a task and persists changes.
func (s *TaskService) Update(id string, updates func(*Task)) (*Task, error) {
	task, err := s.store.Get(id)
	if err != nil {
		return nil, err
	}

	updates(task)
	task.UpdatedAt = time.Now()

	if err := s.store.Save(task); err != nil {
		return nil, fmt.Errorf("save updated task: %w", err)
	}

	return task, nil
}

func (s *TaskService) enrichWithSessionStatus(task *Task) {
	if task.SessionID == "" || s.sessions == nil {
		return
	}

	snapshot, err := s.sessions.LoadMenuSnapshot()
	if err != nil {
		return
	}

	for _, item := range snapshot.Items {
		if item.Session != nil && item.Session.ID == task.SessionID {
			task.AgentStatus = string(item.Session.Status)
			task.TmuxSession = item.Session.TmuxSession
			break
		}
	}
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/web -run TestTaskService -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/web/task_service.go internal/web/task_service_test.go
git commit -m "feat(web): add TaskService with session status enrichment"
```

---

## Task 6: API Handler - GET /api/tasks

**Files:**
- Create: `internal/web/handlers_tasks.go`
- Create: `internal/web/handlers_tasks_test.go`

**Step 1: Write the failing test**

```go
// internal/web/handlers_tasks_test.go
package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleTasks_List(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewTaskStore(tmpDir)
	svc := NewTaskService(store, nil)

	// Create test task
	_, _ = svc.CreateTask("api-service", "Test task", PhaseExecute)

	handler := newTasksHandler(svc)

	req := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var resp struct {
		Tasks []*Task `json:"tasks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.Tasks) != 1 {
		t.Errorf("expected 1 task, got %d", len(resp.Tasks))
	}
}

func TestHandleTasks_ListWithFilter(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewTaskStore(tmpDir)
	svc := NewTaskService(store, nil)

	task1, _ := svc.CreateTask("api-service", "Task 1", PhaseExecute)
	task1.Status = TaskStatusActive
	_ = store.Save(task1)

	task2, _ := svc.CreateTask("web-app", "Task 2", PhaseExecute)
	task2.Status = TaskStatusComplete
	_ = store.Save(task2)

	handler := newTasksHandler(svc)

	// Filter by project
	req := httptest.NewRequest(http.MethodGet, "/api/tasks?project=api-service", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var resp struct {
		Tasks []*Task `json:"tasks"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Tasks) != 1 {
		t.Errorf("expected 1 task for api-service, got %d", len(resp.Tasks))
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/web -run TestHandleTasks -v`
Expected: FAIL with "undefined: newTasksHandler"

**Step 3: Write minimal implementation**

```go
// internal/web/handlers_tasks.go
package web

import (
	"net/http"
	"strings"
)

type tasksHandler struct {
	svc *TaskService
}

func newTasksHandler(svc *TaskService) *tasksHandler {
	return &tasksHandler{svc: svc}
}

func (h *tasksHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.handleList(w, r)
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
	}
}

func (h *tasksHandler) handleList(w http.ResponseWriter, r *http.Request) {
	status := TaskStatus(r.URL.Query().Get("status"))
	project := r.URL.Query().Get("project")

	tasks, err := h.svc.List(status, project)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to list tasks")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"tasks": tasks,
	})
}

type taskByIDHandler struct {
	svc *TaskService
}

func newTaskByIDHandler(svc *TaskService) *taskByIDHandler {
	return &taskByIDHandler{svc: svc}
}

func (h *taskByIDHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Extract task ID from path: /api/tasks/{id}
	path := strings.TrimPrefix(r.URL.Path, "/api/tasks/")
	if path == "" || strings.Contains(path, "/") {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "task id required")
		return
	}
	taskID := path

	switch r.Method {
	case http.MethodGet:
		h.handleGet(w, r, taskID)
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
	}
}

func (h *taskByIDHandler) handleGet(w http.ResponseWriter, r *http.Request, taskID string) {
	task, err := h.svc.Get(taskID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "task not found")
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to get task")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"task": task,
	})
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/web -run TestHandleTasks -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/web/handlers_tasks.go internal/web/handlers_tasks_test.go
git commit -m "feat(web): add GET /api/tasks handler"
```

---

## Task 7: API Handler - GET /api/projects

**Files:**
- Create: `internal/web/handlers_projects.go`
- Create: `internal/web/handlers_projects_test.go`

**Step 1: Write the failing test**

```go
// internal/web/handlers_projects_test.go
package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestHandleProjects_List(t *testing.T) {
	tmpDir := t.TempDir()
	yamlContent := `projects:
  - name: api-service
    path: /api
    keywords: [api]
  - name: web-app
    path: /web
    keywords: [frontend]
`
	yamlPath := filepath.Join(tmpDir, "projects.yaml")
	if err := os.WriteFile(yamlPath, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	svc := NewProjectService(yamlPath)
	handler := newProjectsHandler(svc)

	req := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var resp struct {
		Projects []Project `json:"projects"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.Projects) != 2 {
		t.Errorf("expected 2 projects, got %d", len(resp.Projects))
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/web -run TestHandleProjects -v`
Expected: FAIL with "undefined: newProjectsHandler"

**Step 3: Write minimal implementation**

```go
// internal/web/handlers_projects.go
package web

import (
	"encoding/json"
	"io"
	"net/http"
)

type projectsHandler struct {
	svc *ProjectService
}

func newProjectsHandler(svc *ProjectService) *projectsHandler {
	return &projectsHandler{svc: svc}
}

func (h *projectsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}

	projects, err := h.svc.GetAll()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to load projects")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"projects": projects,
	})
}

type routeHandler struct {
	svc *ProjectService
}

func newRouteHandler(svc *ProjectService) *routeHandler {
	return &routeHandler{svc: svc}
}

func (h *routeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}

	var req struct {
		Message string `json:"message"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body")
		return
	}

	project, confidence := h.svc.FindByKeyword(req.Message)
	if project == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"project":    nil,
			"confidence": 0,
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"project":    project.Name,
		"confidence": confidence,
	})
}

func decodeJSONBody(r *http.Request, v any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1MB limit
	if err != nil {
		return err
	}
	return json.Unmarshal(body, v)
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/web -run TestHandleProjects -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/web/handlers_projects.go internal/web/handlers_projects_test.go
git commit -m "feat(web): add GET /api/projects handler"
```

---

## Task 8: Register Routes in Server

**Files:**
- Modify: `internal/web/server.go`

**Step 1: Add imports and hub initialization**

Add to imports:
```go
import (
	"path/filepath"
	// ... existing imports
	"github.com/asheshgoplani/agent-deck/internal/session"
)
```

**Step 2: Add route registration in NewServer**

After existing `mux.HandleFunc` calls, add:

```go
// Hub routes - tasks and projects
hubDir := filepath.Join(session.GetDataDir(cfg.Profile), "hub")
taskStore := NewTaskStore(filepath.Join(hubDir, "tasks"))
taskSvc := NewTaskService(taskStore, menuData)
projectsPath := filepath.Join(hubDir, "projects.yaml")
projectSvc := NewProjectService(projectsPath)

mux.Handle("/api/tasks", s.withAuth(newTasksHandler(taskSvc)))
mux.Handle("/api/tasks/", s.withAuth(newTaskByIDHandler(taskSvc)))
mux.Handle("/api/projects", s.withAuth(newProjectsHandler(projectSvc)))
mux.Handle("/api/route", s.withAuth(newRouteHandler(projectSvc)))
```

**Step 3: Add withAuth helper method**

```go
func (s *Server) withAuth(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authorizeRequest(r) {
			writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
			return
		}
		h.ServeHTTP(w, r)
	})
}
```

**Step 4: Run tests**

Run: `go test ./internal/web -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/web/server.go
git commit -m "feat(web): register /api/tasks and /api/projects routes"
```

---

## Task 9-11: Dashboard Frontend Files

**Files:**
- Create: `internal/web/static/dashboard.html`
- Create: `internal/web/static/dashboard.css`
- Create: `internal/web/static/dashboard.js`

These files contain the dashboard UI. The JavaScript uses **safe DOM methods** (createElement, textContent, appendChild) instead of innerHTML to prevent XSS vulnerabilities.

**Key patterns in dashboard.js:**

```javascript
// Safe element creation helper
function createElement(tag, attrs, children) {
  const el = document.createElement(tag);
  if (attrs) {
    Object.entries(attrs).forEach(([key, value]) => {
      if (key === 'className') el.className = value;
      else if (key === 'textContent') el.textContent = value;
      else if (key.startsWith('data')) el.dataset[key.slice(4).toLowerCase()] = value;
      else el.setAttribute(key, value);
    });
  }
  if (children) {
    children.forEach(child => {
      if (typeof child === 'string') el.appendChild(document.createTextNode(child));
      else el.appendChild(child);
    });
  }
  return el;
}

// Render task card safely
function renderTaskCard(task) {
  const card = createElement('div', { className: 'task-card', dataTaskid: task.id });

  const header = createElement('div', { className: 'task-card-header' }, [
    createElement('span', { className: 'task-status-badge ' + (task.agentStatus || task.status) }),
    createElement('span', { className: 'task-project', textContent: task.project })
  ]);

  const meta = createElement('div', {
    className: 'task-meta-line',
    textContent: task.id + ' · ' + task.phase
  });

  const desc = createElement('div', {
    className: 'task-description',
    textContent: task.description
  });

  const footer = createElement('div', { className: 'task-footer' }, [
    createElement('span', { textContent: formatDuration(task.createdAt) }),
    createElement('span', { textContent: task.branch || 'main' })
  ]);

  card.appendChild(header);
  card.appendChild(meta);
  card.appendChild(desc);
  card.appendChild(footer);

  card.addEventListener('click', () => openTaskDetail(task.id));
  return card;
}
```

**Step 1: Create files with safe DOM manipulation**

(Files to be created during implementation with full content)

**Step 2: Verify files exist**

Run: `ls -la internal/web/static/dashboard.*`
Expected: Three files listed

**Step 3: Commit**

```bash
git add internal/web/static/dashboard.html internal/web/static/dashboard.css internal/web/static/dashboard.js
git commit -m "feat(web): add dashboard frontend with safe DOM manipulation"
```

---

## Task 12: Serve Dashboard as Default

**Files:**
- Modify: `internal/web/server.go`
- Modify: `internal/web/static_files.go` (if using embed)

**Step 1: Update route handlers**

Change the root handler to serve dashboard, move terminal to `/terminal`:

```go
// In NewServer:
mux.HandleFunc("/", s.handleDashboard)
mux.HandleFunc("/terminal", s.handleTerminal) // renamed from handleIndex
```

**Step 2: Add handleDashboard method**

```go
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	// Serve dashboard.html from embedded FS or static directory
	s.serveStaticFile(w, r, "dashboard.html")
}
```

**Step 3: Test manually**

Run: `go run ./cmd/agent-deck web`
Visit: `http://localhost:8420/`
Expected: Dashboard UI loads

**Step 4: Commit**

```bash
git add internal/web/server.go internal/web/static_files.go
git commit -m "feat(web): serve dashboard as default view at /"
```

---

## Task 13: Integration Test

**Files:**
- Create: `internal/web/dashboard_integration_test.go`

**Step 1: Write integration test**

```go
// internal/web/dashboard_integration_test.go
package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDashboard_Integration(t *testing.T) {
	tmpDir := t.TempDir()
	hubDir := filepath.Join(tmpDir, "hub")

	// Create projects.yaml
	projectsYAML := `projects:
  - name: test-project
    path: /test
    keywords: [test, demo]
`
	if err := os.MkdirAll(hubDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hubDir, "projects.yaml"), []byte(projectsYAML), 0644); err != nil {
		t.Fatal(err)
	}

	// Create task
	store := NewTaskStore(filepath.Join(hubDir, "tasks"))
	task := NewTask("test-project", "Integration test task")
	if err := store.Save(task); err != nil {
		t.Fatal(err)
	}

	// Test API flow
	taskSvc := NewTaskService(store, nil)
	projectSvc := NewProjectService(filepath.Join(hubDir, "projects.yaml"))

	// Test GET /api/projects
	t.Run("GET /api/projects", func(t *testing.T) {
		handler := newProjectsHandler(projectSvc)
		req := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", rec.Code)
		}
	})

	// Test GET /api/tasks
	t.Run("GET /api/tasks", func(t *testing.T) {
		handler := newTasksHandler(taskSvc)
		req := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", rec.Code)
		}

		var resp struct {
			Tasks []*Task `json:"tasks"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if len(resp.Tasks) != 1 {
			t.Errorf("expected 1 task, got %d", len(resp.Tasks))
		}
	})

	// Test POST /api/route
	t.Run("POST /api/route", func(t *testing.T) {
		handler := newRouteHandler(projectSvc)
		body := `{"message": "Fix the test demo"}`
		req := httptest.NewRequest(http.MethodPost, "/api/route", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", rec.Code)
		}

		var resp struct {
			Project    string  `json:"project"`
			Confidence float64 `json:"confidence"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if resp.Project != "test-project" {
			t.Errorf("expected project 'test-project', got %q", resp.Project)
		}
	})
}
```

**Step 2: Run integration test**

Run: `go test ./internal/web -run TestDashboard_Integration -v`
Expected: PASS

**Step 3: Commit**

```bash
git add internal/web/dashboard_integration_test.go
git commit -m "test(web): add dashboard integration test"
```

---

## Summary

| Task | Files | Description |
|------|-------|-------------|
| 1 | project.go, project_test.go | Project data model and YAML loader |
| 2 | project.go, project_test.go | ProjectService with keyword matching |
| 3 | task.go, task_test.go | Task data model with phases |
| 4 | task_store.go, task_store_test.go | Filesystem JSON persistence |
| 5 | task_service.go, task_service_test.go | TaskService with session enrichment |
| 6 | handlers_tasks.go, handlers_tasks_test.go | GET /api/tasks endpoint |
| 7 | handlers_projects.go, handlers_projects_test.go | GET /api/projects endpoint |
| 8 | server.go | Register new routes |
| 9-11 | static/dashboard.* | Dashboard HTML/CSS/JS (safe DOM) |
| 12 | server.go | Serve dashboard as default |
| 13 | dashboard_integration_test.go | Integration test |

**Total: 13 tasks for Phase 1 MVP**
