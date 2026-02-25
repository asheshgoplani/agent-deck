# Workspace Integration Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add full workspace/container lifecycle to the hub dashboard — project creation UI, Docker SDK container management, phasePrompt wiring, and live workspace status view.

**Architecture:** Extend the `Project` model with container config fields. New `internal/hub/workspace/` package wraps Docker Go SDK behind a `ContainerRuntime` interface. Dashboard gets Add Project modal, workspace sidebar, and container controls. Bridge sessions send phasePrompt via tmux SendKeys.

**Tech Stack:** Go 1.24, Docker Go SDK (`github.com/docker/docker`), vanilla JS (ES5 strict), HTML/CSS

---

### Task 1: Add Docker SDK dependency

**Files:**
- Modify: `go.mod`

**Step 1: Add the Docker SDK dependency**

Run:
```bash
go get github.com/docker/docker@latest
```

**Step 2: Tidy modules**

Run:
```bash
go mod tidy
```

**Step 3: Verify build**

Run:
```bash
go build ./...
```
Expected: clean build, no errors

**Step 4: Commit**

```bash
git add go.mod go.sum
git commit -m "deps: add Docker Go SDK"
```

---

### Task 2: Create ContainerRuntime interface and types

**Files:**
- Create: `internal/hub/workspace/runtime.go`
- Test: `internal/hub/workspace/runtime_test.go`

**Step 1: Write the failing test**

```go
// internal/hub/workspace/runtime_test.go
package workspace

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockRuntime struct {
	createID  string
	createErr error
	startErr  error
	stopErr   error
	removeErr error
	state     ContainerState
	stateErr  error
	stats     *ContainerStats
	statsErr  error
	execOut   string
	execErr   error
}

func (m *mockRuntime) Create(_ context.Context, _ CreateOpts) (string, error) {
	return m.createID, m.createErr
}
func (m *mockRuntime) Start(_ context.Context, _ string) error    { return m.startErr }
func (m *mockRuntime) Stop(_ context.Context, _ string, _ time.Duration) error {
	return m.stopErr
}
func (m *mockRuntime) Remove(_ context.Context, _ string) error   { return m.removeErr }
func (m *mockRuntime) Status(_ context.Context, _ string) (ContainerState, error) {
	return m.state, m.stateErr
}
func (m *mockRuntime) Stats(_ context.Context, _ string) (*ContainerStats, error) {
	return m.stats, m.statsErr
}
func (m *mockRuntime) Exec(_ context.Context, _ string, _ []string) (string, error) {
	return m.execOut, m.execErr
}

func TestContainerRuntimeInterface(t *testing.T) {
	var rt ContainerRuntime = &mockRuntime{
		createID: "abc123",
		state:    ContainerState{Status: StatusRunning},
		stats:    &ContainerStats{CPUPercent: 12.5, MemUsage: 500 * 1024 * 1024, MemLimit: 2 * 1024 * 1024 * 1024},
	}

	ctx := context.Background()

	id, err := rt.Create(ctx, CreateOpts{Name: "test", Image: "alpine:latest"})
	require.NoError(t, err)
	assert.Equal(t, "abc123", id)

	require.NoError(t, rt.Start(ctx, id))
	require.NoError(t, rt.Stop(ctx, id, 10*time.Second))
	require.NoError(t, rt.Remove(ctx, id))

	state, err := rt.Status(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, StatusRunning, state.Status)

	stats, err := rt.Stats(ctx, id)
	require.NoError(t, err)
	assert.InDelta(t, 12.5, stats.CPUPercent, 0.01)
}

func TestContainerNameForProject(t *testing.T) {
	assert.Equal(t, "agentdeck-myapp", ContainerNameForProject("myapp"))
	assert.Equal(t, "agentdeck-agent-deck", ContainerNameForProject("agent-deck"))
}
```

**Step 2: Run test to verify it fails**

Run: `go test -race -v ./internal/hub/workspace/ -run TestContainerRuntime`
Expected: FAIL — package doesn't exist

**Step 3: Write the interface**

```go
// internal/hub/workspace/runtime.go
package workspace

import (
	"context"
	"time"
)

// Container status constants.
const (
	StatusRunning    = "running"
	StatusStopped    = "stopped"
	StatusNotFound   = "not_found"
	StatusNotCreated = "not_created"
)

// ContainerRuntime abstracts container lifecycle operations.
// The Docker implementation is the primary provider; the interface
// allows future Coder or Podman implementations.
type ContainerRuntime interface {
	Create(ctx context.Context, opts CreateOpts) (containerID string, err error)
	Start(ctx context.Context, containerID string) error
	Stop(ctx context.Context, containerID string, timeout time.Duration) error
	Remove(ctx context.Context, containerID string) error
	Status(ctx context.Context, containerID string) (ContainerState, error)
	Stats(ctx context.Context, containerID string) (*ContainerStats, error)
	Exec(ctx context.Context, containerID string, cmd []string) (string, error)
}

// CreateOpts configures a new container.
type CreateOpts struct {
	Name        string
	Image       string
	WorkDir     string
	Mounts      []Mount
	Env         map[string]string
	CPULimit    float64 // cores
	MemoryLimit int64   // bytes
	Labels      map[string]string
}

// Mount describes a bind or volume mount.
type Mount struct {
	Source   string
	Target  string
	ReadOnly bool
}

// ContainerState holds the current state of a container.
type ContainerState struct {
	Status    string // StatusRunning, StatusStopped, StatusNotFound
	StartedAt time.Time
}

// ContainerStats holds live resource usage.
type ContainerStats struct {
	CPUPercent float64
	MemUsage   int64
	MemLimit   int64
}

// ContainerNameForProject derives the container name from a project name.
func ContainerNameForProject(projectName string) string {
	return "agentdeck-" + projectName
}
```

**Step 4: Run test to verify it passes**

Run: `go test -race -v ./internal/hub/workspace/ -run TestContainer`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/hub/workspace/
git commit -m "feat(workspace): add ContainerRuntime interface and types"
```

---

### Task 3: Implement DockerRuntime

**Files:**
- Create: `internal/hub/workspace/docker.go`
- Create: `internal/hub/workspace/docker_test.go`

**Step 1: Write failing tests for DockerRuntime**

```go
// internal/hub/workspace/docker_test.go
package workspace

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewDockerRuntime(t *testing.T) {
	// This test verifies the constructor works.
	// It will fail in environments without Docker socket, so skip gracefully.
	rt, err := NewDockerRuntime()
	if err != nil {
		t.Skipf("Docker not available: %v", err)
	}
	assert.NotNil(t, rt)
}
```

**Step 2: Run test to verify it fails**

Run: `go test -race -v ./internal/hub/workspace/ -run TestNewDockerRuntime`
Expected: FAIL — NewDockerRuntime not defined

**Step 3: Implement DockerRuntime**

```go
// internal/hub/workspace/docker.go
package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
)

// DockerRuntime implements ContainerRuntime using the Docker Engine API.
type DockerRuntime struct {
	client *client.Client
}

// NewDockerRuntime creates a DockerRuntime from the environment.
// It uses DOCKER_HOST or the default Unix socket.
func NewDockerRuntime() (*DockerRuntime, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("create docker client: %w", err)
	}
	return &DockerRuntime{client: cli}, nil
}

func (d *DockerRuntime) Create(ctx context.Context, opts CreateOpts) (string, error) {
	env := make([]string, 0, len(opts.Env))
	for k, v := range opts.Env {
		env = append(env, k+"="+v)
	}

	var mounts []mount.Mount
	for _, m := range opts.Mounts {
		mounts = append(mounts, mount.Mount{
			Type:     mount.TypeBind,
			Source:   m.Source,
			Target:   m.Target,
			ReadOnly: m.ReadOnly,
		})
	}

	hostConfig := &container.HostConfig{
		Mounts: mounts,
		Resources: container.Resources{
			NanoCPUs: int64(opts.CPULimit * 1e9),
			Memory:   opts.MemoryLimit,
		},
	}

	config := &container.Config{
		Image:      opts.Image,
		WorkingDir: opts.WorkDir,
		Env:        env,
		Labels:     opts.Labels,
	}

	resp, err := d.client.ContainerCreate(ctx, config, hostConfig, nil, nil, opts.Name)
	if err != nil {
		return "", fmt.Errorf("create container %s: %w", opts.Name, err)
	}
	return resp.ID, nil
}

func (d *DockerRuntime) Start(ctx context.Context, containerID string) error {
	return d.client.ContainerStart(ctx, containerID, container.StartOptions{})
}

func (d *DockerRuntime) Stop(ctx context.Context, containerID string, timeout time.Duration) error {
	timeoutSec := int(timeout.Seconds())
	return d.client.ContainerStop(ctx, containerID, container.StopOptions{Timeout: &timeoutSec})
}

func (d *DockerRuntime) Remove(ctx context.Context, containerID string) error {
	return d.client.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true})
}

func (d *DockerRuntime) Status(ctx context.Context, containerID string) (ContainerState, error) {
	info, err := d.client.ContainerInspect(ctx, containerID)
	if err != nil {
		if client.IsErrNotFound(err) {
			return ContainerState{Status: StatusNotFound}, nil
		}
		return ContainerState{}, fmt.Errorf("inspect container: %w", err)
	}

	status := StatusStopped
	if info.State.Running {
		status = StatusRunning
	}

	startedAt, _ := time.Parse(time.RFC3339Nano, info.State.StartedAt)
	return ContainerState{Status: status, StartedAt: startedAt}, nil
}

func (d *DockerRuntime) Stats(ctx context.Context, containerID string) (*ContainerStats, error) {
	resp, err := d.client.ContainerStats(ctx, containerID, false)
	if err != nil {
		return nil, fmt.Errorf("container stats: %w", err)
	}
	defer resp.Body.Close()

	var raw bytes.Buffer
	if _, err := io.Copy(&raw, resp.Body); err != nil {
		return nil, fmt.Errorf("read stats: %w", err)
	}

	var v types.StatsJSON
	if err := json.Unmarshal(raw.Bytes(), &v); err != nil {
		return nil, fmt.Errorf("unmarshal stats: %w", err)
	}

	cpuDelta := float64(v.CPUStats.CPUUsage.TotalUsage - v.PreCPUStats.CPUUsage.TotalUsage)
	systemDelta := float64(v.CPUStats.SystemUsage - v.PreCPUStats.SystemUsage)
	cpuPercent := 0.0
	if systemDelta > 0 {
		cpuPercent = (cpuDelta / systemDelta) * float64(v.CPUStats.OnlineCPUs) * 100.0
	}

	return &ContainerStats{
		CPUPercent: cpuPercent,
		MemUsage:   int64(v.MemoryStats.Usage),
		MemLimit:   int64(v.MemoryStats.Limit),
	}, nil
}

func (d *DockerRuntime) Exec(ctx context.Context, containerID string, cmd []string) (string, error) {
	execCfg := container.ExecOptions{
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: true,
	}

	execResp, err := d.client.ContainerExecCreate(ctx, containerID, execCfg)
	if err != nil {
		return "", fmt.Errorf("exec create: %w", err)
	}

	attachResp, err := d.client.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		return "", fmt.Errorf("exec attach: %w", err)
	}
	defer attachResp.Close()

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, attachResp.Reader); err != nil {
		return "", fmt.Errorf("exec read: %w", err)
	}

	return buf.String(), nil
}
```

**Step 4: Run test to verify it passes**

Run: `go test -race -v ./internal/hub/workspace/ -run TestNewDockerRuntime`
Expected: PASS (or SKIP if no Docker socket)

**Step 5: Commit**

```bash
git add internal/hub/workspace/docker.go internal/hub/workspace/docker_test.go
git commit -m "feat(workspace): implement DockerRuntime with Docker Go SDK"
```

---

### Task 4: Extend Project model with container config fields

**Files:**
- Modify: `internal/hub/models.go:78-88`
- Test: `internal/hub/project_store_test.go` (extend)

**Step 1: Write failing test for new fields**

Add to `internal/hub/project_store_test.go`:

```go
func TestProjectStoreRoundTripContainerConfig(t *testing.T) {
	dir := t.TempDir()
	store, err := NewProjectStore(dir)
	require.NoError(t, err)

	p := &Project{
		Name:        "myapp",
		Repo:        "org/myapp",
		Path:        "/workspace/myapp",
		Image:       "sandbox-image:latest",
		CPULimit:    2.0,
		MemoryLimit: 2 * 1024 * 1024 * 1024,
		Volumes: []VolumeMount{
			{Host: "/home/user/.ssh", Container: "/tmp/host-ssh", ReadOnly: true},
		},
		Env: map[string]string{"NODE_ENV": "development"},
	}

	require.NoError(t, store.Save(p))

	loaded, err := store.Get("myapp")
	require.NoError(t, err)
	assert.Equal(t, "sandbox-image:latest", loaded.Image)
	assert.InDelta(t, 2.0, loaded.CPULimit, 0.01)
	assert.Equal(t, int64(2*1024*1024*1024), loaded.MemoryLimit)
	assert.Len(t, loaded.Volumes, 1)
	assert.Equal(t, "/home/user/.ssh", loaded.Volumes[0].Host)
	assert.True(t, loaded.Volumes[0].ReadOnly)
	assert.Equal(t, "development", loaded.Env["NODE_ENV"])
}
```

**Step 2: Run test to verify it fails**

Run: `go test -race -v ./internal/hub/ -run TestProjectStoreRoundTripContainerConfig`
Expected: FAIL — `VolumeMount` undefined, `Image` field doesn't exist

**Step 3: Add fields to Project model**

In `internal/hub/models.go`, extend the `Project` struct:

```go
// VolumeMount describes a bind mount for container provisioning.
type VolumeMount struct {
	Host      string `json:"host"`
	Container string `json:"container"`
	ReadOnly  bool   `json:"readOnly,omitempty"`
}

// Project defines a workspace that tasks can be routed to.
type Project struct {
	Name        string    `json:"name"`
	Repo        string    `json:"repo,omitempty"`
	Path        string    `json:"path"`
	Keywords    []string  `json:"keywords"`
	Container   string    `json:"container,omitempty"`
	DefaultMCPs []string  `json:"defaultMcps,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`

	// Container provisioning config.
	Image       string            `json:"image,omitempty"`
	CPULimit    float64           `json:"cpuLimit,omitempty"`
	MemoryLimit int64             `json:"memoryLimit,omitempty"`
	Volumes     []VolumeMount     `json:"volumes,omitempty"`
	Env         map[string]string `json:"env,omitempty"`

	// Runtime state (not persisted, populated at query time).
	ContainerStatus string `json:"containerStatus,omitempty"`
}
```

**Step 4: Run test to verify it passes**

Run: `go test -race -v ./internal/hub/ -run TestProjectStoreRoundTripContainerConfig`
Expected: PASS

**Step 5: Run full hub tests to check no regressions**

Run: `go test -race -v ./internal/hub/...`
Expected: all PASS

**Step 6: Commit**

```bash
git add internal/hub/models.go internal/hub/project_store_test.go
git commit -m "feat(hub): extend Project model with container config fields"
```

---

### Task 5: Refactor ContainerExecutor to use ContainerRuntime

**Files:**
- Modify: `internal/hub/container.go`
- Modify: `internal/hub/session.go`
- Modify: `internal/hub/container_test.go`
- Modify: `internal/hub/session_test.go`
- Modify: `internal/web/server.go:97-104`
- Modify: `internal/web/handlers_hub.go` (references to `containerExec`, `sessionLauncher`)
- Modify: `internal/web/handlers_hub_test.go` (test executor mock)

**Step 1: Update container.go — make ContainerExecutor wrap ContainerRuntime**

Replace `DockerExecutor` (CLI-based) with a `RuntimeExecutor` adapter that satisfies the old `ContainerExecutor` interface but delegates to `ContainerRuntime`. This lets existing code work unchanged while we migrate.

```go
// internal/hub/container.go
package hub

import (
	"context"
	"fmt"

	"github.com/asheshgoplani/agent-deck/internal/hub/workspace"
)

// ContainerExecutor abstracts docker exec operations for testability.
type ContainerExecutor interface {
	IsHealthy(ctx context.Context, container string) bool
	Exec(ctx context.Context, container string, args ...string) (string, error)
}

// RuntimeExecutor adapts a workspace.ContainerRuntime to the ContainerExecutor interface.
type RuntimeExecutor struct {
	Runtime workspace.ContainerRuntime
}

func (r *RuntimeExecutor) IsHealthy(ctx context.Context, container string) bool {
	state, err := r.Runtime.Status(ctx, container)
	if err != nil {
		return false
	}
	return state.Status == workspace.StatusRunning
}

func (r *RuntimeExecutor) Exec(ctx context.Context, container string, args ...string) (string, error) {
	return r.Runtime.Exec(ctx, container, args)
}
```

**Step 2: Run existing container and session tests**

Run: `go test -race -v ./internal/hub/ -run "TestContainer|TestSession"`
Expected: PASS (mock-based tests still work since ContainerExecutor interface unchanged)

**Step 3: Update server.go initialization to use DockerRuntime**

In `internal/web/server.go:97-104`, replace:
```go
s.containerExec = &hub.DockerExecutor{}
s.sessionLauncher = &hub.SessionLauncher{Executor: s.containerExec}
```
with:
```go
if dockerRT, err := workspace.NewDockerRuntime(); err != nil {
    webLog.Warn("docker_disabled", slog.String("error", err.Error()))
} else {
    s.containerRuntime = dockerRT
    s.containerExec = &hub.RuntimeExecutor{Runtime: dockerRT}
    s.sessionLauncher = &hub.SessionLauncher{Executor: s.containerExec}
}
```

Add `containerRuntime workspace.ContainerRuntime` field to `Server` struct.

**Step 4: Run full web tests**

Run: `go test -race -v ./internal/web/...`
Expected: all PASS

**Step 5: Commit**

```bash
git add internal/hub/container.go internal/hub/session.go internal/hub/container_test.go internal/hub/session_test.go internal/web/server.go internal/web/handlers_hub.go internal/web/handlers_hub_test.go
git commit -m "refactor: bridge ContainerExecutor to ContainerRuntime"
```

---

### Task 6: Add workspace API endpoints

**Files:**
- Modify: `internal/web/handlers_hub.go` (add workspace handlers + request/response types)
- Modify: `internal/web/server.go:136-146` (register routes)
- Modify: `internal/web/handlers_hub_test.go` (test workspace endpoints)

**Step 1: Write failing tests for workspace endpoints**

Add to `internal/web/handlers_hub_test.go`:

```go
func TestWorkspacesListEmpty(t *testing.T) {
	srv := newTestServerWithHub(t)

	req := httptest.NewRequest(http.MethodGet, "/api/workspaces", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	var resp workspacesListResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Empty(t, resp.Workspaces)
}

func TestWorkspacesListWithProject(t *testing.T) {
	srv := newTestServerWithHub(t)

	// Create a project first
	p := &hub.Project{Name: "myapp", Path: "/workspace/myapp"}
	require.NoError(t, srv.hubProjects.Save(p))

	req := httptest.NewRequest(http.MethodGet, "/api/workspaces", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	var resp workspacesListResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Len(t, resp.Workspaces, 1)
	assert.Equal(t, "myapp", resp.Workspaces[0].Name)
}
```

**Step 2: Run test to verify it fails**

Run: `go test -race -v ./internal/web/ -run TestWorkspaces`
Expected: FAIL — handler not defined

**Step 3: Implement workspace handlers**

Add to `internal/web/handlers_hub.go`:

- `handleWorkspaces` — dispatches GET /api/workspaces
- `handleWorkspacesList` — lists projects enriched with container status
- `handleWorkspaceByName` — dispatches /api/workspaces/{name}/{action}
- `handleWorkspaceStart` — creates + starts container
- `handleWorkspaceStop` — stops container
- `handleWorkspaceRemove` — removes container
- `handleWorkspaceStats` — returns live CPU/mem

Response types:
```go
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
```

Register routes in `server.go`:
```go
mux.HandleFunc("/api/workspaces", s.handleWorkspaces)
mux.HandleFunc("/api/workspaces/", s.handleWorkspaceByName)
```

**Step 4: Run test to verify it passes**

Run: `go test -race -v ./internal/web/ -run TestWorkspaces`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/web/handlers_hub.go internal/web/server.go internal/web/handlers_hub_test.go
git commit -m "feat(web): add workspace API endpoints"
```

---

### Task 7: Update project creation to auto-provision containers

**Files:**
- Modify: `internal/web/handlers_hub.go` (`handleProjectsCreate`, `handleProjectDelete`)
- Modify: `internal/web/handlers_hub_test.go`

**Step 1: Write failing test for auto-provision on project create**

```go
func TestProjectCreateAutoProvision(t *testing.T) {
	srv := newTestServerWithHub(t)
	mockRT := &mockContainerRuntime{createID: "abc123"}
	srv.containerRuntime = mockRT

	body := `{"repo":"org/myapp","image":"sandbox-image:latest","cpuLimit":2.0,"memoryLimit":2147483648}`
	req := httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(body))
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	assert.Equal(t, http.StatusCreated, rr.Code)
	assert.Equal(t, 1, mockRT.createCalls)
	assert.Equal(t, 1, mockRT.startCalls)
}
```

Need a `mockContainerRuntime` in the test file that tracks calls.

**Step 2: Run test to verify it fails**

Run: `go test -race -v ./internal/web/ -run TestProjectCreateAutoProvision`
Expected: FAIL

**Step 3: Update handleProjectsCreate**

After `hubProjects.Save(project)`, if `project.Image != ""`:
1. Derive container name: `workspace.ContainerNameForProject(project.Name)`
2. Call `containerRuntime.Create(ctx, opts)` with project config
3. Call `containerRuntime.Start(ctx, containerID)`
4. Set `project.Container = containerName` and re-save

Update `handleProjectDelete`: if `removeContainer=true` query param, call `containerRuntime.Remove()`.

Also update `createProjectRequest` to include the new fields:
```go
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
```

**Step 4: Run test to verify it passes**

Run: `go test -race -v ./internal/web/ -run TestProjectCreate`
Expected: PASS

**Step 5: Run full web test suite**

Run: `go test -race -v ./internal/web/...`
Expected: all PASS

**Step 6: Commit**

```bash
git add internal/web/handlers_hub.go internal/web/handlers_hub_test.go
git commit -m "feat(web): auto-provision container on project creation"
```

---

### Task 8: Wire phasePrompt to tmux SendKeys

**Files:**
- Modify: `internal/web/hub_session_bridge.go:40-90` (StartPhase method)
- Modify: `internal/web/hub_session_bridge_test.go`

**Step 1: Write failing test for phasePrompt delivery**

Add to `internal/web/hub_session_bridge_test.go`:

```go
func TestStartPhaseSendsPrompt(t *testing.T) {
	hubDir := t.TempDir()
	taskStore, _ := hub.NewTaskStore(hubDir)
	projectStore, _ := hub.NewProjectStore(hubDir)

	// Create a project with a container
	project := &hub.Project{Name: "myapp", Path: "/workspace/myapp", Container: "test-container"}
	require.NoError(t, projectStore.Save(project))

	// Create a task
	task := &hub.Task{Project: "myapp", Description: "Fix auth bug", Phase: hub.PhaseBrainstorm, Status: hub.TaskStatusBacklog, AgentStatus: hub.AgentStatusIdle}
	require.NoError(t, taskStore.Save(task))

	mockExec := &testExecutor{healthy: true}
	bridge := NewHubSessionBridge("_test", taskStore, projectStore)
	bridge.openStorage = func(profile string) (storageLoader, error) {
		return &testStorageLoader{}, nil
	}
	bridge.executor = mockExec

	result, err := bridge.StartPhase(task.ID, hub.PhaseBrainstorm)
	require.NoError(t, err)
	assert.NotEmpty(t, result.SessionID)
	// Verify prompt was sent
	assert.True(t, mockExec.sendKeysCalled)
	assert.Contains(t, mockExec.lastSendKeysInput, "/brainstorm Fix auth bug")
}
```

**Step 2: Run test to verify it fails**

Run: `go test -race -v ./internal/web/ -run TestStartPhaseSendsPrompt`
Expected: FAIL — `executor` field doesn't exist, `sendKeysCalled` not tracked

**Step 3: Wire phasePrompt in StartPhase**

Add an optional `executor` field (ContainerExecutor or SessionLauncher) to `HubSessionBridge`. After saving the session instance, if the project has a container, use the executor to send the phase prompt via `tmux send-keys`.

For local (bridge) sessions without containers, send via the tmux package directly using the session's tmux name.

**Step 4: Run test to verify it passes**

Run: `go test -race -v ./internal/web/ -run TestStartPhaseSendsPrompt`
Expected: PASS

**Step 5: Run full bridge tests**

Run: `go test -race -v ./internal/web/ -run TestBridge`
Expected: all PASS

**Step 6: Commit**

```bash
git add internal/web/hub_session_bridge.go internal/web/hub_session_bridge_test.go
git commit -m "feat(bridge): wire phasePrompt to tmux SendKeys on phase start"
```

---

### Task 9: Add Project modal to dashboard HTML

**Files:**
- Modify: `internal/web/static/dashboard.html`
- Modify: `internal/web/static/dashboard.css`

**Step 1: Add modal HTML**

Add an "Add Project" modal to `dashboard.html`, similar in structure to the existing `new-task-modal`. Include:
- GitHub Repo input (primary)
- Name input (auto-derived)
- Path input (auto-derived)
- Keywords input (comma-separated)
- Container mode radio: "None (local)", "Existing container", "Auto-provision"
- Conditional fields: container name input, or image/cpu/memory inputs
- Create / Cancel buttons

Also add a "Manage Projects" button to the filter bar alongside "New Task".

**Step 2: Add modal CSS**

Add styles to `dashboard.css` for the new modal. Reuse existing `.modal`, `.modal-backdrop`, `.form-group` patterns from the new-task-modal.

Add container-mode-specific show/hide styles:
```css
.container-fields-existing,
.container-fields-provision { display: none; }
.container-mode-existing .container-fields-existing { display: block; }
.container-mode-provision .container-fields-provision { display: block; }
```

**Step 3: Verify build**

Run: `make build`
Expected: clean build

**Step 4: Commit**

```bash
git add internal/web/static/dashboard.html internal/web/static/dashboard.css
git commit -m "feat(dashboard): add Project modal HTML and CSS"
```

---

### Task 10: Wire Add Project modal in dashboard JS

**Files:**
- Modify: `internal/web/static/dashboard.js`

**Step 1: Add project CRUD functions**

Add to `dashboard.js`:

- `openAddProjectModal()` — opens modal, resets form
- `closeAddProjectModal()` — closes modal
- `submitAddProject()` — POSTs to `/api/projects`, handles auto-provision fields
- Auto-derive name from repo on input (split by `/`, take last segment)
- Auto-derive path as `~/projects/{name}`
- Container mode radio toggles conditional fields
- `openManageProjectsView()` — lists projects with edit/delete buttons
- `deleteProject(name)` — DELETEs `/api/projects/{name}` with confirmation
- `editProject(name)` — opens modal pre-filled

Wire event listeners:
- "Add Project" button in filter bar → `openAddProjectModal()`
- "+ Add Project" option in new-task project dropdown → `openAddProjectModal()`
- Modal close/cancel buttons
- Submit button
- Container mode radio change handler

**Step 2: Verify manually**

Run: `make build && ./build/agent-deck web`
Open browser to `http://127.0.0.1:8420`
Verify: Add Project button visible, modal opens, form fields work, submit creates project via API.

**Step 3: Commit**

```bash
git add internal/web/static/dashboard.js
git commit -m "feat(dashboard): wire Add Project modal and project CRUD"
```

---

### Task 11: Update workspace view to use real API

**Files:**
- Modify: `internal/web/static/dashboard.js` (renderWorkspacesView, createWorkspaceCard)

**Step 1: Update renderWorkspacesView**

Replace the fallback-only approach with a primary fetch to `/api/workspaces`:
- Fetch `/api/workspaces` → render cards with real container status
- Each card: name, status badge (running/stopped/not_created), CPU/mem progress bars, active task count
- "Start" button → POST `/api/workspaces/{name}/start`, refresh view
- "Stop" button → POST `/api/workspaces/{name}/stop`, refresh view
- "Remove" button → POST `/api/workspaces/{name}/remove` with confirmation
- "Provision new workspace" button → opens Add Project modal with auto-provision pre-selected
- Poll `/api/workspaces` every 5s when workspace view is active (clear interval on view switch)

**Step 2: Update createWorkspaceCard**

Replace disabled start/stop buttons with working handlers.
Add CPU/memory progress bars:
```js
var memPercent = ws.memLimit > 0 ? Math.round((ws.memUsage / ws.memLimit) * 100) : 0
var memBar = el("div", "workspace-mem-bar")
var memFill = el("div", "workspace-mem-fill")
memFill.style.width = memPercent + "%"
memBar.appendChild(memFill)
```

**Step 3: Verify manually**

Run: `make build && ./build/agent-deck web`
Navigate to Workspaces view, verify cards render with status, buttons work.

**Step 4: Commit**

```bash
git add internal/web/static/dashboard.js internal/web/static/dashboard.css
git commit -m "feat(dashboard): workspace view with live container status and controls"
```

---

### Task 12: Update task creation to auto-start stopped containers

**Files:**
- Modify: `internal/web/handlers_hub.go:115-152` (handleTasksCreate)

**Step 1: Write failing test**

```go
func TestTaskCreateAutoStartsStoppedContainer(t *testing.T) {
	srv := newTestServerWithHub(t)
	mockRT := &mockContainerRuntime{
		state: workspace.ContainerState{Status: workspace.StatusStopped},
	}
	srv.containerRuntime = mockRT

	// Create project with auto-provisioned container
	p := &hub.Project{Name: "myapp", Path: "/workspace/myapp", Container: "agentdeck-myapp", Image: "sandbox:latest"}
	require.NoError(t, srv.hubProjects.Save(p))

	body := `{"project":"myapp","description":"Fix bug"}`
	req := httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(body))
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	assert.Equal(t, http.StatusCreated, rr.Code)
	assert.Equal(t, 1, mockRT.startCalls, "container should be auto-started")
}
```

**Step 2: Run test to verify it fails**

Run: `go test -race -v ./internal/web/ -run TestTaskCreateAutoStartsStoppedContainer`
Expected: FAIL

**Step 3: Update handleTasksCreate**

Before the existing bridge/container launch logic, check if the project's container is stopped and auto-start it:

```go
if s.containerRuntime != nil && proj.Container != "" && proj.Image != "" {
    state, _ := s.containerRuntime.Status(r.Context(), proj.Container)
    if state.Status == workspace.StatusStopped {
        _ = s.containerRuntime.Start(r.Context(), proj.Container)
    }
}
```

**Step 4: Run test to verify it passes**

Run: `go test -race -v ./internal/web/ -run TestTaskCreateAutoStartsStoppedContainer`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/web/handlers_hub.go internal/web/handlers_hub_test.go
git commit -m "feat(web): auto-start stopped container on task creation"
```

---

### Task 13: Docker integration tests

**Files:**
- Create: `internal/hub/workspace/integration_test.go`

**Step 1: Write integration test**

```go
// internal/hub/workspace/integration_test.go
package workspace

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func skipIfNoDocker(t *testing.T) {
	t.Helper()
	rt, err := NewDockerRuntime()
	if err != nil {
		t.Skipf("Docker not available: %v", err)
	}
	// Quick ping to verify daemon is reachable
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := rt.client.Ping(ctx); err != nil {
		t.Skipf("Docker daemon not reachable: %v", err)
	}
}

func TestDockerRuntimeLifecycle(t *testing.T) {
	skipIfNoDocker(t)
	rt, _ := NewDockerRuntime()
	ctx := context.Background()
	name := "agentdeck-integration-test"

	// Cleanup from any previous failed run
	_ = rt.Remove(ctx, name)

	// Create
	id, err := rt.Create(ctx, CreateOpts{
		Name:    name,
		Image:   "alpine:latest",
		WorkDir: "/workspace",
		Labels:  map[string]string{"agentdeck.test": "true"},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, id)

	// Start
	require.NoError(t, rt.Start(ctx, name))

	// Status
	state, err := rt.Status(ctx, name)
	require.NoError(t, err)
	assert.Equal(t, StatusRunning, state.Status)

	// Exec
	out, err := rt.Exec(ctx, name, []string{"echo", "hello"})
	require.NoError(t, err)
	assert.Contains(t, out, "hello")

	// Stop
	require.NoError(t, rt.Stop(ctx, name, 5*time.Second))

	state, err = rt.Status(ctx, name)
	require.NoError(t, err)
	assert.Equal(t, StatusStopped, state.Status)

	// Remove
	require.NoError(t, rt.Remove(ctx, name))

	state, err = rt.Status(ctx, name)
	require.NoError(t, err)
	assert.Equal(t, StatusNotFound, state.Status)
}
```

**Step 2: Run test**

Run: `go test -race -v ./internal/hub/workspace/ -run TestDockerRuntimeLifecycle`
Expected: PASS (or SKIP if no Docker)

**Step 3: Commit**

```bash
git add internal/hub/workspace/integration_test.go
git commit -m "test(workspace): add Docker integration tests"
```

---

### Task 14: Full build verification and manual testing

**Step 1: Run full test suite**

Run: `go test -race -v ./...`
Expected: all PASS (some SKIP for Docker/tmux-dependent tests)

**Step 2: Build binary**

Run: `make build`
Expected: clean build

**Step 3: Manual verification**

Run: `./build/agent-deck web`
Open `http://127.0.0.1:8420`

Verify:
- [ ] "Add Project" button visible in filter bar
- [ ] Add Project modal opens with all fields
- [ ] Container mode radio toggles conditional fields
- [ ] Creating a project with "None" mode works
- [ ] Creating a project with "Existing container" mode works
- [ ] Creating a project with "Auto-provision" mode creates + starts container (if Docker available)
- [ ] Workspaces sidebar shows real container status
- [ ] Start/Stop buttons work on workspace cards
- [ ] Creating a task auto-starts bridge session with phase prompt
- [ ] Task terminal streams live

**Step 4: Commit any fixes**

```bash
git add -A
git commit -m "fix: address integration issues from manual testing"
```
