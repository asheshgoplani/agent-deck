package hub

import "time"

// Phase represents the workflow phase of a task.
type Phase string

const (
	PhaseBrainstorm Phase = "brainstorm"
	PhasePlan       Phase = "plan"
	PhaseExecute    Phase = "execute"
	PhaseReview     Phase = "review"
)

// TaskStatus represents the current state of a task.
type TaskStatus string

const (
	TaskStatusThinking TaskStatus = "thinking"
	TaskStatusWaiting  TaskStatus = "waiting"
	TaskStatusRunning  TaskStatus = "running"
	TaskStatusIdle     TaskStatus = "idle"
	TaskStatusError    TaskStatus = "error"
	TaskStatusComplete TaskStatus = "complete"
)

// Task wraps a session with orchestration metadata.
type Task struct {
	ID           string     `json:"id"`
	SessionID    string     `json:"sessionId"`
	TmuxSession  string     `json:"tmuxSession"`
	Status       TaskStatus `json:"status"`
	Project      string     `json:"project"`
	Description  string     `json:"description"`
	Phase        Phase      `json:"phase"`
	Branch       string     `json:"branch,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
	ParentTaskID string     `json:"parentTaskId,omitempty"`
}

// Project defines a workspace that tasks can be routed to.
type Project struct {
	Name        string   `json:"name"        yaml:"name"`
	Path        string   `json:"path"        yaml:"path"`
	Keywords    []string `json:"keywords"    yaml:"keywords"`
	Container   string   `json:"container,omitempty"   yaml:"container,omitempty"`
	DefaultMCPs []string `json:"defaultMcps,omitempty" yaml:"default_mcps,omitempty"`
}

// RouteResult describes a keyword-match routing result.
type RouteResult struct {
	Project         string   `json:"project"`
	Confidence      float64  `json:"confidence"`
	MatchedKeywords []string `json:"matchedKeywords"`
}
