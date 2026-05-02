package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDetectCopilotSessionFromDisk(t *testing.T) {
	// Create a temporary session-state directory
	tmpDir := t.TempDir()

	// Override getCopilotHomeDir for testing
	origEnv := os.Getenv("COPILOT_CONFIG_DIR")
	os.Setenv("COPILOT_CONFIG_DIR", tmpDir)
	defer os.Setenv("COPILOT_CONFIG_DIR", origEnv)

	sessionID := "test-session-12345678"
	cwd := "/Users/testuser/projects/myapp"
	startTime := time.Now().Add(-5 * time.Second)

	// Create session directory with events.jsonl
	sessionDir := filepath.Join(tmpDir, "session-state", sessionID)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}

	event := map[string]interface{}{
		"type": "session.start",
		"data": map[string]interface{}{
			"sessionId": sessionID,
			"context": map[string]interface{}{
				"cwd": cwd,
			},
			"startTime": startTime.Format(time.RFC3339Nano),
		},
		"timestamp": startTime.Format(time.RFC3339Nano),
	}
	eventJSON, _ := json.Marshal(event)
	eventsPath := filepath.Join(sessionDir, "events.jsonl")
	if err := os.WriteFile(eventsPath, append(eventJSON, '\n'), 0644); err != nil {
		t.Fatal(err)
	}

	// Test: should find the session
	found := detectCopilotSessionFromDisk(cwd, startTime.Add(-10*time.Second))
	if found != sessionID {
		t.Errorf("expected session %q, got %q", sessionID, found)
	}

	// Test: different cwd should not match
	found = detectCopilotSessionFromDisk("/Users/testuser/other", startTime.Add(-10*time.Second))
	if found != "" {
		t.Errorf("expected empty, got %q", found)
	}

	// Test: startedAfter in the future should not match
	found = detectCopilotSessionFromDisk(cwd, time.Now().Add(1*time.Hour))
	if found != "" {
		t.Errorf("expected empty for future startedAfter, got %q", found)
	}
}

func TestDetectCopilotSessionFromDisk_MostRecent(t *testing.T) {
	tmpDir := t.TempDir()

	origEnv := os.Getenv("COPILOT_CONFIG_DIR")
	os.Setenv("COPILOT_CONFIG_DIR", tmpDir)
	defer os.Setenv("COPILOT_CONFIG_DIR", origEnv)

	cwd := "/Users/testuser/projects/myapp"
	oldTime := time.Now().Add(-10 * time.Second)
	newTime := time.Now().Add(-2 * time.Second)

	// Create two sessions with the same cwd
	for _, tc := range []struct {
		id   string
		time time.Time
	}{
		{"old-session-aaaa", oldTime},
		{"new-session-bbbb", newTime},
	} {
		sessionDir := filepath.Join(tmpDir, "session-state", tc.id)
		if err := os.MkdirAll(sessionDir, 0755); err != nil {
			t.Fatal(err)
		}
		event := map[string]interface{}{
			"type": "session.start",
			"data": map[string]interface{}{
				"sessionId": tc.id,
				"context":   map[string]interface{}{"cwd": cwd},
				"startTime": tc.time.Format(time.RFC3339Nano),
			},
			"timestamp": tc.time.Format(time.RFC3339Nano),
		}
		eventJSON, _ := json.Marshal(event)
		eventsPath := filepath.Join(sessionDir, "events.jsonl")
		if err := os.WriteFile(eventsPath, append(eventJSON, '\n'), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Should return the most recent
	found := detectCopilotSessionFromDisk(cwd, oldTime.Add(-1*time.Second))
	if found != "new-session-bbbb" {
		t.Errorf("expected new-session-bbbb, got %q", found)
	}
}

func TestReadCopilotSessionStart(t *testing.T) {
	tmpDir := t.TempDir()
	eventsPath := filepath.Join(tmpDir, "events.jsonl")

	// Valid session.start event
	event := `{"type":"session.start","data":{"sessionId":"abc-123","context":{"cwd":"/tmp/test"},"startTime":"2026-05-01T10:00:00.000Z"},"timestamp":"2026-05-01T10:00:00.000Z"}`
	if err := os.WriteFile(eventsPath, []byte(event+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	evt := readCopilotSessionStart(eventsPath)
	if evt == nil {
		t.Fatal("expected non-nil event")
	}
	if evt.Data.SessionID != "abc-123" {
		t.Errorf("expected abc-123, got %q", evt.Data.SessionID)
	}
	if evt.Data.Context.CWD != "/tmp/test" {
		t.Errorf("expected /tmp/test, got %q", evt.Data.Context.CWD)
	}
}

func TestReadCopilotSessionStart_InvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	eventsPath := filepath.Join(tmpDir, "events.jsonl")

	if err := os.WriteFile(eventsPath, []byte("not json\n"), 0644); err != nil {
		t.Fatal(err)
	}

	evt := readCopilotSessionStart(eventsPath)
	if evt != nil {
		t.Error("expected nil for invalid JSON")
	}
}

func TestReadCopilotSessionStart_WrongType(t *testing.T) {
	tmpDir := t.TempDir()
	eventsPath := filepath.Join(tmpDir, "events.jsonl")

	event := `{"type":"user.message","data":{"content":"hello"}}`
	if err := os.WriteFile(eventsPath, []byte(event+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	evt := readCopilotSessionStart(eventsPath)
	if evt != nil {
		t.Error("expected nil for non-session.start event")
	}
}

func TestReadCopilotSessionStart_EmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	eventsPath := filepath.Join(tmpDir, "events.jsonl")

	if err := os.WriteFile(eventsPath, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	evt := readCopilotSessionStart(eventsPath)
	if evt != nil {
		t.Error("expected nil for empty file")
	}
}

func TestCopilotSessionHasConversationData(t *testing.T) {
	tmpDir := t.TempDir()

	origEnv := os.Getenv("COPILOT_CONFIG_DIR")
	os.Setenv("COPILOT_CONFIG_DIR", tmpDir)
	defer os.Setenv("COPILOT_CONFIG_DIR", origEnv)

	sessionID := "conv-test-session"
	sessionDir := filepath.Join(tmpDir, "session-state", sessionID)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}

	eventsPath := filepath.Join(sessionDir, "events.jsonl")

	// Small file (no conversation)
	if err := os.WriteFile(eventsPath, make([]byte, 1024), 0644); err != nil {
		t.Fatal(err)
	}
	if copilotSessionHasConversationData(sessionID) {
		t.Error("expected false for small file")
	}

	// Large file (has conversation)
	if err := os.WriteFile(eventsPath, make([]byte, 16*1024), 0644); err != nil {
		t.Fatal(err)
	}
	if !copilotSessionHasConversationData(sessionID) {
		t.Error("expected true for large file")
	}

	// Empty session ID
	if copilotSessionHasConversationData("") {
		t.Error("expected false for empty session ID")
	}
}

func TestCopilotOptions_ToArgs_Extended(t *testing.T) {
	tests := []struct {
		name     string
		opts     CopilotOptions
		expected []string
	}{
		{
			name:     "new with model",
			opts:     CopilotOptions{SessionMode: "new", Model: "claude-opus-4.6"},
			expected: []string{"--model", "claude-opus-4.6"},
		},
		{
			name:     "new with allow-all",
			opts:     CopilotOptions{SessionMode: "new", AllowAll: true},
			expected: []string{"--allow-all"},
		},
		{
			name:     "resume with model and allow-all",
			opts:     CopilotOptions{SessionMode: "resume", ResumeSessionID: "s1", Model: "gpt-5", AllowAll: true},
			expected: []string{"--resume", "s1", "--model", "gpt-5", "--allow-all"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := tt.opts.ToArgs()
			if len(args) == 0 && len(tt.expected) == 0 {
				return
			}
			if len(args) != len(tt.expected) {
				t.Errorf("expected %d args, got %d: %v", len(tt.expected), len(args), args)
				return
			}
			for i, a := range args {
				if a != tt.expected[i] {
					t.Errorf("arg[%d]: expected %q, got %q", i, tt.expected[i], a)
				}
			}
		})
	}
}

func TestNewCopilotOptions_WithModelAndAllowAll(t *testing.T) {
	config := &UserConfig{}
	config.Copilot.DefaultModel = "claude-opus-4.6"
	config.Copilot.AllowAll = true

	opts := NewCopilotOptions(config)
	if opts.Model != "claude-opus-4.6" {
		t.Errorf("expected model claude-opus-4.6, got %q", opts.Model)
	}
	if !opts.AllowAll {
		t.Error("expected AllowAll true")
	}
	if opts.SessionMode != "new" {
		t.Errorf("expected session mode new, got %q", opts.SessionMode)
	}
}

func TestNewCopilotOptions_NilConfig(t *testing.T) {
	opts := NewCopilotOptions(nil)
	if opts.Model != "" {
		t.Errorf("expected empty model, got %q", opts.Model)
	}
	if opts.AllowAll {
		t.Error("expected AllowAll false")
	}
}

func TestBuildCopilotCommand_Fresh(t *testing.T) {
	inst := &Instance{
		Tool:    "copilot",
		Command: "copilot",
	}

	cmd := buildCopilotCommand(inst)
	if !strings.HasSuffix(cmd, "copilot") {
		t.Errorf("expected command ending with 'copilot', got %q", cmd)
	}
}

func TestBuildCopilotCommand_WithModel(t *testing.T) {
	inst := &Instance{
		Tool:         "copilot",
		Command:      "copilot",
		CopilotModel: "claude-opus-4.6",
	}

	cmd := buildCopilotCommand(inst)
	if !strings.HasSuffix(cmd, "copilot --model claude-opus-4.6") {
		t.Errorf("expected command ending with 'copilot --model claude-opus-4.6', got %q", cmd)
	}
}

func TestBuildCopilotCommand_WithAllowAll(t *testing.T) {
	inst := &Instance{
		Tool:            "copilot",
		Command:         "copilot",
		CopilotAllowAll: true,
	}

	cmd := buildCopilotCommand(inst)
	if !strings.HasSuffix(cmd, "copilot --allow-all") {
		t.Errorf("expected command ending with 'copilot --allow-all', got %q", cmd)
	}
}

func TestBuildCopilotCommand_Resume(t *testing.T) {
	tmpDir := t.TempDir()
	origEnv := os.Getenv("COPILOT_CONFIG_DIR")
	os.Setenv("COPILOT_CONFIG_DIR", tmpDir)
	defer os.Setenv("COPILOT_CONFIG_DIR", origEnv)

	// Create session with enough data
	sessionID := "resume-session-test"
	sessionDir := filepath.Join(tmpDir, "session-state", sessionID)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}
	eventsPath := filepath.Join(sessionDir, "events.jsonl")
	// Write a large file to simulate conversation data
	if err := os.WriteFile(eventsPath, make([]byte, 16*1024), 0644); err != nil {
		t.Fatal(err)
	}

	inst := &Instance{
		Tool:              "copilot",
		Command:           "copilot",
		CopilotSessionID:  sessionID,
		CopilotModel:      "gpt-5",
		CopilotAllowAll:   true,
	}

	cmd := buildCopilotCommand(inst)
	expected := "copilot --resume " + sessionID + " --model gpt-5 --allow-all"
	if !strings.HasSuffix(cmd, expected) {
		t.Errorf("expected command ending with %q, got %q", expected, cmd)
	}
}
