package session

import (
	"os"
	"testing"
)

func TestNewInstance(t *testing.T) {
	inst := NewInstance("test-session", "/tmp/project")

	if inst.Title != "test-session" {
		t.Errorf("Title = %s, want test-session", inst.Title)
	}
	if inst.ProjectPath != "/tmp/project" {
		t.Errorf("ProjectPath = %s, want /tmp/project", inst.ProjectPath)
	}
	if inst.ID == "" {
		t.Error("ID should not be empty")
	}
	if inst.Status != StatusIdle {
		t.Errorf("Status = %s, want idle", inst.Status)
	}
	if inst.Tool != "shell" {
		t.Errorf("Tool = %s, want shell", inst.Tool)
	}
}

func TestGenerateID(t *testing.T) {
	id1 := GenerateID()
	id2 := GenerateID()

	if id1 == "" || id2 == "" {
		t.Error("generateID should not return empty string")
	}
	if id1 == id2 {
		t.Error("generateID should return unique IDs")
	}
}

func TestStorageSaveLoad(t *testing.T) {
	storage := newTestStorage(t)

	// Create test instances
	instances := []*Instance{
		NewInstance("session-1", "/tmp/project1"),
		NewInstance("session-2", "/tmp/project2"),
	}

	// Save
	err := storage.Save(instances)
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Verify db file exists
	if _, err := os.Stat(storage.dbPath); os.IsNotExist(err) {
		t.Fatal("state.db was not created")
	}

	// Load
	loaded, err := storage.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if len(loaded) != 2 {
		t.Errorf("Expected 2 instances, got %d", len(loaded))
	}
}

func TestOpenCodeFieldsSerialization(t *testing.T) {
	storage := newTestStorage(t)

	// Create test instance with OpenCode fields populated
	inst := NewInstance("opencode-test", "/tmp/opencode-project")
	inst.Tool = "opencode"
	inst.OpenCodeSessionID = "ses_test123abc"

	instances := []*Instance{inst}

	// Save
	err := storage.Save(instances)
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Load
	loaded, err := storage.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if len(loaded) != 1 {
		t.Fatalf("Expected 1 instance, got %d", len(loaded))
	}

	// Verify OpenCode fields are preserved
	if loaded[0].OpenCodeSessionID != "ses_test123abc" {
		t.Errorf("OpenCodeSessionID = %q, want %q", loaded[0].OpenCodeSessionID, "ses_test123abc")
	}
	if loaded[0].Tool != "opencode" {
		t.Errorf("Tool = %q, want %q", loaded[0].Tool, "opencode")
	}

	t.Logf("✅ OpenCode fields correctly serialized and deserialized")
}

func TestFilterByQuery(t *testing.T) {
	instances := []*Instance{
		{Title: "devops-claude", ProjectPath: "/home/user/devops", Tool: "claude"},
		{Title: "frontend-shell", ProjectPath: "/home/user/frontend", Tool: "shell"},
		{Title: "backend-opencode", ProjectPath: "/home/user/backend", Tool: "opencode"},
	}

	tests := []struct {
		query    string
		expected int
	}{
		{"devops", 1},
		{"claude", 1},
		{"frontend", 1},
		{"user", 3},
		{"xyz", 0},
		{"", 3},
	}

	for _, tt := range tests {
		result := FilterByQuery(instances, tt.query)
		if len(result) != tt.expected {
			t.Errorf("FilterByQuery(%s) returned %d results, want %d", tt.query, len(result), tt.expected)
		}
	}
}

func TestGroupByProject(t *testing.T) {
	instances := []*Instance{
		{Title: "session-1", ProjectPath: "/home/user/projects/devops"},
		{Title: "session-2", ProjectPath: "/home/user/projects/frontend"},
		{Title: "session-3", ProjectPath: "/home/user/personal/blog"},
	}

	groups := GroupByProject(instances)

	if len(groups) != 2 {
		t.Errorf("Expected 2 groups, got %d", len(groups))
	}

	if len(groups["projects"]) != 2 {
		t.Errorf("Expected 2 sessions in projects, got %d", len(groups["projects"]))
	}

	if len(groups["personal"]) != 1 {
		t.Errorf("Expected 1 session in personal, got %d", len(groups["personal"]))
	}
}
