package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
)

// TestGeminiSettings_YoloMode tests parsing of GeminiSettings.YoloMode from config
func TestGeminiSettings_YoloMode(t *testing.T) {
	tests := []struct {
		name           string
		configContent  string
		expectedYolo   bool
		expectParseErr bool
	}{
		{
			name: "yolo_mode=true",
			configContent: `
[gemini]
yolo_mode = true
`,
			expectedYolo:   true,
			expectParseErr: false,
		},
		{
			name: "yolo_mode=false",
			configContent: `
[gemini]
yolo_mode = false
`,
			expectedYolo:   false,
			expectParseErr: false,
		},
		{
			name: "gemini section missing",
			configContent: `
[claude]
dangerous_mode = true
`,
			expectedYolo:   false, // Default is false
			expectParseErr: false,
		},
		{
			name: "yolo_mode missing from gemini section",
			configContent: `
[gemini]
# No yolo_mode field
`,
			expectedYolo:   false, // Default is false
			expectParseErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "config.toml")

			if err := os.WriteFile(configPath, []byte(tt.configContent), 0600); err != nil {
				t.Fatalf("Failed to write config file: %v", err)
			}

			var config UserConfig
			_, err := toml.DecodeFile(configPath, &config)

			if tt.expectParseErr && err == nil {
				t.Error("Expected parse error but got none")
			}
			if !tt.expectParseErr && err != nil {
				t.Fatalf("Unexpected parse error: %v", err)
			}

			if config.Gemini.YoloMode != tt.expectedYolo {
				t.Errorf("Gemini.YoloMode = %v, want %v", config.Gemini.YoloMode, tt.expectedYolo)
			}
		})
	}
}

// TestInstance_buildGeminiCommand_YoloFlag tests that buildGeminiCommand() adds --yolo flag correctly
func TestInstance_buildGeminiCommand_YoloFlag(t *testing.T) {
	tests := []struct {
		name               string
		globalYoloMode     bool
		perSessionYolo     *bool
		sessionID          string
		expectedContains   []string
		expectedNotContain []string
	}{
		{
			name:           "global yolo=true, new session",
			globalYoloMode: true,
			perSessionYolo: nil,
			sessionID:      "",
			expectedContains: []string{
				"--yolo",
				"gemini --resume",
			},
			expectedNotContain: []string{},
		},
		{
			name:           "global yolo=false, new session",
			globalYoloMode: false,
			perSessionYolo: nil,
			sessionID:      "",
			expectedContains: []string{
				"gemini --resume",
			},
			expectedNotContain: []string{
				"--yolo",
			},
		},
		{
			name:           "global yolo=true, existing session with ID",
			globalYoloMode: true,
			perSessionYolo: nil,
			sessionID:      "session-abc-123",
			expectedContains: []string{
				"gemini --resume session-abc-123 --yolo",
			},
			expectedNotContain: []string{},
		},
		{
			name:           "global yolo=false, existing session with ID",
			globalYoloMode: false,
			perSessionYolo: nil,
			sessionID:      "session-abc-123",
			expectedContains: []string{
				"gemini --resume session-abc-123",
			},
			expectedNotContain: []string{
				"--yolo",
			},
		},
		{
			name:           "per-session yolo=true overrides global=false",
			globalYoloMode: false,
			perSessionYolo: boolPtr(true),
			sessionID:      "session-xyz-789",
			expectedContains: []string{
				"gemini --resume session-xyz-789 --yolo",
			},
			expectedNotContain: []string{},
		},
		{
			name:           "per-session yolo=false overrides global=true",
			globalYoloMode: true,
			perSessionYolo: boolPtr(false),
			sessionID:      "session-xyz-789",
			expectedContains: []string{
				"gemini --resume session-xyz-789",
			},
			expectedNotContain: []string{
				"--yolo",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup global config
			userConfigCacheMu.Lock()
			userConfigCache = &UserConfig{
				Gemini: GeminiSettings{
					YoloMode: tt.globalYoloMode,
				},
			}
			userConfigCacheMu.Unlock()
			defer func() {
				userConfigCacheMu.Lock()
				userConfigCache = nil
				userConfigCacheMu.Unlock()
			}()

			// Create instance
			inst := &Instance{
				ID:              "test-gemini-yolo",
				Title:           "test-session",
				ProjectPath:     "/tmp/test",
				Tool:            "gemini",
				GeminiSessionID: tt.sessionID,
				GeminiYoloMode:  tt.perSessionYolo,
			}

			if tt.sessionID != "" {
				inst.GeminiDetectedAt = time.Now()
			}

			// Build command
			cmd := inst.buildGeminiCommand("gemini")

			// Check expected substrings
			for _, expected := range tt.expectedContains {
				if !strings.Contains(cmd, expected) {
					t.Errorf("Command should contain %q\nGot: %s", expected, cmd)
				}
			}

			// Check not-expected substrings
			for _, notExpected := range tt.expectedNotContain {
				if strings.Contains(cmd, notExpected) {
					t.Errorf("Command should NOT contain %q\nGot: %s", notExpected, cmd)
				}
			}
		})
	}
}

// TestInstance_GeminiYoloMode_Persistence tests that GeminiYoloMode persists through save/load
func TestInstance_GeminiYoloMode_Persistence(t *testing.T) {
	tmpDir := t.TempDir()
	storagePath := filepath.Join(tmpDir, "sessions.json")

	s := &Storage{
		path:    storagePath,
		profile: "_test",
	}

	tests := []struct {
		name              string
		yoloMode          *bool
		expectedAfterLoad *bool
	}{
		{
			name:              "yolo=true persists",
			yoloMode:          boolPtr(true),
			expectedAfterLoad: boolPtr(true),
		},
		{
			name:              "yolo=false persists",
			yoloMode:          boolPtr(false),
			expectedAfterLoad: boolPtr(false),
		},
		{
			name:              "yolo=nil persists",
			yoloMode:          nil,
			expectedAfterLoad: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create instance with YOLO mode
			inst := &Instance{
				ID:              "test-persist",
				Title:           "Persistence Test",
				ProjectPath:     "/tmp/test",
				Tool:            "gemini",
				Status:          StatusIdle,
				CreatedAt:       time.Now(),
				GeminiYoloMode:  tt.yoloMode,
				GeminiSessionID: "session-persist-123",
			}

			// Save
			err := s.SaveWithGroups([]*Instance{inst}, nil)
			if err != nil {
				t.Fatalf("SaveWithGroups failed: %v", err)
			}

			// Load
			loaded, _, err := s.LoadWithGroups()
			if err != nil {
				t.Fatalf("LoadWithGroups failed: %v", err)
			}

			if len(loaded) != 1 {
				t.Fatalf("Expected 1 instance, got %d", len(loaded))
			}

			loadedInst := loaded[0]

			// Compare GeminiYoloMode
			if !boolPtrEqual(loadedInst.GeminiYoloMode, tt.expectedAfterLoad) {
				t.Errorf("GeminiYoloMode after load = %v, want %v",
					boolPtrToString(loadedInst.GeminiYoloMode),
					boolPtrToString(tt.expectedAfterLoad))
			}
		})
	}
}

// TestInstance_buildGeminiCommand_NonGeminiTool tests that buildGeminiCommand() returns baseCommand for non-Gemini tools
func TestInstance_buildGeminiCommand_NonGeminiTool(t *testing.T) {
	// Setup config with yolo=true to ensure it doesn't affect non-Gemini tools
	userConfigCacheMu.Lock()
	userConfigCache = &UserConfig{
		Gemini: GeminiSettings{
			YoloMode: true,
		},
	}
	userConfigCacheMu.Unlock()
	defer func() {
		userConfigCacheMu.Lock()
		userConfigCache = nil
		userConfigCacheMu.Unlock()
	}()

	tests := []struct {
		tool        string
		baseCommand string
	}{
		{"claude", "claude"},
		{"opencode", "opencode"},
		{"shell", "bash"},
	}

	for _, tt := range tests {
		t.Run(tt.tool, func(t *testing.T) {
			inst := &Instance{
				Tool: tt.tool,
			}

			cmd := inst.buildGeminiCommand(tt.baseCommand)

			if cmd != tt.baseCommand {
				t.Errorf("buildGeminiCommand for %s should return baseCommand unchanged, got %s", tt.tool, cmd)
			}
		})
	}
}

// TestInstance_buildGeminiCommand_CustomCommand tests that custom commands are returned as-is
func TestInstance_buildGeminiCommand_CustomCommand(t *testing.T) {
	// Setup config
	userConfigCacheMu.Lock()
	userConfigCache = &UserConfig{
		Gemini: GeminiSettings{
			YoloMode: true,
		},
	}
	userConfigCacheMu.Unlock()
	defer func() {
		userConfigCacheMu.Lock()
		userConfigCache = nil
		userConfigCacheMu.Unlock()
	}()

	inst := &Instance{
		Tool:            "gemini",
		GeminiSessionID: "session-123",
	}

	// Custom command (not "gemini") should be returned as-is
	customCmd := "gemini --custom-flag --another-option"
	result := inst.buildGeminiCommand(customCmd)

	if result != customCmd {
		t.Errorf("Custom command should be returned as-is\nGot: %s\nWant: %s", result, customCmd)
	}
}

// Helper function to create bool pointer
func boolPtr(b bool) *bool {
	return &b
}

// Helper function to compare bool pointers
func boolPtrEqual(a, b *bool) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

// Helper function to convert bool pointer to string for error messages
func boolPtrToString(b *bool) string {
	if b == nil {
		return "nil"
	}
	if *b {
		return "true"
	}
	return "false"
}
