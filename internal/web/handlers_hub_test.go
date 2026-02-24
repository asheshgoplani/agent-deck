package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/hub"
)

func newTestServerWithHub(t *testing.T) *Server {
	t.Helper()
	hubDir := t.TempDir()

	taskStore, err := hub.NewTaskStore(hubDir)
	if err != nil {
		t.Fatalf("NewTaskStore: %v", err)
	}

	projectReg := hub.NewProjectRegistry(hubDir)

	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
		Profile:    "test-profile",
	})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{Profile: "test-profile"}}
	srv.hubTasks = taskStore
	srv.hubProjects = projectReg
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

	req := httptest.NewRequest(http.MethodPost, "/api/tasks", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status %d, got %d", http.StatusMethodNotAllowed, rr.Code)
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

	// Write a projects.yaml to the hub dir
	hubDir := filepath.Dir(srv.hubProjects.FilePath())
	yamlContent := `projects:
  - name: api-service
    path: /home/user/code/api
    keywords:
      - api
      - backend
`
	if err := os.WriteFile(filepath.Join(hubDir, "projects.yaml"), []byte(yamlContent), 0o644); err != nil {
		t.Fatalf("write projects.yaml: %v", err)
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

func TestProjectsEndpointMethodNotAllowed(t *testing.T) {
	srv := newTestServerWithHub(t)

	req := httptest.NewRequest(http.MethodPost, "/api/projects", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status %d, got %d", http.StatusMethodNotAllowed, rr.Code)
	}
}
