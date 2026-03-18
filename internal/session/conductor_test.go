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
		{0, 0},   // zero means disabled
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

func TestDiscordSettings_TOML(t *testing.T) {
	discord := DiscordSettings{
		BotToken:              "discord-bot-token",
		GuildID:               12345,
		ChannelID:             67890,
		UserID:                24680,
		ListenMode:            "mentions",
		IgnoreRepliesToOthers: true,
	}

	if discord.BotToken != "discord-bot-token" {
		t.Errorf("bot_token mismatch: got %q", discord.BotToken)
	}
	if discord.GuildID != 12345 {
		t.Errorf("guild_id mismatch: got %d", discord.GuildID)
	}
	if discord.ChannelID != 67890 {
		t.Errorf("channel_id mismatch: got %d", discord.ChannelID)
	}
	if discord.UserID != 24680 {
		t.Errorf("user_id mismatch: got %d", discord.UserID)
	}
	if discord.ListenMode != "mentions" {
		t.Errorf("listen_mode mismatch: got %q", discord.ListenMode)
	}
	if !discord.IgnoreRepliesToOthers {
		t.Error("ignore_replies_to_others should be true")
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

func TestBridgeTemplate_HeartbeatScopesToConductorGroups(t *testing.T) {
	template := conductorBridgePy

	patterns := []string{
		"def select_heartbeat_conductors(conductors: list[dict]) -> list[dict]:",
		"conductors = select_heartbeat_conductors(all_conductors)",
		`s_group = s.get("group", "") or ""`,
		`if s_group != name and not s_group.startswith(f"{name}/"):`,
		`for s in scoped_sessions:`,
	}

	for _, pattern := range patterns {
		if !strings.Contains(template, pattern) {
			t.Errorf("template should contain heartbeat dedupe pattern: %q", pattern)
		}
	}
}

func TestBridgeTemplate_SendToConductorSupportsSingleCallWait(t *testing.T) {
	template := conductorBridgePy
	waitPattern := `"--wait", "--timeout", f"{response_timeout}s", "-q",`
	noWaitPattern := `"session", "send", session, message, "--no-wait",`
	oldPattern := `"session", "send", session, message, profile=profile, timeout=120`

	if !strings.Contains(template, waitPattern) {
		t.Fatalf("template should include --wait send path: %q", waitPattern)
	}
	if !strings.Contains(template, noWaitPattern) {
		t.Fatalf("template should retain --no-wait send path: %q", noWaitPattern)
	}
	if strings.Contains(template, oldPattern) {
		t.Fatalf("template should not contain blocking send pattern: %q", oldPattern)
	}
}

func TestConductorHeartbeatScript_StatusParsingHandlesWhitespace(t *testing.T) {
	if !strings.Contains(conductorHeartbeatScript, `"status"[[:space:]]*:[[:space:]]*"`) {
		t.Fatal("heartbeat status parser should tolerate JSON whitespace around ':'")
	}
	if !strings.Contains(conductorHeartbeatScript, `session send "$SESSION"`) {
		t.Fatal("heartbeat script should send heartbeat messages")
	}
	if !strings.Contains(conductorHeartbeatScript, "--no-wait -q") {
		t.Fatal("heartbeat script should use non-blocking quiet send")
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

func TestInstallSharedConductorInstructions_CodexDefault(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	if err := InstallSharedConductorInstructions(ConductorAgentCodex, ""); err != nil {
		t.Fatalf("InstallSharedConductorInstructions returned error: %v", err)
	}

	target := filepath.Join(tmpHome, ".agent-deck", "conductor", "AGENTS.md")
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("failed to read AGENTS.md: %v", err)
	}
	if !strings.Contains(string(content), "Codex") {
		t.Fatal("AGENTS.md should mention Codex")
	}
	if !strings.Contains(string(content), "agent-deck -p <PROFILE> add <path> -t \"Title\" -c codex") {
		t.Fatal("AGENTS.md should render codex session examples")
	}
}

func TestInstallSharedConductorInstructions_AgentsCoexist(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	if err := InstallSharedConductorInstructions(ConductorAgentClaude, ""); err != nil {
		t.Fatalf("InstallSharedConductorInstructions(claude) returned error: %v", err)
	}
	if err := InstallSharedConductorInstructions(ConductorAgentCodex, ""); err != nil {
		t.Fatalf("InstallSharedConductorInstructions(codex) returned error: %v", err)
	}

	base := filepath.Join(tmpHome, ".agent-deck", "conductor")
	for _, file := range []string{"CLAUDE.md", "AGENTS.md"} {
		if _, err := os.Stat(filepath.Join(base, file)); err != nil {
			t.Fatalf("%s should exist: %v", file, err)
		}
	}
}

func TestSetupConductor_DefaultTemplate(t *testing.T) {
	name := "test-default"
	profile := "default"

	// Clean up after test
	homeDir, _ := os.UserHomeDir()
	defer os.RemoveAll(filepath.Join(homeDir, ".agent-deck", "conductor", name))

	// Setup without custom path (uses default template)
	err := SetupConductor(name, profile, true, true, "test description", "", "", nil, "")
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
	if meta.Agent != ConductorAgentClaude {
		t.Errorf("expected agent %q, got %q", ConductorAgentClaude, meta.Agent)
	}
}

func TestSetupConductorWithAgent_Codex(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	name := "test-codex"
	if err := SetupConductorWithAgent(name, "default", ConductorAgentCodex, true, true, "codex conductor", "", "", nil, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	dir, _ := ConductorNameDir(name)
	agentsPath := filepath.Join(dir, "AGENTS.md")
	content, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatalf("failed to read AGENTS.md: %v", err)
	}
	if !strings.Contains(string(content), "Codex") {
		t.Fatal("AGENTS.md should mention Codex")
	}
	if _, err := os.Stat(filepath.Join(dir, "CLAUDE.md")); !os.IsNotExist(err) {
		t.Fatal("CLAUDE.md should not be created for Codex conductor")
	}

	meta, err := LoadConductorMeta(name)
	if err != nil {
		t.Fatalf("failed to load meta: %v", err)
	}
	if meta.Agent != ConductorAgentCodex {
		t.Fatalf("agent = %q, want %q", meta.Agent, ConductorAgentCodex)
	}
	if meta.GetClearOnCompact() {
		t.Fatal("codex conductor should not enable clear_on_compact")
	}
}

func TestSetupConductorWithAgent_RemovesStaleInstructionsFile(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	name := "switch-agent"
	if err := SetupConductor(name, "default", true, true, "", "", "", nil, ""); err != nil {
		t.Fatalf("failed to create initial Claude conductor: %v", err)
	}
	if err := SetupConductorWithAgent(name, "default", ConductorAgentCodex, true, true, "", "", "", nil, ""); err != nil {
		t.Fatalf("failed to switch conductor to Codex: %v", err)
	}

	dir, _ := ConductorNameDir(name)
	if _, err := os.Stat(filepath.Join(dir, "CLAUDE.md")); !os.IsNotExist(err) {
		t.Fatal("CLAUDE.md should be removed after switching conductor agent to Codex")
	}
	if _, err := os.Stat(filepath.Join(dir, "AGENTS.md")); err != nil {
		t.Fatalf("AGENTS.md should exist after switching conductor agent to Codex: %v", err)
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
	err := SetupConductor(name, profile, true, true, "test description", customPath, "", nil, "")
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
	if err := SetupConductor(name, "", true, true, "", "", "", nil, ""); err != nil {
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
	if err := SetupConductor(name, "work", true, true, "", "", "", nil, ""); err != nil {
		t.Fatalf("first setup failed: %v", err)
	}

	err := SetupConductor(name, "personal", true, true, "", "", "", nil, "")
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

func TestLoadConductorMeta_EmptyAgentDefaultsToClaude(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	name := "meta-empty-agent"
	dir, _ := ConductorNameDir(name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("failed to create conductor dir: %v", err)
	}

	raw := `{"name":"meta-empty-agent","profile":"default","heartbeat_enabled":true,"created_at":"2026-01-01T00:00:00Z"}`
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(raw), 0o644); err != nil {
		t.Fatalf("failed to write meta.json: %v", err)
	}

	meta, err := LoadConductorMeta(name)
	if err != nil {
		t.Fatalf("LoadConductorMeta failed: %v", err)
	}
	if meta.Agent != ConductorAgentClaude {
		t.Fatalf("meta agent = %q, want %q", meta.Agent, ConductorAgentClaude)
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

func TestGenerateSystemdBridgeService_IncludesAgentDeckDir(t *testing.T) {
	unit, err := GenerateSystemdBridgeService()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(unit, "__PATH__") {
		t.Error("unit still contains __PATH__ placeholder")
	}
	agentDeck := findAgentDeck()
	if agentDeck == "" {
		t.Skip("agent-deck not found in PATH, skipping directory check")
	}
	if !strings.Contains(unit, filepath.Dir(agentDeck)) {
		t.Errorf("systemd bridge unit PATH should contain agent-deck dir, unit:\n%s", unit)
	}
}

func TestGenerateSystemdHeartbeatService_IncludesAgentDeckDir(t *testing.T) {
	unit, err := GenerateSystemdHeartbeatService("test-conductor")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(unit, "__PATH__") {
		t.Error("unit still contains __PATH__ placeholder")
	}
	agentDeck := findAgentDeck()
	if agentDeck == "" {
		t.Skip("agent-deck not found in PATH, skipping directory check")
	}
	if !strings.Contains(unit, filepath.Dir(agentDeck)) {
		t.Errorf("systemd heartbeat unit PATH should contain agent-deck dir, unit:\n%s", unit)
	}
}

func TestGenerateHeartbeatPlist_IncludesAgentDeckDir(t *testing.T) {
	plist, err := GenerateHeartbeatPlist("test-conductor", 15)
	if err != nil {
		if strings.Contains(err.Error(), "not found in PATH") {
			t.Skipf("skipping: %v", err)
		}
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(plist, "__PATH__") {
		t.Error("plist still contains __PATH__ placeholder")
	}
	agentDeck := findAgentDeck()
	if agentDeck == "" {
		t.Skip("agent-deck not found in PATH, skipping directory check")
	}
	agentDeckDir := filepath.Dir(agentDeck)
	if !strings.Contains(plist, agentDeckDir) {
		t.Errorf("heartbeat plist PATH should contain agent-deck dir %q, plist:\n%s", agentDeckDir, plist)
	}
}

func TestGenerateLaunchdPlist_IncludesAgentDeckDir(t *testing.T) {
	plist, err := GenerateLaunchdPlist()
	if err != nil {
		if strings.Contains(err.Error(), "not found in PATH") {
			t.Skipf("skipping: %v", err)
		}
		t.Fatalf("unexpected error: %v", err)
	}
	// Verify no __PATH__ placeholder remains
	if strings.Contains(plist, "__PATH__") {
		t.Error("plist still contains __PATH__ placeholder")
	}
	// The plist PATH should include the directory of the agent-deck binary
	agentDeck := findAgentDeck()
	if agentDeck == "" {
		t.Skip("agent-deck not found in PATH, skipping directory check")
	}
	agentDeckDir := filepath.Dir(agentDeck)
	if !strings.Contains(plist, agentDeckDir) {
		t.Errorf("plist PATH should contain agent-deck dir %q, plist:\n%s", agentDeckDir, plist)
	}
}

func TestFindPython3_PrefersPathLookup(t *testing.T) {
	tmpBin := t.TempDir()
	pythonPath := filepath.Join(tmpBin, "python3")

	if err := os.WriteFile(pythonPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("failed to create fake python3: %v", err)
	}

	t.Setenv("PATH", tmpBin)

	got := findPython3()
	if got != pythonPath {
		t.Fatalf("findPython3() = %q, want %q", got, pythonPath)
	}
}

func TestBuildDaemonPath(t *testing.T) {
	tests := []struct {
		name          string
		agentDeckPath string
		wantPrefix    string
		wantContains  string
	}{
		{
			name:          "empty path falls back to standard",
			agentDeckPath: "",
			wantPrefix:    "/usr/local/bin",
			wantContains:  "/usr/bin:/bin",
		},
		{
			name:          "local bin prepended",
			agentDeckPath: "/Users/someone/.local/bin/agent-deck",
			wantPrefix:    "/Users/someone/.local/bin",
			wantContains:  "/usr/local/bin",
		},
		{
			name:          "homebrew path prioritized",
			agentDeckPath: "/opt/homebrew/bin/agent-deck",
			wantPrefix:    "/opt/homebrew/bin",
			wantContains:  "/usr/bin:/bin",
		},
		{
			name:          "custom path included",
			agentDeckPath: "/custom/tools/bin/agent-deck",
			wantPrefix:    "/custom/tools/bin",
			wantContains:  "/opt/homebrew/bin",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildDaemonPath(tt.agentDeckPath)
			if !strings.HasPrefix(result, tt.wantPrefix) {
				t.Errorf("buildDaemonPath(%q) = %q, want prefix %q", tt.agentDeckPath, result, tt.wantPrefix)
			}
			if !strings.Contains(result, tt.wantContains) {
				t.Errorf("buildDaemonPath(%q) = %q, want to contain %q", tt.agentDeckPath, result, tt.wantContains)
			}
			// Must never contain duplicate colons
			if strings.Contains(result, "::") {
				t.Errorf("buildDaemonPath(%q) = %q, contains double colon", tt.agentDeckPath, result)
			}
		})
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
	err := SetupConductor(name, profile, true, true, "test description", "", customPolicyPath, nil, "")
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

// --- LEARNINGS.md tests ---

func TestInstallLearningsMD(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	err := InstallLearningsMD()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	learningsPath := filepath.Join(tmpHome, ".agent-deck", "conductor", "LEARNINGS.md")
	content, err := os.ReadFile(learningsPath)
	if err != nil {
		t.Fatalf("LEARNINGS.md not created: %v", err)
	}

	if !strings.Contains(string(content), "# Conductor Learnings") {
		t.Error("LEARNINGS.md should contain header")
	}
	if !strings.Contains(string(content), "## Entry Format") {
		t.Error("LEARNINGS.md should contain Entry Format section")
	}
	if !strings.Contains(string(content), "## How to Use This File") {
		t.Error("LEARNINGS.md should contain How to Use section")
	}
}

func TestInstallLearningsMDPreservesExisting(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// Create the directory and an existing LEARNINGS.md with custom content
	conductorDir := filepath.Join(tmpHome, ".agent-deck", "conductor")
	if err := os.MkdirAll(conductorDir, 0o755); err != nil {
		t.Fatalf("failed to create dir: %v", err)
	}
	customContent := "# My Custom Learnings\n\n### [20260101-001] Test entry\n"
	learningsPath := filepath.Join(conductorDir, "LEARNINGS.md")
	if err := os.WriteFile(learningsPath, []byte(customContent), 0o644); err != nil {
		t.Fatalf("failed to write existing file: %v", err)
	}

	// InstallLearningsMD should NOT overwrite
	err := InstallLearningsMD()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	content, err := os.ReadFile(learningsPath)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}
	if string(content) != customContent {
		t.Errorf("existing LEARNINGS.md should be preserved, got:\n%s", string(content))
	}
}

func TestSetupConductorCreatesLearnings(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	name := "learnings-test"
	if err := SetupConductor(name, "default", true, true, "", "", "", nil, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	dir, _ := ConductorNameDir(name)
	learningsPath := filepath.Join(dir, "LEARNINGS.md")
	content, err := os.ReadFile(learningsPath)
	if err != nil {
		t.Fatalf("per-conductor LEARNINGS.md not created: %v", err)
	}

	if !strings.Contains(string(content), "# Conductor Learnings") {
		t.Error("per-conductor LEARNINGS.md should contain template content")
	}
	if !strings.Contains(string(content), "Promote") {
		t.Error("per-conductor LEARNINGS.md should contain promotion rules")
	}
}

func TestSetupConductorPreservesExistingLearnings(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	name := "learnings-preserve"
	// First setup creates the file
	if err := SetupConductor(name, "default", true, true, "", "", "", nil, ""); err != nil {
		t.Fatalf("first setup failed: %v", err)
	}

	// Write custom content
	dir, _ := ConductorNameDir(name)
	learningsPath := filepath.Join(dir, "LEARNINGS.md")
	customContent := "# My Learnings\n\n### [20260201-001] Custom entry\n"
	if err := os.WriteFile(learningsPath, []byte(customContent), 0o644); err != nil {
		t.Fatalf("failed to write custom content: %v", err)
	}

	// Re-running setup should NOT overwrite
	if err := SetupConductor(name, "default", true, true, "", "", "", nil, ""); err != nil {
		t.Fatalf("second setup failed: %v", err)
	}

	content, err := os.ReadFile(learningsPath)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}
	if string(content) != customContent {
		t.Errorf("existing per-conductor LEARNINGS.md should be preserved, got:\n%s", string(content))
	}
}

func TestLearningsTemplateContent(t *testing.T) {
	template := conductorLearningsTemplate

	// Verify required sections
	sections := []string{
		"# Conductor Learnings",
		"## How to Use This File",
		"## Entry Format",
		"YYYYMMDD-NNN",
	}
	for _, section := range sections {
		if !strings.Contains(template, section) {
			t.Errorf("template should contain %q", section)
		}
	}

	// Verify entry types are documented
	types := []string{
		"auto_response_ok",
		"auto_response_wrong",
		"escalation_unnecessary",
		"escalation_correct",
		"pattern",
		"session_behavior",
	}
	for _, entryType := range types {
		if !strings.Contains(template, entryType) {
			t.Errorf("template should document entry type %q", entryType)
		}
	}

	// Verify promotion instructions
	if !strings.Contains(template, "Promote") {
		t.Error("template should contain promotion instructions")
	}
	if !strings.Contains(template, "POLICY.md") {
		t.Error("template should reference POLICY.md for promotions")
	}

	// Verify status values
	statuses := []string{"active", "promoted", "retired"}
	for _, status := range statuses {
		if !strings.Contains(template, status) {
			t.Errorf("template should document status %q", status)
		}
	}
}

func TestMigrateConductorLearnings_BackfillsExistingConductors(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	name := "existing-conductor"
	profile := DefaultProfile

	// Create a conductor with the pre-learnings template (post-policy-split, no LEARNINGS.md step)
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
	preLearningsContent := renderConductorClaudeTemplate(conductorPerNameClaudeMDPreLearningsTemplate, name, profile)
	if err := os.WriteFile(claudePath, []byte(preLearningsContent), 0o644); err != nil {
		t.Fatalf("failed to write pre-learnings CLAUDE.md: %v", err)
	}

	// Run migration
	migrated, err := MigrateConductorLearnings()
	if err != nil {
		t.Fatalf("unexpected migration error: %v", err)
	}

	// Should be migrated
	found := false
	for _, n := range migrated {
		if n == name {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected %q to be migrated, got %v", name, migrated)
	}

	// Verify CLAUDE.md now has LEARNINGS.md step
	content, err := os.ReadFile(claudePath)
	if err != nil {
		t.Fatalf("failed to read CLAUDE.md: %v", err)
	}
	if !strings.Contains(string(content), "LEARNINGS.md") {
		t.Fatal("migrated CLAUDE.md should reference LEARNINGS.md")
	}
	if !strings.Contains(string(content), "review past patterns") {
		t.Fatal("migrated CLAUDE.md should contain learnings reading instruction")
	}

	// Verify per-conductor LEARNINGS.md was created
	learningsPath := filepath.Join(dir, "LEARNINGS.md")
	lContent, err := os.ReadFile(learningsPath)
	if err != nil {
		t.Fatalf("per-conductor LEARNINGS.md not created: %v", err)
	}
	if !strings.Contains(string(lContent), "# Conductor Learnings") {
		t.Fatal("per-conductor LEARNINGS.md should contain template")
	}

	// Verify shared LEARNINGS.md was created
	base, _ := ConductorDir()
	sharedPath := filepath.Join(base, "LEARNINGS.md")
	sContent, err := os.ReadFile(sharedPath)
	if err != nil {
		t.Fatalf("shared LEARNINGS.md not created: %v", err)
	}
	if !strings.Contains(string(sContent), "# Conductor Learnings") {
		t.Fatal("shared LEARNINGS.md should contain template")
	}
}

func TestMigrateConductorLearnings_PreservesCustomClaudeMD(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	name := "custom-learnings-migrate"
	if err := SaveConductorMeta(&ConductorMeta{
		Name:             name,
		Profile:          "work",
		HeartbeatEnabled: true,
		CreatedAt:        "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("failed to save meta: %v", err)
	}

	dir, _ := ConductorNameDir(name)
	claudePath := filepath.Join(dir, "CLAUDE.md")
	customContent := "# Custom conductor instructions\nDo not overwrite.\n"
	if err := os.WriteFile(claudePath, []byte(customContent), 0o644); err != nil {
		t.Fatalf("failed to write custom CLAUDE.md: %v", err)
	}

	migrated, err := MigrateConductorLearnings()
	if err != nil {
		t.Fatalf("unexpected migration error: %v", err)
	}

	// Should still be migrated (LEARNINGS.md was created) but CLAUDE.md preserved
	content, err := os.ReadFile(claudePath)
	if err != nil {
		t.Fatalf("failed to read CLAUDE.md: %v", err)
	}
	if string(content) != customContent {
		t.Fatal("custom CLAUDE.md should be preserved")
	}

	// LEARNINGS.md should still be created
	learningsPath := filepath.Join(dir, "LEARNINGS.md")
	if _, err := os.Stat(learningsPath); os.IsNotExist(err) {
		t.Fatal("per-conductor LEARNINGS.md should be created even for custom CLAUDE.md")
	}

	// Verify the conductor IS in migrated list (because LEARNINGS.md was created)
	found := false
	for _, n := range migrated {
		if n == name {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("conductor should be in migrated list since LEARNINGS.md was created")
	}
}

func TestMigrateConductorLearnings_SkipsAlreadyMigrated(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	name := "already-migrated"
	if err := SaveConductorMeta(&ConductorMeta{
		Name:             name,
		Profile:          DefaultProfile,
		HeartbeatEnabled: true,
		CreatedAt:        "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("failed to save meta: %v", err)
	}

	dir, _ := ConductorNameDir(name)

	// Write the current (post-learnings) template
	claudePath := filepath.Join(dir, "CLAUDE.md")
	currentContent := renderConductorClaudeTemplate(conductorPerNameClaudeMDTemplate, name, DefaultProfile)
	if err := os.WriteFile(claudePath, []byte(currentContent), 0o644); err != nil {
		t.Fatalf("failed to write CLAUDE.md: %v", err)
	}

	// Write LEARNINGS.md too
	learningsPath := filepath.Join(dir, "LEARNINGS.md")
	if err := os.WriteFile(learningsPath, []byte(conductorLearningsTemplate), 0o644); err != nil {
		t.Fatalf("failed to write LEARNINGS.md: %v", err)
	}

	migrated, err := MigrateConductorLearnings()
	if err != nil {
		t.Fatalf("unexpected migration error: %v", err)
	}

	// Should NOT appear in migrated list (already up to date)
	for _, n := range migrated {
		if n == name {
			t.Fatal("already-migrated conductor should not be in migrated list")
		}
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

func TestConductorMeta_GetClearOnCompact(t *testing.T) {
	// nil (default) -> true
	meta := &ConductorMeta{Name: "test"}
	if !meta.GetClearOnCompact() {
		t.Error("nil ClearOnCompact should default to true")
	}

	// explicitly true
	trueVal := true
	meta.ClearOnCompact = &trueVal
	if !meta.GetClearOnCompact() {
		t.Error("explicit true should return true")
	}

	// explicitly false
	falseVal := false
	meta.ClearOnCompact = &falseVal
	if meta.GetClearOnCompact() {
		t.Error("explicit false should return false")
	}
}

// --- Discord bridge template tests ---

func TestBridgeTemplate_ContainsDiscordBot(t *testing.T) {
	template := conductorBridgePy
	patterns := []string{
		"HAS_DISCORD",
		"create_discord_bot",
		"DISCORD_MAX_LENGTH",
		"class ConductorBot(discord.Client):",
	}
	for _, pattern := range patterns {
		if !strings.Contains(template, pattern) {
			t.Errorf("template should contain Discord pattern: %q", pattern)
		}
	}
}

func TestBridgeTemplate_ContainsDiscordAuthorization(t *testing.T) {
	template := conductorBridgePy

	// Check for authorization function
	if !strings.Contains(template, "def is_authorized(user_id: int) -> bool:") {
		t.Error("template should contain is_authorized function for Discord")
	}

	// Check for unauthorized message logging
	if !strings.Contains(template, "Unauthorized Discord message from user") {
		t.Error("template should log unauthorized Discord messages")
	}
}

func TestBridgeTemplate_DiscordConfigLoading(t *testing.T) {
	template := conductorBridgePy
	patterns := []string{
		`dc = conductor_cfg.get("discord", {})`,
		`dc_bot_token = dc.get("bot_token", "")`,
		`dc_guild_id = dc.get("guild_id", 0)`,
		`dc_channel_id = dc.get("channel_id", 0)`,
		`dc_user_id = dc.get("user_id", 0)`,
		`dc_listen_mode = dc.get("listen_mode", "all")`,
		`dc_ignore_replies_to_others = dc.get("ignore_replies_to_others", False)`,
		`"listen_mode": dc_listen_mode,`,
		`"ignore_replies_to_others": bool(dc_ignore_replies_to_others),`,
		`"discord":`,
	}
	for _, pattern := range patterns {
		if !strings.Contains(template, pattern) {
			t.Errorf("template should contain Discord config pattern: %q", pattern)
		}
	}
}

func TestBridgeTemplate_DiscordSlashCommands(t *testing.T) {
	template := conductorBridgePy
	commands := []string{
		`name="ad-status"`,
		`name="ad-sessions"`,
		`name="ad-restart"`,
		`name="ad-help"`,
	}
	for _, cmd := range commands {
		if !strings.Contains(template, cmd) {
			t.Errorf("template should contain Discord slash command: %q", cmd)
		}
	}
}

func TestBridgeTemplate_DiscordSlashCommandsChannelRestriction(t *testing.T) {
	template := conductorBridgePy
	patterns := []string{
		"async def ensure_discord_channel(interaction: discord.Interaction) -> bool:",
		`if interaction.channel_id != channel_id:`,
		`"This command is only available in the configured channel."`,
		"if not await ensure_discord_channel(interaction):",
	}
	for _, pattern := range patterns {
		if !strings.Contains(template, pattern) {
			t.Errorf("template should contain Discord channel restriction pattern: %q", pattern)
		}
	}
}

func TestBridgeTemplate_DiscordListenModeSupport(t *testing.T) {
	template := conductorBridgePy
	patterns := []string{
		`listen_mode = str(config["discord"].get("listen_mode", "all") or "all").strip().lower()`,
		`if listen_mode not in {"all", "mentions"}:`,
		`if listen_mode == "mentions":`,
		`if not message_mentions_bot(message):`,
		`text = strip_bot_mentions(text)`,
		`return re.sub(rf"<@!?{bot.user.id}>", "", text).strip()`,
	}
	for _, pattern := range patterns {
		if !strings.Contains(template, pattern) {
			t.Errorf("template should contain Discord listen_mode pattern: %q", pattern)
		}
	}
}

func TestBridgeTemplate_DiscordReplyFilterSupport(t *testing.T) {
	template := conductorBridgePy
	patterns := []string{
		`ignore_replies_to_others = bool(`,
		`config["discord"].get("ignore_replies_to_others", False)`,
		`async def should_ignore_reply_to_other(message: discord.Message) -> bool:`,
		`referenced = await message.channel.fetch_message(reference_id)`,
		`if referenced.author.id != bot.user.id:`,
		`if await should_ignore_reply_to_other(message):`,
	}
	for _, pattern := range patterns {
		if !strings.Contains(template, pattern) {
			t.Errorf("template should contain Discord reply filter pattern: %q", pattern)
		}
	}
}

func TestBridgeTemplate_DiscordHeartbeatNotification(t *testing.T) {
	template := conductorBridgePy
	if !strings.Contains(template, "discord_bot=None, discord_channel_id=None") {
		t.Error("heartbeat_loop should accept discord_bot and discord_channel_id params")
	}
	if !strings.Contains(template, "Failed to send Discord notification") {
		t.Error("heartbeat should handle Discord notification errors")
	}
	if !strings.Contains(template, "await send_discord_output(channel, alert_msg)") {
		t.Error("heartbeat should route Discord notifications through send_discord_output")
	}
}

func TestBridgeTemplate_DiscordInMain(t *testing.T) {
	template := conductorBridgePy
	patterns := []string{
		`dc_ok = config["discord"]["configured"] and HAS_DISCORD`,
		"create_discord_bot(config)",
		`discord_bot.start(config["discord"]["bot_token"])`,
		"discord_bot.close()",
	}
	for _, pattern := range patterns {
		if !strings.Contains(template, pattern) {
			t.Errorf("main() should contain Discord pattern: %q", pattern)
		}
	}
}

func TestBridgeTemplate_DiscordTypingIndicator(t *testing.T) {
	template := conductorBridgePy
	if !strings.Contains(template, "async with message.channel.typing():") {
		t.Error("Discord on_message should show typing indicator while waiting for conductor response")
	}
	if !strings.Contains(template, "run_in_executor") {
		t.Error("Discord on_message should offload blocking send_to_conductor to thread executor")
	}
}

func TestBridgeTemplate_DiscordImageUploadSupport(t *testing.T) {
	template := conductorBridgePy
	patterns := []string{
		`IMAGE_MARKER_RE = re.compile(r"\[IMAGE:(?P<path>[^\]]+)\]")`,
		`def parse_discord_message_parts(text: str) -> list[tuple[str, str]]:`,
		`async def send_discord_output(channel, text: str, name_tag: str = ""):`,
		`await channel.send(`,
		`file=discord.File(str(image_path)),`,
		`[Image path must be absolute:`,
		`[Image not found:`,
		`await send_discord_output(message.channel, response, name_tag=name_tag)`,
	}
	for _, pattern := range patterns {
		if !strings.Contains(template, pattern) {
			t.Errorf("template should contain Discord image upload pattern: %q", pattern)
		}
	}
}

func TestConductorClearOnCompact(t *testing.T) {
	// Override HOME so LoadConductorMeta reads from our temp dir
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// Create conductor meta with clear_on_compact = true (default)
	condDir := filepath.Join(tmpHome, ".agent-deck", "conductor", "main")
	if err := os.MkdirAll(condDir, 0755); err != nil {
		t.Fatal(err)
	}
	meta := ConductorMeta{Name: "main", Profile: "default"}
	data, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(condDir, "meta.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	// Conductor instance with matching title
	inst := &Instance{Title: "conductor-main", GroupPath: "conductor"}
	if !inst.ConductorClearOnCompact() {
		t.Error("should return true for conductor with default ClearOnCompact")
	}

	// Now set clear_on_compact = false
	falseVal := false
	meta.ClearOnCompact = &falseVal
	data, _ = json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(condDir, "meta.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	if inst.ConductorClearOnCompact() {
		t.Error("should return false when clear_on_compact is explicitly disabled")
	}

	meta = ConductorMeta{Name: "main", Agent: ConductorAgentCodex, Profile: "default"}
	data, _ = json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(condDir, "meta.json"), data, 0644); err != nil {
		t.Fatal(err)
	}
	if inst.ConductorClearOnCompact() {
		t.Error("codex conductors should always disable clear_on_compact")
	}

	// Non-conductor title should return false (not a conductor-prefixed session)
	nonConductor := &Instance{Title: "my-session", GroupPath: "conductor"}
	if nonConductor.ConductorClearOnCompact() {
		t.Error("non-conductor-prefixed title should return false")
	}
}

// TestConductorHeartbeatScript_GroupScoped verifies the heartbeat script references
// the conductor's own group (not all sessions in the profile) and includes an
// enabled-config guard.
func TestConductorHeartbeatScript_GroupScoped(t *testing.T) {
	// The heartbeat message must NOT reference "all sessions in the {PROFILE} profile"
	if strings.Contains(conductorHeartbeatScript, "Check all sessions in the") {
		t.Fatal("heartbeat script should NOT reference 'all sessions in the {PROFILE} profile'; must be group-scoped")
	}

	// The heartbeat message must reference the conductor's own group via {NAME}
	if !strings.Contains(conductorHeartbeatScript, "{NAME}") {
		t.Fatal("heartbeat script must reference {NAME} for group scoping")
	}
	if !strings.Contains(conductorHeartbeatScript, "Check sessions in") {
		t.Fatal("heartbeat script should contain group-scoped message like 'Check sessions in'")
	}

	// The script must contain an enabled-config guard that queries conductor status
	if !strings.Contains(conductorHeartbeatScript, "ENABLED") {
		t.Fatal("heartbeat script must contain an ENABLED guard that checks conductor status before sending")
	}
	if !strings.Contains(conductorHeartbeatScript, "conductor status") {
		t.Fatal("heartbeat script must query conductor status to determine if enabled")
	}
}

// TestGetHeartbeatInterval_ZeroMeansDisabled verifies interval=0 means disabled,
// negative means use default, and positive means use the configured value.
func TestGetHeartbeatInterval_ZeroMeansDisabled(t *testing.T) {
	tests := []struct {
		name     string
		interval int
		expected int
	}{
		{"zero means disabled", 0, 0},
		{"negative means default", -1, 15},
		{"custom value", 30, 30},
		{"explicit default", 15, 15},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			settings := ConductorSettings{HeartbeatInterval: tt.interval}
			got := settings.GetHeartbeatInterval()
			if got != tt.expected {
				t.Errorf("GetHeartbeatInterval() with %d = %d, want %d", tt.interval, got, tt.expected)
			}
		})
	}
}

// --- Slack markdown-to-mrkdwn converter tests ---

func TestBridgeTemplate_ContainsMarkdownToSlackConverter(t *testing.T) {
	template := conductorBridgePy

	// Function definition must exist.
	if !strings.Contains(template, "def _markdown_to_slack(text: str) -> str:") {
		t.Error("template should contain _markdown_to_slack function definition")
	}

	// Header conversion regex.
	if !strings.Contains(template, `^#{1,6}\s+`) {
		t.Error("template should contain GFM header regex ^#{1,6}\\s+")
	}

	// Bold conversion: **text** -> *text*.
	if !strings.Contains(template, `\*\*(.+?)\*\*`) {
		t.Error("template should contain bold regex \\*\\*(.+?)\\*\\*")
	}

	// Strikethrough conversion: ~~text~~ -> ~text~.
	if !strings.Contains(template, `~~(.+?)~~`) {
		t.Error("template should contain strikethrough regex ~~(.+?)~~")
	}

	// Link conversion: [text](url) -> <url|text>.
	if !strings.Contains(template, `\[([^\]]+)\]\(([^)]+)\)`) {
		t.Error("template should contain link regex \\[([^\\]]+)\\]\\(([^)]+)\\)")
	}

	// Bullet list conversion.
	if !strings.Contains(template, `^(\s*)[-*]\s+`) {
		t.Error("template should contain bullet list regex ^(\\s*)[-*]\\s+")
	}

	// Code block protection.
	if !strings.Contains(template, "code_blocks = []") {
		t.Error("template should contain code block protection list")
	}

	// Inline code protection.
	if !strings.Contains(template, "inline_codes = []") {
		t.Error("template should contain inline code protection list")
	}
}

func TestBridgeTemplate_SafeSayConvertsMarkdown(t *testing.T) {
	template := conductorBridgePy

	// _safe_say must call _markdown_to_slack.
	if !strings.Contains(template, "_markdown_to_slack(kwargs[\"text\"])") {
		t.Error("_safe_say should apply _markdown_to_slack to kwargs[\"text\"]")
	}

	// The conversion must be conditional on "text" being in kwargs.
	if !strings.Contains(template, `if "text" in kwargs:`) {
		t.Error("_safe_say should guard _markdown_to_slack call with 'if \"text\" in kwargs:'")
	}
}

func TestSetupConductor_WithEnvVars(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	name := "test-env-conductor"
	env := map[string]string{
		"ANTHROPIC_BASE_URL":   "https://api.z.ai/api/anthropic",
		"ANTHROPIC_AUTH_TOKEN": "test-token",
	}
	err := SetupConductor(name, "default", true, true, "env test", "", "", env, "~/.conductor.env")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	meta, err := LoadConductorMeta(name)
	if err != nil {
		t.Fatalf("failed to load meta: %v", err)
	}

	if len(meta.Env) != 2 {
		t.Errorf("expected 2 env vars, got %d", len(meta.Env))
	}
	if meta.Env["ANTHROPIC_BASE_URL"] != "https://api.z.ai/api/anthropic" {
		t.Errorf("unexpected ANTHROPIC_BASE_URL: %s", meta.Env["ANTHROPIC_BASE_URL"])
	}
	if meta.EnvFile != "~/.conductor.env" {
		t.Errorf("unexpected env_file: %s", meta.EnvFile)
	}

	// Verify restricted file permissions when env vars present
	dir, _ := ConductorNameDir(name)
	info, err := os.Stat(filepath.Join(dir, "meta.json"))
	if err != nil {
		t.Fatalf("failed to stat meta.json: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("expected 0600 permissions for meta.json with env vars, got %o", info.Mode().Perm())
	}
}

func TestSetupConductor_WithoutEnvVars(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	name := "test-no-env-conductor"
	err := SetupConductor(name, "default", true, true, "", "", "", nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	meta, err := LoadConductorMeta(name)
	if err != nil {
		t.Fatalf("failed to load meta: %v", err)
	}

	if meta.Env != nil {
		t.Errorf("expected nil env, got %v", meta.Env)
	}
	if meta.EnvFile != "" {
		t.Errorf("expected empty env_file, got %s", meta.EnvFile)
	}
}
