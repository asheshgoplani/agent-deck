package session

import (
	"os"
	"path/filepath"
	"strings"
)

// DefaultGooseSettings returns GooseSettings with sensible defaults.
// Command defaults to "goose"; ConfigDir defaults to gooseDefaultConfigDir().
func DefaultGooseSettings() *GooseSettings {
	return &GooseSettings{
		Command:   "goose",
		ConfigDir: gooseDefaultConfigDir(),
	}
}

// gooseDefaultConfigDir returns the Goose config directory.
// Respects XDG_CONFIG_HOME; falls back to ~/.config/goose.
func gooseDefaultConfigDir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "goose")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".config", "goose")
	}
	return filepath.Join(home, ".config", "goose")
}

// BuildGooseCommand builds the launch command for Goose CLI.
// Returns a string slice suitable for exec.Command (first element is the
// binary, rest are arguments).
//
// Goose opens in the current working directory, so projectPath is used as
// the CWD rather than a CLI argument.
func BuildGooseCommand(settings *GooseSettings, projectPath string, profile string) []string {
	cmd := "goose"
	if settings != nil && settings.Command != "" {
		cmd = settings.Command
	}

	// ponytail: split command string so flags in Command are preserved
	args := strings.Fields(cmd)
	if len(args) == 0 {
		args = []string{"goose"}
	}
	cmd = args[0]

	// Add --auto if YoloMode is enabled (goose's equivalent of auto-approve).
	if settings != nil && settings.YoloMode {
		args = append(args, "--auto")
	}

	// Add profile flag if specified. Goose uses --profile to select
	// a named profile from profiles.yaml.
	if profile != "" {
		args = append(args, "--profile", profile)
	}

	return args
}

// HasGoosePrompt checks whether the goose TUI output indicates an idle
// prompt (ready for user input). Goose uses a simple ">_" or ">"
// prompt at the bottom of its interactive interface.
func HasGoosePrompt(output string) bool {
	if output == "" {
		return false
	}
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return false
	}

	// Goose prompt indicators: ">_" or ">" at the end of output.
	// Check last non-empty line for prompt patterns.
	lines := strings.Split(trimmed, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		// goose shows ">_" as its primary prompt indicator
		if line == ">_" || line == ">" {
			return true
		}
		// goose prompt with cursor indicator
		if strings.HasSuffix(line, ">_") || strings.HasSuffix(line, "> ") {
			return true
		}
		break
	}

	return false
}

// HasGooseBusyIndicator checks whether the goose TUI output indicates
// the agent is actively processing (working on a task).
func HasGooseBusyIndicator(output string) bool {
	if output == "" {
		return false
	}
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return false
	}

	// Goose busy indicators — text that appears while the agent is thinking
	// or executing tool calls.
	busyPatterns := []string{
		"thinking...",
		"Thinking...",
		"executing...",
		"Running...",
		"running...",
		"Processing...",
		"processing...",
		"Planning...",
		"planning...",
		"Generating...",
		"generating...",
		"ctrl+c to interrupt",
		"esc to interrupt",
	}

	lower := strings.ToLower(trimmed)
	for _, pattern := range busyPatterns {
		if strings.Contains(lower, strings.ToLower(pattern)) {
			return true
		}
	}

	return false
}

// GetGooseDefaultEnvFile returns the default .env file path for Goose
// sessions. Falls back to ~/.config/goose/.env.
func GetGooseDefaultEnvFile() string {
	return filepath.Join(gooseDefaultConfigDir(), ".env")
}

// GooseSettingsFromConfig returns GooseSettings with user config values
// merged over defaults. Only Command defaults to "goose" when empty;
// other fields (YoloMode, EnvFile, ConfigDir) are preserved as-is from
// user config even when empty (zero value = not configured).
func GooseSettingsFromConfig() *GooseSettings {
	def := DefaultGooseSettings()
	cfg, _ := LoadUserConfig()
	if cfg == nil {
		return def
	}
	merged := cfg.Goose
	if merged.Command == "" {
		merged.Command = def.Command
	}
	return &merged
}

// GetGooseConfigDir returns the Goose config directory. Checks the user
// config for an explicit override; otherwise returns the default.
func GetGooseConfigDir() string {
	config, _ := LoadUserConfig()
	if config != nil && config.Goose.ConfigDir != "" {
		return config.Goose.ConfigDir
	}
	return gooseDefaultConfigDir()
}
