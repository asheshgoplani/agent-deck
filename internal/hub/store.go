package hub

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// TaskStore provides filesystem JSON-based CRUD for Task records.
// Each task is stored as an individual JSON file (e.g. t-001.json) under basePath/tasks/.
type TaskStore struct {
	mu      sync.RWMutex
	taskDir string
}

// validTaskID returns true if id is safe to use as a filename component.
func validTaskID(id string) bool {
	return id != "" && id != "." && id != ".." &&
		!strings.Contains(id, "/") && !strings.Contains(id, "\\")
}

// NewTaskStore creates a TaskStore backed by the given base directory.
// It creates the tasks/ subdirectory if it does not exist.
func NewTaskStore(basePath string) (*TaskStore, error) {
	taskDir := filepath.Join(basePath, "tasks")
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		return nil, fmt.Errorf("create task directory: %w", err)
	}
	return &TaskStore{taskDir: taskDir}, nil
}

// List returns all tasks sorted by creation time (oldest first).
func (s *TaskStore) List() ([]*Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries, err := os.ReadDir(s.taskDir)
	if err != nil {
		return nil, fmt.Errorf("read task directory: %w", err)
	}

	var tasks []*Task
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		task, err := s.readTaskFile(entry.Name())
		if err != nil {
			continue // skip corrupt files
		}
		tasks = append(tasks, task)
	}

	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].CreatedAt.Before(tasks[j].CreatedAt)
	})

	return tasks, nil
}

// Get retrieves a single task by ID.
func (s *TaskStore) Get(id string) (*Task, error) {
	if !validTaskID(id) {
		return nil, fmt.Errorf("invalid task ID: %q", id)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.readTaskFile(id + ".json")
}

// Save persists a task. If the task has no ID, one is generated.
// UpdatedAt is always set to now.
func (s *TaskStore) Save(task *Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if task.ID == "" {
		id, err := s.nextID()
		if err != nil {
			return err
		}
		task.ID = id
	} else if !validTaskID(task.ID) {
		return fmt.Errorf("invalid task ID: %q", task.ID)
	}
	if task.CreatedAt.IsZero() {
		task.CreatedAt = time.Now().UTC()
	}
	task.UpdatedAt = time.Now().UTC()

	data, err := json.MarshalIndent(task, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal task: %w", err)
	}

	path := filepath.Join(s.taskDir, task.ID+".json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write task file: %w", err)
	}

	return nil
}

// Delete removes a task by ID.
func (s *TaskStore) Delete(id string) error {
	if !validTaskID(id) {
		return fmt.Errorf("invalid task ID: %q", id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(s.taskDir, id+".json")
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("task not found: %s", id)
		}
		return fmt.Errorf("delete task file: %w", err)
	}
	return nil
}

// oldStatusMigration maps legacy agent-level status values to the new
// separated TaskStatus + AgentStatus pair.
var oldStatusMigration = map[string]struct {
	taskStatus  TaskStatus
	agentStatus AgentStatus
}{
	"thinking": {TaskStatusRunning, AgentStatusThinking},
	"waiting":  {TaskStatusPlanning, AgentStatusWaiting},
	"running":  {TaskStatusRunning, AgentStatusRunning},
	"idle":     {TaskStatusBacklog, AgentStatusIdle},
	"error":    {TaskStatusRunning, AgentStatusError},
	"complete": {TaskStatusDone, AgentStatusComplete},
}

// migrateTask detects old-format status values and migrates them.
// Returns true if migration was applied.
func migrateTask(task *Task) bool {
	m, ok := oldStatusMigration[string(task.Status)]
	if !ok {
		return false
	}
	// Only migrate if AgentStatus is empty (old format didn't have it).
	if task.AgentStatus != "" {
		return false
	}
	task.Status = m.taskStatus
	task.AgentStatus = m.agentStatus
	return true
}

func (s *TaskStore) readTaskFile(filename string) (*Task, error) {
	data, err := os.ReadFile(filepath.Join(s.taskDir, filename))
	if err != nil {
		return nil, fmt.Errorf("read task file %s: %w", filename, err)
	}
	var task Task
	if err := json.Unmarshal(data, &task); err != nil {
		return nil, fmt.Errorf("unmarshal task %s: %w", filename, err)
	}
	migrateTask(&task)
	return &task, nil
}

// nextID scans existing task files and returns the next sequential ID.
// Must be called with s.mu held.
func (s *TaskStore) nextID() (string, error) {
	entries, err := os.ReadDir(s.taskDir)
	if err != nil {
		return "", fmt.Errorf("read task directory: %w", err)
	}

	maxNum := 0
	for _, entry := range entries {
		name := strings.TrimSuffix(entry.Name(), ".json")
		if !strings.HasPrefix(name, "t-") {
			continue
		}
		numStr := strings.TrimPrefix(name, "t-")
		num, err := strconv.Atoi(numStr)
		if err != nil {
			continue
		}
		if num > maxNum {
			maxNum = num
		}
	}

	return fmt.Sprintf("t-%03d", maxNum+1), nil
}
