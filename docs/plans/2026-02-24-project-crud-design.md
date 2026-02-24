# Project CRUD via Dashboard

## Problem

The hub dashboard has no mechanism for creating projects. Projects are required to create tasks, blocking all dashboard task management. The current `ProjectRegistry` is a read-only YAML file that must be manually edited.

## Solution

Replace the YAML-based `ProjectRegistry` with a JSON-based `ProjectStore` (matching the existing `TaskStore` pattern) and add full CRUD via the REST API and dashboard UI.

## Backend: ProjectStore

### Storage

- Individual JSON files under `~/.agent-deck/hub/projects/` (e.g., `agent-deck.json`)
- Project `Name` serves as the ID (derived from GitHub repo name)
- Thread-safe via `sync.RWMutex`
- `CreatedAt`/`UpdatedAt` timestamps added to `Project` struct

### Operations

- `List()` — returns all projects sorted by creation time
- `Get(name)` — retrieves a single project by name
- `Save(project)` — creates or updates a project
- `Delete(name)` — removes a project by name

### Validation

- Name: non-empty, no `/`, `\`, `.`, `..`
- Name uniqueness enforced (409 Conflict on duplicate create)

## API Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/api/projects` | GET | List all projects |
| `/api/projects` | POST | Create a project |
| `/api/projects/{name}` | GET | Get a single project |
| `/api/projects/{name}` | PATCH | Update a project |
| `/api/projects/{name}` | DELETE | Delete a project |

### Create Request (`POST /api/projects`)

```json
{
  "repo": "C0ntr0lledCha0s/agent-deck",
  "name": "agent-deck",
  "path": "~/projects/agent-deck",
  "keywords": ["api", "cli"],
  "container": "",
  "defaultMcps": []
}
```

- `repo` is the primary input
- `name` defaults to the last segment of `repo` (e.g., `agent-deck`)
- `path` defaults to `~/projects/{name}` (expanded via `os.UserHomeDir()`)
- Both `name` and `path` can be overridden
- `keywords`, `container`, `defaultMcps` are optional

### Update Request (`PATCH /api/projects/{name}`)

Optional fields: `path`, `keywords`, `container`, `defaultMcps`. Name is immutable (delete + recreate to rename).

## Dashboard UI

### Add Project Modal

A dialog with:
- **GitHub Repo** text input — primary field (e.g., `C0ntr0lledCha0s/agent-deck`)
- **Name** text input — auto-filled from repo, editable
- **Path** text input — auto-filled as `~/projects/{name}`, editable
- **Keywords** text input — comma-separated, optional
- **Container** text input — optional
- Create / Cancel buttons

### Project Management

- **"Manage Projects" button** in the filter bar alongside "New Task"
- **Project list view** showing name, path, keyword count with Edit/Delete actions
- Edit opens pre-filled form; Delete requires confirmation

### New Task Modal Integration

- Project dropdown includes a "+ Add Project" option at the top
- Selecting it opens the Add Project modal
- After creating, the New Task modal re-populates with the new project selected

## Integration Changes

### server.go

- Replace `hubProjects *hub.ProjectRegistry` with `hubProjects *hub.ProjectStore`
- Initialize with `hub.NewProjectStore(hubDir)` instead of `hub.NewProjectRegistry(hubDir)`

### Deleted Code

- `projects.go` — the YAML-based `ProjectRegistry` (replaced by `ProjectStore`)
- `gopkg.in/yaml.v3` dependency if unused elsewhere

### Unchanged Code

- `containerForProject()` — calls `hubProjects.List()`, same interface
- `handleRoute()` — calls `hubProjects.List()` + `hub.Route()`, same interface
- `router.go` — keyword routing logic unchanged

## Testing

- `ProjectStore` unit tests: CRUD operations, name validation, duplicate handling, concurrent access
- HTTP handler tests: all project endpoints (create, list, get, update, delete), auth, error cases
- Follow existing patterns from `store_test.go` and `handlers_hub_test.go`
