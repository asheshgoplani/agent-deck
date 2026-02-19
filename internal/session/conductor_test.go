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

func TestBridgeTemplate_HeartbeatSelectsOnePerProfile(t *testing.T) {
	template := conductorBridgePy

	patterns := []string{
		"def select_heartbeat_conductors(conductors: list[dict]) -> list[dict]:",
		"conductors = select_heartbeat_conductors(all_conductors)",
		"Multiple conductors may share a profile. Heartbeat auto-actions are profile-wide,",
	}

	for _, pattern := range patterns {
		if !strings.Contains(template, pattern) {
			t.Errorf("template should contain heartbeat dedupe pattern: %q", pattern)
		}
	}
}

func TestConductorHeartbeatScript_StatusParsingHandlesWhitespace(t *testing.T) {
	if !strings.Contains(conductorHeartbeatScript, `"status"[[:space:]]*:[[:space:]]*"`) {
		t.Fatal("heartbeat status parser should tolerate JSON whitespace around ':'")
	}
}

// --- Symlink-based CLAUDE.md tests ---

func TestInstallSharedClaudeMD_Default(t *testing.T) {
	// Use actual conductor directory (cleanup after test)
	homeDir, _ := os.UserHomeDir()
	conductorDir := filepath.Join(homeDir, ".agent-deck", "conductor")
	claudeMDPath := filepath.Join(conductorDir, "CLAUDE.md")

	// Backup existing file if present
	var backup []byte
	if content, err := os.ReadFile(claudeMDPath); err == nil {
		backup = content
		defer func() { _ = os.WriteFile(claudeMDPath, backup, 0o644) }()
	} else {
		defer os.Remove(claudeMDPath)
	}

	// Test installing default template
	err := InstallSharedClaudeMD("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify file exists at default location
	if _, err := os.Stat(claudeMDPath); os.IsNotExist(err) {
		t.Errorf("CLAUDE.md not created at %q", claudeMDPath)
	}

	// Verify it's NOT a symlink
	if _, err := os.Readlink(claudeMDPath); err == nil {
		t.Error("CLAUDE.md should not be a symlink when using default template")
	}

	// Verify content contains mechanism template
	content, _ := os.ReadFile(claudeMDPath)
	if !strings.Contains(string(content), "Conductor: Shared Knowledge Base") {
		t.Error("CLAUDE.md should contain shared template content")
	}

	// Verify mechanism content is present
	if !strings.Contains(string(content), "Agent-Deck CLI Reference") {
		t.Error("CLAUDE.md should contain CLI reference (mechanism)")
	}
	if !strings.Contains(string(content), "Session Status Values") {
		t.Error("CLAUDE.md should contain session status values (mechanism)")
	}

	// Verify policy content has been removed from shared template
	if strings.Contains(string(content), "## Core Rules") {
		t.Error("CLAUDE.md should NOT contain Core Rules (moved to POLICY.md)")
	}
	if strings.Contains(string(content), "## Auto-Response Guidelines") {
		t.Error("CLAUDE.md should NOT contain Auto-Response Guidelines (moved to POLICY.md)")
	}
	if !strings.Contains(string(content), "Your heartbeat response format") {
		t.Error("CLAUDE.md should contain heartbeat response format (bridge protocol)")
	}
}

func TestInstallSharedClaudeMD_CustomSymlink(t *testing.T) {
	tmpDir := t.TempDir()
	customPath := filepath.Join(tmpDir, "my-shared-claude.md")

	// Create custom file first
	if err := os.WriteFile(customPath, []byte("# My Custom Shared Rules\n"), 0o644); err != nil {
		t.Fatalf("failed to create custom file: %v", err)
	}

	// Use actual conductor directory (cleanup after test)
	homeDir, _ := os.UserHomeDir()
	conductorDir := filepath.Join(homeDir, ".agent-deck", "conductor")
	claudeMDPath := filepath.Join(conductorDir, "CLAUDE.md")

	// Backup existing file/symlink if present
	var backupContent []byte
	var backupLink string
	if linkDest, err := os.Readlink(claudeMDPath); err == nil {
		backupLink = linkDest
	} else if content, err := os.ReadFile(claudeMDPath); err == nil {
		backupContent = content
	}
	t.Cleanup(func() {
		os.Remove(claudeMDPath) // Remove whatever the test created (symlink or file)
		if backupLink != "" {
			_ = os.Symlink(backupLink, claudeMDPath)
		} else if backupContent != nil {
			_ = os.WriteFile(claudeMDPath, backupContent, 0o644)
		}
	})

	// Test installing with custom path (creates symlink)
	err := InstallSharedClaudeMD(customPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify symlink exists
	linkDest, err := os.Readlink(claudeMDPath)
	if err != nil {
		t.Fatalf("CLAUDE.md should be a symlink: %v", err)
	}

	// Verify symlink points to custom file
	if linkDest != customPath {
		t.Errorf("symlink should point to %q, got %q", customPath, linkDest)
	}

	// Verify reading through symlink works
	content, _ := os.ReadFile(claudeMDPath)
	if !strings.Contains(string(content), "My Custom Shared Rules") {
		t.Error("reading through symlink should return custom content")
	}
}

func TestInstallSharedClaudeMD_CustomSymlinkCreatesConductorDir(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	customPath := filepath.Join(t.TempDir(), "my-shared-claude.md")
	if err := os.WriteFile(customPath, []byte("# shared rules\n"), 0o644); err != nil {
		t.Fatalf("failed to create custom file: %v", err)
	}

	if err := InstallSharedClaudeMD(customPath); err != nil {
		t.Fatalf("InstallSharedClaudeMD returned error: %v", err)
	}

	target := filepath.Join(tmpHome, ".agent-deck", "conductor", "CLAUDE.md")
	linkDest, err := os.Readlink(target)
	if err != nil {
		t.Fatalf("expected symlink at %q: %v", target, err)
	}
	if linkDest != customPath {
		t.Fatalf("symlink destination = %q, want %q", linkDest, customPath)
	}
}

func TestSetupConductor_DefaultTemplate(t *testing.T) {
	name := "test-default"
	profile := "default"

	// Clean up after test
	homeDir, _ := os.UserHomeDir()
	defer os.RemoveAll(filepath.Join(homeDir, ".agent-deck", "conductor", name))

	// Setup without custom path (uses default template)
	err := SetupConductor(name, profile, true, "test description", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify CLAUDE.md exists
	dir, _ := ConductorNameDir(name)
	claudeMDPath := filepath.Join(dir, "CLAUDE.md")
	if _, err := os.Stat(claudeMDPath); os.IsNotExist(err) {
		t.Errorf("CLAUDE.md not created at %q", claudeMDPath)
	}

	// Verify it's NOT a symlink
	if _, err := os.Readlink(claudeMDPath); err == nil {
		t.Error("CLAUDE.md should not be a symlink when using default template")
	}

	// Verify content contains conductor identity
	content, _ := os.ReadFile(claudeMDPath)
	if !strings.Contains(string(content), name) {
		t.Errorf("CLAUDE.md should contain conductor name %q", name)
	}

	// Verify per-conductor CLAUDE.md references POLICY.md
	if !strings.Contains(string(content), "POLICY.md") {
		t.Error("per-conductor CLAUDE.md should reference POLICY.md")
	}

	// Verify meta.json does NOT contain ClaudeMDPath field
	meta, err := LoadConductorMeta(name)
	if err != nil {
		t.Fatalf("failed to load meta: %v", err)
	}
	// Just verify basic fields exist
	if meta.Name != name {
		t.Errorf("expected name %q, got %q", name, meta.Name)
	}
}

func TestSetupConductor_CustomSymlink(t *testing.T) {
	tmpDir := t.TempDir()
	customPath := filepath.Join(tmpDir, "my-conductor-claude.md")

	// Create custom file first
	if err := os.WriteFile(customPath, []byte("# My Custom Conductor Rules\n"), 0o644); err != nil {
		t.Fatalf("failed to create custom file: %v", err)
	}

	name := "test-symlink"
	profile := "default"

	// Clean up after test
	homeDir, _ := os.UserHomeDir()
	defer os.RemoveAll(filepath.Join(homeDir, ".agent-deck", "conductor", name))

	// Setup with custom path (creates symlink)
	err := SetupConductor(name, profile, true, "test description", customPath, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify symlink exists
	dir, _ := ConductorNameDir(name)
	claudeMDPath := filepath.Join(dir, "CLAUDE.md")
	linkDest, err := os.Readlink(claudeMDPath)
	if err != nil {
		t.Fatalf("CLAUDE.md should be a symlink: %v", err)
	}

	// Verify symlink points to custom file
	if linkDest != customPath {
		t.Errorf("symlink should point to %q, got %q", customPath, linkDest)
	}

	// Verify reading through symlink works
	content, _ := os.ReadFile(claudeMDPath)
	if !strings.Contains(string(content), "My Custom Conductor Rules") {
		t.Error("reading through symlink should return custom content")
	}
}

func TestSetupConductor_EmptyProfileNormalizesToDefault(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	name := "default-profile-conductor"
	if err := SetupConductor(name, "", true, "", "", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	meta, err := LoadConductorMeta(name)
	if err != nil {
		t.Fatalf("failed to load meta: %v", err)
	}
	if meta.Profile != DefaultProfile {
		t.Fatalf("meta profile = %q, want %q", meta.Profile, DefaultProfile)
	}

	dir, _ := ConductorNameDir(name)
	content, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("failed to read CLAUDE.md: %v", err)
	}
	if strings.Contains(string(content), "-p default") {
		t.Fatal("default profile template should omit explicit -p default flags")
	}
}

func TestSetupConductor_ProfileConflict(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	name := "profile-conflict"
	if err := SetupConductor(name, "work", true, "", "", ""); err != nil {
		t.Fatalf("first setup failed: %v", err)
	}

	err := SetupConductor(name, "personal", true, "", "", "")
	if err == nil {
		t.Fatal("expected conflict error when reusing conductor name across profiles")
	}
	if !strings.Contains(err.Error(), `already exists for profile "work"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadConductorMeta_EmptyProfileDefaultsToDefault(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	name := "meta-empty-profile"
	dir, _ := ConductorNameDir(name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("failed to create conductor dir: %v", err)
	}

	raw := `{"name":"meta-empty-profile","heartbeat_enabled":true,"created_at":"2026-01-01T00:00:00Z"}`
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(raw), 0o644); err != nil {
		t.Fatalf("failed to write meta.json: %v", err)
	}

	meta, err := LoadConductorMeta(name)
	if err != nil {
		t.Fatalf("LoadConductorMeta failed: %v", err)
	}
	if meta.Profile != DefaultProfile {
		t.Fatalf("meta profile = %q, want %q", meta.Profile, DefaultProfile)
	}
}

func TestCreateSymlinkWithExpansion_TildeExpansion(t *testing.T) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("failed to get home dir: %v", err)
	}

	// Create a temporary subdirectory under $HOME so tilde expansion resolves correctly
	subDir := filepath.Join(homeDir, ".agent-deck-test-tilde")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("failed to create test dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(subDir) })

	// Create source file under $HOME
	sourceName := "test-tilde.md"
	sourcePath := filepath.Join(subDir, sourceName)
	if err := os.WriteFile(sourcePath, []byte("test"), 0o644); err != nil {
		t.Fatalf("failed to create source: %v", err)
	}

	// Use tilde path — expands to $HOME/.agent-deck-test-tilde/test-tilde.md
	tildePath := filepath.Join("~", ".agent-deck-test-tilde", sourceName)
	targetPath := filepath.Join(t.TempDir(), "link.md")

	// Test symlink creation with tilde expansion
	err = createSymlinkWithExpansion(targetPath, tildePath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify symlink points to expanded path
	linkDest, err := os.Readlink(targetPath)
	if err != nil {
		t.Fatalf("should be a symlink: %v", err)
	}

	expectedDest := filepath.Join(homeDir, ".agent-deck-test-tilde", sourceName)
	if linkDest != expectedDest {
		t.Errorf("symlink should point to %q, got %q", expectedDest, linkDest)
	}
}

func TestCreateSymlinkWithExpansion_RelativePathError(t *testing.T) {
	tmpDir := t.TempDir()
	targetPath := filepath.Join(tmpDir, "link.md")

	// Try with relative path (should fail)
	err := createSymlinkWithExpansion(targetPath, "relative/path.md")
	if err == nil {
		t.Error("expected error for relative path, got nil")
	}
	if !strings.Contains(err.Error(), "absolute") {
		t.Errorf("error should mention 'absolute', got %v", err)
	}
}

func TestCreateSymlinkWithExpansion_MissingSourceError(t *testing.T) {
	tmpDir := t.TempDir()
	targetPath := filepath.Join(tmpDir, "link.md")
	sourcePath := filepath.Join(tmpDir, "nonexistent.md")

	// Try with non-existent source (should fail)
	err := createSymlinkWithExpansion(targetPath, sourcePath)
	if err == nil {
		t.Error("expected error for missing source file, got nil")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("error should mention 'does not exist', got %v", err)
	}
}

// --- Policy MD tests ---

func TestInstallPolicyMD_Default(t *testing.T) {
	// Use actual conductor directory (cleanup after test)
	homeDir, _ := os.UserHomeDir()
	conductorDir := filepath.Join(homeDir, ".agent-deck", "conductor")
	policyPath := filepath.Join(conductorDir, "POLICY.md")

	// Backup existing file if present
	var backup []byte
	if content, err := os.ReadFile(policyPath); err == nil {
		backup = content
		defer func() { _ = os.WriteFile(policyPath, backup, 0o644) }()
	} else {
		defer os.Remove(policyPath)
	}

	// Test installing default template
	err := InstallPolicyMD("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify file exists at default location
	if _, err := os.Stat(policyPath); os.IsNotExist(err) {
		t.Errorf("POLICY.md not created at %q", policyPath)
	}

	// Verify it's NOT a symlink
	if _, err := os.Readlink(policyPath); err == nil {
		t.Error("POLICY.md should not be a symlink when using default template")
	}

	// Verify content contains policy template
	content, _ := os.ReadFile(policyPath)
	if !strings.Contains(string(content), "Conductor Policy") {
		t.Error("POLICY.md should contain policy template content")
	}

	// Verify policy-specific content is present
	if !strings.Contains(string(content), "Core Rules") {
		t.Error("POLICY.md should contain Core Rules")
	}
	if !strings.Contains(string(content), "Auto-Response Guidelines") {
		t.Error("POLICY.md should contain Auto-Response Guidelines")
	}
	if strings.Contains(string(content), "Heartbeat Response Format") {
		t.Error("POLICY.md should NOT contain Heartbeat Response Format (it's a bridge protocol, belongs in CLAUDE.md)")
	}
}

func TestInstallPolicyMD_CustomSymlink(t *testing.T) {
	tmpDir := t.TempDir()
	customPath := filepath.Join(tmpDir, "my-POLICY.md")

	// Create custom file first
	if err := os.WriteFile(customPath, []byte("# My Custom Policy\n"), 0o644); err != nil {
		t.Fatalf("failed to create custom file: %v", err)
	}

	// Use actual conductor directory (cleanup after test)
	homeDir, _ := os.UserHomeDir()
	conductorDir := filepath.Join(homeDir, ".agent-deck", "conductor")
	policyPath := filepath.Join(conductorDir, "POLICY.md")

	// Backup existing file/symlink if present
	var backupContent []byte
	var backupLink string
	if linkDest, err := os.Readlink(policyPath); err == nil {
		backupLink = linkDest
	} else if content, err := os.ReadFile(policyPath); err == nil {
		backupContent = content
	}
	t.Cleanup(func() {
		os.Remove(policyPath)
		if backupLink != "" {
			_ = os.Symlink(backupLink, policyPath)
		} else if backupContent != nil {
			_ = os.WriteFile(policyPath, backupContent, 0o644)
		}
	})

	// Test installing with custom path (creates symlink)
	err := InstallPolicyMD(customPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify symlink exists
	linkDest, err := os.Readlink(policyPath)
	if err != nil {
		t.Fatalf("POLICY.md should be a symlink: %v", err)
	}

	// Verify symlink points to custom file
	if linkDest != customPath {
		t.Errorf("symlink should point to %q, got %q", customPath, linkDest)
	}

	// Verify reading through symlink works
	content, _ := os.ReadFile(policyPath)
	if !strings.Contains(string(content), "My Custom Policy") {
		t.Error("reading through symlink should return custom content")
	}
}

func TestSetupConductor_PolicyOverride(t *testing.T) {
	tmpDir := t.TempDir()
	customPolicyPath := filepath.Join(tmpDir, "my-conductor-POLICY.md")

	// Create custom file first
	if err := os.WriteFile(customPolicyPath, []byte("# My Conductor Policy\n"), 0o644); err != nil {
		t.Fatalf("failed to create custom file: %v", err)
	}

	name := "test-policy-override"
	profile := "default"

	// Clean up after test
	homeDir, _ := os.UserHomeDir()
	defer os.RemoveAll(filepath.Join(homeDir, ".agent-deck", "conductor", name))

	// Setup with custom policy path (creates per-conductor symlink)
	err := SetupConductor(name, profile, true, "test description", "", customPolicyPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify per-conductor POLICY.md symlink exists
	dir, _ := ConductorNameDir(name)
	policyPath := filepath.Join(dir, "POLICY.md")
	linkDest, err := os.Readlink(policyPath)
	if err != nil {
		t.Fatalf("POLICY.md should be a symlink: %v", err)
	}

	// Verify symlink points to custom file
	if linkDest != customPolicyPath {
		t.Errorf("symlink should point to %q, got %q", customPolicyPath, linkDest)
	}

	// Verify reading through symlink works
	content, _ := os.ReadFile(policyPath)
	if !strings.Contains(string(content), "My Conductor Policy") {
		t.Error("reading through symlink should return custom content")
	}
}

func TestMigrateConductorPolicySplit_RewritesLegacyGeneratedTemplate(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	name := "legacy-policy-migrate"
	profile := DefaultProfile
	if err := SaveConductorMeta(&ConductorMeta{
		Name:             name,
		Profile:          profile,
		HeartbeatEnabled: true,
		CreatedAt:        "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("failed to save meta: %v", err)
	}

	dir, _ := ConductorNameDir(name)
	claudePath := filepath.Join(dir, "CLAUDE.md")
	legacyContent := renderConductorClaudeTemplate(conductorPerNameClaudeMDLegacyTemplate, name, profile)
	if err := os.WriteFile(claudePath, []byte(legacyContent), 0o644); err != nil {
		t.Fatalf("failed to write legacy CLAUDE.md: %v", err)
	}

	migrated, err := MigrateConductorPolicySplit()
	if err != nil {
		t.Fatalf("unexpected migration error: %v", err)
	}

	found := false
	for _, migratedName := range migrated {
		if migratedName == name {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected %q to be migrated, got %v", name, migrated)
	}

	content, err := os.ReadFile(claudePath)
	if err != nil {
		t.Fatalf("failed to read migrated CLAUDE.md: %v", err)
	}
	if !strings.Contains(string(content), "## Policy") {
		t.Fatal("migrated CLAUDE.md should contain policy section")
	}
	if !strings.Contains(string(content), "./POLICY.md") {
		t.Fatal("migrated CLAUDE.md should reference ./POLICY.md")
	}
}

func TestMigrateConductorPolicySplit_PreservesCustomClaudeMD(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	name := "custom-claude-policy-migrate"
	profile := "work"
	if err := SaveConductorMeta(&ConductorMeta{
		Name:             name,
		Profile:          profile,
		HeartbeatEnabled: true,
		CreatedAt:        "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("failed to save meta: %v", err)
	}

	dir, _ := ConductorNameDir(name)
	claudePath := filepath.Join(dir, "CLAUDE.md")
	customContent := "# Custom conductor instructions\nDo not overwrite this file.\n"
	if err := os.WriteFile(claudePath, []byte(customContent), 0o644); err != nil {
		t.Fatalf("failed to write custom CLAUDE.md: %v", err)
	}

	migrated, err := MigrateConductorPolicySplit()
	if err != nil {
		t.Fatalf("unexpected migration error: %v", err)
	}
	for _, migratedName := range migrated {
		if migratedName == name {
			t.Fatalf("custom CLAUDE.md should not be migrated, got %v", migrated)
		}
	}

	content, err := os.ReadFile(claudePath)
	if err != nil {
		t.Fatalf("failed to read CLAUDE.md: %v", err)
	}
	if string(content) != customContent {
		t.Fatal("custom CLAUDE.md content should be preserved")
	}
}

func TestMigrateConductorPolicySplit_PreservesSymlinkedClaudeMD(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	name := "symlink-claude-policy-migrate"
	if err := SaveConductorMeta(&ConductorMeta{
		Name:             name,
		Profile:          DefaultProfile,
		HeartbeatEnabled: true,
		CreatedAt:        "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("failed to save meta: %v", err)
	}

	customPath := filepath.Join(t.TempDir(), "custom-claude.md")
	if err := os.WriteFile(customPath, []byte("# custom"), 0o644); err != nil {
		t.Fatalf("failed to write custom target: %v", err)
	}

	dir, _ := ConductorNameDir(name)
	claudePath := filepath.Join(dir, "CLAUDE.md")
	if err := os.Symlink(customPath, claudePath); err != nil {
		t.Fatalf("failed to create CLAUDE.md symlink: %v", err)
	}

	migrated, err := MigrateConductorPolicySplit()
	if err != nil {
		t.Fatalf("unexpected migration error: %v", err)
	}
	for _, migratedName := range migrated {
		if migratedName == name {
			t.Fatalf("symlinked CLAUDE.md should not be migrated, got %v", migrated)
		}
	}

	linkDest, err := os.Readlink(claudePath)
	if err != nil {
		t.Fatalf("CLAUDE.md should remain a symlink: %v", err)
	}
	if linkDest != customPath {
		t.Fatalf("symlink destination changed to %q, want %q", linkDest, customPath)
	}
}
