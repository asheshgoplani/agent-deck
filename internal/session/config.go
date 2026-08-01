package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
)

const (
	// DefaultProfile is the name of the default profile
	DefaultProfile = "default"

	// ProfilesDirName is the directory containing all profiles
	ProfilesDirName = "profiles"

	// ConfigFileName is the global config file name
	ConfigFileName = "config.json"
)

// Config represents the global agent-deck configuration
type Config struct {
	// DefaultProfile is the profile to use when none is specified
	DefaultProfile string `json:"default_profile"`

	// LastUsed is the most recently used profile (for future use)
	LastUsed string `json:"last_used,omitempty"`

	// Version tracks config format for future migrations
	Version int `json:"version"`
}

// GetAgentDeckDir returns the effective agent-deck data directory.
// It is a broad compatibility wrapper for data/runtime callers, not the
// config root. Profile/session migrations must use profileDataRootDir().
func GetAgentDeckDir() (string, error) {
	return agentpaths.EffectiveDataDir(
		ProfilesDirName,
		"sessions.json",
		"hooks",
		"events",
		"inboxes",
		"conductor",
		"watcher",
		"watchers",
		"locks",
		"runtime",
		"logs",
		"cost-events",
	)
}

func profileDataRootDir() (string, error) {
	return agentpaths.EffectiveDataDir(ProfilesDirName, "sessions.json")
}

// GetConfigPath returns the path to the global config file
func GetConfigPath() (string, error) {
	return agentpaths.EffectiveConfigPath(ConfigFileName)
}

// GetProfilesDir returns the path to the profiles directory
func GetProfilesDir() (string, error) {
	dir, err := profileDataRootDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, ProfilesDirName), nil
}

// GetProfileDir returns the path to a specific profile's directory
func GetProfileDir(profile string) (string, error) {
	if profile == "" {
		profile = DefaultProfile
	}

	// Sanitize profile name (prevent path traversal)
	profile = filepath.Base(profile)
	if profile == "." || profile == ".." {
		return "", fmt.Errorf("invalid profile name: %s", profile)
	}

	profilesDir, err := GetProfilesDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(profilesDir, profile), nil
}

// LoadConfig loads the global configuration
func LoadConfig() (*Config, error) {
	configPath, err := GetConfigPath()
	if err != nil {
		return nil, err
	}

	// Check if config exists
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		// Return default config
		return &Config{
			DefaultProfile: DefaultProfile,
			Version:        1,
		}, nil
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config: %w", err)
	}

	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	// Ensure default profile is set
	if config.DefaultProfile == "" {
		config.DefaultProfile = DefaultProfile
	}

	return &config, nil
}

// SaveConfig saves the global configuration
func SaveConfig(config *Config) error {
	configPath, err := GetConfigPath()
	if err != nil {
		return err
	}

	// Ensure directory exists
	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(configPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	return nil
}

// ListProfiles returns all available profile names
func ListProfiles() ([]string, error) {
	profilesDir, err := GetProfilesDir()
	if err != nil {
		return nil, err
	}

	// Check if profiles directory exists
	if _, err := os.Stat(profilesDir); os.IsNotExist(err) {
		// No profiles yet - check if we need migration
		return []string{}, nil
	}

	entries, err := os.ReadDir(profilesDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read profiles directory: %w", err)
	}

	var profiles []string
	for _, entry := range entries {
		if entry.IsDir() {
			// Check for state.db (SQLite, v0.11.0+) or sessions.json (legacy, auto-migrates on open)
			dbPath := filepath.Join(profilesDir, entry.Name(), "state.db")
			jsonPath := filepath.Join(profilesDir, entry.Name(), "sessions.json")
			if _, err := os.Stat(dbPath); err == nil {
				profiles = append(profiles, entry.Name())
			} else if _, err := os.Stat(jsonPath); err == nil {
				profiles = append(profiles, entry.Name())
			}
		}
	}

	sort.Strings(profiles)
	return profiles, nil
}

// ProfileExists checks if a profile exists
func ProfileExists(profile string) (bool, error) {
	profileDir, err := GetProfileDir(profile)
	if err != nil {
		return false, err
	}

	// Check for state.db (SQLite, v0.11.0+) or sessions.json (legacy)
	dbPath := filepath.Join(profileDir, "state.db")
	if _, err = os.Stat(dbPath); err == nil {
		return true, nil
	}
	jsonPath := filepath.Join(profileDir, "sessions.json")
	if _, err = os.Stat(jsonPath); err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// CreateProfile creates a new empty profile
func CreateProfile(profile string) error {
	// Validate profile name
	if profile == "" {
		return fmt.Errorf("profile name cannot be empty")
	}

	// Check if already exists
	exists, err := ProfileExists(profile)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("profile '%s' already exists", profile)
	}

	profileDir, err := GetProfileDir(profile)
	if err != nil {
		return err
	}

	// Create profile directory
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		return fmt.Errorf("failed to create profile directory: %w", err)
	}

	// Initialize SQLite database for the new profile.
	// NewStorageWithProfile auto-creates tables, so just opening it is sufficient.
	_, err = NewStorageWithProfile(profile)
	if err != nil {
		return fmt.Errorf("failed to initialize profile storage: %w", err)
	}

	return nil
}

// DeleteProfile deletes a profile and all its data
func DeleteProfile(profile string) error {
	// Prevent deleting the default profile if it's the only one
	if profile == DefaultProfile {
		profiles, err := ListProfiles()
		if err != nil {
			return err
		}
		if len(profiles) <= 1 {
			return fmt.Errorf("cannot delete the only remaining profile")
		}
	}

	profileDir, err := GetProfileDir(profile)
	if err != nil {
		return err
	}

	// Check if profile exists
	exists, err := ProfileExists(profile)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("profile '%s' does not exist", profile)
	}

	// Remove the profile directory
	if err := os.RemoveAll(profileDir); err != nil {
		return fmt.Errorf("failed to delete profile: %w", err)
	}

	// Update config if this was the default profile
	config, err := LoadConfig()
	if err != nil {
		return err
	}
	if config.DefaultProfile == profile {
		config.DefaultProfile = DefaultProfile
		if err := SaveConfig(config); err != nil {
			return fmt.Errorf("profile deleted but failed to update config: %w", err)
		}
	}

	return nil
}

// SetDefaultProfile sets the default profile in the config
func SetDefaultProfile(profile string) error {
	// Verify profile exists
	exists, err := ProfileExists(profile)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("profile '%s' does not exist", profile)
	}

	config, err := LoadConfig()
	if err != nil {
		return err
	}

	config.DefaultProfile = profile
	return SaveConfig(config)
}

// Profile resolution sources returned by getEffectiveProfileWithSource.
// Callers that must decide whether it is safe to silently materialise a
// profile on first use (e.g. NewStorageWithProfile) key off this value —
// see issue #1790.
const (
	// ProfileSourceExplicit means the caller passed a profile explicitly
	// (typically the -p/--profile flag). Auto-creating on first use is the
	// documented, intentional way to spin up a new profile.
	ProfileSourceExplicit = "explicit"
	// ProfileSourceEnv means AGENTDECK_PROFILE selected the profile. Also
	// explicit user/CI intent (e.g. AGENTDECK_PROFILE=_test); auto-create
	// on first use is expected and unchanged.
	ProfileSourceEnv = "env"
	// ProfileSourceInferred means the profile name was *derived* from
	// CLAUDE_CONFIG_DIR (issue #881), not chosen by the user for
	// agent-deck's purposes. CLAUDE_CONFIG_DIR selects a Claude account,
	// not an agent-deck profile — a name landing here that doesn't match
	// an existing profile must not be silently auto-created (#1790).
	ProfileSourceInferred = "inferred"
	// ProfileSourceConfigDefault means config.json's default_profile applied.
	ProfileSourceConfigDefault = "config-default"
	// ProfileSourceFallback means nothing resolved and "default" applied.
	ProfileSourceFallback = "fallback-default"
)

// GetEffectiveProfile returns the profile to use, considering:
// 1. Explicitly provided profile (from -p flag)
// 2. Environment variable AGENTDECK_PROFILE
// 3. Inferred from CLAUDE_CONFIG_DIR (e.g. ~/.claude-work -> "work")
// 4. Config default profile
// 5. Fallback to "default"
//
// Priority 3 was added to fix issue #881: prior to this, the TUI's
// profile.DetectCurrentProfile honored CLAUDE_CONFIG_DIR while the web /
// storage / push paths did not, so the same user on the same machine could
// see different sessions in TUI vs web. Both call sites now route through
// this function to guarantee a single source of truth.
func GetEffectiveProfile(explicit string) string {
	profile, _ := getEffectiveProfileWithSource(explicit)
	return profile
}

// getEffectiveProfileWithSource is GetEffectiveProfile plus the resolution
// source, so callers that create on-disk state (NewStorageWithProfile) can
// tell an explicit/env-selected profile (safe to auto-create) apart from one
// merely inferred from CLAUDE_CONFIG_DIR (must not be silently materialised
// if it doesn't already exist — #1790).
func getEffectiveProfileWithSource(explicit string) (string, string) {
	if explicit != "" {
		return explicit, ProfileSourceExplicit
	}

	if envProfile := os.Getenv("AGENTDECK_PROFILE"); envProfile != "" {
		return envProfile, ProfileSourceEnv
	}

	if inferred := profileFromClaudeConfigDir(os.Getenv("CLAUDE_CONFIG_DIR")); inferred != "" {
		return inferred, ProfileSourceInferred
	}

	config, err := LoadConfig()
	if err != nil {
		return DefaultProfile, ProfileSourceFallback
	}

	if config.DefaultProfile != "" {
		return config.DefaultProfile, ProfileSourceConfigDefault
	}

	return DefaultProfile, ProfileSourceFallback
}

// configuredDefaultProfile returns config.json's default_profile, or
// DefaultProfile ("default") when unset or unreadable. This mirrors the
// tail of getEffectiveProfileWithSource (priority 4-5) and is the safe
// landing spot ResolveProfileForStorage falls back to instead of an
// unrecognized CLAUDE_CONFIG_DIR-inferred name (#1790).
func configuredDefaultProfile() string {
	config, err := LoadConfig()
	if err != nil || config.DefaultProfile == "" {
		return DefaultProfile
	}
	return config.DefaultProfile
}

// ResolveProfileForStorage is GetEffectiveProfile plus the #1790 safety
// guard: a profile name merely *inferred* from CLAUDE_CONFIG_DIR (e.g.
// ~/.claude-work -> "work") is not user intent to select an agent-deck
// profile — CLAUDE_CONFIG_DIR picks a Claude account, a separate axis. If
// that inferred name doesn't already exist, this warns on stderr and falls
// back to the configured default profile instead of a name that would go on
// to be silently auto-created as an empty profile, potentially shadowing a
// real configured default with no message. This matches #1790's "Expected"
// behavior verbatim: do not auto-create, and do not hard-error either — say
// so and fall back. A hard error here would make agent-deck refuse to start
// from any shell exporting an unrelated CLAUDE_CONFIG_DIR whose basename
// merely contains a dash (profileFromClaudeConfigDir's generic last-segment
// fallback is intentionally broad, per #881/profile_resolver_test.go) —
// turning "wrong profile inferred" into "agent-deck won't launch", which is
// strictly worse for a class of paths far wider than the CLAUDE_CONFIG_DIR
// dual-account pattern this inference exists for.
//
// Every call site that is about to CREATE or OPEN on-disk profile state from
// a possibly-empty/possibly-inferred profile argument (NewStorageWithProfile,
// and any pre-resolution done before handing a profile name down to it, e.g.
// the web server bootstrap in cmd/agent-deck/main.go) must route through
// this function rather than GetEffectiveProfile, or the guard is bypassed:
// once a caller has already resolved "" to a concrete inferred name and
// passes that name onward, it looks indistinguishable from an explicit -p
// selection to any code further down the chain.
//
// Explicit (-p) and env (AGENTDECK_PROFILE) resolution are unaffected —
// those remain the documented way to spin up a brand-new profile on first
// use.
//
// The only error this returns is a genuine ProfileExists I/O failure
// (permission, transient filesystem); it never fails open on such an error
// (fail open would silently restore the pre-#1790 auto-create hole).
func ResolveProfileForStorage(explicit string) (string, error) {
	effectiveProfile, source := getEffectiveProfileWithSource(explicit)
	if source != ProfileSourceInferred {
		return effectiveProfile, nil
	}

	exists, existsErr := ProfileExists(effectiveProfile)
	if existsErr != nil {
		return "", fmt.Errorf("checking whether inferred profile %q exists: %w", effectiveProfile, existsErr)
	}
	if exists {
		return effectiveProfile, nil
	}

	fallback := configuredDefaultProfile()
	known, listErr := ListProfiles()
	knownDesc := "none yet"
	if listErr == nil && len(known) > 0 {
		knownDesc = strings.Join(known, ", ")
	}
	fmt.Fprintf(os.Stderr,
		"agent-deck: CLAUDE_CONFIG_DIR=%q would select profile %q, which does not exist; "+
			"falling back to profile %q instead of creating it. Known profiles: %s. "+
			"Pass -p/--profile or set AGENTDECK_PROFILE explicitly to pick a different profile, "+
			"run `agent-deck profile create %s` if you intend to create it, "+
			"or unset CLAUDE_CONFIG_DIR to use the default profile.\n",
		os.Getenv("CLAUDE_CONFIG_DIR"), effectiveProfile, fallback, knownDesc, effectiveProfile,
	)
	return fallback, nil
}

// profileFromClaudeConfigDir maps a CLAUDE_CONFIG_DIR path to a profile name.
// The supported shapes mirror the cdw / cdp shell aliases that drive the
// dual-profile setup:
//
//	~/.claude-work        -> "work"
//	~/.claude-personal    -> "personal"
//	~/.claude             -> ""  (no inference; let config default apply)
//	/opt/claude-prod      -> "prod"
//
// Returns "" when no profile can be inferred — the caller then falls back
// to the global config default.
func profileFromClaudeConfigDir(configDir string) string {
	if configDir == "" {
		return ""
	}
	baseName := filepath.Base(configDir)
	if strings.HasPrefix(baseName, ".claude-") {
		if suffix := strings.TrimPrefix(baseName, ".claude-"); suffix != "" {
			return suffix
		}
	}
	if strings.Contains(baseName, "-") {
		parts := strings.Split(baseName, "-")
		if last := parts[len(parts)-1]; last != "" {
			return last
		}
	}
	return ""
}
