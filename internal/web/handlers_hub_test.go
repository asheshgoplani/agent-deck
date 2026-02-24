package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/hub"
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
	return srv
}

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

	for _, s := range []hub.TaskStatus{hub.TaskStatusRunning, hub.TaskStatusComplete, hub.TaskStatusRunning} {
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
	if strings.Contains(body, `"status":"complete"`) {
		t.Fatalf("should not contain complete tasks, got: %s", body)
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
	if resp.Task.Status != hub.TaskStatusIdle {
		t.Fatalf("expected status idle, got %s", resp.Task.Status)
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

	body := `{"status":"complete"}`
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
	if resp.Task.Status != hub.TaskStatusComplete {
		t.Fatalf("expected status complete, got %s", resp.Task.Status)
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
		Status:      hub.TaskStatusWaiting,
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
		Status:      hub.TaskStatusWaiting,
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
		Status:      hub.TaskStatusComplete,
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
	if resp.Task.Status != hub.TaskStatusThinking {
		t.Fatalf("expected status thinking after launch, got %s", resp.Task.Status)
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
		Status:      hub.TaskStatusWaiting,
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

func TestTaskPreviewNoSession(t *testing.T) {
	srv := newTestServerWithHub(t)

	task := &hub.Task{
		Project:     "api-service",
		Description: "Fix auth bug",
		Phase:       hub.PhaseExecute,
		Status:      hub.TaskStatusIdle,
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
