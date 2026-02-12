package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
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
}

// TelegramSettings defines Telegram bot configuration for the conductor bridge
type TelegramSettings struct {
	// Token is the Telegram bot token from @BotFather
	Token string `toml:"token"`

	// UserID is the authorized Telegram user ID from @userinfobot
	UserID int64 `toml:"user_id"`
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
	return &meta, nil
}

// SaveConductorMeta writes meta.json for a conductor
func SaveConductorMeta(meta *ConductorMeta) error {
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

// SetupConductor creates the conductor directory, per-conductor CLAUDE.md, and meta.json.
// It does NOT register the session (that's done by the CLI handler which has access to storage).
func SetupConductor(name, profile string, heartbeatEnabled bool, description string) error {
	dir, err := ConductorNameDir(name)
	if err != nil {
		return fmt.Errorf("failed to get conductor dir: %w", err)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create conductor dir: %w", err)
	}

	// Write per-conductor CLAUDE.md with name and profile substitution
	content := strings.ReplaceAll(conductorPerNameClaudeMDTemplate, "{NAME}", name)
	content = strings.ReplaceAll(content, "{PROFILE}", profile)
	claudeMD := filepath.Join(dir, "CLAUDE.md")
	if err := os.WriteFile(claudeMD, []byte(content), 0o644); err != nil {
		return fmt.Errorf("failed to write CLAUDE.md: %w", err)
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
	script := strings.ReplaceAll(conductorHeartbeatScript, "{NAME}", name)
	script = strings.ReplaceAll(script, "{PROFILE}", profile)
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
STATUS=$(agent-deck -p "$PROFILE" session show "$SESSION" --json 2>/dev/null | grep -o '"status":"[^"]*"' | cut -d'"' -f4)

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
	return SetupConductor(profile, profile, true, "")
}

// InstallSharedClaudeMD writes the shared CLAUDE.md to the conductor base directory.
// This contains CLI reference, protocols, and rules shared by all conductors.
func InstallSharedClaudeMD() error {
	dir, err := ConductorDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create conductor dir: %w", err)
	}
	claudeMD := filepath.Join(dir, "CLAUDE.md")
	if err := os.WriteFile(claudeMD, []byte(conductorSharedClaudeMDTemplate), 0o644); err != nil {
		return fmt.Errorf("failed to write shared CLAUDE.md: %w", err)
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
