package web

import (
	"context"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/hub"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestBridge(t *testing.T) (*HubSessionBridge, *hub.TaskStore, *hub.ProjectStore) {
	t.Helper()
	hubDir := t.TempDir()
	ts, err := hub.NewTaskStore(hubDir)
	require.NoError(t, err)
	ps, err := hub.NewProjectStore(hubDir)
	require.NoError(t, err)

	bridge := NewHubSessionBridge("_test", ts, ps, nil)
	// Override storage opener to return a mock that records calls
	bridge.openStorage = func(profile string) (storageLoader, error) {
		return &mockStorageLoader{}, nil
	}
	return bridge, ts, ps
}

type mockStorageLoader struct {
	instances []*session.Instance
	groups    []*session.GroupData
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

func TestSaveInstance_PersistsToStorage(t *testing.T) {
	t.Setenv("AGENTDECK_PROFILE", "_test")

	hubDir := t.TempDir()
	ts, err := hub.NewTaskStore(hubDir)
	require.NoError(t, err)
	ps, err := hub.NewProjectStore(hubDir)
	require.NoError(t, err)

	bridge := NewHubSessionBridge("_test", ts, ps, nil)
	// Use real storage opener (defaultStorageOpener) — not mocked

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

// sendKeysExecutor tracks SendInput calls made through the SessionLauncher.
type sendKeysExecutor struct {
	healthy            bool
	sendKeysCalled     bool
	lastContainer      string
	lastSessionName    string
	lastSendKeysInput  string
}

func (e *sendKeysExecutor) IsHealthy(_ context.Context, _ string) bool {
	return e.healthy
}

func (e *sendKeysExecutor) Exec(_ context.Context, container string, cmd ...string) (string, error) {
	// Track send-keys calls (the SessionLauncher calls Exec twice: once for
	// the literal text, once for Enter). We capture the literal text call.
	if len(cmd) >= 6 && cmd[0] == "tmux" && cmd[1] == "send-keys" && cmd[2] == "-l" {
		e.sendKeysCalled = true
		e.lastContainer = container
		e.lastSessionName = cmd[4] // -t <session>
		e.lastSendKeysInput = cmd[5]
	}
	return "", nil
}

func TestStartPhase_SendsPromptToContainer(t *testing.T) {
	hubDir := t.TempDir()
	ts, err := hub.NewTaskStore(hubDir)
	require.NoError(t, err)
	ps, err := hub.NewProjectStore(hubDir)
	require.NoError(t, err)

	exec := &sendKeysExecutor{healthy: true}
	launcher := &hub.SessionLauncher{Executor: exec}

	bridge := NewHubSessionBridge("_test", ts, ps, launcher)
	bridge.openStorage = func(profile string) (storageLoader, error) {
		return &mockStorageLoader{}, nil
	}

	// Create a project with a container.
	proj := &hub.Project{
		Name:      "web-app",
		Path:      "/tmp/test-project",
		Keywords:  []string{"web"},
		Container: "sandbox-web",
	}
	require.NoError(t, ps.Save(proj))

	// Create a task routed to that project.
	task := &hub.Task{
		Project:     "web-app",
		Description: "Fix auth bug in login flow",
		Phase:       hub.PhaseBrainstorm,
		Status:      hub.TaskStatusBacklog,
		AgentStatus: hub.AgentStatusIdle,
	}
	require.NoError(t, ts.Save(task))

	// Start the brainstorm phase — should send prompt to container.
	result, err := bridge.StartPhase(task.ID, hub.PhaseBrainstorm)
	require.NoError(t, err)
	assert.NotEmpty(t, result.SessionID)

	// Verify SendInput was called via the executor.
	assert.True(t, exec.sendKeysCalled, "expected SendInput to be called for container session")
	assert.Equal(t, "sandbox-web", exec.lastContainer)
	assert.Equal(t, "agent-"+task.ID, exec.lastSessionName)
	assert.Contains(t, exec.lastSendKeysInput, "Fix auth bug in login flow")
}

func TestStartPhase_NoPromptWithoutContainer(t *testing.T) {
	hubDir := t.TempDir()
	ts, err := hub.NewTaskStore(hubDir)
	require.NoError(t, err)
	ps, err := hub.NewProjectStore(hubDir)
	require.NoError(t, err)

	exec := &sendKeysExecutor{healthy: true}
	launcher := &hub.SessionLauncher{Executor: exec}

	bridge := NewHubSessionBridge("_test", ts, ps, launcher)
	bridge.openStorage = func(profile string) (storageLoader, error) {
		return &mockStorageLoader{}, nil
	}

	// Project without a container.
	proj := &hub.Project{
		Name:     "local-proj",
		Path:     "/tmp/test-local",
		Keywords: []string{"local"},
	}
	require.NoError(t, ps.Save(proj))

	task := &hub.Task{
		Project:     "local-proj",
		Description: "Local task",
		Phase:       hub.PhaseExecute,
		Status:      hub.TaskStatusBacklog,
		AgentStatus: hub.AgentStatusIdle,
	}
	require.NoError(t, ts.Save(task))

	_, err = bridge.StartPhase(task.ID, hub.PhaseExecute)
	require.NoError(t, err)

	// Should NOT have called SendInput since there's no container.
	assert.False(t, exec.sendKeysCalled, "should not send prompt for non-container project")
}

func TestStartPhase_NilLauncherNoError(t *testing.T) {
	bridge, ts, ps := newTestBridge(t)

	proj := &hub.Project{
		Name:      "web-app",
		Path:      "/tmp/test-project",
		Keywords:  []string{"web"},
		Container: "sandbox-web",
	}
	require.NoError(t, ps.Save(proj))

	task := &hub.Task{
		Project:     "web-app",
		Description: "Task with container but nil launcher",
		Phase:       hub.PhasePlan,
		Status:      hub.TaskStatusBacklog,
		AgentStatus: hub.AgentStatusIdle,
	}
	require.NoError(t, ts.Save(task))

	// Should succeed even with nil launcher — prompt sending is skipped.
	result, err := bridge.StartPhase(task.ID, hub.PhasePlan)
	require.NoError(t, err)
	assert.NotEmpty(t, result.SessionID)
}
