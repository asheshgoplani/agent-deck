package web

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/hub"
)

// handleTasks dispatches GET /api/tasks and POST /api/tasks.
func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.handleTasksList(w, r)
	case http.MethodPost:
		s.handleTasksCreate(w, r)
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
	}
}

// handleTasksList serves GET /api/tasks with optional ?status= and ?project= filters.
func (s *Server) handleTasksList(w http.ResponseWriter, r *http.Request) {
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

// handleTasksCreate serves POST /api/tasks.
func (s *Server) handleTasksCreate(w http.ResponseWriter, r *http.Request) {
	if s.hubTasks == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "hub not initialized")
		return
	}

	var req createTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body")
		return
	}

	if req.Project == "" {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "project is required")
		return
	}
	if req.Description == "" {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "description is required")
		return
	}

	phase := hub.PhaseExecute
	if req.Phase != "" {
		phase = hub.Phase(req.Phase)
	}

	task := &hub.Task{
		Project:     req.Project,
		Description: req.Description,
		Phase:       phase,
		Branch:      req.Branch,
		Status:      hub.TaskStatusIdle,
	}

	if err := s.hubTasks.Save(task); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to create task")
		return
	}

	s.notifyTaskChanged()
	writeJSON(w, http.StatusCreated, taskDetailResponse{Task: task})
}

// handleTaskByID dispatches /api/tasks/{id}, /api/tasks/{id}/input, /api/tasks/{id}/fork.
func (s *Server) handleTaskByID(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}

	const prefix = "/api/tasks/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "route not found")
		return
	}

	remaining := strings.TrimPrefix(r.URL.Path, prefix)
	parts := strings.SplitN(remaining, "/", 2)
	taskID := parts[0]
	subPath := ""
	if len(parts) > 1 {
		subPath = parts[1]
	}

	if taskID == "" {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "task id is required")
		return
	}

	switch subPath {
	case "":
		switch r.Method {
		case http.MethodGet:
			s.handleTaskGet(w, taskID)
		case http.MethodPatch:
			s.handleTaskUpdate(w, r, taskID)
		case http.MethodDelete:
			s.handleTaskDelete(w, taskID)
		default:
			writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		}
	case "input":
		if r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
			return
		}
		s.handleTaskInput(w, r, taskID)
	case "fork":
		if r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
			return
		}
		s.handleTaskFork(w, r, taskID)
	default:
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "route not found")
	}
}

// handleTaskGet serves GET /api/tasks/{id}.
func (s *Server) handleTaskGet(w http.ResponseWriter, taskID string) {
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

// handleTaskUpdate serves PATCH /api/tasks/{id}.
func (s *Server) handleTaskUpdate(w http.ResponseWriter, r *http.Request, taskID string) {
	if s.hubTasks == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "hub not initialized")
		return
	}

	task, err := s.hubTasks.Get(taskID)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "task not found")
		return
	}

	var req updateTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body")
		return
	}

	if req.Description != nil {
		task.Description = *req.Description
	}
	if req.Phase != nil {
		task.Phase = hub.Phase(*req.Phase)
	}
	if req.Status != nil {
		task.Status = hub.TaskStatus(*req.Status)
	}
	if req.Branch != nil {
		task.Branch = *req.Branch
	}

	if err := s.hubTasks.Save(task); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to update task")
		return
	}

	s.notifyTaskChanged()
	writeJSON(w, http.StatusOK, taskDetailResponse{Task: task})
}

// handleTaskDelete serves DELETE /api/tasks/{id}.
func (s *Server) handleTaskDelete(w http.ResponseWriter, taskID string) {
	if s.hubTasks == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "hub not initialized")
		return
	}

	if err := s.hubTasks.Delete(taskID); err != nil {
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "task not found")
		return
	}

	s.notifyTaskChanged()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTaskInput(w http.ResponseWriter, r *http.Request, taskID string) {
	writeAPIError(w, http.StatusNotImplemented, "NOT_IMPLEMENTED", "not implemented")
}

func (s *Server) handleTaskFork(w http.ResponseWriter, r *http.Request, taskID string) {
	writeAPIError(w, http.StatusNotImplemented, "NOT_IMPLEMENTED", "not implemented")
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

type createTaskRequest struct {
	Project     string `json:"project"`
	Description string `json:"description"`
	Phase       string `json:"phase,omitempty"`
	Branch      string `json:"branch,omitempty"`
}

type updateTaskRequest struct {
	Description *string `json:"description,omitempty"`
	Phase       *string `json:"phase,omitempty"`
	Status      *string `json:"status,omitempty"`
	Branch      *string `json:"branch,omitempty"`
}

type taskInputRequest struct {
	Input string `json:"input"`
}

type taskInputResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}
