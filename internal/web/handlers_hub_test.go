package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/hub"
	"github.com/asheshgoplani/agent-deck/internal/hub/workspace"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testExecutor struct {
	healthy    bool
	execOutput string
	execErr    error
}

func (e *testExecutor) IsHealthy(_ context.Context, _ string) bool {
	return e.healthy
}

func (e *testExecutor) Exec(_ context.Context, _ string, _ ...string) (string, error) {
	return e.execOutput, e.execErr
}

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

	// Initialize bridge with mock storage for tests
	srv.hubBridge = NewHubSessionBridge("_test", taskStore, projectStore, nil)
	srv.hubBridge.openStorage = func(profile string) (storageLoader, error) {
		return &testStorageLoader{}, nil
	}

	return srv
}

type testStorageLoader struct{}

func (t *testStorageLoader) LoadWithGroups() ([]*session.Instance, []*session.GroupData, error) {
	return nil, nil, nil
}

func (t *testStorageLoader) Close() error { return nil }

func TestTasksEndpointEmpty(t *testing.T) {
	srv := newTestServerWithHub(t)

	req := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"tasks":[]`) {
		t.Fatalf("expected empty tasks array, got: %s", rr.Body.String())
	}
}

func TestTasksEndpointWithTasks(t *testing.T) {
	srv := newTestServerWithHub(t)

	task := &hub.Task{
		Project:     "api-service",
		Description: "Fix auth bug",
		Phase:       hub.PhaseExecute,
		Status:      hub.TaskStatusRunning,
	}
	if err := srv.hubTasks.Save(task); err != nil {
		t.Fatalf("Save: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"project":"api-service"`) {
		t.Fatalf("expected project in response, got: %s", body)
	}
	if !strings.Contains(body, `"description":"Fix auth bug"`) {
		t.Fatalf("expected description in response, got: %s", body)
	}
}

func TestTasksEndpointFilterByStatus(t *testing.T) {
	srv := newTestServerWithHub(t)

	for _, s := range []hub.TaskStatus{hub.TaskStatusRunning, hub.TaskStatusDone, hub.TaskStatusRunning} {
		task := &hub.Task{
			Project:     "test",
			Description: "task-" + string(s),
			Phase:       hub.PhaseExecute,
			Status:      s,
		}
		if err := srv.hubTasks.Save(task); err != nil {
			t.Fatalf("Save: %v", err)
		}
		time.Sleep(time.Millisecond)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/tasks?status=running", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
	}
	body := rr.Body.String()
	if strings.Contains(body, `"status":"done"`) {
		t.Fatalf("should not contain done tasks, got: %s", body)
	}
}

func TestTasksEndpointFilterByProject(t *testing.T) {
	srv := newTestServerWithHub(t)

	for _, proj := range []string{"api-service", "web-app", "api-service"} {
		task := &hub.Task{
			Project:     proj,
			Description: "task in " + proj,
			Phase:       hub.PhaseExecute,
			Status:      hub.TaskStatusRunning,
		}
		if err := srv.hubTasks.Save(task); err != nil {
			t.Fatalf("Save: %v", err)
		}
		time.Sleep(time.Millisecond)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/tasks?project=web-app", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
	}
	body := rr.Body.String()
	if strings.Contains(body, `"project":"api-service"`) {
		t.Fatalf("should not contain api-service tasks, got: %s", body)
	}
	if !strings.Contains(body, `"project":"web-app"`) {
		t.Fatalf("should contain web-app tasks, got: %s", body)
	}
}

func TestTasksEndpointMethodNotAllowed(t *testing.T) {
	srv := newTestServerWithHub(t)

	req := httptest.NewRequest(http.MethodPut, "/api/tasks", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status %d, got %d", http.StatusMethodNotAllowed, rr.Code)
	}
}

func TestCreateTask(t *testing.T) {
	srv := newTestServerWithHub(t)

	body := `{"project":"web-app","description":"Add dark mode"}`
	req := httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected %d, got %d: %s", http.StatusCreated, rr.Code, rr.Body.String())
	}

	var resp taskDetailResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Task.Project != "web-app" {
		t.Fatalf("expected project web-app, got %s", resp.Task.Project)
	}
	if resp.Task.Description != "Add dark mode" {
		t.Fatalf("expected description 'Add dark mode', got %s", resp.Task.Description)
	}
	if resp.Task.ID == "" {
		t.Fatal("expected auto-generated ID")
	}
	if resp.Task.Status != hub.TaskStatusBacklog {
		t.Fatalf("expected status backlog, got %s", resp.Task.Status)
	}
	if resp.Task.AgentStatus != hub.AgentStatusIdle {
		t.Fatalf("expected agentStatus idle, got %s", resp.Task.AgentStatus)
	}
	if resp.Task.Phase != hub.PhaseExecute {
		t.Fatalf("expected default phase execute, got %s", resp.Task.Phase)
	}
}

func TestCreateTaskWithPhase(t *testing.T) {
	srv := newTestServerWithHub(t)

	body := `{"project":"web-app","description":"Research auth options","phase":"brainstorm"}`
	req := httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected %d, got %d: %s", http.StatusCreated, rr.Code, rr.Body.String())
	}

	var resp taskDetailResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Task.Phase != hub.PhaseBrainstorm {
		t.Fatalf("expected phase brainstorm, got %s", resp.Task.Phase)
	}
}

func TestCreateTaskMissingProject(t *testing.T) {
	srv := newTestServerWithHub(t)

	body := `{"description":"Add dark mode"}`
	req := httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d: %s", http.StatusBadRequest, rr.Code, rr.Body.String())
	}
}

func TestCreateTaskMissingDescription(t *testing.T) {
	srv := newTestServerWithHub(t)

	body := `{"project":"web-app"}`
	req := httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d: %s", http.StatusBadRequest, rr.Code, rr.Body.String())
	}
}

func TestCreateTaskInvalidJSON(t *testing.T) {
	srv := newTestServerWithHub(t)

	req := httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d: %s", http.StatusBadRequest, rr.Code, rr.Body.String())
	}
}

func TestCreateTaskInvalidPhase(t *testing.T) {
	srv := newTestServerWithHub(t)

	body := `{"project":"web-app","description":"Add dark mode","phase":"notarealphase"}`
	req := httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d: %s", http.StatusBadRequest, rr.Code, rr.Body.String())
	}
}

func TestUpdateTaskInvalidPhase(t *testing.T) {
	srv := newTestServerWithHub(t)

	task := &hub.Task{
		Project:     "api-service",
		Description: "Fix auth bug",
		Phase:       hub.PhaseExecute,
		Status:      hub.TaskStatusRunning,
	}
	if err := srv.hubTasks.Save(task); err != nil {
		t.Fatalf("Save: %v", err)
	}

	body := `{"phase":"notarealphase"}`
	req := httptest.NewRequest(http.MethodPatch, "/api/tasks/"+task.ID, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d: %s", http.StatusBadRequest, rr.Code, rr.Body.String())
	}
}

func TestUpdateTaskInvalidStatus(t *testing.T) {
	srv := newTestServerWithHub(t)

	task := &hub.Task{
		Project:     "api-service",
		Description: "Fix auth bug",
		Phase:       hub.PhaseExecute,
		Status:      hub.TaskStatusRunning,
	}
	if err := srv.hubTasks.Save(task); err != nil {
		t.Fatalf("Save: %v", err)
	}

	body := `{"status":"notarealstatus"}`
	req := httptest.NewRequest(http.MethodPatch, "/api/tasks/"+task.ID, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d: %s", http.StatusBadRequest, rr.Code, rr.Body.String())
	}
}

func TestTasksEndpointUnauthorized(t *testing.T) {
	srv := newTestServerWithHub(t)
	srv.cfg.Token = "secret"

	req := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rr.Code)
	}
}

func TestTaskByIDEndpoint(t *testing.T) {
	srv := newTestServerWithHub(t)

	task := &hub.Task{
		Project:     "api-service",
		Description: "Fix auth bug",
		Phase:       hub.PhaseExecute,
		Status:      hub.TaskStatusRunning,
	}
	if err := srv.hubTasks.Save(task); err != nil {
		t.Fatalf("Save: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/tasks/"+task.ID, nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"id":"`+task.ID+`"`) {
		t.Fatalf("expected task ID in response, got: %s", body)
	}
}

func TestTaskByIDNotFound(t *testing.T) {
	srv := newTestServerWithHub(t)

	req := httptest.NewRequest(http.MethodGet, "/api/tasks/t-nonexistent", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, rr.Code)
	}
}

func TestTaskByIDMissingID(t *testing.T) {
	srv := newTestServerWithHub(t)

	req := httptest.NewRequest(http.MethodGet, "/api/tasks/", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	// /api/tasks/ with empty ID should return bad request
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rr.Code)
	}
}

func TestUpdateTaskPhase(t *testing.T) {
	srv := newTestServerWithHub(t)

	task := &hub.Task{
		Project:     "api-service",
		Description: "Fix auth bug",
		Phase:       hub.PhaseExecute,
		Status:      hub.TaskStatusRunning,
	}
	if err := srv.hubTasks.Save(task); err != nil {
		t.Fatalf("Save: %v", err)
	}

	body := `{"phase":"review"}`
	req := httptest.NewRequest(http.MethodPatch, "/api/tasks/"+task.ID, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}

	var resp taskDetailResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Task.Phase != hub.PhaseReview {
		t.Fatalf("expected phase review, got %s", resp.Task.Phase)
	}
	if resp.Task.Description != "Fix auth bug" {
		t.Fatalf("description should be unchanged, got %s", resp.Task.Description)
	}
}

func TestUpdateTaskStatus(t *testing.T) {
	srv := newTestServerWithHub(t)

	task := &hub.Task{
		Project:     "api-service",
		Description: "Fix auth bug",
		Phase:       hub.PhaseExecute,
		Status:      hub.TaskStatusRunning,
	}
	if err := srv.hubTasks.Save(task); err != nil {
		t.Fatalf("Save: %v", err)
	}

	body := `{"status":"done"}`
	req := httptest.NewRequest(http.MethodPatch, "/api/tasks/"+task.ID, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}

	var resp taskDetailResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Task.Status != hub.TaskStatusDone {
		t.Fatalf("expected status done, got %s", resp.Task.Status)
	}
}

func TestUpdateTaskNotFound(t *testing.T) {
	srv := newTestServerWithHub(t)

	body := `{"phase":"review"}`
	req := httptest.NewRequest(http.MethodPatch, "/api/tasks/t-nonexistent", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected %d, got %d", http.StatusNotFound, rr.Code)
	}
}

func TestTaskInputAccepted(t *testing.T) {
	srv := newTestServerWithHub(t)

	task := &hub.Task{
		Project:     "api-service",
		Description: "Fix auth bug",
		Phase:       hub.PhaseExecute,
		Status:      hub.TaskStatusPlanning,
		AgentStatus: hub.AgentStatusWaiting,
	}
	if err := srv.hubTasks.Save(task); err != nil {
		t.Fatalf("Save: %v", err)
	}

	body := `{"input":"Use JWT tokens"}`
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/"+task.ID+"/input", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}

	if !strings.Contains(rr.Body.String(), `"status"`) {
		t.Fatalf("expected status in response, got: %s", rr.Body.String())
	}
}

func TestTaskInputNotFound(t *testing.T) {
	srv := newTestServerWithHub(t)

	body := `{"input":"hello"}`
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/t-nonexistent/input", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected %d, got %d", http.StatusNotFound, rr.Code)
	}
}

func TestTaskInputEmptyInput(t *testing.T) {
	srv := newTestServerWithHub(t)

	task := &hub.Task{
		Project:     "api-service",
		Description: "Fix auth bug",
		Phase:       hub.PhaseExecute,
		Status:      hub.TaskStatusPlanning,
		AgentStatus: hub.AgentStatusWaiting,
	}
	if err := srv.hubTasks.Save(task); err != nil {
		t.Fatalf("Save: %v", err)
	}

	body := `{"input":""}`
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/"+task.ID+"/input", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d: %s", http.StatusBadRequest, rr.Code, rr.Body.String())
	}
}

func TestForkTask(t *testing.T) {
	srv := newTestServerWithHub(t)

	parent := &hub.Task{
		Project:     "api-service",
		Description: "Fix auth bug",
		Phase:       hub.PhaseExecute,
		Status:      hub.TaskStatusRunning,
		Branch:      "feat/auth",
	}
	if err := srv.hubTasks.Save(parent); err != nil {
		t.Fatalf("Save: %v", err)
	}

	body := `{"description":"Try JWT approach"}`
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/"+parent.ID+"/fork", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected %d, got %d: %s", http.StatusCreated, rr.Code, rr.Body.String())
	}

	var resp taskDetailResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Task.ParentTaskID != parent.ID {
		t.Fatalf("expected parentTaskId %s, got %s", parent.ID, resp.Task.ParentTaskID)
	}
	if resp.Task.Project != parent.Project {
		t.Fatalf("expected project %s inherited from parent, got %s", parent.Project, resp.Task.Project)
	}
	if resp.Task.Description != "Try JWT approach" {
		t.Fatalf("expected description 'Try JWT approach', got %s", resp.Task.Description)
	}
	if resp.Task.ID == parent.ID {
		t.Fatal("child should have a different ID than parent")
	}
}

func TestForkTaskDefaultDescription(t *testing.T) {
	srv := newTestServerWithHub(t)

	parent := &hub.Task{
		Project:     "api-service",
		Description: "Fix auth bug",
		Phase:       hub.PhaseExecute,
		Status:      hub.TaskStatusRunning,
	}
	if err := srv.hubTasks.Save(parent); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Empty body — should use default description.
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/"+parent.ID+"/fork", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected %d, got %d: %s", http.StatusCreated, rr.Code, rr.Body.String())
	}

	var resp taskDetailResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Task.Description != "Fix auth bug (fork)" {
		t.Fatalf("expected default fork description, got %s", resp.Task.Description)
	}
}

func TestForkTaskNotFound(t *testing.T) {
	srv := newTestServerWithHub(t)

	req := httptest.NewRequest(http.MethodPost, "/api/tasks/t-nonexistent/fork", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected %d, got %d", http.StatusNotFound, rr.Code)
	}
}

func TestUpdateTaskInvalidJSON(t *testing.T) {
	srv := newTestServerWithHub(t)

	task := &hub.Task{
		Project:     "api-service",
		Description: "Fix auth bug",
		Phase:       hub.PhaseExecute,
		Status:      hub.TaskStatusRunning,
	}
	if err := srv.hubTasks.Save(task); err != nil {
		t.Fatalf("Save: %v", err)
	}

	req := httptest.NewRequest(http.MethodPatch, "/api/tasks/"+task.ID, strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d: %s", http.StatusBadRequest, rr.Code, rr.Body.String())
	}
}

func TestTaskInputMethodNotAllowed(t *testing.T) {
	srv := newTestServerWithHub(t)

	task := &hub.Task{
		Project:     "api-service",
		Description: "Fix auth bug",
		Phase:       hub.PhaseExecute,
		Status:      hub.TaskStatusRunning,
	}
	if err := srv.hubTasks.Save(task); err != nil {
		t.Fatalf("Save: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/tasks/"+task.ID+"/input", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected %d, got %d", http.StatusMethodNotAllowed, rr.Code)
	}
}

func TestTaskForkMethodNotAllowed(t *testing.T) {
	srv := newTestServerWithHub(t)

	task := &hub.Task{
		Project:     "api-service",
		Description: "Fix auth bug",
		Phase:       hub.PhaseExecute,
		Status:      hub.TaskStatusRunning,
	}
	if err := srv.hubTasks.Save(task); err != nil {
		t.Fatalf("Save: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/tasks/"+task.ID+"/fork", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected %d, got %d", http.StatusMethodNotAllowed, rr.Code)
	}
}

func TestTaskUnknownSubPath(t *testing.T) {
	srv := newTestServerWithHub(t)

	task := &hub.Task{
		Project:     "api-service",
		Description: "Fix auth bug",
		Phase:       hub.PhaseExecute,
		Status:      hub.TaskStatusRunning,
	}
	if err := srv.hubTasks.Save(task); err != nil {
		t.Fatalf("Save: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/tasks/"+task.ID+"/unknown", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected %d, got %d", http.StatusNotFound, rr.Code)
	}
}

func TestDeleteTask(t *testing.T) {
	srv := newTestServerWithHub(t)

	task := &hub.Task{
		Project:     "api-service",
		Description: "Fix auth bug",
		Phase:       hub.PhaseExecute,
		Status:      hub.TaskStatusDone,
		AgentStatus: hub.AgentStatusComplete,
	}
	if err := srv.hubTasks.Save(task); err != nil {
		t.Fatalf("Save: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/tasks/"+task.ID, nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected %d, got %d: %s", http.StatusNoContent, rr.Code, rr.Body.String())
	}

	// Verify task is gone.
	getReq := httptest.NewRequest(http.MethodGet, "/api/tasks/"+task.ID, nil)
	getRR := httptest.NewRecorder()
	srv.Handler().ServeHTTP(getRR, getReq)
	if getRR.Code != http.StatusNotFound {
		t.Fatalf("expected deleted task to return 404, got %d", getRR.Code)
	}
}

func TestDeleteTaskNotFound(t *testing.T) {
	srv := newTestServerWithHub(t)

	req := httptest.NewRequest(http.MethodDelete, "/api/tasks/t-nonexistent", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected %d, got %d", http.StatusNotFound, rr.Code)
	}
}

func TestProjectsEndpointEmpty(t *testing.T) {
	srv := newTestServerWithHub(t)

	req := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"projects":[]`) {
		t.Fatalf("expected empty projects array, got: %s", rr.Body.String())
	}
}

func TestProjectsEndpointWithData(t *testing.T) {
	srv := newTestServerWithHub(t)

	if err := srv.hubProjects.Save(&hub.Project{
		Name:     "api-service",
		Path:     "/home/user/code/api",
		Keywords: []string{"api", "backend"},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"name":"api-service"`) {
		t.Fatalf("expected project name in response, got: %s", body)
	}
}

func TestRouteEndpoint(t *testing.T) {
	srv := newTestServerWithHub(t)

	if err := srv.hubProjects.Save(&hub.Project{
		Name:     "api-service",
		Path:     "/home/user/code/api",
		Keywords: []string{"api", "backend", "auth"},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := srv.hubProjects.Save(&hub.Project{
		Name:     "web-app",
		Path:     "/home/user/code/web",
		Keywords: []string{"frontend", "ui", "react"},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	body := `{"message":"Fix the auth endpoint in the API"}`
	req := httptest.NewRequest(http.MethodPost, "/api/route", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}

	var resp hub.RouteResult
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Project != "api-service" {
		t.Fatalf("expected api-service, got %s", resp.Project)
	}
	if resp.Confidence <= 0 {
		t.Fatalf("expected positive confidence, got %f", resp.Confidence)
	}
}

func TestRouteEndpointNoMatch(t *testing.T) {
	srv := newTestServerWithHub(t)

	if err := srv.hubProjects.Save(&hub.Project{
		Name:     "api-service",
		Path:     "/home/user/code/api",
		Keywords: []string{"api"},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	body := `{"message":"Update kubernetes deployment config"}`
	req := httptest.NewRequest(http.MethodPost, "/api/route", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}

	var resp routeResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Project != "" {
		t.Fatalf("expected empty project for no match, got %s", resp.Project)
	}
}

func TestRouteEndpointEmptyMessage(t *testing.T) {
	srv := newTestServerWithHub(t)

	body := `{"message":""}`
	req := httptest.NewRequest(http.MethodPost, "/api/route", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d: %s", http.StatusBadRequest, rr.Code, rr.Body.String())
	}
}

func TestRouteEndpointMethodNotAllowed(t *testing.T) {
	srv := newTestServerWithHub(t)

	req := httptest.NewRequest(http.MethodGet, "/api/route", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected %d, got %d", http.StatusMethodNotAllowed, rr.Code)
	}
}

func TestRouteEndpointInvalidJSON(t *testing.T) {
	srv := newTestServerWithHub(t)

	req := httptest.NewRequest(http.MethodPost, "/api/route", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d: %s", http.StatusBadRequest, rr.Code, rr.Body.String())
	}
}

func TestTaskHealthCheckHealthy(t *testing.T) {
	srv := newTestServerWithHub(t)
	srv.containerExec = &testExecutor{healthy: true}

	task := &hub.Task{
		Project:     "api-service",
		Description: "Fix auth bug",
		Phase:       hub.PhaseExecute,
		Status:      hub.TaskStatusRunning,
	}
	if err := srv.hubTasks.Save(task); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := srv.hubProjects.Save(&hub.Project{
		Name:      "api-service",
		Path:      "/home/user/code/api",
		Keywords:  []string{"api"},
		Container: "sandbox-api",
	}); err != nil {
		t.Fatalf("Save project: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/tasks/"+task.ID+"/health", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"healthy":true`) {
		t.Fatalf("expected healthy:true, got: %s", rr.Body.String())
	}
}

func TestCreateTaskLaunchesSession(t *testing.T) {
	srv := newTestServerWithHub(t)
	exec := &testExecutor{healthy: true, execOutput: ""}
	srv.containerExec = exec
	srv.sessionLauncher = &hub.SessionLauncher{Executor: exec}

	if err := srv.hubProjects.Save(&hub.Project{
		Name:      "api-service",
		Path:      "/home/user/code/api",
		Keywords:  []string{"api"},
		Container: "sandbox-api",
	}); err != nil {
		t.Fatalf("Save project: %v", err)
	}

	body := `{"project":"api-service","description":"Fix auth bug"}`
	req := httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected %d, got %d: %s", http.StatusCreated, rr.Code, rr.Body.String())
	}

	var resp taskDetailResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Task.TmuxSession == "" {
		t.Fatal("expected tmuxSession to be set when container is available")
	}
	if resp.Task.Status != hub.TaskStatusRunning {
		t.Fatalf("expected status running after launch, got %s", resp.Task.Status)
	}
	if resp.Task.AgentStatus != hub.AgentStatusThinking {
		t.Fatalf("expected agentStatus thinking after launch, got %s", resp.Task.AgentStatus)
	}
}

func TestTaskHealthCheckNoContainer(t *testing.T) {
	srv := newTestServerWithHub(t)
	srv.containerExec = &testExecutor{healthy: false}

	task := &hub.Task{
		Project:     "api-service",
		Description: "Fix auth bug",
		Phase:       hub.PhaseExecute,
		Status:      hub.TaskStatusRunning,
	}
	if err := srv.hubTasks.Save(task); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// No projects.yaml — project has no container configured.
	req := httptest.NewRequest(http.MethodGet, "/api/tasks/"+task.ID+"/health", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"healthy":false`) {
		t.Fatalf("expected healthy:false, got: %s", rr.Body.String())
	}
}

func TestTaskInputSendsToContainer(t *testing.T) {
	srv := newTestServerWithHub(t)
	exec := &testExecutor{healthy: true, execOutput: ""}
	srv.containerExec = exec
	srv.sessionLauncher = &hub.SessionLauncher{Executor: exec}

	if err := srv.hubProjects.Save(&hub.Project{
		Name:      "api-service",
		Path:      "/home/user/code/api",
		Keywords:  []string{"api"},
		Container: "sandbox-api",
	}); err != nil {
		t.Fatalf("Save project: %v", err)
	}

	task := &hub.Task{
		Project:     "api-service",
		Description: "Fix auth bug",
		Phase:       hub.PhaseExecute,
		Status:      hub.TaskStatusPlanning,
		AgentStatus: hub.AgentStatusWaiting,
		TmuxSession: "agent-t-001",
	}
	if err := srv.hubTasks.Save(task); err != nil {
		t.Fatalf("Save: %v", err)
	}

	body := `{"input":"Use JWT tokens"}`
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/"+task.ID+"/input", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"delivered"`) {
		t.Fatalf("expected 'delivered' status, got: %s", rr.Body.String())
	}
}

func TestUpdateTaskAgentStatus(t *testing.T) {
	srv := newTestServerWithHub(t)

	task := &hub.Task{
		Project:     "api-service",
		Description: "Fix auth bug",
		Phase:       hub.PhaseExecute,
		Status:      hub.TaskStatusRunning,
		AgentStatus: hub.AgentStatusRunning,
	}
	if err := srv.hubTasks.Save(task); err != nil {
		t.Fatalf("Save: %v", err)
	}

	body := `{"agentStatus":"waiting","askQuestion":"Which auth method?"}`
	req := httptest.NewRequest(http.MethodPatch, "/api/tasks/"+task.ID, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d: %s", http.StatusOK, rr.Code, rr.Body.String())
	}

	var resp taskDetailResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Task.AgentStatus != hub.AgentStatusWaiting {
		t.Fatalf("expected agentStatus waiting, got %s", resp.Task.AgentStatus)
	}
	if resp.Task.AskQuestion != "Which auth method?" {
		t.Fatalf("expected askQuestion, got %s", resp.Task.AskQuestion)
	}
}

func TestUpdateTaskInvalidAgentStatus(t *testing.T) {
	srv := newTestServerWithHub(t)

	task := &hub.Task{
		Project:     "api-service",
		Description: "Fix auth bug",
		Phase:       hub.PhaseExecute,
		Status:      hub.TaskStatusRunning,
		AgentStatus: hub.AgentStatusRunning,
	}
	if err := srv.hubTasks.Save(task); err != nil {
		t.Fatalf("Save: %v", err)
	}

	body := `{"agentStatus":"notreal"}`
	req := httptest.NewRequest(http.MethodPatch, "/api/tasks/"+task.ID, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d: %s", http.StatusBadRequest, rr.Code, rr.Body.String())
	}
}

func TestTaskPreviewNoSession(t *testing.T) {
	srv := newTestServerWithHub(t)

	task := &hub.Task{
		Project:     "api-service",
		Description: "Fix auth bug",
		Phase:       hub.PhaseExecute,
		Status:      hub.TaskStatusBacklog,
		AgentStatus: hub.AgentStatusIdle,
	}
	if err := srv.hubTasks.Save(task); err != nil {
		t.Fatalf("Save: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/tasks/"+task.ID+"/preview", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected %d, got %d: %s", http.StatusServiceUnavailable, rr.Code, rr.Body.String())
	}
}

func TestTaskPreviewNoContainer(t *testing.T) {
	srv := newTestServerWithHub(t)
	srv.containerExec = &testExecutor{healthy: true}

	task := &hub.Task{
		Project:     "api-service",
		Description: "Fix auth bug",
		Phase:       hub.PhaseExecute,
		Status:      hub.TaskStatusRunning,
		TmuxSession: "agent-t-001",
	}
	if err := srv.hubTasks.Save(task); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// No projects.yaml — no container configured.
	req := httptest.NewRequest(http.MethodGet, "/api/tasks/"+task.ID+"/preview", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected %d, got %d: %s", http.StatusServiceUnavailable, rr.Code, rr.Body.String())
	}
}

func TestTaskPreviewNotFound(t *testing.T) {
	srv := newTestServerWithHub(t)

	req := httptest.NewRequest(http.MethodGet, "/api/tasks/t-nonexistent/preview", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected %d, got %d: %s", http.StatusNotFound, rr.Code, rr.Body.String())
	}
}

func TestTaskPreviewMethodNotAllowed(t *testing.T) {
	srv := newTestServerWithHub(t)

	task := &hub.Task{
		Project:     "api-service",
		Description: "Fix auth bug",
		Phase:       hub.PhaseExecute,
		Status:      hub.TaskStatusRunning,
	}
	if err := srv.hubTasks.Save(task); err != nil {
		t.Fatalf("Save: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/tasks/"+task.ID+"/preview", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected %d, got %d: %s", http.StatusMethodNotAllowed, rr.Code, rr.Body.String())
	}
}

// ── Project CRUD endpoint tests ─────────────────────────────────────────

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

// --- Hub-Session Bridge endpoint tests ---

func TestStartPhaseEndpoint(t *testing.T) {
	srv := newTestServerWithHub(t)

	proj := &hub.Project{Name: "api-service", Path: "/tmp/test-project", Keywords: []string{"api"}}
	require.NoError(t, srv.hubProjects.Save(proj))

	task := &hub.Task{
		Project:     "api-service",
		Description: "Fix auth bug",
		Phase:       hub.PhaseBrainstorm,
		Status:      hub.TaskStatusBacklog,
		AgentStatus: hub.AgentStatusIdle,
	}
	require.NoError(t, srv.hubTasks.Save(task))

	body := strings.NewReader(`{"phase":"brainstorm"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/"+task.ID+"/start-phase", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)

	var resp startPhaseResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.NotEmpty(t, resp.SessionID)
	assert.Equal(t, "brainstorm", resp.Phase)
}

func TestTransitionEndpoint(t *testing.T) {
	srv := newTestServerWithHub(t)

	proj := &hub.Project{Name: "api-service", Path: "/tmp/test-project", Keywords: []string{"api"}}
	require.NoError(t, srv.hubProjects.Save(proj))

	task := &hub.Task{
		Project:     "api-service",
		Description: "Fix auth bug",
		Phase:       hub.PhaseBrainstorm,
		Status:      hub.TaskStatusRunning,
		Sessions: []hub.Session{
			{ID: "t-001-brainstorm", Phase: hub.PhaseBrainstorm, Status: "active", ClaudeSessionID: "sess-old"},
		},
	}
	require.NoError(t, srv.hubTasks.Save(task))

	body := strings.NewReader(`{"nextPhase":"plan","summary":"Done brainstorming"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/"+task.ID+"/transition", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)

	var resp startPhaseResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, "plan", resp.Phase)
}

func TestTaskCreate_AutoStartsPhaseSession(t *testing.T) {
	srv := newTestServerWithHub(t)

	proj := &hub.Project{Name: "api-service", Path: "/tmp/test-project", Keywords: []string{"api"}}
	require.NoError(t, srv.hubProjects.Save(proj))

	body := strings.NewReader(`{"project":"api-service","description":"Fix auth bug","phase":"brainstorm"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/tasks", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	assert.Equal(t, http.StatusCreated, rr.Code)

	var resp taskDetailResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))

	// Task should have a session entry from the bridge
	assert.Len(t, resp.Task.Sessions, 1)
	assert.Equal(t, hub.PhaseBrainstorm, resp.Task.Sessions[0].Phase)
	assert.Equal(t, "active", resp.Task.Sessions[0].Status)
	assert.NotEmpty(t, resp.Task.Sessions[0].ClaudeSessionID)
	assert.Equal(t, hub.TaskStatusRunning, resp.Task.Status)
}

func TestFullHubSessionFlow(t *testing.T) {
	srv := newTestServerWithHub(t)

	proj := &hub.Project{Name: "api-service", Path: "/tmp/test-project", Keywords: []string{"api"}}
	require.NoError(t, srv.hubProjects.Save(proj))

	// 1. Create task — should auto-start brainstorm phase session
	body := strings.NewReader(`{"project":"api-service","description":"Fix auth bug","phase":"brainstorm"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/tasks", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	require.Equal(t, http.StatusCreated, rr.Code)

	var createResp taskDetailResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&createResp))
	taskID := createResp.Task.ID
	assert.Len(t, createResp.Task.Sessions, 1)

	// 2. Transition to plan
	body = strings.NewReader(`{"nextPhase":"plan","summary":"Explored approaches"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/tasks/"+taskID+"/transition", body)
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	// 3. Verify task has 2 sessions via GET
	req = httptest.NewRequest(http.MethodGet, "/api/tasks/"+taskID, nil)
	rr = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	var getResp taskDetailResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&getResp))
	assert.Len(t, getResp.Task.Sessions, 2)
	assert.Equal(t, "complete", getResp.Task.Sessions[0].Status)
	assert.Equal(t, "Explored approaches", getResp.Task.Sessions[0].Summary)
	assert.Equal(t, "active", getResp.Task.Sessions[1].Status)
	assert.Equal(t, hub.PhasePlan, getResp.Task.Phase)
}

// --- mockContainerRuntime for workspace tests ---

type mockContainerRuntime struct {
	createCalls int
	startCalls  int
	stopCalls   int
	removeCalls int
	createID    string
	state       workspace.ContainerState
	stats       workspace.ContainerStats
}

func (m *mockContainerRuntime) Create(_ context.Context, opts workspace.CreateOpts) (string, error) {
	m.createCalls++
	if m.createID != "" {
		return m.createID, nil
	}
	return opts.Name, nil
}

func (m *mockContainerRuntime) Start(_ context.Context, _ string) error {
	m.startCalls++
	return nil
}

func (m *mockContainerRuntime) Stop(_ context.Context, _ string, _ int) error {
	m.stopCalls++
	return nil
}

func (m *mockContainerRuntime) Remove(_ context.Context, _ string, _ bool) error {
	m.removeCalls++
	return nil
}

func (m *mockContainerRuntime) Status(_ context.Context, _ string) (workspace.ContainerState, error) {
	return m.state, nil
}

func (m *mockContainerRuntime) Stats(_ context.Context, _ string) (workspace.ContainerStats, error) {
	return m.stats, nil
}

func (m *mockContainerRuntime) Exec(_ context.Context, _ string, _ []string, _ io.Reader) ([]byte, int, error) {
	return nil, 0, nil
}

// --- Workspace endpoint tests ---

func TestWorkspacesListEmpty(t *testing.T) {
	srv := newTestServerWithHub(t)

	req := httptest.NewRequest(http.MethodGet, "/api/workspaces", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)

	var resp workspacesListResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Empty(t, resp.Workspaces)
}

func TestWorkspacesListWithProject(t *testing.T) {
	srv := newTestServerWithHub(t)
	mock := &mockContainerRuntime{
		state: workspace.ContainerState{Status: workspace.StatusRunning},
	}
	srv.containerRuntime = mock

	// Create a project with a container name.
	project := &hub.Project{
		Name:      "my-app",
		Repo:      "github.com/org/my-app",
		Path:      "/home/user/my-app",
		Image:     "ubuntu:24.04",
		Container: "agentdeck-my-app",
	}
	require.NoError(t, srv.hubProjects.Save(project))

	// Create an active task for the project.
	task := &hub.Task{
		Project:     "my-app",
		Description: "Fix bug",
		Phase:       hub.PhaseExecute,
		Status:      hub.TaskStatusRunning,
		AgentStatus: hub.AgentStatusThinking,
	}
	require.NoError(t, srv.hubTasks.Save(task))

	req := httptest.NewRequest(http.MethodGet, "/api/workspaces", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)

	var resp workspacesListResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	require.Len(t, resp.Workspaces, 1)

	ws := resp.Workspaces[0]
	assert.Equal(t, "my-app", ws.Name)
	assert.Equal(t, "github.com/org/my-app", ws.Repo)
	assert.Equal(t, "agentdeck-my-app", ws.Container)
	assert.Equal(t, workspace.StatusRunning, ws.ContainerStatus)
	assert.Equal(t, "ubuntu:24.04", ws.Image)
	assert.Equal(t, 1, ws.ActiveTasks)
}

func TestProjectCreateAutoProvision(t *testing.T) {
	srv := newTestServerWithHub(t)
	mock := &mockContainerRuntime{}
	srv.containerRuntime = mock

	body := strings.NewReader(`{
		"repo": "github.com/org/auto-app",
		"name": "auto-app",
		"path": "/tmp/auto-app",
		"image": "node:20",
		"cpuLimit": 2.0,
		"memoryLimit": 1073741824,
		"keywords": ["auto"]
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/projects", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	require.Equal(t, http.StatusCreated, rr.Code)

	// Verify the container runtime was called.
	assert.Equal(t, 1, mock.createCalls, "expected Create to be called once")
	assert.Equal(t, 1, mock.startCalls, "expected Start to be called once")

	// Verify the project was saved with the container name.
	var resp projectDetailResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, "agentdeck-auto-app", resp.Project.Container)
	assert.Equal(t, "node:20", resp.Project.Image)
}
