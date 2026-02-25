# Hub-Session Integration Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Connect the hub dashboard to real session instances so tasks create live agent sessions with phase-specific prompts, terminal streaming, and real-time status via SSE correlation.

**Architecture:** A `HubSessionBridge` service in the web package orchestrates between `hub.TaskStore` and `session.Storage`. It creates real `session.Instance` objects when task phases start, linking them via `Task.Sessions[].ClaudeSessionID`. The frontend correlates SSE `menu` + `tasks` events to show live session status and stream terminals.

**Tech Stack:** Go 1.24, vanilla JS (dashboard.js), existing SSE/WebSocket infrastructure, `session.Instance.StartWithMessage()` for initial prompts.

---

### Task 1: HubSessionBridge — Core Struct and PhasePrompt Config

**Files:**
- Create: `internal/web/hub_session_bridge.go`
- Test: `internal/web/hub_session_bridge_test.go`

**Step 1: Write the failing test**

In `internal/web/hub_session_bridge_test.go`:

```go
package web

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/hub"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPhasePrompt(t *testing.T) {
	prompt := phasePrompt(hub.PhaseBrainstorm, "Fix auth bug in API service")
	require.NotEmpty(t, prompt)
	assert.Contains(t, prompt, "Fix auth bug in API service")
}

func TestPhasePromptAllPhases(t *testing.T) {
	phases := []hub.Phase{hub.PhaseBrainstorm, hub.PhasePlan, hub.PhaseExecute, hub.PhaseReview}
	for _, phase := range phases {
		prompt := phasePrompt(phase, "test task")
		assert.NotEmpty(t, prompt, "phase %s should have a prompt", phase)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test -race -v ./internal/web -run TestPhasePrompt`
Expected: FAIL — `phasePrompt` undefined

**Step 3: Write minimal implementation**

In `internal/web/hub_session_bridge.go`:

```go
package web

import (
	"fmt"

	"github.com/asheshgoplani/agent-deck/internal/hub"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// HubSessionBridge orchestrates the lifecycle between hub tasks and real session instances.
// It creates session.Instance objects when task phases start and links them to hub tasks.
type HubSessionBridge struct {
	tasks       *hub.TaskStore
	projects    *hub.ProjectStore
	openStorage storageOpener
	profile     string
}

// NewHubSessionBridge creates a bridge for the given profile.
func NewHubSessionBridge(profile string, tasks *hub.TaskStore, projects *hub.ProjectStore) *HubSessionBridge {
	return &HubSessionBridge{
		tasks:       tasks,
		projects:    projects,
		openStorage: defaultStorageOpener,
		profile:     session.GetEffectiveProfile(profile),
	}
}

// phasePrompt returns the initial prompt to send to a new session for the given phase.
func phasePrompt(phase hub.Phase, description string) string {
	switch phase {
	case hub.PhaseBrainstorm:
		return fmt.Sprintf("/brainstorm %s", description)
	case hub.PhasePlan:
		return fmt.Sprintf("Create an implementation plan for: %s", description)
	case hub.PhaseExecute:
		return description
	case hub.PhaseReview:
		return fmt.Sprintf("Review the implementation of: %s", description)
	default:
		return description
	}
}
```

**Step 4: Run test to verify it passes**

Run: `go test -race -v ./internal/web -run TestPhasePrompt`
Expected: PASS

**Step 5: Commit**

```
feat: add HubSessionBridge struct and phase prompt config
```

---

### Task 2: HubSessionBridge — StartPhase Method

**Files:**
- Modify: `internal/web/hub_session_bridge.go`
- Modify: `internal/web/hub_session_bridge_test.go`

**Step 1: Write the failing test**

Append to `hub_session_bridge_test.go`:

```go
func newTestBridge(t *testing.T) (*HubSessionBridge, *hub.TaskStore, *hub.ProjectStore) {
	t.Helper()
	hubDir := t.TempDir()
	ts, err := hub.NewTaskStore(hubDir)
	require.NoError(t, err)
	ps, err := hub.NewProjectStore(hubDir)
	require.NoError(t, err)

	bridge := NewHubSessionBridge("_test", ts, ps)
	// Override storage opener to return a mock that records calls
	bridge.openStorage = func(profile string) (storageLoader, error) {
		return &mockStorageLoader{}, nil
	}
	return bridge, ts, ps
}

type mockStorageLoader struct {
	instances []*session.Instance
	groups    []*session.GroupData
	saved     []*session.Instance
}

func (m *mockStorageLoader) LoadWithGroups() ([]*session.Instance, []*session.GroupData, error) {
	return m.instances, m.groups, nil
}

func (m *mockStorageLoader) Close() error { return nil }

func TestStartPhase_CreatesSessionEntry(t *testing.T) {
	bridge, ts, ps := newTestBridge(t)

	// Create a project with a path
	proj := &hub.Project{Name: "api-service", Path: "/tmp/test-project", Keywords: []string{"api"}}
	require.NoError(t, ps.Save(proj))

	// Create a task
	task := &hub.Task{
		Project:     "api-service",
		Description: "Fix auth bug",
		Phase:       hub.PhaseBrainstorm,
		Status:      hub.TaskStatusBacklog,
		AgentStatus: hub.AgentStatusIdle,
	}
	require.NoError(t, ts.Save(task))

	// StartPhase should create a session entry in the task (without actually starting tmux)
	result, err := bridge.StartPhase(task.ID, hub.PhaseBrainstorm)
	require.NoError(t, err)
	assert.NotEmpty(t, result.SessionID)
	assert.Equal(t, string(hub.PhaseBrainstorm), result.Phase)

	// Verify task was updated with session entry
	updated, err := ts.Get(task.ID)
	require.NoError(t, err)
	assert.Len(t, updated.Sessions, 1)
	assert.Equal(t, hub.PhaseBrainstorm, updated.Sessions[0].Phase)
	assert.Equal(t, "active", updated.Sessions[0].Status)
	assert.NotEmpty(t, updated.Sessions[0].ClaudeSessionID)
	assert.Equal(t, hub.TaskStatusRunning, updated.Status)
}
```

**Step 2: Run test to verify it fails**

Run: `go test -race -v ./internal/web -run TestStartPhase`
Expected: FAIL — `StartPhase` not defined

**Step 3: Write minimal implementation**

Add to `hub_session_bridge.go`:

```go
// StartPhaseResult contains the result of starting a phase.
type StartPhaseResult struct {
	SessionID string `json:"sessionId"`
	Phase     string `json:"phase"`
}

// StartPhase creates a new session.Instance for the given task phase.
// It links the session to the task's Sessions slice and updates task status.
// The session is created but NOT started (no tmux) — the caller decides when to start.
func (b *HubSessionBridge) StartPhase(taskID string, phase hub.Phase) (*StartPhaseResult, error) {
	if b.tasks == nil {
		return nil, fmt.Errorf("task store not initialized")
	}

	task, err := b.tasks.Get(taskID)
	if err != nil {
		return nil, fmt.Errorf("task %s not found: %w", taskID, err)
	}

	// Resolve project path
	projectPath := ""
	if b.projects != nil {
		if proj, err := b.projects.Get(task.Project); err == nil {
			projectPath = proj.Path
		}
	}
	if projectPath == "" {
		projectPath = "/tmp"
	}

	// Create session instance
	title := fmt.Sprintf("[%s] %s: %s", task.ID, phaseLabel(phase), truncate(task.Description, 40))
	inst := session.NewInstanceWithGroupAndTool(title, projectPath, "hub", "claude")

	// Add session entry to task
	hubSession := hub.Session{
		ID:              fmt.Sprintf("%s-%s", task.ID, string(phase)),
		Phase:           phase,
		Status:          "active",
		ClaudeSessionID: inst.ID,
	}
	task.Sessions = append(task.Sessions, hubSession)
	task.Phase = phase
	task.Status = hub.TaskStatusRunning
	task.AgentStatus = hub.AgentStatusThinking

	if err := b.tasks.Save(task); err != nil {
		return nil, fmt.Errorf("update task: %w", err)
	}

	// Save instance to session storage
	if err := b.saveInstance(inst); err != nil {
		return nil, fmt.Errorf("save session instance: %w", err)
	}

	return &StartPhaseResult{
		SessionID: inst.ID,
		Phase:     string(phase),
	}, nil
}

func (b *HubSessionBridge) saveInstance(inst *session.Instance) error {
	storage, err := b.openStorage(b.profile)
	if err != nil {
		return fmt.Errorf("open storage: %w", err)
	}
	defer storage.Close()

	existing, _, err := storage.LoadWithGroups()
	if err != nil {
		return fmt.Errorf("load existing sessions: %w", err)
	}

	instances := append(existing, inst)
	return storage.(interface {
		Save([]*session.Instance) error
	}).Save(instances)
}

func phaseLabel(p hub.Phase) string {
	switch p {
	case hub.PhaseBrainstorm:
		return "Brainstorm"
	case hub.PhasePlan:
		return "Plan"
	case hub.PhaseExecute:
		return "Execute"
	case hub.PhaseReview:
		return "Review"
	default:
		return string(p)
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}
```

Note: The `saveInstance` method needs the storage to implement `Save` — check if `storageLoader` needs extending. The test mock sidesteps this. We'll handle the real storage saving in a later step.

**Step 4: Run test to verify it passes**

Run: `go test -race -v ./internal/web -run TestStartPhase`
Expected: PASS

**Step 5: Commit**

```
feat: add HubSessionBridge.StartPhase — creates session entries for task phases
```

---

### Task 3: HubSessionBridge — TransitionPhase Method

**Files:**
- Modify: `internal/web/hub_session_bridge.go`
- Modify: `internal/web/hub_session_bridge_test.go`

**Step 1: Write the failing test**

```go
func TestTransitionPhase(t *testing.T) {
	bridge, ts, ps := newTestBridge(t)

	proj := &hub.Project{Name: "api-service", Path: "/tmp/test-project", Keywords: []string{"api"}}
	require.NoError(t, ps.Save(proj))

	task := &hub.Task{
		Project:     "api-service",
		Description: "Fix auth bug",
		Phase:       hub.PhaseBrainstorm,
		Status:      hub.TaskStatusRunning,
		AgentStatus: hub.AgentStatusThinking,
		Sessions: []hub.Session{
			{ID: "t-001-brainstorm", Phase: hub.PhaseBrainstorm, Status: "active", ClaudeSessionID: "sess-123"},
		},
	}
	require.NoError(t, ts.Save(task))

	result, err := bridge.TransitionPhase(task.ID, hub.PhasePlan, "Brainstorm complete")
	require.NoError(t, err)
	assert.NotEmpty(t, result.SessionID)
	assert.Equal(t, "plan", result.Phase)

	updated, err := ts.Get(task.ID)
	require.NoError(t, err)
	assert.Len(t, updated.Sessions, 2)
	assert.Equal(t, "complete", updated.Sessions[0].Status)
	assert.Equal(t, "Brainstorm complete", updated.Sessions[0].Summary)
	assert.Equal(t, "active", updated.Sessions[1].Status)
	assert.Equal(t, hub.PhasePlan, updated.Phase)
}
```

**Step 2: Run test to verify it fails**

Run: `go test -race -v ./internal/web -run TestTransitionPhase`
Expected: FAIL — `TransitionPhase` undefined

**Step 3: Write minimal implementation**

Add to `hub_session_bridge.go`:

```go
// TransitionPhase completes the current phase and starts the next one.
// It marks the current active session as complete with the given summary,
// then creates a new session for the next phase.
func (b *HubSessionBridge) TransitionPhase(taskID string, nextPhase hub.Phase, summary string) (*StartPhaseResult, error) {
	if b.tasks == nil {
		return nil, fmt.Errorf("task store not initialized")
	}

	task, err := b.tasks.Get(taskID)
	if err != nil {
		return nil, fmt.Errorf("task %s not found: %w", taskID, err)
	}

	// Mark current active session as complete
	for i := range task.Sessions {
		if task.Sessions[i].Status == "active" {
			task.Sessions[i].Status = "complete"
			task.Sessions[i].Summary = summary
		}
	}

	if err := b.tasks.Save(task); err != nil {
		return nil, fmt.Errorf("save task after completing phase: %w", err)
	}

	// Start the next phase
	return b.StartPhase(taskID, nextPhase)
}
```

**Step 4: Run test to verify it passes**

Run: `go test -race -v ./internal/web -run TestTransitionPhase`
Expected: PASS

**Step 5: Commit**

```
feat: add HubSessionBridge.TransitionPhase — completes current phase, starts next
```

---

### Task 4: HubSessionBridge — GetActiveSession Method

**Files:**
- Modify: `internal/web/hub_session_bridge.go`
- Modify: `internal/web/hub_session_bridge_test.go`

**Step 1: Write the failing test**

```go
func TestGetActiveSession(t *testing.T) {
	bridge, ts, ps := newTestBridge(t)

	proj := &hub.Project{Name: "api-service", Path: "/tmp/test-project", Keywords: []string{"api"}}
	require.NoError(t, ps.Save(proj))

	task := &hub.Task{
		Project:     "api-service",
		Description: "Fix auth bug",
		Phase:       hub.PhaseBrainstorm,
		Status:      hub.TaskStatusRunning,
		Sessions: []hub.Session{
			{ID: "t-001-brainstorm", Phase: hub.PhaseBrainstorm, Status: "active", ClaudeSessionID: "sess-abc"},
		},
	}
	require.NoError(t, ts.Save(task))

	sessionID, err := bridge.GetActiveSessionID(task.ID)
	require.NoError(t, err)
	assert.Equal(t, "sess-abc", sessionID)
}

func TestGetActiveSession_NoActive(t *testing.T) {
	bridge, ts, _ := newTestBridge(t)

	task := &hub.Task{
		Project:     "api-service",
		Description: "Done task",
		Phase:       hub.PhaseBrainstorm,
		Status:      hub.TaskStatusDone,
		Sessions: []hub.Session{
			{ID: "t-001-brainstorm", Phase: hub.PhaseBrainstorm, Status: "complete", ClaudeSessionID: "sess-abc"},
		},
	}
	require.NoError(t, ts.Save(task))

	_, err := bridge.GetActiveSessionID(task.ID)
	assert.Error(t, err)
}
```

**Step 2: Run test to verify it fails**

Run: `go test -race -v ./internal/web -run TestGetActiveSession`
Expected: FAIL — `GetActiveSessionID` undefined

**Step 3: Write minimal implementation**

```go
// GetActiveSessionID returns the session.Instance ID for the task's currently active phase.
func (b *HubSessionBridge) GetActiveSessionID(taskID string) (string, error) {
	if b.tasks == nil {
		return "", fmt.Errorf("task store not initialized")
	}

	task, err := b.tasks.Get(taskID)
	if err != nil {
		return "", fmt.Errorf("task %s not found: %w", taskID, err)
	}

	for _, s := range task.Sessions {
		if s.Status == "active" {
			return s.ClaudeSessionID, nil
		}
	}

	return "", fmt.Errorf("no active session for task %s", taskID)
}
```

**Step 4: Run test to verify it passes**

Run: `go test -race -v ./internal/web -run TestGetActiveSession`
Expected: PASS

**Step 5: Commit**

```
feat: add HubSessionBridge.GetActiveSessionID — resolves active session for a task
```

---

### Task 5: Wire Bridge into Server + Register New Routes

**Files:**
- Modify: `internal/web/server.go` (lines 38-58, 80-98, 130-140)
- Modify: `internal/web/handlers_hub.go` (add new handler methods, update dispatch)

**Step 1: Write the failing test**

In `handlers_hub_test.go`:

```go
func TestStartPhaseEndpoint(t *testing.T) {
	srv := newTestServerWithHub(t)

	// Create project and task
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
```

**Step 2: Run test to verify it fails**

Run: `go test -race -v ./internal/web -run TestStartPhaseEndpoint`
Expected: FAIL — route not registered / handler not defined

**Step 3: Write implementation**

In `server.go`, add the bridge field and initialization:

```go
// In Server struct (after sessionLauncher field):
hubBridge *HubSessionBridge

// In NewServer(), after hub store initialization (around line 94):
if s.hubTasks != nil && s.hubProjects != nil {
	s.hubBridge = NewHubSessionBridge(cfg.Profile, s.hubTasks, s.hubProjects)
}
```

In `server.go`, the routes are already dispatched via `handleTaskByID` which parses subpaths. Add cases to the switch in `handleTaskByID` (around line 164):

```go
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
```

In `handlers_hub.go`, add handler methods and request/response types:

```go
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
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}

	s.notifyTaskChanged()
	s.notifyMenuChanged()
	writeJSON(w, http.StatusOK, startPhaseResponse{
		SessionID: result.SessionID,
		Phase:     result.Phase,
	})
}

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
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}

	s.notifyTaskChanged()
	s.notifyMenuChanged()
	writeJSON(w, http.StatusOK, startPhaseResponse{
		SessionID: result.SessionID,
		Phase:     result.Phase,
	})
}
```

**Step 4: Run tests to verify they pass**

Run: `go test -race -v ./internal/web -run "TestStartPhaseEndpoint|TestTransitionEndpoint"`
Expected: PASS

**Step 5: Commit**

```
feat: wire HubSessionBridge into server, add /start-phase and /transition endpoints
```

---

### Task 6: Update Task Creation to Auto-Start Phase Session

**Files:**
- Modify: `internal/web/handlers_hub.go` (handleTasksCreate, around lines 110-136)
- Modify: `internal/web/handlers_hub_test.go`

**Step 1: Write the failing test**

```go
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
```

**Step 2: Run test to verify it fails**

Run: `go test -race -v ./internal/web -run TestTaskCreate_AutoStartsPhaseSession`
Expected: FAIL — task has no sessions (bridge not called during creation)

**Step 3: Modify handleTasksCreate**

In `handlers_hub.go`, after the task is saved (around line 113) and before the container launch logic, add:

```go
// Auto-start phase session via bridge (local sessions).
// This takes precedence over container-based launch for projects with a local path.
if s.hubBridge != nil {
	if proj, projErr := s.hubProjects.Get(task.Project); projErr == nil && proj.Path != "" {
		if result, bridgeErr := s.hubBridge.StartPhase(task.ID, phase); bridgeErr == nil {
			// Re-read task to include session entry
			if updated, getErr := s.hubTasks.Get(task.ID); getErr == nil {
				task = updated
			}
			_ = result // Session ID available if needed
		} else {
			slog.Warn("bridge_start_phase_failed",
				slog.String("task", task.ID),
				slog.String("error", bridgeErr.Error()))
		}
	}
}
```

Keep the existing container-based launch as a fallback for container-configured projects.

**Step 4: Run test to verify it passes**

Run: `go test -race -v ./internal/web -run TestTaskCreate_AutoStartsPhaseSession`
Expected: PASS

**Step 5: Commit**

```
feat: auto-start phase session when creating hub tasks with a local project path
```

---

### Task 7: Frontend — SSE Session Map Correlation

**Files:**
- Modify: `internal/web/static/dashboard.js` (lines 4-18, 129-165, 241-367)

**Step 1: Add sessionMap to state**

In `dashboard.js`, add to the state object (line 17):

```js
sessionMap: {},  // menuSession.id → menuSession (from SSE menu events)
```

**Step 2: Build sessionMap from SSE menu events**

Update the `menu` event listener (line 129) to parse and store sessions:

```js
es.addEventListener("menu", function (e) {
  setConnectionState("connected")
  try {
    var data = JSON.parse(e.data)
    // Build session lookup map
    state.sessionMap = {}
    var items = data.items || []
    for (var i = 0; i < items.length; i++) {
      if (items[i].session) {
        state.sessionMap[items[i].session.id] = items[i].session
      }
    }
  } catch (err) {
    console.error("menu SSE parse error:", err)
  }
  fetchTasks()
})
```

**Step 3: Add helper to resolve live session for a task**

```js
function getActiveSessionForTask(task) {
  if (!task.sessions) return null
  for (var i = task.sessions.length - 1; i >= 0; i--) {
    if (task.sessions[i].status === "active" && task.sessions[i].claudeSessionId) {
      return state.sessionMap[task.sessions[i].claudeSessionId] || null
    }
  }
  return null
}
```

**Step 4: Update agent card to show live status**

In `createAgentCard` (around line 332), replace the static badge with live session status:

```js
// In the footer section, replace:
//   footer.appendChild(createAgentStatusBadge(task.agentStatus))
// With:
var liveSession = getActiveSessionForTask(task)
if (liveSession) {
  footer.appendChild(createAgentStatusBadge(mapSessionStatus(liveSession.status)))
} else {
  footer.appendChild(createAgentStatusBadge(task.agentStatus))
}
```

Add the mapping function:

```js
function mapSessionStatus(sessionStatus) {
  switch (sessionStatus) {
    case "running": return "running"
    case "waiting": return "waiting"
    case "idle":    return "idle"
    case "error":   return "error"
    case "starting": return "thinking"
    default:        return "idle"
  }
}
```

**Step 5: Commit**

```
feat: frontend SSE correlation — show live session status on hub agent cards
```

---

### Task 8: Frontend — Terminal Streaming via Real Session

**Files:**
- Modify: `internal/web/static/dashboard.js` (connectTerminal function, lines 580-636)

**Step 1: Update connectTerminal to use real session ID**

Replace the current `connectTerminal` function logic that uses `task.tmuxSession` to also check for the active real session:

```js
function connectTerminal(task) {
  disconnectTerminal()
  var container = document.getElementById("terminal-container")
  if (!container) return
  clearChildren(container)

  // Resolve the tmux session name:
  // 1. Try live session from session map (real session.Instance)
  // 2. Fall back to task.tmuxSession (container-based legacy)
  var tmuxName = null
  var liveSession = getActiveSessionForTask(task)
  if (liveSession && liveSession.tmuxSession) {
    tmuxName = liveSession.tmuxSession
  } else if (task.tmuxSession) {
    tmuxName = task.tmuxSession
  }

  if (!tmuxName) {
    var placeholder = el("div", "terminal-placeholder", "No session attached.")
    container.appendChild(placeholder)
    return
  }

  // ... rest of terminal setup stays the same, using tmuxName ...
}
```

Update the WebSocket URL line to use `tmuxName`:

```js
var wsUrl = protocol + "//" + window.location.host + "/ws/session/" + encodeURIComponent(tmuxName)
```

**Step 2: Update detail header to show live status**

In `renderDetailHeader` (around line 482), update the status badge:

```js
var liveSession = getActiveSessionForTask(task)
if (liveSession) {
  actions.appendChild(createAgentStatusBadge(mapSessionStatus(liveSession.status)))
} else {
  actions.appendChild(createAgentStatusBadge(task.agentStatus))
}
```

Similarly in `renderPreviewHeader` (around line 573):

```js
var liveSession = getActiveSessionForTask(task)
var effectiveStatus = liveSession ? mapSessionStatus(liveSession.status) : task.agentStatus
var agentMeta = AGENT_STATUS_META[effectiveStatus] || AGENT_STATUS_META.idle
```

**Step 3: Commit**

```
feat: frontend terminal streams from real session, live status in detail panel
```

---

### Task 9: Frontend — Phase Transition UI

**Files:**
- Modify: `internal/web/static/dashboard.js`
- Modify: `internal/web/static/dashboard.html`

**Step 1: Add transition button to session chain**

In `renderSessionChain` (around line 559), after the phase pips, add a "Next Phase" button when the task has an active phase that isn't `review`:

```js
// After the phase chain rendering, add transition button
var currentPhaseIdx = PHASES.indexOf(task.phase)
if (task.status !== "done" && currentPhaseIdx >= 0 && currentPhaseIdx < PHASES.length - 1) {
  var nextPhase = PHASES[currentPhaseIdx + 1]
  var transBtn = el("button", "phase-transition-btn", "→ " + phaseLabel(nextPhase))
  transBtn.dataset.taskId = task.id
  transBtn.dataset.nextPhase = nextPhase
  transBtn.addEventListener("click", handlePhaseTransition)
  container.appendChild(transBtn)
}

// Helper
function phaseLabel(phase) {
  return phase.charAt(0).toUpperCase() + phase.slice(1)
}
```

**Step 2: Add transition handler**

```js
function handlePhaseTransition(e) {
  var taskId = e.currentTarget.dataset.taskId
  var nextPhase = e.currentTarget.dataset.nextPhase
  if (!taskId || !nextPhase) return

  var headers = authHeaders()
  headers["Content-Type"] = "application/json"

  fetch(apiPathWithToken("/api/tasks/" + taskId + "/transition"), {
    method: "POST",
    headers: headers,
    body: JSON.stringify({ nextPhase: nextPhase }),
  })
    .then(function (r) {
      if (!r.ok) throw new Error("transition failed: " + r.status)
      return r.json()
    })
    .then(function (data) {
      fetchTasks()
    })
    .catch(function (err) {
      console.error("handlePhaseTransition:", err)
    })
}
```

**Step 3: Add CSS for transition button**

In `dashboard.css`, add:

```css
.phase-transition-btn {
  margin-left: 8px;
  padding: 2px 10px;
  border: 1px solid var(--accent);
  border-radius: 4px;
  background: transparent;
  color: var(--accent);
  font-size: 0.75rem;
  cursor: pointer;
  transition: background 0.15s;
}
.phase-transition-btn:hover {
  background: var(--accent);
  color: var(--bg);
}
```

**Step 4: Commit**

```
feat: frontend phase transition button in session chain
```

---

### Task 10: Storage Integration — Save Session Instances to SQLite

**Files:**
- Modify: `internal/web/hub_session_bridge.go` (saveInstance method)
- Modify: `internal/web/hub_session_bridge_test.go`

**Step 1: Write the failing test**

```go
func TestSaveInstance_PersistsToStorage(t *testing.T) {
	os.Setenv("AGENTDECK_PROFILE", "_test")
	defer os.Unsetenv("AGENTDECK_PROFILE")

	hubDir := t.TempDir()
	ts, err := hub.NewTaskStore(hubDir)
	require.NoError(t, err)
	ps, err := hub.NewProjectStore(hubDir)
	require.NoError(t, err)

	bridge := NewHubSessionBridge("_test", ts, ps)
	// Use real storage opener (with _test profile)

	proj := &hub.Project{Name: "test-proj", Path: t.TempDir(), Keywords: []string{"test"}}
	require.NoError(t, ps.Save(proj))

	task := &hub.Task{
		Project:     "test-proj",
		Description: "Integration test",
		Phase:       hub.PhaseExecute,
		Status:      hub.TaskStatusBacklog,
	}
	require.NoError(t, ts.Save(task))

	result, err := bridge.StartPhase(task.ID, hub.PhaseExecute)
	require.NoError(t, err)
	assert.NotEmpty(t, result.SessionID)

	// Verify session is in storage
	storage, err := session.NewStorageWithProfile("_test")
	require.NoError(t, err)
	defer storage.Close()

	instances, _, err := storage.LoadWithGroups()
	require.NoError(t, err)

	found := false
	for _, inst := range instances {
		if inst.ID == result.SessionID {
			found = true
			assert.Equal(t, "hub", inst.GroupPath)
			assert.Equal(t, "claude", inst.GetToolThreadSafe())
		}
	}
	assert.True(t, found, "session should be persisted in storage")
}
```

**Step 2: Run to verify failure, then fix**

The `saveInstance` method needs to use the real `session.Storage.Save` interface. Update `storageLoader` or use `session.Storage` directly:

```go
// In hub_session_bridge.go, update saveInstance:
func (b *HubSessionBridge) saveInstance(inst *session.Instance) error {
	storage, err := session.NewStorageWithProfile(b.profile)
	if err != nil {
		return fmt.Errorf("open storage: %w", err)
	}
	defer storage.Close()

	existing, _, err := storage.LoadWithGroups()
	if err != nil {
		return fmt.Errorf("load existing sessions: %w", err)
	}

	instances := append(existing, inst)
	return storage.Save(instances)
}
```

**Step 3: Run test to verify it passes**

Run: `AGENTDECK_PROFILE=_test go test -race -v ./internal/web -run TestSaveInstance_PersistsToStorage`
Expected: PASS

**Step 4: Commit**

```
feat: persist hub-created sessions to SQLite via session.Storage
```

---

### Task 11: Full Integration Test — End-to-End Flow

**Files:**
- Modify: `internal/web/handlers_hub_test.go`

**Step 1: Write integration test**

```go
func TestFullHubSessionFlow(t *testing.T) {
	srv := newTestServerWithHub(t)
	// Initialize bridge on test server
	srv.hubBridge = NewHubSessionBridge("_test", srv.hubTasks, srv.hubProjects)
	srv.hubBridge.openStorage = func(profile string) (storageLoader, error) {
		return &mockStorageLoader{}, nil
	}

	proj := &hub.Project{Name: "api-service", Path: "/tmp/test-project", Keywords: []string{"api"}}
	require.NoError(t, srv.hubProjects.Save(proj))

	// 1. Create task
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

	// 3. Verify task has 2 sessions
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
```

**Step 2: Run integration test**

Run: `go test -race -v ./internal/web -run TestFullHubSessionFlow`
Expected: PASS

**Step 3: Commit**

```
test: add end-to-end hub session flow integration test
```

---

### Task 12: Run Full Test Suite + Build Verification

**Step 1: Run all tests**

Run: `go test -race -v ./internal/web/...`
Expected: All tests pass

**Step 2: Run all project tests**

Run: `make test`
Expected: All tests pass

**Step 3: Build and verify**

Run: `make build`
Expected: Binary builds successfully

**Step 4: Commit any remaining fixes**

```
fix: address any test or build issues from hub-session integration
```

---

### Task 13: Manual Verification — Visual Check

**Step 1: Start the app**

Run: `./build/agent-deck web`

**Step 2: Open browser**

Navigate to `http://127.0.0.1:8420`

**Step 3: Verify:**
- Hub dashboard loads
- Creating a task creates a session entry
- SSE events show both menu and tasks data
- Agent status badges reflect real session status (if sessions are running)
- Phase transition button appears and works
- Terminal panel streams from real session

**Step 4: Final commit if visual fixes needed**

```
fix: visual adjustments for hub-session integration
```
