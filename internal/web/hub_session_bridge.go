package web

import (
	"fmt"
	"sync"

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
	mu          sync.Mutex // serializes saveInstance to prevent load-append-save races
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

func (b *HubSessionBridge) saveInstance(inst *session.Instance) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	storage, err := b.openStorage(b.profile)
	if err != nil {
		return fmt.Errorf("open storage: %w", err)
	}
	defer storage.Close()

	// storageLoader may not implement Save (e.g. in tests with mocks).
	type saver interface {
		Save([]*session.Instance) error
	}
	if s, ok := storage.(saver); ok {
		existing, _, loadErr := storage.LoadWithGroups()
		if loadErr != nil {
			return fmt.Errorf("load existing sessions: %w", loadErr)
		}
		instances := append(existing, inst)
		return s.Save(instances)
	}
	return nil
}

// phasePrompt returns the initial prompt to send to a new session for the given phase.
// TODO: Wire to tmux SendKeys / StartWithMessage when session launch is implemented.
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
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max-3]) + "..."
}
