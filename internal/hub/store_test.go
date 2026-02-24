package hub

import (
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
		Status:      TaskStatusIdle,
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
		Status:      TaskStatusThinking,
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
		Status:      TaskStatusComplete,
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

func newTestStore(t *testing.T) *TaskStore {
	t.Helper()
	store, err := NewTaskStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewTaskStore: %v", err)
	}
	return store
}
