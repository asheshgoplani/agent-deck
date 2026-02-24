package web

import (
	"net/http"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/hub"
)

// handleTasks serves GET /api/tasks with optional ?status= and ?project= filters.
func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}

	if s.hubTasks == nil {
		writeJSON(w, http.StatusOK, tasksListResponse{Tasks: []*hub.Task{}})
		return
	}

	tasks, err := s.hubTasks.List()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to load tasks")
		return
	}

	statusFilter := r.URL.Query().Get("status")
	projectFilter := r.URL.Query().Get("project")

	if statusFilter != "" || projectFilter != "" {
		filtered := make([]*hub.Task, 0, len(tasks))
		for _, t := range tasks {
			if statusFilter != "" && string(t.Status) != statusFilter {
				continue
			}
			if projectFilter != "" && t.Project != projectFilter {
				continue
			}
			filtered = append(filtered, t)
		}
		tasks = filtered
	}

	if tasks == nil {
		tasks = []*hub.Task{}
	}

	writeJSON(w, http.StatusOK, tasksListResponse{Tasks: tasks})
}

// handleTaskByID serves GET /api/tasks/{id}.
func (s *Server) handleTaskByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}

	const prefix = "/api/tasks/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "route not found")
		return
	}
	taskID := strings.TrimPrefix(r.URL.Path, prefix)
	if taskID == "" || strings.Contains(taskID, "/") {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "task id is required")
		return
	}

	if s.hubTasks == nil {
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "task not found")
		return
	}

	task, err := s.hubTasks.Get(taskID)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "task not found")
		return
	}

	writeJSON(w, http.StatusOK, taskDetailResponse{Task: task})
}

// handleProjects serves GET /api/projects.
func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}

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

// Response types for hub API endpoints and SSE events.

type tasksListResponse struct {
	Tasks []*hub.Task `json:"tasks"`
}

type tasksSSEPayload struct {
	Tasks []*hub.Task `json:"tasks"`
}

type taskDetailResponse struct {
	Task *hub.Task `json:"task"`
}

type projectsListResponse struct {
	Projects []*hub.Project `json:"projects"`
}
