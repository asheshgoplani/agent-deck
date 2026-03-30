package web

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

type quickCreateRequest struct {
	GroupPath string `json:"groupPath"`
}

type quickCreateResponse struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type stopResponse struct {
	OK bool `json:"ok"`
}

// handleQuickCreate creates a new Claude Code session under a group with
// auto-generated name, similar to the TUI "N" (quick-create) shortcut.
func (s *Server) handleQuickCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	if s.cfg.ReadOnly {
		writeAPIError(w, http.StatusForbidden, "READ_ONLY", "session creation is disabled in read-only mode")
		return
	}

	var req quickCreateRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	groupPath := req.GroupPath
	if groupPath == "" {
		groupPath = session.DefaultGroupPath
	}

	// Open storage and load existing data.
	storage, err := session.NewStorageWithProfile(s.cfg.Profile)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "STORAGE_ERROR", "failed to open storage")
		return
	}
	defer storage.Close()

	instances, groupsData, err := storage.LoadWithGroups()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "LOAD_ERROR", "failed to load sessions")
		return
	}

	// Determine defaults from existing sessions in the group.
	tool := "claude"
	projectPath := ""
	if dt := session.GetDefaultTool(); dt != "" {
		tool = dt
	}
	for _, inst := range instances {
		if inst.GroupPath == groupPath && inst.ProjectPath != "" {
			projectPath = inst.ProjectPath
		}
	}
	if projectPath == "" {
		projectPath = "/tmp"
	}

	// Generate unique name and create the instance.
	title := session.GenerateUniqueSessionName(instances, groupPath)
	inst := session.NewInstanceWithGroupAndTool(title, projectPath, groupPath, tool)
	inst.Command = tool // e.g. "claude" — required for buildClaudeCommand to work

	// Start the tmux session.
	if err := inst.Start(); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "START_ERROR", "failed to start session: "+err.Error())
		return
	}

	// Persist.
	instances = append(instances, inst)
	groupTree := session.NewGroupTreeWithGroups(instances, groupsData)
	if err := storage.SaveWithGroups(instances, groupTree); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "SAVE_ERROR", "failed to save session")
		return
	}

	s.notifyMenuChanged()
	writeJSON(w, http.StatusCreated, quickCreateResponse{
		ID:    inst.ID,
		Title: inst.Title,
	})
}

// handleSessionStart starts (or restarts) a session, resuming any prior
// Claude conversation when a ClaudeSessionID is already recorded.
// This mirrors the TUI Enter-key behaviour on error/stopped sessions.
func (s *Server) handleSessionStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	if s.cfg.ReadOnly {
		writeAPIError(w, http.StatusForbidden, "READ_ONLY", "session management is disabled in read-only mode")
		return
	}

	const prefix = "/api/session/"
	const suffix = "/start"
	path := r.URL.Path
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid path")
		return
	}
	sessionID := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	if sessionID == "" {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "session id is required")
		return
	}

	storage, err := session.NewStorageWithProfile(s.cfg.Profile)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "STORAGE_ERROR", "failed to open storage")
		return
	}
	defer storage.Close()

	instances, groupsData, err := storage.LoadWithGroups()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "LOAD_ERROR", "failed to load sessions")
		return
	}

	var target *session.Instance
	for _, inst := range instances {
		if inst.ID == sessionID {
			target = inst
			break
		}
	}
	if target == nil {
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "session not found")
		return
	}

	if err := target.Restart(); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "START_ERROR", "failed to start session: "+err.Error())
		return
	}

	// Dedup and save updated state (new tmux session name, etc.)
	session.UpdateClaudeSessionsWithDedup(instances)
	groupTree := session.NewGroupTreeWithGroups(instances, groupsData)
	if err := storage.SaveWithGroups(instances, groupTree); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "SAVE_ERROR", "failed to save session state")
		return
	}

	s.notifyMenuChanged()
	writeJSON(w, http.StatusOK, stopResponse{OK: true})
}

// handleSessionStop stops a running session by killing its tmux session.
func (s *Server) handleSessionStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	if s.cfg.ReadOnly {
		writeAPIError(w, http.StatusForbidden, "READ_ONLY", "session management is disabled in read-only mode")
		return
	}

	// Extract session ID from /api/session/{id}/stop
	const prefix = "/api/session/"
	const suffix = "/stop"
	path := r.URL.Path
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid path")
		return
	}
	sessionID := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	if sessionID == "" {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "session id is required")
		return
	}

	storage, err := session.NewStorageWithProfile(s.cfg.Profile)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "STORAGE_ERROR", "failed to open storage")
		return
	}
	defer storage.Close()

	instances, groupsData, err := storage.LoadWithGroups()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "LOAD_ERROR", "failed to load sessions")
		return
	}

	var target *session.Instance
	for _, inst := range instances {
		if inst.ID == sessionID {
			target = inst
			break
		}
	}
	if target == nil {
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "session not found")
		return
	}

	// Kill tmux session (ignore errors — it may already be stopped).
	_ = target.Kill()

	// Remove from instances list.
	filtered := make([]*session.Instance, 0, len(instances)-1)
	for _, inst := range instances {
		if inst.ID != sessionID {
			filtered = append(filtered, inst)
		}
	}

	groupTree := session.NewGroupTreeWithGroups(filtered, groupsData)
	if err := storage.SaveWithGroups(filtered, groupTree); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "SAVE_ERROR", "failed to save session state")
		return
	}

	s.notifyMenuChanged()
	writeJSON(w, http.StatusOK, stopResponse{OK: true})
}
