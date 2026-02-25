# Hub-Session Integration Design

**Date:** 2026-02-25
**Branch:** feature/hub-integration
**Status:** Approved

## Problem

The hub dashboard operates on its own `hub.Task` / `hub.Project` data model stored as JSON files, completely disconnected from the real `session.Instance` objects in SQLite. Real agent sessions (visible in the TUI and terminal view) are invisible to the hub. The hub needs to create and manage real sessions so that tasks have live terminal streaming, real status detection, and proper agent lifecycle management.

## Mental Model

```
Task (hub.Task — "Fix auth bug")
  └── Workflow Phase (hub.Session — "Brainstorming", "Planning", "Executing", "Review")
       └── Real Session (session.Instance — actual tmux pane running Claude Code)
```

Each hub task goes through phases. Each phase gets its own real `session.Instance` with a phase-specific prompt. The hub dashboard shows the task hierarchy and streams the active phase's terminal.

## Architecture: SessionBridge (Approach C)

A new `HubSessionBridge` in `internal/web/hub_session_bridge.go` orchestrates between hub tasks and real session instances. Hub stays focused on task/project storage. Session stays focused on tmux/instance management. The bridge handles lifecycle orchestration.

### SessionBridge Service

**File:** `internal/web/hub_session_bridge.go`

**Dependencies:**
- `*hub.TaskStore` — read/write hub tasks
- `session.Storage` opener — create/save real session instances
- Phase prompt configuration — maps `hub.Phase` → initial prompt/skill invocation

**Key methods:**
- `StartPhase(taskID string, phase hub.Phase) → (sessionInstanceID string, error)` — creates a real `session.Instance` for the given phase, links it to the task
- `GetActiveSession(taskID string) → (*session.Instance, error)` — resolves the real session for the task's current active phase
- `TransitionPhase(taskID string, nextPhase hub.Phase) → (sessionInstanceID string, error)` — completes current phase, starts next with context carry-forward

### Phase-Specific Session Creation

| Phase | Session Title | Tool | Initial Prompt |
|-------|--------------|------|----------------|
| `brainstorm` | `"[task-id] Brainstorm: <desc>"` | `claude` | `/brainstorm` skill invocation |
| `plan` | `"[task-id] Plan: <desc>"` | `claude` | Continuation with brainstorm output |
| `execute` | `"[task-id] Execute: <desc>"` | `claude` | Continuation with plan output |
| `review` | `"[task-id] Review: <desc>"` | `claude` | Continuation with execution output |

**Session grouping:** All hub-created sessions use `GroupPath: "hub"` for visual separation in the TUI.

**ProjectPath:** Set from `Project.Path` — session runs in the project's working directory.

**Continuation flow:** On transition, the bridge marks the current phase session as "complete" in `Task.Sessions`, captures summary/artifact, and creates the next phase session with prior context as initial prompt.

### SSE Correlation (Frontend)

No backend status sync needed. The frontend correlates two existing SSE event types:

1. `menu` events — contain real `MenuSession` objects with live status from tmux polling
2. `tasks` events — contain hub tasks with `Sessions[].ClaudeSessionID`

**Frontend logic:**
1. Build lookup: `sessionMap[menuSession.id] → menuSession`
2. For each task, find active phase's `claudeSessionId` in the map
3. Pull live status from matched session (running/waiting/idle/error)
4. Stream terminal via WebSocket `/ws/session/{realSessionId}`

### API Changes

**New endpoints:**

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `POST /api/tasks/{id}/start-phase` | POST | Start a phase — creates real session, returns session ID |
| `POST /api/tasks/{id}/transition` | POST | Transition to next phase — completes current, starts next |

**Modified existing:**
- `POST /api/tasks` — auto-creates first phase session if project has a valid path. Returns `sessionId` in response.
- Container-based `SessionLauncher` remains for container use cases. Local sessions use the bridge.

**Request/response types:**

```go
type startPhaseRequest struct {
    Phase hub.Phase `json:"phase"`
}

type startPhaseResponse struct {
    SessionID string `json:"sessionId"`
    Phase     string `json:"phase"`
}

type transitionRequest struct {
    NextPhase hub.Phase `json:"nextPhase"`
    Summary   string    `json:"summary,omitempty"`
}
```

### Frontend Updates (dashboard.js)

**State:**
- Add `state.sessionMap = {}` — built from SSE menu events

**Rendering:**
- Task list shows live status from real session instead of hub's `agentStatus`
- Terminal panel opens WebSocket to real session (replaces log-file preview)
- Phase transition button calls `/api/tasks/{id}/transition`
- Chat bar task creation auto-selects new session for streaming

**Deprecated:**
- Log-file-based `handleTaskPreview` SSE stream — replaced by WebSocket PTY via real session

### Testing

- `hub_session_bridge_test.go` — unit tests with mock storage and mock task store
- Updated `handlers_hub_test.go` — test `/start-phase` and `/transition` endpoints
- All tests use `AGENTDECK_PROFILE=_test`
- Bridge accepts `storageOpener` function for test injection (same pattern as `SessionDataService`)
- Frontend verified manually via `make build && ./build/agent-deck web`

## Files to Create/Modify

| File | Action | Purpose |
|------|--------|---------|
| `internal/web/hub_session_bridge.go` | Create | SessionBridge service |
| `internal/web/hub_session_bridge_test.go` | Create | Bridge unit tests |
| `internal/web/handlers_hub.go` | Modify | Add `/start-phase`, `/transition` endpoints; update task creation |
| `internal/web/handlers_hub_test.go` | Modify | Test new endpoints |
| `internal/web/server.go` | Modify | Initialize bridge, register new routes |
| `internal/web/handlers_events.go` | No change | SSE already broadcasts both streams |
| `internal/web/static/dashboard.js` | Modify | SSE correlation, terminal streaming, phase transitions |
| `internal/web/static/dashboard.html` | Modify | Phase transition UI elements |
| `internal/hub/models.go` | No change | Existing model sufficient |
