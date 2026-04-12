package session

import (
	"encoding/json"
	"testing"
	"time"
)

// TestOpenCodeSessionMatching tests the session matching logic for OpenCode
func TestOpenCodeSessionMatching(t *testing.T) {
	// Mock session data similar to real OpenCode output
	mockSessionsJSON := `[
		{
			"id": "ses_NEW001",
			"title": "New session",
			"updated": 1768982200000,
			"created": 1768982195000,
			"projectId": "60e0658efac270ccb48e12c801746116f86763fa",
			"directory": "/Users/ashesh/claude-deck"
		},
		{
			"id": "ses_OLD001",
			"title": "Old session",
			"updated": 1768982100000,
			"created": 1768982000000,
			"projectId": "60e0658efac270ccb48e12c801746116f86763fa",
			"directory": "/Users/ashesh/claude-deck"
		},
		{
			"id": "ses_OTHER001",
			"title": "Different project",
			"updated": 1768982300000,
			"created": 1768982300000,
			"projectId": "other-project-id",
			"directory": "/Users/ashesh/other-project"
		}
	]`

	tests := []struct {
		name        string
		projectPath string
		currentID   string
		wantID      string
		wantMatch   bool
	}{
		{
			name:        "Finds most recent session for matching directory",
			projectPath: "/Users/ashesh/claude-deck",
			wantID:      "ses_NEW001", // Should pick the most recently updated one
			wantMatch:   true,
		},
		{
			name:        "Keeps existing session when it still belongs to project",
			projectPath: "/Users/ashesh/claude-deck",
			currentID:   "ses_OLD001",
			wantID:      "ses_OLD001",
			wantMatch:   true,
		},
		{
			name:        "Ignores sessions from different directories",
			projectPath: "/Users/ashesh/other-project",
			wantID:      "ses_OTHER001", // Only session matching this directory
			wantMatch:   true,
		},
		{
			name:        "Falls back to most recent when existing session is missing",
			projectPath: "/Users/ashesh/claude-deck",
			currentID:   "ses_MISSING001",
			wantID:      "ses_NEW001",
			wantMatch:   true,
		},
		{
			name:        "No match for unknown directory",
			projectPath: "/Users/ashesh/nonexistent",
			wantID:      "",
			wantMatch:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Parse mock sessions
			var sessions []openCodeSessionMetadata

			if err := json.Unmarshal([]byte(mockSessionsJSON), &sessions); err != nil {
				t.Fatalf("Failed to parse mock sessions: %v", err)
			}

			// Apply the matching logic (same as queryOpenCodeSession but testable)
			gotID := findBestOpenCodeSession(sessions, tt.projectPath, tt.currentID)

			if tt.wantMatch {
				if gotID != tt.wantID {
					t.Errorf("Expected session ID %q, got %q", tt.wantID, gotID)
				}
			} else {
				if gotID != "" {
					t.Errorf("Expected no match, got %q", gotID)
				}
			}
		})
	}
}

// TestOpenCodeBuildCommand tests the command building for OpenCode sessions
func TestOpenCodeBuildCommand(t *testing.T) {
	tests := []struct {
		name              string
		baseCommand       string
		openCodeSessionID string
		wantContains      []string
		wantNotContains   []string
	}{
		{
			name:              "Fresh start without session ID",
			baseCommand:       "opencode",
			openCodeSessionID: "",
			wantContains:      []string{"opencode"},
			wantNotContains:   []string{"-s", "tmux set-environment"},
		},
		{
			name:              "Resume with existing session ID",
			baseCommand:       "opencode",
			openCodeSessionID: "ses_ABC123",
			wantContains:      []string{"opencode -s ses_ABC123"},
			wantNotContains:   []string{"tmux set-environment"},
		},
		{
			name:              "Custom command passes through unchanged",
			baseCommand:       "opencode --model gpt-4",
			openCodeSessionID: "ses_ABC123",
			wantContains:      []string{"opencode --model gpt-4"},
			wantNotContains:   []string{"-s"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inst := &Instance{
				Tool:              "opencode",
				OpenCodeSessionID: tt.openCodeSessionID,
			}

			got := inst.buildOpenCodeCommand(tt.baseCommand)

			for _, want := range tt.wantContains {
				if !containsSubstring(got, want) {
					t.Errorf("Expected command to contain %q, got: %q", want, got)
				}
			}

			for _, notWant := range tt.wantNotContains {
				if containsSubstring(got, notWant) {
					t.Errorf("Expected command to NOT contain %q, got: %q", notWant, got)
				}
			}
		})
	}
}

func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && (s[:len(substr)] == substr || s[len(s)-len(substr):] == substr || findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestApplyOpenCodeSessionCandidate_BindsWhenEmpty(t *testing.T) {
	inst := &Instance{Tool: "opencode"}

	changed := inst.applyOpenCodeSessionCandidate("ses_NEW001")

	if !changed {
		t.Fatal("expected change when binding first OpenCode session ID")
	}
	if inst.OpenCodeSessionID != "ses_NEW001" {
		t.Fatalf("OpenCodeSessionID = %q, want %q", inst.OpenCodeSessionID, "ses_NEW001")
	}
	if inst.OpenCodeDetectedAt.IsZero() {
		t.Fatal("OpenCodeDetectedAt should be set when binding OpenCode session ID")
	}
}

func TestApplyOpenCodeSessionCandidate_RebindsWhenDifferent(t *testing.T) {
	inst := &Instance{
		Tool:              "opencode",
		OpenCodeSessionID: "ses_OLD001",
		OpenCodeDetectedAt: time.Now().Add(
			-1 * time.Hour,
		),
	}

	changed := inst.applyOpenCodeSessionCandidate("ses_NEW001")

	if !changed {
		t.Fatal("expected change when OpenCode session rotates")
	}
	if inst.OpenCodeSessionID != "ses_NEW001" {
		t.Fatalf("OpenCodeSessionID = %q, want %q", inst.OpenCodeSessionID, "ses_NEW001")
	}
}

func TestApplyOpenCodeSessionCandidate_NoChangeWhenSame(t *testing.T) {
	detectedAt := time.Now().Add(-5 * time.Minute)
	inst := &Instance{
		Tool:               "opencode",
		OpenCodeSessionID:  "ses_SAME001",
		OpenCodeDetectedAt: detectedAt,
	}

	changed := inst.applyOpenCodeSessionCandidate("ses_SAME001")

	if changed {
		t.Fatal("expected no change when candidate matches current OpenCode session ID")
	}
	if inst.OpenCodeSessionID != "ses_SAME001" {
		t.Fatalf("OpenCodeSessionID = %q, want %q", inst.OpenCodeSessionID, "ses_SAME001")
	}
	if !inst.OpenCodeDetectedAt.Equal(detectedAt) {
		t.Fatal("OpenCodeDetectedAt should remain unchanged when candidate matches current ID")
	}
}

func TestApplyOpenCodeSessionCandidate_IgnoresEmptyCandidate(t *testing.T) {
	inst := &Instance{Tool: "opencode", OpenCodeSessionID: "ses_EXISTING001"}

	changed := inst.applyOpenCodeSessionCandidate("")

	if changed {
		t.Fatal("expected no change when OpenCode candidate is empty")
	}
	if inst.OpenCodeSessionID != "ses_EXISTING001" {
		t.Fatalf("OpenCodeSessionID = %q, want %q", inst.OpenCodeSessionID, "ses_EXISTING001")
	}
}
