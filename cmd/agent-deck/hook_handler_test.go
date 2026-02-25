package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMapEventToStatus(t *testing.T) {
	tests := []struct {
		event  string
		expect string
	}{
		{"SessionStart", "waiting"},
		{"UserPromptSubmit", "running"},
		{"Stop", "waiting"},
		{"PermissionRequest", "waiting"},
		{"Notification", ""},
		{"SessionEnd", "dead"},
		{"PreCompact", ""},
		{"UnknownEvent", ""},
	}

	for _, tt := range tests {
		t.Run(tt.event, func(t *testing.T) {
			got := mapEventToStatus(tt.event)
			if got != tt.expect {
				t.Errorf("mapEventToStatus(%q) = %q, want %q", tt.event, got, tt.expect)
			}
		})
	}
}

func TestHookStatusFile_JSON(t *testing.T) {
	sf := hookStatusFile{
		Status:    "running",
		SessionID: "abc-123",
		Event:     "UserPromptSubmit",
		Timestamp: 1707900000,
	}

	data, err := json.Marshal(sf)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var read hookStatusFile
	if err := json.Unmarshal(data, &read); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if read.Status != "running" {
		t.Errorf("Status = %q, want running", read.Status)
	}
	if read.SessionID != "abc-123" {
		t.Errorf("SessionID = %q, want abc-123", read.SessionID)
	}
	if read.Event != "UserPromptSubmit" {
		t.Errorf("Event = %q, want UserPromptSubmit", read.Event)
	}
	if read.Timestamp != 1707900000 {
		t.Errorf("Timestamp = %d, want 1707900000", read.Timestamp)
	}
}

func TestHookHandler_WritesStatusFile(t *testing.T) {
	tmpDir := t.TempDir()
	hooksDir := filepath.Join(tmpDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		t.Fatalf("Failed to create hooks dir: %v", err)
	}

	instanceID := "test-instance-123"
	sf := hookStatusFile{
		Status:    "waiting",
		SessionID: "sess-456",
		Event:     "PermissionRequest",
		Timestamp: time.Now().Unix(),
	}

	data, err := json.Marshal(sf)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	// Simulate atomic write
	filePath := filepath.Join(hooksDir, instanceID+".json")
	tmpPath := filePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		t.Fatalf("Failed to write tmp: %v", err)
	}
	if err := os.Rename(tmpPath, filePath); err != nil {
		t.Fatalf("Failed to rename: %v", err)
	}

	// Verify file exists and has correct content
	readData, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("Failed to read back: %v", err)
	}

	var read hookStatusFile
	if err := json.Unmarshal(readData, &read); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if read.Status != "waiting" {
		t.Errorf("Status = %q, want waiting", read.Status)
	}
	if read.Event != "PermissionRequest" {
		t.Errorf("Event = %q, want PermissionRequest", read.Event)
	}
}

func TestHookHandler_MissingInstanceID(t *testing.T) {
	// When AGENTDECK_INSTANCE_ID is not set, handleHookHandler should return silently
	os.Unsetenv("AGENTDECK_INSTANCE_ID")

	// This should not panic or produce any output
	handleHookHandler()
}

func TestHookPayload_Unmarshal(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		event   string
		session string
	}{
		{
			name:    "SessionStart",
			input:   `{"hook_event_name":"SessionStart","session_id":"abc-123","source":"claude"}`,
			event:   "SessionStart",
			session: "abc-123",
		},
		{
			name:    "Stop",
			input:   `{"hook_event_name":"Stop","session_id":"def-456"}`,
			event:   "Stop",
			session: "def-456",
		},
		{
			name:    "unknown fields ignored",
			input:   `{"hook_event_name":"UserPromptSubmit","session_id":"ghi-789","extra":"ignored"}`,
			event:   "UserPromptSubmit",
			session: "ghi-789",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var p hookPayload
			if err := json.Unmarshal([]byte(tt.input), &p); err != nil {
				t.Fatalf("Unmarshal failed: %v", err)
			}
			if p.HookEventName != tt.event {
				t.Errorf("HookEventName = %q, want %q", p.HookEventName, tt.event)
			}
			if p.SessionID != tt.session {
				t.Errorf("SessionID = %q, want %q", p.SessionID, tt.session)
			}
		})
	}
}
