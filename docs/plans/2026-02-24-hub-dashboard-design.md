# Hub Dashboard Design

**Status:** Approved
**Date:** 2026-02-24
**Purpose:** Extend agent-deck's web UI with a mobile-first dashboard for multi-agent visibility, task management, and project routing.

---

## 1. Overview

### Problem

Agent-deck has a mature terminal-focused web UI (xterm.js, WebSocket PTY streaming, SSE status updates, push notifications). However, it lacks:

1. **Multi-project routing** — dispatching tasks to the right workspace by keywords
2. **Visual dashboard** — at-a-glance task cards with status badges (vs terminal-only)
3. **Container integration** — docker exec patterns for sandboxed agent sessions
4. **Conductor/orchestration** — activity logs, cross-project task management

### Solution

Add a **dashboard mode** to agent-deck's existing web server. Users see task cards with status; clicking opens the terminal. The dashboard shares backend infrastructure (SSE, PTY streaming, push) with the existing terminal view.

### Approach

**Dashboard-First** — add visual layer on existing backend, then layer in container support and routing.

---

## 2. Architecture

### Current State

```
agent-deck web (port 8420)
├── /                     → index.html (terminal-focused UI)
├── /api/menu             → session list JSON
├── /api/session/{id}     → session details
├── /events/menu          → SSE for status changes
├── /ws/session/{id}      → WebSocket PTY stream
└── /api/push/*           → web push endpoints
```

### Proposed Addition

```
agent-deck web (port 8420)
├── /                     → dashboard UI (new default)
├── /terminal             → terminal-focused UI (existing, renamed)
├── /api/menu             → session list (existing)
├── /api/tasks            → task cards with project/phase metadata (new)
├── /api/tasks/{id}       → task details (new)
├── /api/tasks/{id}/input → send input to task session (new)
├── /api/tasks/{id}/fork  → fork task (new)
├── /api/projects         → project registry (new)
├── /api/route            → route message to project (new)
├── /events/menu          → SSE for status changes (existing, extended)
├── /ws/session/{id}      → WebSocket PTY stream (existing)
└── ...
```

### Key Decisions

1. **Single binary** — dashboard is part of `agent-deck web`, not a separate service
2. **Shared SSE** — dashboard uses existing `/events/menu` for real-time updates
3. **Terminal as detail view** — clicking a task card loads the terminal in a panel
4. **Progressive enhancement** — dashboard works without JavaScript for status; terminal requires JS

---

## 3. Data Model

### Task

Tasks wrap sessions with orchestration metadata:

```go
type Task struct {
    // From existing session
    SessionID    string       `json:"sessionId"`
    TmuxSession  string       `json:"tmuxSession"`
    Status       AgentStatus  `json:"status"`      // thinking, waiting, running, idle, error, complete

    // New task-level fields
    ID           string       `json:"id"`          // e.g. "t-007"
    Project      string       `json:"project"`     // project name from registry
    Description  string       `json:"description"` // natural language task
    Phase        Phase        `json:"phase"`       // brainstorm, plan, execute, review
    Branch       string       `json:"branch,omitempty"`
    CreatedAt    time.Time    `json:"createdAt"`
    UpdatedAt    time.Time    `json:"updatedAt"`
    ParentTaskID string       `json:"parentTaskId,omitempty"` // for forks
}

type Phase string
const (
    PhaseBrainstorm Phase = "brainstorm"
    PhasePlan       Phase = "plan"
    PhaseExecute    Phase = "execute"
    PhaseReview     Phase = "review"
)
```

### Project

```go
type Project struct {
    Name        string   `json:"name"`        // unique identifier
    Path        string   `json:"path"`        // workspace path
    Keywords    []string `json:"keywords"`    // for routing ("api", "backend", "auth")
    Container   string   `json:"container,omitempty"` // for future docker exec
    DefaultMCPs []string `json:"defaultMcps,omitempty"`
}
```

### Storage

```
~/.agent-deck/hub/
├── tasks/
│   ├── t-001.json
│   └── t-002.json
└── projects.yaml      # user-edited project registry
```

Filesystem JSON for MVP. SQLite later if query performance matters.

### Relationship to Sessions

Tasks **wrap** sessions. A task references one or more session IDs (for phase transitions or forks). Existing session infrastructure handles tmux/PTY; tasks add the project/workflow layer.

---

## 4. UI Components

### Navigation

```
┌─────────────────────────────────────────────────────────┐
│ [☰] Agent Deck                    [⊞ Dashboard] [▤ Terminal] │
├─────────────────────────────────────────────────────────┤
```

- **Dashboard** (new default): Task cards with status
- **Terminal**: Existing session list + xterm.js

Mobile: Bottom tab bar instead of top buttons.

### Dashboard View — Task Cards

```
┌─────────────────────────────────────────────────────────┐
│  Filter: [All ▾] [Running ▾]          [+ New Task]      │
├─────────────────────────────────────────────────────────┤
│                                                         │
│  ┌─────────────────────┐  ┌─────────────────────┐      │
│  │ ● api-service       │  │ ◐ web-app           │      │
│  │ t-007 · execute     │  │ t-006 · review      │      │
│  │ ───────────────     │  │ ───────────────     │      │
│  │ Fix auth token      │  │ Add dark mode       │      │
│  │ refresh bug         │  │ toggle              │      │
│  │                     │  │                     │      │
│  │ 12m · main          │  │ 3h · feat/dark      │      │
│  └─────────────────────┘  └─────────────────────┘      │
└─────────────────────────────────────────────────────────┘
```

### Status Badges

| Status | Icon | Color | Meaning |
|--------|------|-------|---------|
| thinking | ● | amber | Claude reasoning |
| waiting | ◐ | orange (pulse) | Needs user input |
| running | ⟳ | blue | Executing tools |
| idle | ○ | grey | Clean prompt |
| error | ✕ | red | Crash/failure |
| complete | ✓ | green | Task finished |

### Task Detail Panel

Clicking a card opens a slide-over panel:

```
┌─────────────────────────────────────────────────────────┐
│ ← Back                                    [Attach] [⋮]  │
├─────────────────────────────────────────────────────────┤
│ ● api-service · t-007                                   │
│ Fix auth token refresh bug                              │
│                                                         │
│ Phase: [brainstorm]─[plan]─[■execute]─[review]          │
│ Branch: main                                            │
│ Duration: 12m                                           │
├─────────────────────────────────────────────────────────┤
│ ┌─────────────────────────────────────────────────────┐ │
│ │ (xterm.js terminal preview)                         │ │
│ │                                                     │ │
│ │ > Implementing isTokenExpired...                    │ │
│ │   Created src/auth/utils.ts (+12 lines)             │ │
│ │   ✓ 24 tests passed                                 │ │
│ └─────────────────────────────────────────────────────┘ │
├─────────────────────────────────────────────────────────┤
│ [Send message...]                              [Send ↵] │
└─────────────────────────────────────────────────────────┘
```

### Mobile Layout

- Cards stack vertically (1 column)
- Detail panel is full-screen overlay
- Terminal shrinks to ~60% height; chat input always visible
- Phase pips horizontally scrollable

### Technology

- Extend existing vanilla JS + CSS (no framework change)
- Cards rendered client-side from `/api/tasks` JSON
- SSE updates trigger card re-renders

---

## 5. API

### Task Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `GET /api/tasks` | GET | List tasks (`?status=running&project=api-service`) |
| `POST /api/tasks` | POST | Create task → creates session, returns task |
| `GET /api/tasks/{id}` | GET | Task details with session reference |
| `PATCH /api/tasks/{id}` | PATCH | Update task (phase, description) |
| `POST /api/tasks/{id}/input` | POST | Send input to task's session |
| `POST /api/tasks/{id}/fork` | POST | Fork task with inherited context |

### Project Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `GET /api/projects` | GET | List projects from `projects.yaml` |
| `POST /api/route` | POST | Route message → project (keyword match) |

### Example: Create Task

```http
POST /api/tasks
Content-Type: application/json

{
  "project": "api-service",
  "description": "Fix auth token refresh bug",
  "phase": "execute"
}
```

Response:
```json
{
  "task": {
    "id": "t-007",
    "project": "api-service",
    "description": "Fix auth token refresh bug",
    "phase": "execute",
    "sessionId": "abc123",
    "status": "thinking",
    "createdAt": "2026-02-24T12:00:00Z"
  }
}
```

### Example: Route Message

```http
POST /api/route
Content-Type: application/json

{
  "message": "Fix the login validation in the API"
}
```

Response:
```json
{
  "project": "api-service",
  "confidence": 0.85,
  "matchedKeywords": ["api", "login"]
}
```

### SSE Extensions

Extend `/events/menu` with task events:

```
event: task_status
data: {"taskId": "t-007", "status": "waiting", "question": "What auth model?"}

event: task_created
data: {"taskId": "t-008", "project": "web-app"}

event: task_phase
data: {"taskId": "t-007", "phase": "review"}
```

---

## 6. Implementation Phases

### Phase 1: Dashboard MVP (1 week)

- Add `/api/tasks` endpoint (wraps sessions with task metadata)
- Add `projects.yaml` loader and `/api/projects` endpoint
- New dashboard HTML/CSS/JS alongside existing terminal view
- Task cards with status badges (reuse SSE for updates)
- Click card → slide-over panel with existing xterm.js
- Mobile-responsive card layout

### Phase 2: Task Creation & Input (3-4 days)

- `POST /api/tasks` → creates session + task record
- `POST /api/tasks/{id}/input` → send to tmux
- Chat input in task detail panel
- "New Task" button with project selector

### Phase 3: Multi-Project Routing (3-4 days)

- Keyword matching from `projects.yaml`
- `/api/route` endpoint
- Auto-suggest project in new task flow
- Global chat input that routes automatically

### Phase 4: Container Integration (1 week)

- `Container` field in project config
- `docker exec {container} tmux ...` wrapper
- Terminal bridge updated for docker exec PTY
- Health check for container availability

### Phase 5: Conductor View (3-4 days)

- Conductor activity stored in `~/.agent-deck/hub/conductor/`
- `/api/conductor/log` and `/api/conductor/message` endpoints
- Conductor view in dashboard navigation

---

## 7. Out of Scope (Future)

- Kanban board view (defer to post-MVP)
- Diff viewer / approve-reject workflow
- Cross-project DAG orchestration
- SQLite migration (JSON sufficient for single-user)

---

## 8. Success Criteria

1. **Mobile usability** — can create, monitor, and message tasks from phone
2. **At-a-glance status** — see all running agents without opening terminals
3. **No regression** — existing terminal view continues to work
4. **Real-time updates** — status changes appear within 1 second via SSE
