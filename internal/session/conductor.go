package session

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/platform"
)

// ConductorSettings defines conductor (meta-agent orchestration) configuration
type ConductorSettings struct {
	// Enabled activates the conductor system
	Enabled bool `toml:"enabled"`

	// HeartbeatInterval is the interval in minutes between heartbeat checks
	// Default: 15
	HeartbeatInterval int `toml:"heartbeat_interval"`

	// Profiles is the list of agent-deck profiles to manage
	// Kept for backward compat but ignored after migration to meta.json-based discovery
	Profiles []string `toml:"profiles"`

	// Telegram defines Telegram bot integration settings
	Telegram TelegramSettings `toml:"telegram"`

	// Slack defines Slack bot integration settings
	Slack SlackSettings `toml:"slack"`
}

// TelegramSettings defines Telegram bot configuration for the conductor bridge
type TelegramSettings struct {
	// Token is the Telegram bot token from @BotFather
	Token string `toml:"token"`

	// UserID is the authorized Telegram user ID from @userinfobot
	UserID int64 `toml:"user_id"`
}

// SlackSettings defines Slack bot configuration for the conductor bridge
type SlackSettings struct {
	// BotToken is the Slack bot token (xoxb-...)
	BotToken string `toml:"bot_token"`

	// AppToken is the Slack app-level token for Socket Mode (xapp-...)
	AppToken string `toml:"app_token"`

	// ChannelID is the Slack channel where the bot listens and posts (C01234...)
	ChannelID string `toml:"channel_id"`

	// ListenMode controls when the bot responds: "mentions" (only @mentions) or "all" (all channel messages)
	// Default: "mentions"
	ListenMode string `toml:"listen_mode"`

	// AllowedUserIDs is a list of Slack user IDs authorized to use the bot.
	// If empty, all users are allowed (backward compatible).
	// Get user ID from Slack: Right-click user → View profile → More → Copy member ID
	AllowedUserIDs []string `toml:"allowed_user_ids"`
}

// ConductorMeta holds metadata for a named conductor instance
type ConductorMeta struct {
	Name              string `json:"name"`
	Profile           string `json:"profile"`
	HeartbeatEnabled  bool   `json:"heartbeat_enabled"`
	HeartbeatInterval int    `json:"heartbeat_interval"` // 0 = use global default
	Description       string `json:"description,omitempty"`
	CreatedAt         string `json:"created_at"`
}

// conductorNameRegex validates conductor names: starts with alphanumeric, then alphanumeric/._-
var conductorNameRegex = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

// GetHeartbeatInterval returns the heartbeat interval, defaulting to 15 minutes
func (c *ConductorSettings) GetHeartbeatInterval() int {
	if c.HeartbeatInterval <= 0 {
		return 15
	}
	return c.HeartbeatInterval
}

// GetProfiles returns the configured profiles, defaulting to ["default"]
func (c *ConductorSettings) GetProfiles() []string {
	if len(c.Profiles) == 0 {
		return []string{DefaultProfile}
	}
	return c.Profiles
}

// normalizeConductorProfile returns a stable profile value for conductor metadata.
// Empty profile values are normalized to the canonical default profile.
func normalizeConductorProfile(profile string) string {
	if profile == "" {
		return DefaultProfile
	}
	return profile
}

// ConductorDir returns the base conductor directory (~/.agent-deck/conductor)
func ConductorDir() (string, error) {
	dir, err := GetAgentDeckDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "conductor"), nil
}

// ConductorNameDir returns the directory for a named conductor (~/.agent-deck/conductor/<name>)
func ConductorNameDir(name string) (string, error) {
	base, err := ConductorDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, name), nil
}

// ConductorProfileDir returns the per-profile conductor directory.
// Deprecated: Use ConductorNameDir instead. Kept for backward compatibility.
func ConductorProfileDir(profile string) (string, error) {
	return ConductorNameDir(profile)
}

// ConductorSessionTitle returns the session title for a named conductor
func ConductorSessionTitle(name string) string {
	return fmt.Sprintf("conductor-%s", name)
}

// ValidateConductorName checks that a conductor name is valid
func ValidateConductorName(name string) error {
	if name == "" {
		return fmt.Errorf("conductor name cannot be empty")
	}
	if len(name) > 64 {
		return fmt.Errorf("conductor name too long (max 64 characters)")
	}
	if !conductorNameRegex.MatchString(name) {
		return fmt.Errorf("invalid conductor name %q: must start with alphanumeric and contain only alphanumeric, dots, underscores, or hyphens", name)
	}
	return nil
}

// IsConductorSetup checks if a named conductor is set up by verifying meta.json exists
func IsConductorSetup(name string) bool {
	dir, err := ConductorNameDir(name)
	if err != nil {
		return false
	}
	metaPath := filepath.Join(dir, "meta.json")
	if _, err := os.Stat(metaPath); os.IsNotExist(err) {
		return false
	}
	return true
}

// LoadConductorMeta reads meta.json for a named conductor
func LoadConductorMeta(name string) (*ConductorMeta, error) {
	dir, err := ConductorNameDir(name)
	if err != nil {
		return nil, err
	}
	metaPath := filepath.Join(dir, "meta.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read meta.json for conductor %q: %w", name, err)
	}
	var meta ConductorMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("failed to parse meta.json for conductor %q: %w", name, err)
	}
	if meta.Name == "" {
		meta.Name = name
	}
	meta.Profile = normalizeConductorProfile(meta.Profile)
	return &meta, nil
}

// SaveConductorMeta writes meta.json for a conductor
func SaveConductorMeta(meta *ConductorMeta) error {
	if meta == nil {
		return fmt.Errorf("conductor metadata cannot be nil")
	}
	if meta.Name == "" {
		return fmt.Errorf("conductor name cannot be empty")
	}
	meta.Profile = normalizeConductorProfile(meta.Profile)

	dir, err := ConductorNameDir(meta.Name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create conductor dir: %w", err)
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal meta.json: %w", err)
	}
	metaPath := filepath.Join(dir, "meta.json")
	if err := os.WriteFile(metaPath, data, 0o644); err != nil {
		return fmt.Errorf("failed to write meta.json: %w", err)
	}
	return nil
}

// ListConductors scans all conductor directories that have meta.json
func ListConductors() ([]ConductorMeta, error) {
	base, err := ConductorDir()
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(base); os.IsNotExist(err) {
		return nil, nil
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil, fmt.Errorf("failed to read conductor directory: %w", err)
	}
	var conductors []ConductorMeta
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		metaPath := filepath.Join(base, entry.Name(), "meta.json")
		data, err := os.ReadFile(metaPath)
		if err != nil {
			continue // skip dirs without meta.json
		}
		var meta ConductorMeta
		if err := json.Unmarshal(data, &meta); err != nil {
			continue
		}
		if meta.Name == "" {
			meta.Name = entry.Name()
		}
		meta.Profile = normalizeConductorProfile(meta.Profile)
		conductors = append(conductors, meta)
	}
	return conductors, nil
}

// ListConductorsForProfile returns conductors belonging to a specific profile
func ListConductorsForProfile(profile string) ([]ConductorMeta, error) {
	all, err := ListConductors()
	if err != nil {
		return nil, err
	}
	var filtered []ConductorMeta
	for _, c := range all {
		if c.Profile == profile {
			filtered = append(filtered, c)
		}
	}
	return filtered, nil
}

func renderConductorClaudeTemplate(baseTemplate, name, profile string) string {
	content := strings.ReplaceAll(baseTemplate, "{NAME}", name)
	if profile == DefaultProfile {
		// For default profile, show "default" in display text and omit -p flag in commands
		content = strings.ReplaceAll(content, "{PROFILE}", "default")
		content = strings.ReplaceAll(content, "agent-deck -p default ", "agent-deck ")
		content = strings.ReplaceAll(content, "Always pass `-p default` to all CLI commands.", "Use CLI commands without `-p` flag (default profile).")
	} else {
		content = strings.ReplaceAll(content, "{PROFILE}", profile)
	}
	return content
}

func matchesTemplateContent(actual, expected string) bool {
	return strings.TrimSuffix(actual, "\n") == strings.TrimSuffix(expected, "\n")
}

// SetupConductor creates the conductor directory, per-conductor CLAUDE.md, and meta.json.
// If customClaudeMD is provided, creates a symlink instead of writing the template.
// If customPolicyMD is provided, creates a per-conductor POLICY.md symlink (overrides the shared POLICY.md).
// It does NOT register the session (that's done by the CLI handler which has access to storage).
func SetupConductor(name, profile string, heartbeatEnabled bool, description string, customClaudeMD string, customPolicyMD string) error {
	if err := ValidateConductorName(name); err != nil {
		return err
	}
	profile = normalizeConductorProfile(profile)

	if existing, err := LoadConductorMeta(name); err == nil {
		if existing.Profile != profile {
			return fmt.Errorf("conductor %q already exists for profile %q (requested profile: %q)", name, existing.Profile, profile)
		}
	}

	dir, err := ConductorNameDir(name)
	if err != nil {
		return fmt.Errorf("failed to get conductor dir: %w", err)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create conductor dir: %w", err)
	}

	targetPath := filepath.Join(dir, "CLAUDE.md")

	if customClaudeMD != "" {
		// Custom path provided - create symlink
		if err := createSymlinkWithExpansion(targetPath, customClaudeMD); err != nil {
			return err
		}
	} else if info, err := os.Lstat(targetPath); err != nil || info.Mode()&os.ModeSymlink == 0 {
		// No custom path - write default template (but preserve existing symlink)
		content := renderConductorClaudeTemplate(conductorPerNameClaudeMDTemplate, name, profile)
		if err := os.WriteFile(targetPath, []byte(content), 0o644); err != nil {
			return fmt.Errorf("failed to write CLAUDE.md: %w", err)
		}
	}

	// Write per-conductor POLICY.md symlink if custom path provided
	if customPolicyMD != "" {
		policyPath := filepath.Join(dir, "POLICY.md")
		if err := createSymlinkWithExpansion(policyPath, customPolicyMD); err != nil {
			return fmt.Errorf("failed to create POLICY.md symlink: %w", err)
		}
	}

	// Write meta.json
	meta := &ConductorMeta{
		Name:             name,
		Profile:          profile,
		HeartbeatEnabled: heartbeatEnabled,
		Description:      description,
		CreatedAt:        time.Now().UTC().Format(time.RFC3339),
	}
	if err := SaveConductorMeta(meta); err != nil {
		return fmt.Errorf("failed to write meta.json: %w", err)
	}

	return nil
}

// InstallHeartbeatScript writes the heartbeat.sh script for a conductor.
// This is a standalone heartbeat that works without Telegram.
func InstallHeartbeatScript(name, profile string) error {
	dir, err := ConductorNameDir(name)
	if err != nil {
		return err
	}
	profile = normalizeConductorProfile(profile)

	script := strings.ReplaceAll(conductorHeartbeatScript, "{NAME}", name)
	if profile == DefaultProfile {
		// For default profile, omit -p flag entirely
		script = strings.ReplaceAll(script, "{PROFILE}", "default")
		script = strings.ReplaceAll(script, `-p "$PROFILE" `, "")
		script = strings.ReplaceAll(script, `$PROFILE profile`, "default profile")
	} else {
		script = strings.ReplaceAll(script, "{PROFILE}", profile)
	}
	scriptPath := filepath.Join(dir, "heartbeat.sh")
	return os.WriteFile(scriptPath, []byte(script), 0o755)
}

// HeartbeatPlistLabel returns the launchd label for a conductor's heartbeat
func HeartbeatPlistLabel(name string) string {
	return fmt.Sprintf("com.agentdeck.conductor-heartbeat.%s", name)
}

// GenerateHeartbeatPlist returns a launchd plist for a conductor's heartbeat timer
func GenerateHeartbeatPlist(name string, intervalMinutes int) (string, error) {
	dir, err := ConductorNameDir(name)
	if err != nil {
		return "", err
	}

	agentDeckPath := findAgentDeck()
	if agentDeckPath == "" {
		return "", fmt.Errorf("agent-deck not found in PATH")
	}

	scriptPath := filepath.Join(dir, "heartbeat.sh")
	logPath := filepath.Join(dir, "heartbeat.log")
	label := HeartbeatPlistLabel(name)
	intervalSeconds := intervalMinutes * 60

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	plist := strings.ReplaceAll(conductorHeartbeatPlistTemplate, "__LABEL__", label)
	plist = strings.ReplaceAll(plist, "__SCRIPT_PATH__", scriptPath)
	plist = strings.ReplaceAll(plist, "__LOG_PATH__", logPath)
	plist = strings.ReplaceAll(plist, "__HOME__", homeDir)
	plist = strings.ReplaceAll(plist, "__INTERVAL__", fmt.Sprintf("%d", intervalSeconds))

	return plist, nil
}

// HeartbeatPlistPath returns the path where a conductor's heartbeat plist should be installed
func HeartbeatPlistPath(name string) (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(homeDir, "Library", "LaunchAgents", HeartbeatPlistLabel(name)+".plist"), nil
}

// RemoveHeartbeatPlist removes the launchd plist for a conductor's heartbeat
func RemoveHeartbeatPlist(name string) error {
	path, err := HeartbeatPlistPath(name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	return os.Remove(path)
}

// findAgentDeck looks for agent-deck in common locations
func findAgentDeck() string {
	paths := []string{
		"/usr/local/bin/agent-deck",
		"/opt/homebrew/bin/agent-deck",
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		p := filepath.Join(dir, "agent-deck")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// conductorHeartbeatScript is the shell script that sends a heartbeat to a conductor session
const conductorHeartbeatScript = `#!/bin/bash
# Heartbeat for conductor: {NAME} (profile: {PROFILE})
# Sends a check-in message to the conductor session

SESSION="conductor-{NAME}"
PROFILE="{PROFILE}"

# Only send if the session is running
STATUS=$(agent-deck -p "$PROFILE" session show "$SESSION" --json 2>/dev/null | tr -d '\n' | sed -n 's/.*"status"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')

if [ "$STATUS" = "idle" ] || [ "$STATUS" = "waiting" ]; then
    agent-deck -p "$PROFILE" session send "$SESSION" "Heartbeat: Check all sessions in the $PROFILE profile. List any waiting sessions, auto-respond where safe, and report what needs my attention."
fi
`

// conductorHeartbeatPlistTemplate is the launchd plist for a per-conductor heartbeat timer
const conductorHeartbeatPlistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>__LABEL__</string>

    <key>ProgramArguments</key>
    <array>
        <string>/bin/bash</string>
        <string>__SCRIPT_PATH__</string>
    </array>

    <key>StartInterval</key>
    <integer>__INTERVAL__</integer>

    <key>StandardOutPath</key>
    <string>__LOG_PATH__</string>

    <key>StandardErrorPath</key>
    <string>__LOG_PATH__</string>

    <key>WorkingDirectory</key>
    <string>__HOME__</string>

    <key>EnvironmentVariables</key>
    <dict>
        <key>PATH</key>
        <string>/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
        <key>HOME</key>
        <string>__HOME__</string>
    </dict>

    <key>LowPriorityIO</key>
    <true/>
</dict>
</plist>
`

// SetupConductorProfile creates the conductor directory and CLAUDE.md for a profile.
// Deprecated: Use SetupConductor instead. Kept for backward compatibility.
func SetupConductorProfile(profile string) error {
	return SetupConductor(profile, profile, true, "", "", "")
}

// createSymlinkWithExpansion creates a symlink from target to source, with ~ expansion and validation.
// target: the symlink path (e.g., ~/.agent-deck/conductor/CLAUDE.md)
// source: the user's custom file path (e.g., ~/my/custom.md)
func createSymlinkWithExpansion(target, source string) error {
	// Expand ~ in source path
	if strings.HasPrefix(source, "~/") {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("failed to expand ~: %w", err)
		}
		source = filepath.Join(homeDir, source[2:])
	}

	// Validate source is absolute
	if !filepath.IsAbs(source) {
		return fmt.Errorf("custom path must be absolute or start with ~/: %s", source)
	}

	// Check if source file exists
	if _, err := os.Stat(source); os.IsNotExist(err) {
		return fmt.Errorf("custom file does not exist: %s\nCreate the file first, then run setup again", source)
	}

	// Remove existing file/symlink at target
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove existing file: %w", err)
	}

	// Create symlink
	if err := os.Symlink(source, target); err != nil {
		return fmt.Errorf("failed to create symlink: %w", err)
	}

	return nil
}

// InstallSharedClaudeMD writes the shared CLAUDE.md to the conductor base directory,
// or creates a symlink if customPath is provided.
// This contains CLI reference, protocols, and rules shared by all conductors.
func InstallSharedClaudeMD(customPath string) error {
	dir, err := ConductorDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}
	targetPath := filepath.Join(dir, "CLAUDE.md")

	if customPath != "" {
		// Custom path provided - create symlink
		return createSymlinkWithExpansion(targetPath, customPath)
	}

	// No custom path - write default template (but preserve existing symlink)
	if info, err := os.Lstat(targetPath); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	if err := os.WriteFile(targetPath, []byte(conductorSharedClaudeMDTemplate), 0o644); err != nil {
		return fmt.Errorf("failed to write shared CLAUDE.md: %w", err)
	}
	return nil
}

// InstallPolicyMD writes the default POLICY.md to the conductor base directory,
// or creates a symlink if customPath is provided.
// This contains agent behavior rules (auto-response policy, escalation guidelines).
func InstallPolicyMD(customPath string) error {
	dir, err := ConductorDir()
	if err != nil {
		return err
	}
	targetPath := filepath.Join(dir, "POLICY.md")

	if customPath != "" {
		// Custom path provided - create symlink
		return createSymlinkWithExpansion(targetPath, customPath)
	}

	// No custom path - write default template (but preserve existing symlink)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}
	if info, err := os.Lstat(targetPath); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	if err := os.WriteFile(targetPath, []byte(conductorPolicyTemplate), 0o644); err != nil {
		return fmt.Errorf("failed to write POLICY.md: %w", err)
	}
	return nil
}

// TeardownConductor removes the conductor directory for a named conductor.
// It does NOT remove the session from storage (that's done by the CLI handler).
func TeardownConductor(name string) error {
	dir, err := ConductorNameDir(name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil // Already removed
	}
	return os.RemoveAll(dir)
}

// TeardownConductorProfile removes the conductor directory for a profile.
// Deprecated: Use TeardownConductor instead. Kept for backward compatibility.
func TeardownConductorProfile(profile string) error {
	return TeardownConductor(profile)
}

// MigrateLegacyConductors scans for conductor dirs that have CLAUDE.md but no meta.json,
// and creates meta.json for them. Returns the names of migrated conductors.
func MigrateLegacyConductors() ([]string, error) {
	base, err := ConductorDir()
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(base); os.IsNotExist(err) {
		return nil, nil
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil, fmt.Errorf("failed to read conductor directory: %w", err)
	}
	var migrated []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		dirPath := filepath.Join(base, name)
		metaPath := filepath.Join(dirPath, "meta.json")
		claudePath := filepath.Join(dirPath, "CLAUDE.md")

		// Skip if meta.json already exists (already migrated)
		if _, err := os.Stat(metaPath); err == nil {
			continue
		}
		// Skip if no CLAUDE.md (not a conductor dir)
		if _, err := os.Stat(claudePath); os.IsNotExist(err) {
			continue
		}

		// Legacy conductor: name=dirName, profile=dirName
		meta := &ConductorMeta{
			Name:             name,
			Profile:          name,
			HeartbeatEnabled: true,
			CreatedAt:        time.Now().UTC().Format(time.RFC3339),
		}
		if err := SaveConductorMeta(meta); err != nil {
			continue
		}
		migrated = append(migrated, name)
	}
	return migrated, nil
}

// MigrateConductorPolicySplit updates legacy generated per-conductor CLAUDE.md
// templates to include POLICY.md instructions.
// It only rewrites non-symlink CLAUDE.md files that exactly match the legacy generated template.
func MigrateConductorPolicySplit() ([]string, error) {
	base, err := ConductorDir()
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(base); os.IsNotExist(err) {
		return nil, nil
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil, fmt.Errorf("failed to read conductor directory: %w", err)
	}

	var migrated []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		claudePath := filepath.Join(base, name, "CLAUDE.md")

		info, err := os.Lstat(claudePath)
		if err != nil {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}

		meta, err := LoadConductorMeta(name)
		if err != nil {
			continue
		}

		contentBytes, err := os.ReadFile(claudePath)
		if err != nil {
			continue
		}
		content := string(contentBytes)

		// Already on the new template format (or custom file with policy instructions).
		if strings.Contains(content, "## Policy") && strings.Contains(content, "POLICY.md") {
			continue
		}

		legacyTemplate := renderConductorClaudeTemplate(conductorPerNameClaudeMDLegacyTemplate, name, meta.Profile)
		if !matchesTemplateContent(content, legacyTemplate) {
			continue
		}

		updatedTemplate := renderConductorClaudeTemplate(conductorPerNameClaudeMDTemplate, name, meta.Profile)
		if err := os.WriteFile(claudePath, []byte(updatedTemplate), 0o644); err != nil {
			return migrated, fmt.Errorf("failed to migrate %s CLAUDE.md: %w", name, err)
		}
		migrated = append(migrated, name)
	}

	return migrated, nil
}

// InstallBridgeScript copies bridge.py to the conductor base directory.
// It writes from the embedded const.
func InstallBridgeScript() error {
	dir, err := ConductorDir()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create conductor dir: %w", err)
	}

	bridgePath := filepath.Join(dir, "bridge.py")
	if err := os.WriteFile(bridgePath, []byte(conductorBridgePy), 0o755); err != nil {
		return fmt.Errorf("failed to write bridge.py: %w", err)
	}

	return nil
}

// GetConductorSettings loads and returns conductor settings from config
func GetConductorSettings() ConductorSettings {
	config, err := LoadUserConfig()
	if err != nil || config == nil {
		return ConductorSettings{}
	}
	return config.Conductor
}

// LaunchdPlistName is the launchd label for the conductor bridge daemon
const LaunchdPlistName = "com.agentdeck.conductor-bridge"

// GenerateLaunchdPlist returns a launchd plist with paths substituted
func GenerateLaunchdPlist() (string, error) {
	condDir, err := ConductorDir()
	if err != nil {
		return "", err
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	// Find python3
	python3Path := findPython3()
	if python3Path == "" {
		return "", fmt.Errorf("python3 not found in PATH")
	}

	bridgePath := filepath.Join(condDir, "bridge.py")
	logPath := filepath.Join(condDir, "bridge.log")

	plist := strings.ReplaceAll(conductorPlistTemplate, "__PYTHON3__", python3Path)
	plist = strings.ReplaceAll(plist, "__BRIDGE_PATH__", bridgePath)
	plist = strings.ReplaceAll(plist, "__LOG_PATH__", logPath)
	plist = strings.ReplaceAll(plist, "__HOME__", homeDir)

	return plist, nil
}

// LaunchdPlistPath returns the path where the plist should be installed
func LaunchdPlistPath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(homeDir, "Library", "LaunchAgents", LaunchdPlistName+".plist"), nil
}

// findPython3 looks for python3 in common locations
func findPython3() string {
	paths := []string{
		"/opt/homebrew/bin/python3",
		"/usr/local/bin/python3",
		"/usr/bin/python3",
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	// Try PATH lookup
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		p := filepath.Join(dir, "python3")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// conductorPlistTemplate is the launchd plist for the bridge daemon
const conductorPlistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.agentdeck.conductor-bridge</string>

    <key>ProgramArguments</key>
    <array>
        <string>__PYTHON3__</string>
        <string>__BRIDGE_PATH__</string>
    </array>

    <key>RunAtLoad</key>
    <true/>

    <key>KeepAlive</key>
    <true/>

    <key>StandardOutPath</key>
    <string>__LOG_PATH__</string>

    <key>StandardErrorPath</key>
    <string>__LOG_PATH__</string>

    <key>WorkingDirectory</key>
    <string>__HOME__</string>

    <key>EnvironmentVariables</key>
    <dict>
        <key>PATH</key>
        <string>/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
        <key>HOME</key>
        <string>__HOME__</string>
    </dict>

    <key>ThrottleInterval</key>
    <integer>10</integer>

    <key>LowPriorityIO</key>
    <true/>
</dict>
</plist>
`

// --- Systemd unit templates ---

const systemdBridgeServiceTemplate = `[Unit]
Description=Agent Deck Conductor Bridge
After=network.target

[Service]
Type=simple
ExecStart=__PYTHON3__ __BRIDGE_PATH__
Restart=always
RestartSec=10
WorkingDirectory=__HOME__
StandardOutput=append:__LOG_PATH__
StandardError=append:__LOG_PATH__
Environment=PATH=/usr/local/bin:/usr/bin:/bin
Environment=HOME=__HOME__

[Install]
WantedBy=default.target
`

const systemdHeartbeatTimerTemplate = `[Unit]
Description=Agent Deck Conductor Heartbeat Timer (__NAME__)

[Timer]
OnBootSec=__INTERVAL__s
OnUnitActiveSec=__INTERVAL__s

[Install]
WantedBy=timers.target
`

const systemdHeartbeatServiceTemplate = `[Unit]
Description=Agent Deck Conductor Heartbeat (__NAME__)

[Service]
Type=oneshot
ExecStart=/bin/bash __SCRIPT_PATH__
WorkingDirectory=__HOME__
Environment=PATH=/usr/local/bin:/usr/bin:/bin
Environment=HOME=__HOME__
`

// --- Systemd path helpers ---

const systemdBridgeServiceName = "agent-deck-conductor-bridge.service"

// SystemdUserDir returns the systemd user unit directory (~/.config/systemd/user/)
func SystemdUserDir() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(homeDir, ".config", "systemd", "user"), nil
}

// SystemdBridgeServicePath returns the full path to the bridge systemd service file
func SystemdBridgeServicePath() (string, error) {
	dir, err := SystemdUserDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, systemdBridgeServiceName), nil
}

// SystemdHeartbeatServiceName returns the systemd service name for a conductor heartbeat
func SystemdHeartbeatServiceName(name string) string {
	return fmt.Sprintf("agent-deck-conductor-heartbeat-%s.service", name)
}

// SystemdHeartbeatTimerName returns the systemd timer name for a conductor heartbeat
func SystemdHeartbeatTimerName(name string) string {
	return fmt.Sprintf("agent-deck-conductor-heartbeat-%s.timer", name)
}

// SystemdHeartbeatServicePath returns the full path to a heartbeat systemd service
func SystemdHeartbeatServicePath(name string) (string, error) {
	dir, err := SystemdUserDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, SystemdHeartbeatServiceName(name)), nil
}

// SystemdHeartbeatTimerPath returns the full path to a heartbeat systemd timer
func SystemdHeartbeatTimerPath(name string) (string, error) {
	dir, err := SystemdUserDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, SystemdHeartbeatTimerName(name)), nil
}

// --- Systemd unit generators ---

// GenerateSystemdBridgeService returns a systemd unit for the bridge daemon
func GenerateSystemdBridgeService() (string, error) {
	condDir, err := ConductorDir()
	if err != nil {
		return "", err
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	python3Path := findPython3()
	if python3Path == "" {
		return "", fmt.Errorf("python3 not found in PATH")
	}
	bridgePath := filepath.Join(condDir, "bridge.py")
	logPath := filepath.Join(condDir, "bridge.log")

	unit := strings.ReplaceAll(systemdBridgeServiceTemplate, "__PYTHON3__", python3Path)
	unit = strings.ReplaceAll(unit, "__BRIDGE_PATH__", bridgePath)
	unit = strings.ReplaceAll(unit, "__LOG_PATH__", logPath)
	unit = strings.ReplaceAll(unit, "__HOME__", homeDir)
	return unit, nil
}

// GenerateSystemdHeartbeatTimer returns a systemd timer unit for a conductor heartbeat
func GenerateSystemdHeartbeatTimer(name string, intervalMinutes int) string {
	intervalSeconds := intervalMinutes * 60
	unit := strings.ReplaceAll(systemdHeartbeatTimerTemplate, "__NAME__", name)
	unit = strings.ReplaceAll(unit, "__INTERVAL__", fmt.Sprintf("%d", intervalSeconds))
	return unit
}

// GenerateSystemdHeartbeatService returns a systemd service unit for a conductor heartbeat
func GenerateSystemdHeartbeatService(name string) (string, error) {
	dir, err := ConductorNameDir(name)
	if err != nil {
		return "", err
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	scriptPath := filepath.Join(dir, "heartbeat.sh")
	unit := strings.ReplaceAll(systemdHeartbeatServiceTemplate, "__NAME__", name)
	unit = strings.ReplaceAll(unit, "__SCRIPT_PATH__", scriptPath)
	unit = strings.ReplaceAll(unit, "__HOME__", homeDir)
	return unit, nil
}

// --- Platform-aware daemon management ---

// systemdUserAvailable checks if systemd user session is functional.
// Returns false on containers/VMs without a running user manager (common with SSH-only access).
// Verifies XDG_RUNTIME_DIR exists and loginctl can show the current user session,
// which is more reliable than just checking daemon-reload success.
func systemdUserAvailable() bool {
	// Check 1: XDG_RUNTIME_DIR must be set (indicates a proper login session)
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		return false
	}
	if _, err := os.Stat(runtimeDir); err != nil {
		return false
	}

	// Check 2: loginctl show-user verifies systemd-logind manages this user
	if err := exec.Command("loginctl", "show-user", "--no-pager").Run(); err != nil {
		// Fallback: try daemon-reload (loginctl may not be available)
		return exec.Command("systemctl", "--user", "daemon-reload").Run() == nil
	}

	return true
}

// InstallBridgeDaemon installs and starts the bridge daemon.
// macOS: launchd plist; Linux: systemd user service.
// Returns the unit/plist file path on success.
func InstallBridgeDaemon() (string, error) {
	plat := platform.Detect()
	switch plat {
	case platform.PlatformMacOS:
		return installBridgeDaemonLaunchd()
	case platform.PlatformLinux, platform.PlatformWSL2:
		return installBridgeDaemonSystemd()
	default:
		condDir, _ := ConductorDir()
		return "", fmt.Errorf("unsupported platform %s for daemon management; run manually: python3 %s/bridge.py", plat, condDir)
	}
}

func installBridgeDaemonLaunchd() (string, error) {
	plistContent, err := GenerateLaunchdPlist()
	if err != nil {
		return "", fmt.Errorf("failed to generate plist: %w", err)
	}
	plistPath, err := LaunchdPlistPath()
	if err != nil {
		return "", fmt.Errorf("failed to get plist path: %w", err)
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Join(homeDir, "Library", "LaunchAgents"), 0o755); err != nil {
		return "", fmt.Errorf("failed to create LaunchAgents dir: %w", err)
	}
	_ = exec.Command("launchctl", "unload", plistPath).Run()
	if err := os.WriteFile(plistPath, []byte(plistContent), 0o644); err != nil {
		return "", fmt.Errorf("failed to write plist: %w", err)
	}
	if err := exec.Command("launchctl", "load", plistPath).Run(); err != nil {
		return plistPath, fmt.Errorf("plist written but failed to load daemon: %w", err)
	}
	return plistPath, nil
}

func installBridgeDaemonSystemd() (string, error) {
	unitContent, err := GenerateSystemdBridgeService()
	if err != nil {
		return "", fmt.Errorf("failed to generate systemd unit: %w", err)
	}
	unitPath, err := SystemdBridgeServicePath()
	if err != nil {
		return "", fmt.Errorf("failed to get systemd unit path: %w", err)
	}
	dir, err := SystemdUserDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create systemd user dir: %w", err)
	}
	if err := os.WriteFile(unitPath, []byte(unitContent), 0o644); err != nil {
		return "", fmt.Errorf("failed to write systemd unit: %w", err)
	}
	if !systemdUserAvailable() {
		condDir, _ := ConductorDir()
		return "", fmt.Errorf("systemd user session not available (common in containers/VMs without lingering); run manually: python3 %s/bridge.py", condDir)
	}
	if err := exec.Command("systemctl", "--user", "enable", "--now", systemdBridgeServiceName).Run(); err != nil {
		return unitPath, fmt.Errorf("unit written but enable failed: %w", err)
	}
	return unitPath, nil
}

// UninstallBridgeDaemon stops and removes the bridge daemon.
func UninstallBridgeDaemon() error {
	plat := platform.Detect()
	switch plat {
	case platform.PlatformMacOS:
		return uninstallBridgeDaemonLaunchd()
	case platform.PlatformLinux, platform.PlatformWSL2:
		return uninstallBridgeDaemonSystemd()
	default:
		return nil
	}
}

func uninstallBridgeDaemonLaunchd() error {
	plistPath, err := LaunchdPlistPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(plistPath); os.IsNotExist(err) {
		return nil
	}
	_ = exec.Command("launchctl", "unload", plistPath).Run()
	return os.Remove(plistPath)
}

func uninstallBridgeDaemonSystemd() error {
	_ = exec.Command("systemctl", "--user", "disable", "--now", systemdBridgeServiceName).Run()
	unitPath, err := SystemdBridgeServicePath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(unitPath); os.IsNotExist(err) {
		return nil
	}
	if err := os.Remove(unitPath); err != nil {
		return err
	}
	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	return nil
}

// IsBridgeDaemonRunning checks if the bridge daemon is currently running.
func IsBridgeDaemonRunning() bool {
	plat := platform.Detect()
	switch plat {
	case platform.PlatformMacOS:
		out, err := exec.Command("launchctl", "list", LaunchdPlistName).Output()
		return err == nil && len(out) > 0
	case platform.PlatformLinux, platform.PlatformWSL2:
		err := exec.Command("systemctl", "--user", "is-active", "--quiet", systemdBridgeServiceName).Run()
		return err == nil
	default:
		return false
	}
}

// BridgeDaemonHint returns a platform-appropriate hint for starting the bridge daemon.
func BridgeDaemonHint() string {
	plat := platform.Detect()
	switch plat {
	case platform.PlatformMacOS:
		plistPath, err := LaunchdPlistPath()
		if err == nil {
			if _, err := os.Stat(plistPath); err == nil {
				return fmt.Sprintf("Start daemon with: launchctl load %s", plistPath)
			}
		}
		return "Run 'agent-deck conductor setup <name>' to install the daemon"
	case platform.PlatformLinux, platform.PlatformWSL2:
		condDir, _ := ConductorDir()
		if !systemdUserAvailable() {
			return fmt.Sprintf("Run manually: python3 %s/bridge.py", condDir)
		}
		unitPath, err := SystemdBridgeServicePath()
		if err == nil {
			if _, err := os.Stat(unitPath); err == nil {
				return "Start daemon with: systemctl --user start agent-deck-conductor-bridge"
			}
		}
		return "Run 'agent-deck conductor setup <name>' to install the daemon"
	default:
		condDir, _ := ConductorDir()
		return fmt.Sprintf("Run manually: python3 %s/bridge.py", condDir)
	}
}

// InstallHeartbeatDaemon installs and starts the heartbeat timer for a conductor.
// macOS: launchd plist; Linux: systemd timer/service pair.
func InstallHeartbeatDaemon(name, profile string, intervalMinutes int) error {
	plat := platform.Detect()
	switch plat {
	case platform.PlatformMacOS:
		return installHeartbeatDaemonLaunchd(name, intervalMinutes)
	case platform.PlatformLinux, platform.PlatformWSL2:
		return installHeartbeatDaemonSystemd(name, intervalMinutes)
	default:
		return fmt.Errorf("unsupported platform %s for heartbeat daemon; run heartbeat.sh manually via cron", plat)
	}
}

func installHeartbeatDaemonLaunchd(name string, intervalMinutes int) error {
	plistContent, err := GenerateHeartbeatPlist(name, intervalMinutes)
	if err != nil {
		return fmt.Errorf("failed to generate heartbeat plist: %w", err)
	}
	hbPlistPath, err := HeartbeatPlistPath(name)
	if err != nil {
		return err
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	_ = os.MkdirAll(filepath.Join(homeDir, "Library", "LaunchAgents"), 0o755)
	_ = exec.Command("launchctl", "unload", hbPlistPath).Run()
	if err := os.WriteFile(hbPlistPath, []byte(plistContent), 0o644); err != nil {
		return fmt.Errorf("failed to write heartbeat plist: %w", err)
	}
	if err := exec.Command("launchctl", "load", hbPlistPath).Run(); err != nil {
		return fmt.Errorf("plist written but failed to load: %w", err)
	}
	return nil
}

func installHeartbeatDaemonSystemd(name string, intervalMinutes int) error {
	dir, err := SystemdUserDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create systemd user dir: %w", err)
	}

	svcContent, err := GenerateSystemdHeartbeatService(name)
	if err != nil {
		return fmt.Errorf("failed to generate heartbeat service: %w", err)
	}
	svcPath, err := SystemdHeartbeatServicePath(name)
	if err != nil {
		return err
	}
	if err := os.WriteFile(svcPath, []byte(svcContent), 0o644); err != nil {
		return fmt.Errorf("failed to write heartbeat service: %w", err)
	}

	timerContent := GenerateSystemdHeartbeatTimer(name, intervalMinutes)
	timerPath, err := SystemdHeartbeatTimerPath(name)
	if err != nil {
		return err
	}
	if err := os.WriteFile(timerPath, []byte(timerContent), 0o644); err != nil {
		return fmt.Errorf("failed to write heartbeat timer: %w", err)
	}

	if !systemdUserAvailable() {
		condDir, _ := ConductorNameDir(name)
		return fmt.Errorf("systemd user session not available; run heartbeat manually via cron or: bash %s/heartbeat.sh", condDir)
	}
	timerName := SystemdHeartbeatTimerName(name)
	if err := exec.Command("systemctl", "--user", "enable", "--now", timerName).Run(); err != nil {
		return fmt.Errorf("failed to enable heartbeat timer: %w", err)
	}
	return nil
}

// UninstallHeartbeatDaemon stops and removes the heartbeat timer for a conductor.
func UninstallHeartbeatDaemon(name string) error {
	plat := platform.Detect()
	switch plat {
	case platform.PlatformMacOS:
		return uninstallHeartbeatDaemonLaunchd(name)
	case platform.PlatformLinux, platform.PlatformWSL2:
		return uninstallHeartbeatDaemonSystemd(name)
	default:
		return nil
	}
}

func uninstallHeartbeatDaemonLaunchd(name string) error {
	hbPlistPath, err := HeartbeatPlistPath(name)
	if err != nil {
		return err
	}
	_ = exec.Command("launchctl", "unload", hbPlistPath).Run()
	return RemoveHeartbeatPlist(name)
}

func uninstallHeartbeatDaemonSystemd(name string) error {
	timerName := SystemdHeartbeatTimerName(name)
	_ = exec.Command("systemctl", "--user", "disable", "--now", timerName).Run()

	timerPath, err := SystemdHeartbeatTimerPath(name)
	if err == nil {
		_ = os.Remove(timerPath)
	}
	svcPath, err := SystemdHeartbeatServicePath(name)
	if err == nil {
		_ = os.Remove(svcPath)
	}
	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	return nil
}
