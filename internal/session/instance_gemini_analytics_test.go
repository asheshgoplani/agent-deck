package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInstance_UpdateGeminiAnalytics(t *testing.T) {
	// Setup temp dir for Gemini session files
	tmpDir := t.TempDir()
	geminiConfigDirOverride = tmpDir
	defer func() { geminiConfigDirOverride = "" }()

	projectPath := "/tmp/test-project"
	// Create sessions directory
	sessionsDir := GetGeminiSessionsDir(projectPath)
	if err := os.MkdirAll(sessionsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create a mock Gemini session file
	sessionID := "abc-123-def"
	sessionFile := filepath.Join(sessionsDir, "session-2025-01-17T12-00-"+sessionID[:8]+".json")

		// Mock content with start time, last updated and tokens

		content := `{

			"sessionId": "abc-123-def",

			"startTime": "2025-01-17T12:00:00Z",

			"lastUpdated": "2025-01-17T12:30:00Z",

			"messages": [

				{"type": "user", "content": "hello"},

				{"type": "gemini", "content": "world", "tokens": {"input": 100, "output": 50}}

			]

		}`

		

		if err := os.WriteFile(sessionFile, []byte(content), 0644); err != nil {

			t.Fatal(err)

		}

	

		inst := NewInstanceWithTool("test", projectPath, "gemini")

		inst.GeminiSessionID = sessionID

	

		// Initial state: no analytics

		if inst.GeminiAnalytics != nil {

			t.Error("Analytics should be nil initially")

		}

	

		// Trigger update

		inst.UpdateGeminiSession(nil)

	

		// Verify analytics updated

		if inst.GeminiAnalytics == nil {

			t.Fatal("Analytics should be initialized after update")

		}

	

		// Verify StartTime

		expectedStart, _ := time.Parse(time.RFC3339, "2025-01-17T12:00:00Z")

		if !inst.GeminiAnalytics.StartTime.Equal(expectedStart) {

			t.Errorf("StartTime = %v, want %v", inst.GeminiAnalytics.StartTime, expectedStart)

		}

	

		// Verify Duration (30 mins)

		expectedDuration := 30 * time.Minute

		if inst.GeminiAnalytics.Duration != expectedDuration {

			t.Errorf("Duration = %v, want %v", inst.GeminiAnalytics.Duration, expectedDuration)

		}

	

		// Verify Tokens

		if inst.GeminiAnalytics.InputTokens != 100 {

			t.Errorf("InputTokens = %d, want 100", inst.GeminiAnalytics.InputTokens)

		}

		if inst.GeminiAnalytics.OutputTokens != 50 {

			t.Errorf("OutputTokens = %d, want 50", inst.GeminiAnalytics.OutputTokens)

		}

		if inst.GeminiAnalytics.CurrentContextTokens != 100 {

			t.Errorf("CurrentContextTokens = %d, want 100", inst.GeminiAnalytics.CurrentContextTokens)

		}

	

	    // Verify TotalTurns

	    if inst.GeminiAnalytics.TotalTurns != 1 {

	        t.Errorf("TotalTurns = %d, want 1", inst.GeminiAnalytics.TotalTurns)

	    }

	}

	
