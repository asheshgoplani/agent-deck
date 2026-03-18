package session

import (
	"os/exec"
	"testing"
)

func TestDiscoverExistingTmuxSessions(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	// Should not error even with no existing instances
	discovered, err := DiscoverExistingTmuxSessions([]*Instance{})
	if err != nil {
		t.Logf("DiscoverExistingTmuxSessions error (may be expected): %v", err)
	}
	_ = discovered
}

func TestDiscoverSkipsAgentDeckSessions(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	// Create a mock existing instance
	existing := []*Instance{
		{
			ID:          "test-123",
			Title:       "existing-session",
			ProjectPath: "/tmp",
		},
	}

	discovered, err := DiscoverExistingTmuxSessions(existing)
	if err != nil {
		t.Logf("Error (may be expected): %v", err)
	}

	// Should not include sessions that are already tracked
	for _, d := range discovered {
		if d.Title == "existing-session" {
			t.Error("Should not discover already tracked sessions")
		}
	}
}

func TestGroupByProjectDeep(t *testing.T) {
	instances := []*Instance{
		{Title: "s1", ProjectPath: "/home/user/projects/devops"},
		{Title: "s2", ProjectPath: "/home/user/projects/frontend"},
		{Title: "s3", ProjectPath: "/home/user/personal/blog"},
		{Title: "s4", ProjectPath: "/tmp"},
	}

	groups := GroupByProject(instances)

	// Check grouping
	if _, ok := groups["projects"]; !ok {
		t.Error("Expected 'projects' group")
	}
	if _, ok := groups["personal"]; !ok {
		t.Error("Expected 'personal' group")
	}
}

func TestFilterByQueryCaseInsensitive(t *testing.T) {
	instances := []*Instance{
		{Title: "DevOps-Claude", ProjectPath: "/tmp", Tool: "claude"},
		{Title: "frontend-shell", ProjectPath: "/tmp", Tool: "shell"},
	}

	// Should match case-insensitively
	result := FilterByQuery(instances, "DEVOPS")
	if len(result) != 1 {
		t.Errorf("Expected 1 result for 'DEVOPS', got %d", len(result))
	}

	result = FilterByQuery(instances, "Claude")
	if len(result) != 1 {
		t.Errorf("Expected 1 result for 'Claude', got %d", len(result))
	}
}

func TestFilterByQueryByInstanceID(t *testing.T) {
	instances := []*Instance{
		{ID: "abc12345-1710777600", Title: "session-one", ProjectPath: "/tmp", Tool: "claude"},
		{ID: "def67890-1710777601", Title: "session-two", ProjectPath: "/tmp", Tool: "shell"},
		{ID: "abc12345-1710777602", Title: "session-three", ProjectPath: "/home", Tool: "gemini"},
	}

	// Full hex prefix match
	result := FilterByQuery(instances, "def67890")
	if len(result) != 1 || result[0].Title != "session-two" {
		t.Errorf("Expected 1 result for 'def67890', got %d", len(result))
	}

	// Partial ID prefix match
	result = FilterByQuery(instances, "abc12345")
	if len(result) != 2 {
		t.Errorf("Expected 2 results for 'abc12345' (shared prefix), got %d", len(result))
	}

	// Full ID match (hex + timestamp)
	result = FilterByQuery(instances, "abc12345-1710777600")
	if len(result) != 1 || result[0].Title != "session-one" {
		t.Errorf("Expected 1 result for full ID, got %d", len(result))
	}

	// No match
	result = FilterByQuery(instances, "zzz99999")
	if len(result) != 0 {
		t.Errorf("Expected 0 results for 'zzz99999', got %d", len(result))
	}
}

func TestDetectToolFromName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"Claude uppercase", "CLAUDE-session", "claude"},
		{"claude lowercase", "my-claude-session", "claude"},
		{"Gemini mixed case", "Gemini-AI", "gemini"},
		{"OpenCode", "opencode-session", "opencode"},
		{"Codex", "codex-test", "codex"},
		{"Unknown", "random-session", "shell"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := detectToolFromName(tt.input)
			if result != tt.expected {
				t.Errorf("detectToolFromName(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestExtractProjectName(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected string
	}{
		{"Deep path", "/home/user/projects/devops", "projects"},
		{"Home path", "/home/user/personal/blog", "personal"},
		{"Root level", "/tmp", "tmp"},
		{"Single level", "/home", "home"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractProjectName(tt.path)
			if result != tt.expected {
				t.Errorf("extractProjectName(%q) = %q, want %q", tt.path, result, tt.expected)
			}
		})
	}
}
