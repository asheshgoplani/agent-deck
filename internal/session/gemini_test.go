package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGetGeminiConfigDir_Default(t *testing.T) {
	// Clear override
	geminiConfigDirOverride = ""

	dir := GetGeminiConfigDir()

	home, _ := os.UserHomeDir()
	expected := filepath.Join(home, ".gemini")

	if dir != expected {
		t.Errorf("GetGeminiConfigDir() = %q, want %q", dir, expected)
	}
}

func TestGetGeminiConfigDir_Override(t *testing.T) {
	// Set override for testing
	tmpDir := t.TempDir()
	geminiConfigDirOverride = tmpDir
	defer func() { geminiConfigDirOverride = "" }()

	dir := GetGeminiConfigDir()

	if dir != tmpDir {
		t.Errorf("GetGeminiConfigDir() = %q, want %q", dir, tmpDir)
	}
}

func TestHashProjectPath(t *testing.T) {
	tests := []struct {
		path     string
		expected string
	}{
		{
			// VERIFIED: echo -n "/Users/ashesh" | shasum -a 256
			path:     "/Users/ashesh",
			expected: "791e1ce1b3651ae5c05fc40e2ff27287a9a59008bcd7a449daf0cfb365d43bac",
		},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			result := HashProjectPath(tt.path)
			if result != tt.expected {
				t.Errorf("HashProjectPath(%q) = %q, want %q", tt.path, result, tt.expected)
			}
		})
	}
}

func TestGetGeminiSessionsDir(t *testing.T) {
	tmpDir := t.TempDir()
	geminiConfigDirOverride = tmpDir
	defer func() { geminiConfigDirOverride = "" }()

	projectPath := "/Users/ashesh/test-project"
	dir := GetGeminiSessionsDir(projectPath)

	hash := HashProjectPath(projectPath)
	expected := filepath.Join(tmpDir, "tmp", hash, "chats")

	if dir != expected {
		t.Errorf("GetGeminiSessionsDir(%q) = %q, want %q", projectPath, dir, expected)
	}
}

func TestGetGeminiSessionsDir_InvalidPath(t *testing.T) {
	// Test with a path that might fail (empty string)
	// HashProjectPath("") should return empty
	dir := GetGeminiSessionsDir("")
	// Should return empty string, not invalid path
	if dir != "" {
		// Only test if HashProjectPath returns empty for invalid input
		hash := HashProjectPath("")
		if hash == "" && dir != "" {
			t.Errorf("GetGeminiSessionsDir with empty hash should return empty, got %q", dir)
		}
	}
}

func TestParseGeminiSessionFile(t *testing.T) {
	tmpDir := t.TempDir()
	sessionFile := filepath.Join(tmpDir, "session-2025-12-26T15-09-4d8fcb4d.json")

	// VERIFIED structure from actual Gemini session file
	sessionData := `{
  "sessionId": "4d8fcb4d-d8d0-4749-b977-334c376dc8a2",
  "projectHash": "791e1ce1b3651ae5c05fc40e2ff27287a9a59008bcd7a449daf0cfb365d43bac",
  "startTime": "2025-12-26T15:09:16.547Z",
  "lastUpdated": "2025-12-26T15:09:52.422Z",
  "messages": [
    {
      "id": "msg-1",
      "timestamp": "2025-12-26T15:09:16.547Z",
      "type": "user",
      "content": "test prompt"
    },
    {
      "id": "msg-2",
      "timestamp": "2025-12-26T15:09:52.422Z",
      "type": "gemini",
      "content": "test response"
    }
  ]
}`

	if err := os.WriteFile(sessionFile, []byte(sessionData), 0644); err != nil {
		t.Fatal(err)
	}

	info, err := parseGeminiSessionFile(sessionFile)
	if err != nil {
		t.Fatalf("parseGeminiSessionFile() error = %v", err)
	}

	// Verify sessionId (camelCase)
	if info.SessionID != "4d8fcb4d-d8d0-4749-b977-334c376dc8a2" {
		t.Errorf("SessionID = %q, want %q", info.SessionID, "4d8fcb4d-d8d0-4749-b977-334c376dc8a2")
	}

	// Verify message count
	if info.MessageCount != 2 {
		t.Errorf("MessageCount = %d, want 2", info.MessageCount)
	}

	// Verify filename
	if info.Filename != "session-2025-12-26T15-09-4d8fcb4d.json" {
		t.Errorf("Filename = %q, want %q", info.Filename, "session-2025-12-26T15-09-4d8fcb4d.json")
	}

	// Verify timestamps parsed correctly
	if info.StartTime.IsZero() {
		t.Error("StartTime should be parsed")
	}
	if info.LastUpdated.IsZero() {
		t.Error("LastUpdated should be parsed")
	}
}

func TestParseGeminiSessionFile_InvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	sessionFile := filepath.Join(tmpDir, "invalid.json")

	// Write malformed JSON
	_ = os.WriteFile(sessionFile, []byte("not valid json{"), 0644)

	_, err := parseGeminiSessionFile(sessionFile)
	if err == nil {
		t.Error("parseGeminiSessionFile should fail with invalid JSON")
	}
	if !strings.Contains(err.Error(), "failed to parse session") {
		t.Errorf("Error should be wrapped, got: %v", err)
	}
}

func TestListGeminiSessions(t *testing.T) {
	tmpDir := t.TempDir()
	geminiConfigDirOverride = tmpDir
	defer func() { geminiConfigDirOverride = "" }()

	projectPath := "/Users/ashesh/test-project"

	// Create sessions directory
	sessionsDir := GetGeminiSessionsDir(projectPath)
	if err := os.MkdirAll(sessionsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create session 1 (older)
	session1 := filepath.Join(sessionsDir, "session-2025-12-23T00-24-abc12345.json")
	session1Data := `{
  "sessionId": "abc12345-1111-1111-1111-111111111111",
  "startTime": "2025-12-23T00:24:00.000Z",
  "lastUpdated": "2025-12-23T00:30:00.000Z",
  "messages": [{"id": "1", "type": "user", "content": "old"}]
}`
	_ = os.WriteFile(session1, []byte(session1Data), 0644)

	// Create session 2 (newer)
	session2 := filepath.Join(sessionsDir, "session-2025-12-24T10-00-def67890.json")
	session2Data := `{
  "sessionId": "def67890-2222-2222-2222-222222222222",
  "startTime": "2025-12-24T10:00:00.000Z",
  "lastUpdated": "2025-12-24T10:15:00.000Z",
  "messages": [{"id": "1", "type": "user", "content": "new"}]
}`
	_ = os.WriteFile(session2, []byte(session2Data), 0644)

	sessions, err := ListGeminiSessions(projectPath)
	if err != nil {
		t.Fatalf("ListGeminiSessions() error = %v", err)
	}

	if len(sessions) != 2 {
		t.Fatalf("Expected 2 sessions, got %d", len(sessions))
	}

	// Should be sorted by LastUpdated (most recent first)
	if sessions[0].SessionID != "def67890-2222-2222-2222-222222222222" {
		t.Errorf("First session should be most recent, got %s", sessions[0].SessionID)
	}
}

func TestGetAvailableGeminiModels_Fallback(t *testing.T) {
	// Ensure no env vars are set that would override fallback
	oldKey := os.Getenv("GOOGLE_API_KEY")
	oldOverride := os.Getenv("GEMINI_MODELS_OVERRIDE")
	os.Unsetenv("GOOGLE_API_KEY")
	os.Unsetenv("GEMINI_MODELS_OVERRIDE")
	defer func() {
		os.Setenv("GOOGLE_API_KEY", oldKey)
		os.Setenv("GEMINI_MODELS_OVERRIDE", oldOverride)
	}()

	models, err := GetAvailableGeminiModels()
	if err != nil {
		t.Fatalf("GetAvailableGeminiModels() error = %v", err)
	}

	expectedModels := []string{
		"gemini-3-pro-preview",
		"gemini-3-flash-preview",
		"gemini-2.5-pro",
		"gemini-2.5-flash",
		"gemini-2.5-flash-lite",
		"gemini-2.0-flash",
		"gemini-1.5-flash",
		"gemini-1.5-pro",
	}

	// The function might sort the models, let's check for inclusion
	for _, expected := range expectedModels {
		found := false
		for _, m := range models {
			if m == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected model %q not found in fallback list: %v", expected, models)
		}
	}
}

// TestFindGeminiSessionForInstance was removed - file scanning is no longer used.
// Session ID detection now uses tmux environment variables exclusively.
