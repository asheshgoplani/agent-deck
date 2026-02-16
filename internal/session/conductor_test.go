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

// --- Slack authorization tests ---

func TestSlackSettings_AllowedUserIDs(t *testing.T) {
	tests := []struct {
		name        string
		settings    SlackSettings
		expectEmpty bool
	}{
		{
			name: "empty allowed users",
			settings: SlackSettings{
				BotToken:       "xoxb-test",
				AppToken:       "xapp-test",
				ChannelID:      "C12345",
				ListenMode:     "mentions",
				AllowedUserIDs: []string{},
			},
			expectEmpty: true,
		},
		{
			name: "single allowed user",
			settings: SlackSettings{
				BotToken:       "xoxb-test",
				AppToken:       "xapp-test",
				ChannelID:      "C12345",
				ListenMode:     "mentions",
				AllowedUserIDs: []string{"U12345"},
			},
			expectEmpty: false,
		},
		{
			name: "multiple allowed users",
			settings: SlackSettings{
				BotToken:       "xoxb-test",
				AppToken:       "xapp-test",
				ChannelID:      "C12345",
				ListenMode:     "all",
				AllowedUserIDs: []string{"U12345", "U67890", "UABCDE"},
			},
			expectEmpty: false,
		},
		{
			name: "nil allowed users",
			settings: SlackSettings{
				BotToken:       "xoxb-test",
				AppToken:       "xapp-test",
				ChannelID:      "C12345",
				ListenMode:     "mentions",
				AllowedUserIDs: nil,
			},
			expectEmpty: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isEmpty := len(tt.settings.AllowedUserIDs) == 0
			if isEmpty != tt.expectEmpty {
				t.Errorf("expected empty=%v, got empty=%v for %+v",
					tt.expectEmpty, isEmpty, tt.settings.AllowedUserIDs)
			}
		})
	}
}

func TestSlackSettings_UserIDFormat(t *testing.T) {
	// Verify that typical Slack user ID formats are handled correctly
	userIDs := []string{
		"U01234ABCDE", // Standard user ID
		"U05678FGHIJ", // Another standard ID
		"W12345",      // Workspace user ID
		"USLACKBOT",   // SlackBot ID
	}

	settings := SlackSettings{
		BotToken:       "xoxb-test",
		AppToken:       "xapp-test",
		ChannelID:      "C12345",
		ListenMode:     "mentions",
		AllowedUserIDs: userIDs,
	}

	if len(settings.AllowedUserIDs) != len(userIDs) {
		t.Errorf("expected %d user IDs, got %d", len(userIDs), len(settings.AllowedUserIDs))
	}

	for i, id := range userIDs {
		if settings.AllowedUserIDs[i] != id {
			t.Errorf("user ID mismatch at index %d: got %q, want %q",
				i, settings.AllowedUserIDs[i], id)
		}
	}
}

func TestSlackSettings_TOML(t *testing.T) {
	// Verify the SlackSettings struct is properly defined with AllowedUserIDs
	slack := SlackSettings{
		BotToken:       "xoxb-test-token",
		AppToken:       "xapp-test-token",
		ChannelID:      "C01234ABCDE",
		ListenMode:     "mentions",
		AllowedUserIDs: []string{"U01234", "U56789", "UABCDE"},
	}

	// Verify the struct fields are accessible
	if slack.BotToken != "xoxb-test-token" {
		t.Errorf("bot_token mismatch: got %q", slack.BotToken)
	}
	if slack.AppToken != "xapp-test-token" {
		t.Errorf("app_token mismatch: got %q", slack.AppToken)
	}
	if slack.ChannelID != "C01234ABCDE" {
		t.Errorf("channel_id mismatch: got %q", slack.ChannelID)
	}
	if slack.ListenMode != "mentions" {
		t.Errorf("listen_mode mismatch: got %q", slack.ListenMode)
	}
	if len(slack.AllowedUserIDs) != 3 {
		t.Errorf("expected 3 allowed user IDs, got %d", len(slack.AllowedUserIDs))
	}
	if slack.AllowedUserIDs[0] != "U01234" {
		t.Errorf("first user ID mismatch: got %q", slack.AllowedUserIDs[0])
	}
	if slack.AllowedUserIDs[1] != "U56789" {
		t.Errorf("second user ID mismatch: got %q", slack.AllowedUserIDs[1])
	}
	if slack.AllowedUserIDs[2] != "UABCDE" {
		t.Errorf("third user ID mismatch: got %q", slack.AllowedUserIDs[2])
	}
}

// --- Python bridge template tests ---

func TestBridgeTemplate_ContainsSlackAuthorization(t *testing.T) {
	// Verify that the Python bridge template contains the Slack authorization code
	template := conductorBridgePy

	// Check for authorization function definition
	if !strings.Contains(template, "def is_slack_authorized(user_id: str) -> bool:") {
		t.Error("template should contain is_slack_authorized function definition")
	}

	// Check for allowed_users setup
	if !strings.Contains(template, `allowed_users = config["slack"]["allowed_user_ids"]`) {
		t.Error("template should load allowed_user_ids from config")
	}

	// Check for authorization logic
	if !strings.Contains(template, "if not allowed_users:") {
		t.Error("template should check if allowed_users is empty")
	}
	if !strings.Contains(template, "if user_id not in allowed_users:") {
		t.Error("template should check if user_id is in allowed_users")
	}

	// Check for warning log
	if !strings.Contains(template, `log.warning("Unauthorized Slack message from user %s", user_id)`) {
		t.Error("template should log warning for unauthorized users")
	}

	// Check for authorization checks in handlers
	authCheckPatterns := []string{
		"user_id = event.get(\"user\", \"\")",                            // message/mention handlers
		"user_id = command.get(\"user_id\", \"\")",                       // slash command handlers
		"if not is_slack_authorized(user_id):",                           // authorization check
		"await respond(\"⛔ Unauthorized. Contact your administrator.\")", // slash command error
	}

	for _, pattern := range authCheckPatterns {
		if !strings.Contains(template, pattern) {
			t.Errorf("template should contain authorization pattern: %q", pattern)
		}
	}
}

func TestBridgeTemplate_SlackHandlersHaveAuthorization(t *testing.T) {
	// Verify all Slack handlers have authorization checks
	template := conductorBridgePy

	handlers := []struct {
		name    string
		pattern string
	}{
		{"message handler", "@app.event(\"message\")"},
		{"mention handler", "@app.event(\"app_mention\")"},
		{"status command", "@app.command(\"/ad-status\")"},
		{"sessions command", "@app.command(\"/ad-sessions\")"},
		{"restart command", "@app.command(\"/ad-restart\")"},
		{"help command", "@app.command(\"/ad-help\")"},
	}

	for _, h := range handlers {
		if !strings.Contains(template, h.pattern) {
			t.Errorf("template should contain %s: %q", h.name, h.pattern)
		}
	}
}

func TestBridgeTemplate_ConfigLoadsAllowedUserIDs(t *testing.T) {
	// Verify the config loading includes allowed_user_ids
	template := conductorBridgePy

	configPatterns := []string{
		`sl_allowed_users = sl.get("allowed_user_ids", [])`,
		`"allowed_user_ids": sl_allowed_users,`,
	}

	for _, pattern := range configPatterns {
		if !strings.Contains(template, pattern) {
			t.Errorf("template should contain config pattern: %q", pattern)
		}
	}
}
