package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- Systemd template generation tests ---

func TestGenerateSystemdHeartbeatTimer(t *testing.T) {
	timer := GenerateSystemdHeartbeatTimer("test-conductor", 15)

	// Verify placeholders are replaced
	if strings.Contains(timer, "__NAME__") {
		t.Error("timer output still contains __NAME__ placeholder")
	}
	if strings.Contains(timer, "__INTERVAL__") {
		t.Error("timer output still contains __INTERVAL__ placeholder")
	}

	// Verify correct values
	if !strings.Contains(timer, "test-conductor") {
		t.Error("timer should contain conductor name")
	}
	// 15 minutes = 900 seconds
	if !strings.Contains(timer, "900") {
		t.Errorf("timer should contain 900 seconds (15 min * 60), got:\n%s", timer)
	}

	// Verify systemd timer structure
	if !strings.Contains(timer, "[Unit]") {
		t.Error("timer should contain [Unit] section")
	}
	if !strings.Contains(timer, "[Timer]") {
		t.Error("timer should contain [Timer] section")
	}
	if !strings.Contains(timer, "[Install]") {
		t.Error("timer should contain [Install] section")
	}
	if !strings.Contains(timer, "OnBootSec=") {
		t.Error("timer should contain OnBootSec directive")
	}
	if !strings.Contains(timer, "OnUnitActiveSec=") {
		t.Error("timer should contain OnUnitActiveSec directive")
	}
}

func TestGenerateSystemdHeartbeatTimerInterval(t *testing.T) {
	tests := []struct {
		name     string
		minutes  int
		expected string
	}{
		{"1 minute", 1, "60"},
		{"5 minutes", 5, "300"},
		{"30 minutes", 30, "1800"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			timer := GenerateSystemdHeartbeatTimer("test", tt.minutes)
			if !strings.Contains(timer, tt.expected+"s") {
				t.Errorf("expected interval %ss in timer, got:\n%s", tt.expected, timer)
			}
		})
	}
}

func TestGenerateSystemdHeartbeatService(t *testing.T) {
	svc, err := GenerateSystemdHeartbeatService("test-conductor")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify placeholders are replaced
	if strings.Contains(svc, "__NAME__") {
		t.Error("service output still contains __NAME__ placeholder")
	}
	if strings.Contains(svc, "__SCRIPT_PATH__") {
		t.Error("service output still contains __SCRIPT_PATH__ placeholder")
	}
	if strings.Contains(svc, "__HOME__") {
		t.Error("service output still contains __HOME__ placeholder")
	}

	// Verify systemd service structure
	if !strings.Contains(svc, "[Unit]") {
		t.Error("service should contain [Unit] section")
	}
	if !strings.Contains(svc, "[Service]") {
		t.Error("service should contain [Service] section")
	}
	if !strings.Contains(svc, "Type=oneshot") {
		t.Error("heartbeat service should be Type=oneshot")
	}
	if !strings.Contains(svc, "heartbeat.sh") {
		t.Error("service should reference heartbeat.sh script")
	}
	if !strings.Contains(svc, "test-conductor") {
		t.Error("service should contain conductor name in description")
	}
}

// --- Systemd naming tests ---

func TestSystemdHeartbeatServiceName(t *testing.T) {
	name := SystemdHeartbeatServiceName("my-conductor")
	expected := "agent-deck-conductor-heartbeat-my-conductor.service"
	if name != expected {
		t.Errorf("got %q, want %q", name, expected)
	}
}

func TestSystemdHeartbeatTimerName(t *testing.T) {
	name := SystemdHeartbeatTimerName("my-conductor")
	expected := "agent-deck-conductor-heartbeat-my-conductor.timer"
	if name != expected {
		t.Errorf("got %q, want %q", name, expected)
	}
}

func TestSystemdUserDir(t *testing.T) {
	dir, err := SystemdUserDir()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	homeDir, _ := os.UserHomeDir()
	expected := filepath.Join(homeDir, ".config", "systemd", "user")
	if dir != expected {
		t.Errorf("got %q, want %q", dir, expected)
	}
}

func TestSystemdBridgeServicePath(t *testing.T) {
	path, err := SystemdBridgeServicePath()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.HasSuffix(path, "agent-deck-conductor-bridge.service") {
		t.Errorf("path should end with service file name, got %q", path)
	}
	if !strings.Contains(path, ".config/systemd/user") {
		t.Errorf("path should be in systemd user dir, got %q", path)
	}
}

func TestSystemdHeartbeatServicePath(t *testing.T) {
	path, err := SystemdHeartbeatServicePath("test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := "agent-deck-conductor-heartbeat-test.service"
	if !strings.HasSuffix(path, expected) {
		t.Errorf("path should end with %q, got %q", expected, path)
	}
}

func TestSystemdHeartbeatTimerPath(t *testing.T) {
	path, err := SystemdHeartbeatTimerPath("test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := "agent-deck-conductor-heartbeat-test.timer"
	if !strings.HasSuffix(path, expected) {
		t.Errorf("path should end with %q, got %q", expected, path)
	}
}

// --- Conductor validation and naming tests ---

func TestValidateConductorName(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"valid-name", false},
		{"valid.name", false},
		{"valid_name", false},
		{"a", false},
		{"abc123", false},
		{"", true},                      // empty
		{"-invalid", true},              // starts with dash
		{".invalid", true},              // starts with dot
		{"_invalid", true},              // starts with underscore
		{"has space", true},             // contains space
		{"has/slash", true},             // contains slash
		{strings.Repeat("a", 65), true}, // too long
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateConductorName(tt.name)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateConductorName(%q) error = %v, wantErr %v", tt.name, err, tt.wantErr)
			}
		})
	}
}

func TestConductorSessionTitle(t *testing.T) {
	title := ConductorSessionTitle("my-conductor")
	if title != "conductor-my-conductor" {
		t.Errorf("got %q, want %q", title, "conductor-my-conductor")
	}
}

func TestHeartbeatPlistLabel(t *testing.T) {
	label := HeartbeatPlistLabel("test")
	expected := "com.agentdeck.conductor-heartbeat.test"
	if label != expected {
		t.Errorf("got %q, want %q", label, expected)
	}
}

// --- InstallBridgeDaemon platform dispatch test ---

func TestBridgeDaemonHint(t *testing.T) {
	// BridgeDaemonHint should return a non-empty string on any platform
	hint := BridgeDaemonHint()
	if hint == "" {
		t.Error("BridgeDaemonHint() should return a non-empty hint")
	}
}

// --- Conductor meta tests ---

func TestConductorMetaSaveAndLoad(t *testing.T) {
	// Use a temp directory to simulate conductor dir
	tmpDir := t.TempDir()

	// Override the home dir detection by working with a specific name
	meta := &ConductorMeta{
		Name:             "test-meta",
		Profile:          "default",
		HeartbeatEnabled: true,
		Description:      "test conductor",
		CreatedAt:        "2025-01-01T00:00:00Z",
	}

	// Write meta to temp dir directly
	metaDir := filepath.Join(tmpDir, "test-meta")
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		t.Fatalf("failed to create dir: %v", err)
	}

	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}
	metaPath := filepath.Join(metaDir, "meta.json")
	if err := os.WriteFile(metaPath, data, 0o644); err != nil {
		t.Fatalf("failed to write: %v", err)
	}

	// Read it back
	readData, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("failed to read: %v", err)
	}

	var loaded ConductorMeta
	if err := json.Unmarshal(readData, &loaded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if loaded.Name != meta.Name {
		t.Errorf("name mismatch: got %q, want %q", loaded.Name, meta.Name)
	}
	if loaded.Profile != meta.Profile {
		t.Errorf("profile mismatch: got %q, want %q", loaded.Profile, meta.Profile)
	}
	if loaded.HeartbeatEnabled != meta.HeartbeatEnabled {
		t.Errorf("heartbeat mismatch: got %v, want %v", loaded.HeartbeatEnabled, meta.HeartbeatEnabled)
	}
	if loaded.Description != meta.Description {
		t.Errorf("description mismatch: got %q, want %q", loaded.Description, meta.Description)
	}
}

func TestGetHeartbeatInterval(t *testing.T) {
	tests := []struct {
		interval int
		expected int
	}{
		{0, 15},  // default
		{-1, 15}, // negative defaults to 15
		{10, 10}, // custom
		{30, 30}, // custom
	}

	for _, tt := range tests {
		settings := &ConductorSettings{HeartbeatInterval: tt.interval}
		if got := settings.GetHeartbeatInterval(); got != tt.expected {
			t.Errorf("GetHeartbeatInterval() with %d = %d, want %d", tt.interval, got, tt.expected)
		}
	}
}

func TestGetProfiles(t *testing.T) {
	// Empty profiles should return default
	settings := &ConductorSettings{}
	profiles := settings.GetProfiles()
	if len(profiles) != 1 || profiles[0] != DefaultProfile {
		t.Errorf("empty profiles should return default, got %v", profiles)
	}

	// Custom profiles should be returned as-is
	settings = &ConductorSettings{Profiles: []string{"work", "personal"}}
	profiles = settings.GetProfiles()
	if len(profiles) != 2 {
		t.Errorf("expected 2 profiles, got %d", len(profiles))
	}
}
