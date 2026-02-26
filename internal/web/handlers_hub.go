package web

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/hub"
	"github.com/asheshgoplani/agent-deck/internal/hub/workspace"
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
		if !isValidPhase(req.Phase) {
			writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid phase value")
			return
		}
		phase = hub.Phase(req.Phase)
	}

	task := &hub.Task{
		Project:     req.Project,
		Description: req.Description,
		Phase:       phase,
		Branch:      req.Branch,
		Status:      hub.TaskStatusBacklog,
		AgentStatus: hub.AgentStatusIdle,
	}

	if err := s.hubTasks.Save(task); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to create task")
		return
	}

	// Auto-start phase session via bridge (local sessions).
	// Bridge handles projects with a local path but no container.
	// Container-based launch is preferred when a container is configured.
	bridgeHandled := false
	if s.hubBridge != nil {
		if proj, projErr := s.hubProjects.Get(task.Project); projErr == nil && proj.Path != "" && proj.Container == "" {
			if _, bridgeErr := s.hubBridge.StartPhase(task.ID, phase); bridgeErr == nil {
				bridgeHandled = true
				// Re-read task to include session entry
				if updated, getErr := s.hubTasks.Get(task.ID); getErr == nil {
					task = updated
				}
			} else {
				slog.Warn("bridge_start_phase_failed",
					slog.String("task", task.ID),
					slog.String("error", bridgeErr.Error()))
			}
		}
	}

	// Attempt to launch tmux session if container is configured and bridge didn't handle it.
	if !bridgeHandled && s.sessionLauncher != nil {
		container := s.containerForProject(task.Project)
		if container != "" {
			sessionName, launchErr := s.sessionLauncher.Launch(r.Context(), container, task.ID)
			if launchErr == nil {
				task.TmuxSession = sessionName
				task.Status = hub.TaskStatusRunning
				task.AgentStatus = hub.AgentStatusThinking
				_ = s.hubTasks.Save(task) // Update with session info.
			} else {
				slog.Warn("session_launch_failed",
					slog.String("task", task.ID),
					slog.String("container", container),
					slog.String("error", launchErr.Error()))
			}
		}
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
	case "health":
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
			return
		}
		s.handleTaskHealth(w, r, taskID)
	case "preview":
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
			return
		}
		s.handleTaskPreview(w, r, taskID)
	case "start-phase":
		if r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
			return
		}
		s.handleTaskStartPhase(w, r, taskID)
	case "transition":
		if r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
			return
		}
		s.handleTaskTransition(w, r, taskID)
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

	if req.Phase != nil && !isValidPhase(*req.Phase) {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid phase value")
		return
	}
	if req.Status != nil && !isValidStatus(*req.Status) {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid status value")
		return
	}
	if req.AgentStatus != nil && !isValidAgentStatus(*req.AgentStatus) {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid agentStatus value")
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
	if req.AgentStatus != nil {
		task.AgentStatus = hub.AgentStatus(*req.AgentStatus)
	}
	if req.Branch != nil {
		task.Branch = *req.Branch
	}
	if req.AskQuestion != nil {
		task.AskQuestion = *req.AskQuestion
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

// handleTaskInput serves POST /api/tasks/{id}/input.
func (s *Server) handleTaskInput(w http.ResponseWriter, r *http.Request, taskID string) {
	if s.hubTasks == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "hub not initialized")
		return
	}

	task, err := s.hubTasks.Get(taskID)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "task not found")
		return
	}

	var req taskInputRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body")
		return
	}

	if req.Input == "" {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "input is required")
		return
	}

	// Attempt to deliver input to container tmux session.
	if s.sessionLauncher != nil && task.TmuxSession != "" {
		container := s.containerForProject(task.Project)
		if container != "" {
			if sendErr := s.sessionLauncher.SendInput(r.Context(), container, task.TmuxSession, req.Input); sendErr == nil {
				writeJSON(w, http.StatusOK, taskInputResponse{
					Status:  "delivered",
					Message: "input sent to session",
				})
				return
			} else {
				slog.Warn("send_input_failed",
					slog.String("task", taskID),
					slog.String("session", task.TmuxSession),
					slog.String("error", sendErr.Error()))
			}
		}
	}

	// Fallback: no container/session available.
	writeJSON(w, http.StatusOK, taskInputResponse{
		Status:  "queued",
		Message: "input accepted (session not connected)",
	})
}

// handleTaskFork serves POST /api/tasks/{id}/fork.
func (s *Server) handleTaskFork(w http.ResponseWriter, r *http.Request, taskID string) {
	if s.hubTasks == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "hub not initialized")
		return
	}

	parent, err := s.hubTasks.Get(taskID)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "parent task not found")
		return
	}

	var req createTaskRequest
	if decodeErr := json.NewDecoder(r.Body).Decode(&req); decodeErr != nil {
		req = createTaskRequest{}
	}

	description := req.Description
	if description == "" {
		description = parent.Description + " (fork)"
	}

	child := &hub.Task{
		Project:      parent.Project,
		Description:  description,
		Phase:        parent.Phase,
		Branch:       parent.Branch,
		Status:       hub.TaskStatusBacklog,
		AgentStatus:  hub.AgentStatusIdle,
		ParentTaskID: parent.ID,
	}

	if err := s.hubTasks.Save(child); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to create fork")
		return
	}

	s.notifyTaskChanged()
	writeJSON(w, http.StatusCreated, taskDetailResponse{Task: child})
}

type taskHealthResponse struct {
	Healthy   bool   `json:"healthy"`
	Container string `json:"container,omitempty"`
	Message   string `json:"message,omitempty"`
}

// handleTaskHealth serves GET /api/tasks/{id}/health.
func (s *Server) handleTaskHealth(w http.ResponseWriter, r *http.Request, taskID string) {
	if s.hubTasks == nil {
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "task not found")
		return
	}

	task, err := s.hubTasks.Get(taskID)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "task not found")
		return
	}

	container := s.containerForProject(task.Project)
	if container == "" {
		writeJSON(w, http.StatusOK, taskHealthResponse{
			Healthy: false,
			Message: "no container configured for project",
		})
		return
	}

	if s.containerExec == nil {
		writeJSON(w, http.StatusOK, taskHealthResponse{
			Healthy:   false,
			Container: container,
			Message:   "container executor not configured",
		})
		return
	}

	healthy := s.containerExec.IsHealthy(r.Context(), container)
	resp := taskHealthResponse{
		Healthy:   healthy,
		Container: container,
	}
	if !healthy {
		resp.Message = "container not running"
	}

	writeJSON(w, http.StatusOK, resp)
}

// containerForProject looks up the container name for a project from the store.
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

// handleTaskPreview serves GET /api/tasks/{id}/preview as an SSE stream.
// Streams tmux output from the container's pipe-pane log file.
func (s *Server) handleTaskPreview(w http.ResponseWriter, r *http.Request, taskID string) {
	if s.hubTasks == nil {
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "task not found")
		return
	}

	task, err := s.hubTasks.Get(taskID)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "task not found")
		return
	}

	if task.TmuxSession == "" || s.containerExec == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "no active session")
		return
	}

	container := s.containerForProject(task.Project)
	if container == "" {
		writeAPIError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "no container configured")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	logFile := fmt.Sprintf("/tmp/%s.log", task.TmuxSession)
	ctx := r.Context()

	pollTicker := time.NewTicker(1 * time.Second)
	defer pollTicker.Stop()

	heartbeatTicker := time.NewTicker(15 * time.Second)
	defer heartbeatTicker.Stop()

	var lastHash [32]byte
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeatTicker.C:
			if err := writeSSEComment(w, flusher, "keepalive"); err != nil {
				return
			}
		case <-pollTicker.C:
			output, execErr := s.containerExec.Exec(ctx, container, "tail", "-n", "50", logFile)
			if execErr != nil {
				continue
			}
			currentHash := sha256.Sum256([]byte(output))
			if currentHash != lastHash {
				lastHash = currentHash
				if writeErr := writeSSEEvent(w, flusher, "preview", map[string]string{
					"taskId": taskID,
					"output": output,
				}); writeErr != nil {
					return
				}
			}
		}
	}
}

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
		Image:       req.Image,
		CPULimit:    req.CPULimit,
		MemoryLimit: req.MemoryLimit,
		Volumes:     req.Volumes,
		Env:         req.Env,
	}

	if err := s.hubProjects.Save(project); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to create project")
		return
	}

	// Auto-provision container if an image is specified and a runtime is available.
	if project.Image != "" && s.containerRuntime != nil {
		containerName := workspace.ContainerNameForProject(project.Name)

		// Build environment variables from map.
		var envSlice []string
		for k, v := range project.Env {
			envSlice = append(envSlice, k+"="+v)
		}

		// Convert VolumeMount to workspace.Mount.
		var mounts []workspace.Mount
		for _, v := range project.Volumes {
			mounts = append(mounts, workspace.Mount{
				Source:   v.Host,
				Target:   v.Container,
				ReadOnly: v.ReadOnly,
			})
		}

		opts := workspace.CreateOpts{
			Name:     containerName,
			Image:    project.Image,
			Env:      envSlice,
			Mounts:   mounts,
			NanoCPUs: int64(project.CPULimit * 1e9),
			Memory:   project.MemoryLimit,
			Labels: map[string]string{
				"agentdeck.project": project.Name,
			},
		}

		if _, createErr := s.containerRuntime.Create(r.Context(), opts); createErr != nil {
			slog.Warn("auto_provision_create_failed",
				slog.String("project", project.Name),
				slog.String("image", project.Image),
				slog.String("error", createErr.Error()))
		} else {
			if startErr := s.containerRuntime.Start(r.Context(), containerName); startErr != nil {
				slog.Warn("auto_provision_start_failed",
					slog.String("project", project.Name),
					slog.String("container", containerName),
					slog.String("error", startErr.Error()))
			}
			project.Container = containerName
			_ = s.hubProjects.Save(project) // Re-save with container name.
		}
	}

	s.notifyTaskChanged()
	writeJSON(w, http.StatusCreated, projectDetailResponse{Project: project})
}

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
		s.handleProjectDelete(w, r, name)
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
		project.Keywords = *req.Keywords
	}
	if req.Container != nil {
		project.Container = *req.Container
	}
	if req.DefaultMCPs != nil {
		project.DefaultMCPs = *req.DefaultMCPs
	}

	if err := s.hubProjects.Save(project); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to update project")
		return
	}

	s.notifyTaskChanged()
	writeJSON(w, http.StatusOK, projectDetailResponse{Project: project})
}

// handleProjectDelete serves DELETE /api/projects/{name}.
func (s *Server) handleProjectDelete(w http.ResponseWriter, r *http.Request, name string) {
	if s.hubProjects == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "hub not initialized")
		return
	}

	// Optionally remove the associated container.
	if r.URL.Query().Get("removeContainer") == "true" && s.containerRuntime != nil {
		if project, err := s.hubProjects.Get(name); err == nil && project.Container != "" {
			if rmErr := s.containerRuntime.Remove(r.Context(), project.Container, true); rmErr != nil {
				slog.Warn("container_remove_on_project_delete",
					slog.String("project", name),
					slog.String("container", project.Container),
					slog.String("error", rmErr.Error()))
			}
		}
	}

	if err := s.hubProjects.Delete(name); err != nil {
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "project not found")
		return
	}

	s.notifyTaskChanged()
	w.WriteHeader(http.StatusNoContent)
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

type projectDetailResponse struct {
	Project *hub.Project `json:"project"`
}

type createProjectRequest struct {
	Repo        string            `json:"repo"`
	Name        string            `json:"name,omitempty"`
	Path        string            `json:"path,omitempty"`
	Keywords    []string          `json:"keywords,omitempty"`
	Container   string            `json:"container,omitempty"`
	DefaultMCPs []string          `json:"defaultMcps,omitempty"`
	Image       string            `json:"image,omitempty"`
	CPULimit    float64           `json:"cpuLimit,omitempty"`
	MemoryLimit int64             `json:"memoryLimit,omitempty"`
	Volumes     []hub.VolumeMount `json:"volumes,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
}

type updateProjectRequest struct {
	Path        *string   `json:"path,omitempty"`
	Keywords    *[]string `json:"keywords,omitempty"`
	Container   *string   `json:"container,omitempty"`
	DefaultMCPs *[]string `json:"defaultMcps,omitempty"`
}

type routeRequest struct {
	Message string `json:"message"`
}

type routeResponse struct {
	Project         string   `json:"project"`
	Confidence      float64  `json:"confidence"`
	MatchedKeywords []string `json:"matchedKeywords"`
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
	AgentStatus *string `json:"agentStatus,omitempty"`
	Branch      *string `json:"branch,omitempty"`
	AskQuestion *string `json:"askQuestion,omitempty"`
}

type taskInputRequest struct {
	Input string `json:"input"`
}

type taskInputResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

func isValidPhase(p string) bool {
	switch hub.Phase(p) {
	case hub.PhaseBrainstorm, hub.PhasePlan, hub.PhaseExecute, hub.PhaseReview:
		return true
	}
	return false
}

func isValidStatus(s string) bool {
	switch hub.TaskStatus(s) {
	case hub.TaskStatusBacklog, hub.TaskStatusPlanning, hub.TaskStatusRunning,
		hub.TaskStatusReview, hub.TaskStatusDone:
		return true
	}
	return false
}

func isValidAgentStatus(s string) bool {
	switch hub.AgentStatus(s) {
	case hub.AgentStatusThinking, hub.AgentStatusWaiting, hub.AgentStatusRunning,
		hub.AgentStatusIdle, hub.AgentStatusError, hub.AgentStatusComplete:
		return true
	}
	return false
}

// --- Hub-Session Bridge handlers ---

type startPhaseRequest struct {
	Phase string `json:"phase"`
}

type startPhaseResponse struct {
	SessionID string `json:"sessionId"`
	Phase     string `json:"phase"`
}

type transitionRequest struct {
	NextPhase string `json:"nextPhase"`
	Summary   string `json:"summary,omitempty"`
}

// handleTaskStartPhase serves POST /api/tasks/{id}/start-phase.
func (s *Server) handleTaskStartPhase(w http.ResponseWriter, r *http.Request, taskID string) {
	if s.hubBridge == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "hub bridge not initialized")
		return
	}

	var req startPhaseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body")
		return
	}

	phase := hub.Phase(req.Phase)
	if req.Phase == "" {
		// Default to task's current phase
		task, err := s.hubTasks.Get(taskID)
		if err != nil {
			writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "task not found")
			return
		}
		phase = task.Phase
	} else if !isValidPhase(req.Phase) {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid phase value")
		return
	}

	result, err := s.hubBridge.StartPhase(taskID, phase)
	if err != nil {
		slog.Error("start_phase_failed", slog.String("task", taskID), slog.String("error", err.Error()))
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to start phase")
		return
	}

	s.notifyTaskChanged()
	s.notifyMenuChanged()
	writeJSON(w, http.StatusOK, startPhaseResponse{
		SessionID: result.SessionID,
		Phase:     result.Phase,
	})
}

// handleTaskTransition serves POST /api/tasks/{id}/transition.
func (s *Server) handleTaskTransition(w http.ResponseWriter, r *http.Request, taskID string) {
	if s.hubBridge == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "hub bridge not initialized")
		return
	}

	var req transitionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body")
		return
	}

	if req.NextPhase == "" || !isValidPhase(req.NextPhase) {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid or missing nextPhase")
		return
	}

	result, err := s.hubBridge.TransitionPhase(taskID, hub.Phase(req.NextPhase), req.Summary)
	if err != nil {
		slog.Error("transition_phase_failed", slog.String("task", taskID), slog.String("error", err.Error()))
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to transition phase")
		return
	}

	s.notifyTaskChanged()
	s.notifyMenuChanged()
	writeJSON(w, http.StatusOK, startPhaseResponse{
		SessionID: result.SessionID,
		Phase:     result.Phase,
	})
}

// --- Workspace API endpoints ---

// workspaceView is the API representation of a project enriched with container status.
type workspaceView struct {
	Name            string  `json:"name"`
	Repo            string  `json:"repo,omitempty"`
	Path            string  `json:"path"`
	Image           string  `json:"image,omitempty"`
	Container       string  `json:"container,omitempty"`
	ContainerStatus string  `json:"containerStatus"`
	CPUPercent      float64 `json:"cpuPercent,omitempty"`
	MemUsage        int64   `json:"memUsage,omitempty"`
	MemLimit        int64   `json:"memLimit,omitempty"`
	ActiveTasks     int     `json:"activeTasks"`
}

type workspacesListResponse struct {
	Workspaces []workspaceView `json:"workspaces"`
}

// handleWorkspaces dispatches GET /api/workspaces.
func (s *Server) handleWorkspaces(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.handleWorkspacesList(w, r)
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
	}
}

// handleWorkspacesList serves GET /api/workspaces — lists all projects enriched with container status.
func (s *Server) handleWorkspacesList(w http.ResponseWriter, r *http.Request) {
	if s.hubProjects == nil {
		writeJSON(w, http.StatusOK, workspacesListResponse{Workspaces: []workspaceView{}})
		return
	}

	projects, err := s.hubProjects.List()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to load projects")
		return
	}

	// Count active tasks per project.
	taskCounts := make(map[string]int)
	if s.hubTasks != nil {
		if tasks, taskErr := s.hubTasks.List(); taskErr == nil {
			for _, t := range tasks {
				if t.Status == hub.TaskStatusRunning || t.Status == hub.TaskStatusPlanning {
					taskCounts[t.Project]++
				}
			}
		}
	}

	views := make([]workspaceView, 0, len(projects))
	for _, p := range projects {
		v := workspaceView{
			Name:            p.Name,
			Repo:            p.Repo,
			Path:            p.Path,
			Image:           p.Image,
			Container:       p.Container,
			ContainerStatus: workspace.StatusNotCreated,
			ActiveTasks:     taskCounts[p.Name],
		}

		// Query container status if a container name is set and runtime is available.
		if p.Container != "" && s.containerRuntime != nil {
			if state, stErr := s.containerRuntime.Status(r.Context(), p.Container); stErr == nil {
				v.ContainerStatus = state.Status
			}
		}

		views = append(views, v)
	}

	writeJSON(w, http.StatusOK, workspacesListResponse{Workspaces: views})
}

// handleWorkspaceByName dispatches /api/workspaces/{name}/{action}.
func (s *Server) handleWorkspaceByName(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}

	const prefix = "/api/workspaces/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "route not found")
		return
	}

	remaining := strings.TrimPrefix(r.URL.Path, prefix)
	parts := strings.SplitN(remaining, "/", 2)
	name := parts[0]
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}

	if name == "" {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "workspace name is required")
		return
	}

	switch action {
	case "start":
		if r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
			return
		}
		s.handleWorkspaceStart(w, r, name)
	case "stop":
		if r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
			return
		}
		s.handleWorkspaceStop(w, r, name)
	case "remove":
		if r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
			return
		}
		s.handleWorkspaceRemove(w, r, name)
	case "stats":
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
			return
		}
		s.handleWorkspaceStats(w, r, name)
	default:
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "route not found")
	}
}

// handleWorkspaceStart serves POST /api/workspaces/{name}/start.
func (s *Server) handleWorkspaceStart(w http.ResponseWriter, r *http.Request, name string) {
	if s.containerRuntime == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "container runtime not available")
		return
	}
	if s.hubProjects == nil {
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "project not found")
		return
	}

	project, err := s.hubProjects.Get(name)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "project not found")
		return
	}

	ctx := r.Context()
	containerName := project.Container

	// If no container exists yet, create one from the project config.
	if containerName == "" {
		if project.Image == "" {
			writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "project has no image configured")
			return
		}
		containerName = workspace.ContainerNameForProject(project.Name)

		var envSlice []string
		for k, v := range project.Env {
			envSlice = append(envSlice, k+"="+v)
		}
		var mounts []workspace.Mount
		for _, v := range project.Volumes {
			mounts = append(mounts, workspace.Mount{
				Source:   v.Host,
				Target:   v.Container,
				ReadOnly: v.ReadOnly,
			})
		}

		opts := workspace.CreateOpts{
			Name:     containerName,
			Image:    project.Image,
			Env:      envSlice,
			Mounts:   mounts,
			NanoCPUs: int64(project.CPULimit * 1e9),
			Memory:   project.MemoryLimit,
			Labels: map[string]string{
				"agentdeck.project": project.Name,
			},
		}

		if _, createErr := s.containerRuntime.Create(ctx, opts); createErr != nil {
			writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to create container: "+createErr.Error())
			return
		}
		project.Container = containerName
		_ = s.hubProjects.Save(project)
	}

	if startErr := s.containerRuntime.Start(ctx, containerName); startErr != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to start container: "+startErr.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "started", "container": containerName})
}

// handleWorkspaceStop serves POST /api/workspaces/{name}/stop.
func (s *Server) handleWorkspaceStop(w http.ResponseWriter, r *http.Request, name string) {
	if s.containerRuntime == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "container runtime not available")
		return
	}
	if s.hubProjects == nil {
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "project not found")
		return
	}

	project, err := s.hubProjects.Get(name)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "project not found")
		return
	}

	if project.Container == "" {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "project has no container")
		return
	}

	if stopErr := s.containerRuntime.Stop(r.Context(), project.Container, 30); stopErr != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to stop container: "+stopErr.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped", "container": project.Container})
}

// handleWorkspaceRemove serves POST /api/workspaces/{name}/remove.
func (s *Server) handleWorkspaceRemove(w http.ResponseWriter, r *http.Request, name string) {
	if s.containerRuntime == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "container runtime not available")
		return
	}
	if s.hubProjects == nil {
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "project not found")
		return
	}

	project, err := s.hubProjects.Get(name)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "project not found")
		return
	}

	if project.Container == "" {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "project has no container")
		return
	}

	if rmErr := s.containerRuntime.Remove(r.Context(), project.Container, true); rmErr != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to remove container: "+rmErr.Error())
		return
	}

	// Clear the container reference from the project.
	project.Container = ""
	_ = s.hubProjects.Save(project)

	writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

type workspaceStatsResponse struct {
	CPUPercent float64 `json:"cpuPercent"`
	MemUsage   uint64  `json:"memUsage"`
	MemLimit   uint64  `json:"memLimit"`
}

// handleWorkspaceStats serves GET /api/workspaces/{name}/stats.
func (s *Server) handleWorkspaceStats(w http.ResponseWriter, r *http.Request, name string) {
	if s.containerRuntime == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "container runtime not available")
		return
	}
	if s.hubProjects == nil {
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "project not found")
		return
	}

	project, err := s.hubProjects.Get(name)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "project not found")
		return
	}

	if project.Container == "" {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "project has no container")
		return
	}

	stats, statsErr := s.containerRuntime.Stats(r.Context(), project.Container)
	if statsErr != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to get stats: "+statsErr.Error())
		return
	}

	writeJSON(w, http.StatusOK, workspaceStatsResponse{
		CPUPercent: stats.CPUPercent,
		MemUsage:   stats.MemUsage,
		MemLimit:   stats.MemLimit,
	})
}

// handleRoute serves POST /api/route.
func (s *Server) handleRoute(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}

	var req routeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body")
		return
	}

	if req.Message == "" {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "message is required")
		return
	}

	if s.hubProjects == nil {
		writeJSON(w, http.StatusOK, routeResponse{})
		return
	}

	projects, err := s.hubProjects.List()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to load projects")
		return
	}

	result := hub.Route(req.Message, projects)
	if result == nil {
		writeJSON(w, http.StatusOK, routeResponse{})
		return
	}

	writeJSON(w, http.StatusOK, routeResponse{
		Project:         result.Project,
		Confidence:      result.Confidence,
		MatchedKeywords: result.MatchedKeywords,
	})
}
