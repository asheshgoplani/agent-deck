package hub

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewTaskStoreCreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	basePath := filepath.Join(dir, "hub")

	store, err := NewTaskStore(basePath)
	if err != nil {
		t.Fatalf("NewTaskStore: %v", err)
	}

	info, err := os.Stat(filepath.Join(basePath, "tasks"))
	if err != nil {
		t.Fatalf("tasks directory not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("tasks path is not a directory")
	}
	_ = store
}

func TestSaveAndGet(t *testing.T) {
	store := newTestStore(t)

	task := &Task{
		Project:     "api-service",
		Description: "Fix auth bug",
		Phase:       PhaseExecute,
		Status:      TaskStatusRunning,
	}

	if err := store.Save(task); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if task.ID == "" {
		t.Fatal("expected task ID to be set after save")
	}
	if task.ID != "t-001" {
		t.Fatalf("expected ID t-001, got %s", task.ID)
	}
	if task.CreatedAt.IsZero() {
		t.Fatal("expected CreatedAt to be set")
	}
	if task.UpdatedAt.IsZero() {
		t.Fatal("expected UpdatedAt to be set")
	}

	got, err := store.Get(task.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != task.ID {
		t.Fatalf("expected ID %s, got %s", task.ID, got.ID)
	}
	if got.Project != "api-service" {
		t.Fatalf("expected project api-service, got %s", got.Project)
	}
	if got.Description != "Fix auth bug" {
		t.Fatalf("expected description 'Fix auth bug', got %s", got.Description)
	}
}

func TestSavePreservesExistingID(t *testing.T) {
	store := newTestStore(t)

	task := &Task{
		ID:          "t-custom",
		Project:     "web-app",
		Description: "Custom ID task",
		Phase:       PhasePlan,
		Status:      TaskStatusBacklog,
	}

	if err := store.Save(task); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if task.ID != "t-custom" {
		t.Fatalf("expected ID t-custom, got %s", task.ID)
	}
}

func TestSaveUpdatesExistingTask(t *testing.T) {
	store := newTestStore(t)

	task := &Task{
		Project:     "api-service",
		Description: "Original",
		Phase:       PhaseBrainstorm,
		Status:      TaskStatusRunning,
		AgentStatus: AgentStatusThinking,
	}
	if err := store.Save(task); err != nil {
		t.Fatalf("Save: %v", err)
	}

	firstUpdated := task.UpdatedAt
	time.Sleep(time.Millisecond)

	task.Description = "Updated"
	task.Phase = PhaseExecute
	if err := store.Save(task); err != nil {
		t.Fatalf("Save update: %v", err)
	}

	got, err := store.Get(task.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Description != "Updated" {
		t.Fatalf("expected description 'Updated', got %s", got.Description)
	}
	if !got.UpdatedAt.After(firstUpdated) {
		t.Fatal("expected UpdatedAt to advance on update")
	}
}

func TestList(t *testing.T) {
	store := newTestStore(t)

	for _, desc := range []string{"First", "Second", "Third"} {
		task := &Task{
			Project:     "test",
			Description: desc,
			Phase:       PhaseExecute,
			Status:      TaskStatusRunning,
		}
		if err := store.Save(task); err != nil {
			t.Fatalf("Save %s: %v", desc, err)
		}
		time.Sleep(time.Millisecond) // ensure different timestamps
	}

	tasks, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(tasks) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(tasks))
	}
	if tasks[0].Description != "First" {
		t.Fatalf("expected first task 'First', got %s", tasks[0].Description)
	}
	if tasks[2].Description != "Third" {
		t.Fatalf("expected last task 'Third', got %s", tasks[2].Description)
	}
}

func TestListEmpty(t *testing.T) {
	store := newTestStore(t)

	tasks, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("expected 0 tasks, got %d", len(tasks))
	}
}

func TestDelete(t *testing.T) {
	store := newTestStore(t)

	task := &Task{
		Project:     "test",
		Description: "To delete",
		Phase:       PhaseExecute,
		Status:      TaskStatusDone,
	}
	if err := store.Save(task); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := store.Delete(task.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err := store.Get(task.ID)
	if err == nil {
		t.Fatal("expected error after delete, got nil")
	}
}

func TestDeleteNotFound(t *testing.T) {
	store := newTestStore(t)

	err := store.Delete("t-nonexistent")
	if err == nil {
		t.Fatal("expected error for non-existent task, got nil")
	}
}

func TestGetNotFound(t *testing.T) {
	store := newTestStore(t)

	_, err := store.Get("t-nonexistent")
	if err == nil {
		t.Fatal("expected error for non-existent task, got nil")
	}
}

func TestNextIDSequence(t *testing.T) {
	store := newTestStore(t)

	for i := 1; i <= 5; i++ {
		task := &Task{
			Project:     "test",
			Description: "task",
			Phase:       PhaseExecute,
			Status:      TaskStatusRunning,
		}
		if err := store.Save(task); err != nil {
			t.Fatalf("Save %d: %v", i, err)
		}
	}

	// Delete middle task and add new one
	if err := store.Delete("t-003"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	task := &Task{
		Project:     "test",
		Description: "after gap",
		Phase:       PhaseExecute,
		Status:      TaskStatusRunning,
	}
	if err := store.Save(task); err != nil {
		t.Fatalf("Save after gap: %v", err)
	}
	// Should be t-006 (max was t-005, not reusing t-003)
	if task.ID != "t-006" {
		t.Fatalf("expected ID t-006, got %s", task.ID)
	}
}

func TestMigrateOldStatusOnRead(t *testing.T) {
	store := newTestStore(t)

	// Write old-format JSON directly to simulate legacy data.
	oldJSON := `{
		"id": "t-001",
		"sessionId": "",
		"status": "thinking",
		"project": "api-service",
		"description": "Legacy task",
		"phase": "execute",
		"createdAt": "2026-01-01T00:00:00Z",
		"updatedAt": "2026-01-01T00:00:00Z"
	}`
	taskFile := filepath.Join(store.taskDir, "t-001.json")
	if err := os.WriteFile(taskFile, []byte(oldJSON), 0o644); err != nil {
		t.Fatalf("write old task: %v", err)
	}

	task, err := store.Get("t-001")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if task.Status != TaskStatusRunning {
		t.Fatalf("expected migrated status 'running', got %s", task.Status)
	}
	if task.AgentStatus != AgentStatusThinking {
		t.Fatalf("expected migrated agentStatus 'thinking', got %s", task.AgentStatus)
	}
}

func TestMigrateAllOldStatuses(t *testing.T) {
	store := newTestStore(t)

	cases := []struct {
		oldStatus       string
		wantTaskStatus  TaskStatus
		wantAgentStatus AgentStatus
	}{
		{"thinking", TaskStatusRunning, AgentStatusThinking},
		{"waiting", TaskStatusPlanning, AgentStatusWaiting},
		{"running", TaskStatusRunning, AgentStatusRunning},
		{"idle", TaskStatusBacklog, AgentStatusIdle},
		{"error", TaskStatusRunning, AgentStatusError},
		{"complete", TaskStatusDone, AgentStatusComplete},
	}

	for i, tc := range cases {
		id := fmt.Sprintf("t-%03d", i+1)
		raw := fmt.Sprintf(`{"id":"%s","status":"%s","project":"test","description":"test","phase":"execute","createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}`, id, tc.oldStatus)
		if err := os.WriteFile(filepath.Join(store.taskDir, id+".json"), []byte(raw), 0o644); err != nil {
			t.Fatalf("write %s: %v", id, err)
		}

		task, err := store.Get(id)
		if err != nil {
			t.Fatalf("Get %s: %v", id, err)
		}
		if task.Status != tc.wantTaskStatus {
			t.Fatalf("%s: expected status %s, got %s", tc.oldStatus, tc.wantTaskStatus, task.Status)
		}
		if task.AgentStatus != tc.wantAgentStatus {
			t.Fatalf("%s: expected agentStatus %s, got %s", tc.oldStatus, tc.wantAgentStatus, task.AgentStatus)
		}
	}
}

func TestNewStatusNotMigrated(t *testing.T) {
	store := newTestStore(t)

	// New-format JSON should not be modified.
	newJSON := `{
		"id": "t-001",
		"status": "review",
		"agentStatus": "thinking",
		"project": "test",
		"description": "New format",
		"phase": "review",
		"createdAt": "2026-01-01T00:00:00Z",
		"updatedAt": "2026-01-01T00:00:00Z"
	}`
	if err := os.WriteFile(filepath.Join(store.taskDir, "t-001.json"), []byte(newJSON), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	task, err := store.Get("t-001")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if task.Status != TaskStatusReview {
		t.Fatalf("expected status review, got %s", task.Status)
	}
	if task.AgentStatus != AgentStatusThinking {
		t.Fatalf("expected agentStatus thinking, got %s", task.AgentStatus)
	}
}

func TestNewTaskStatusValues(t *testing.T) {
	store := newTestStore(t)

	for _, tc := range []struct {
		status TaskStatus
		agent  AgentStatus
	}{
		{TaskStatusBacklog, AgentStatusIdle},
		{TaskStatusPlanning, AgentStatusWaiting},
		{TaskStatusRunning, AgentStatusRunning},
		{TaskStatusReview, AgentStatusThinking},
		{TaskStatusDone, AgentStatusComplete},
	} {
		task := &Task{
			Project:     "test",
			Description: "status " + string(tc.status),
			Phase:       PhaseExecute,
			Status:      tc.status,
			AgentStatus: tc.agent,
		}
		if err := store.Save(task); err != nil {
			t.Fatalf("Save %s: %v", tc.status, err)
		}
		got, err := store.Get(task.ID)
		if err != nil {
			t.Fatalf("Get %s: %v", tc.status, err)
		}
		if got.Status != tc.status {
			t.Fatalf("expected status %s, got %s", tc.status, got.Status)
		}
		if got.AgentStatus != tc.agent {
			t.Fatalf("expected agentStatus %s, got %s", tc.agent, got.AgentStatus)
		}
	}
}

func TestSaveAndGetNewFields(t *testing.T) {
	store := newTestStore(t)

	task := &Task{
		Project:     "web-app",
		Description: "Test new fields",
		Phase:       PhaseExecute,
		Status:      TaskStatusRunning,
		AgentStatus: AgentStatusThinking,
		Skills:      []string{"git", "docker"},
		MCPs:        []string{"filesystem"},
		Diff:        &DiffInfo{Files: 3, Add: 42, Del: 7},
		Container:   "sandbox-web",
		AskQuestion: "Which auth method?",
		Sessions: []Session{
			{ID: "s-1", Phase: PhasePlan, Status: "complete", Duration: "5m", Summary: "Planned approach"},
			{ID: "s-2", Phase: PhaseExecute, Status: "active", Duration: "12m"},
		},
	}

	if err := store.Save(task); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := store.Get(task.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.AgentStatus != AgentStatusThinking {
		t.Fatalf("expected agentStatus thinking, got %s", got.AgentStatus)
	}
	if len(got.Skills) != 2 || got.Skills[0] != "git" {
		t.Fatalf("expected skills [git docker], got %v", got.Skills)
	}
	if got.Diff == nil || got.Diff.Files != 3 {
		t.Fatalf("expected diff with 3 files, got %v", got.Diff)
	}
	if got.Container != "sandbox-web" {
		t.Fatalf("expected container sandbox-web, got %s", got.Container)
	}
	if got.AskQuestion != "Which auth method?" {
		t.Fatalf("expected askQuestion, got %s", got.AskQuestion)
	}
	if len(got.Sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(got.Sessions))
	}
	if got.Sessions[0].Summary != "Planned approach" {
		t.Fatalf("expected session summary, got %s", got.Sessions[0].Summary)
	}
}

func newTestStore(t *testing.T) *TaskStore {
	t.Helper()
	store, err := NewTaskStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewTaskStore: %v", err)
	}
	return store
}
