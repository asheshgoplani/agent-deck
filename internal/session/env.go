package session

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// buildEnvSourceCommand builds shell commands to source .env files before the main command.
// Returns empty string if no env files or theme vars are configured.
// Order of sourcing (later overrides earlier):
//  1. Theme environment (COLORFGBG) for terminal-aware tools
//  2. Global [shell].env_files (in order)
//  3. [shell].init_script (for direnv, nvm, etc.)
//  4. Tool-specific env_file ([claude].env_file, [gemini].env_file, [tools.X].env_file)
//  5. Inline env vars from [tools.X].env
//  6. Conductor-specific env from meta.json (highest priority, overrides tool env)
func (i *Instance) buildEnvSourceCommand() string {
	var sources []string

	// 1. Theme environment (COLORFGBG) so tools like Codex detect light/dark theme.
	// Set early so env files or init scripts can override if needed.
	if themeExport := themeEnvExport(); themeExport != "" {
		sources = append(sources, themeExport)
	}

	config, _ := LoadUserConfig()
	if config == nil {
		if len(sources) == 0 {
			return ""
		}
		return strings.Join(sources, " && ") + " && "
	}

	ignoreMissing := config.Shell.GetIgnoreMissingEnvFiles()

	// 2. Global env_files from [shell] section
	for _, envFile := range config.Shell.EnvFiles {
		resolved := resolvePath(envFile, i.ProjectPath)
		sources = append(sources, buildSourceCmd(resolved, ignoreMissing))
	}

	// 3. Shell init script (direnv, nvm, pyenv, etc.)
	if config.Shell.InitScript != "" {
		script := config.Shell.InitScript
		if isFilePath(script) {
			resolved := ExpandPath(script)
			sources = append(sources, buildSourceCmd(resolved, ignoreMissing))
		} else {
			// Inline command (e.g., 'eval "$(direnv hook bash)"')
			sources = append(sources, script)
		}
	}

	// 4. Tool-specific env_file
	toolEnvFile := i.getToolEnvFile()
	if toolEnvFile != "" {
		resolved := resolvePath(toolEnvFile, i.ProjectPath)
		sources = append(sources, buildSourceCmd(resolved, ignoreMissing))
	}

	// 5. Inline env vars from [tools.X].env
	if inlineEnv := i.getToolInlineEnv(); inlineEnv != "" {
		sources = append(sources, inlineEnv)
	}

	// 6. Conductor-specific env (highest priority, overrides tool env)
	if conductorEnv := i.getConductorEnv(ignoreMissing); conductorEnv != "" {
		sources = append(sources, conductorEnv)
	}

	if len(sources) == 0 {
		return ""
	}

	// Join all sources with && and add trailing && for the main command
	return strings.Join(sources, " && ") + " && "
}

// themeEnvExport returns a shell export command for COLORFGBG based on the
// resolved agent-deck theme. This allows terminal-aware tools (Codex, vim, etc.)
// running inside tmux to detect the correct light/dark background.
// Returns empty string if the parent terminal already has COLORFGBG set and
// it matches the resolved theme (avoid unnecessary override).
func themeEnvExport() string {
	theme := ResolveTheme()

	// Determine the COLORFGBG value for the resolved theme.
	// Format: "foreground;background" using terminal color indices.
	// Background >= 8 signals a light terminal to most tools.
	var colorfgbg string
	switch theme {
	case "light":
		colorfgbg = "0;15" // black on white
	default:
		colorfgbg = "15;0" // white on black
	}

	// If the parent terminal already has the matching COLORFGBG, propagate
	// its exact value (it may encode more nuance than our synthetic value).
	if existing := os.Getenv("COLORFGBG"); existing != "" {
		if matchesTheme, ok := colorfgbgMatchesTheme(existing, theme); ok && matchesTheme {
			colorfgbg = existing
		}
	}

	return fmt.Sprintf("export COLORFGBG='%s'", colorfgbg)
}

// ThemeColorFGBG returns the COLORFGBG value for the current resolved theme.
// Used by tmux session setup to persist the value via set-environment.
func ThemeColorFGBG() string {
	theme := ResolveTheme()
	if existing := os.Getenv("COLORFGBG"); existing != "" {
		if matchesTheme, ok := colorfgbgMatchesTheme(existing, theme); ok && matchesTheme {
			return existing
		}
	}
	if theme == "light" {
		return "0;15"
	}
	return "15;0"
}

// colorfgbgMatchesTheme checks if a COLORFGBG value matches the given theme.
// Returns (matches, parsedOK). Background index >= 8 is considered light.
func colorfgbgMatchesTheme(colorfgbg, theme string) (bool, bool) {
	idx := strings.LastIndex(colorfgbg, ";")
	if idx < 0 {
		return false, false
	}
	bgStr := colorfgbg[idx+1:]
	var bg int
	if _, err := fmt.Sscanf(bgStr, "%d", &bg); err != nil {
		return false, false
	}
	isLight := bg >= 8
	return (theme == "light") == isLight, true
}

// buildSourceCmd creates a shell command to source a file.
// If ignoreMissing is true, wraps in a file existence check.
func buildSourceCmd(path string, ignoreMissing bool) string {
	if ignoreMissing {
		// Use [ -f file ] && source file pattern for safe sourcing
		return fmt.Sprintf(`[ -f "%s" ] && source "%s"`, path, path)
	}
	return fmt.Sprintf(`source "%s"`, path)
}

// resolvePath resolves a user-specified config file path:
//   - Expands environment variables ($HOME, ${VAR}, etc.)
//   - Expands ~ prefix to home directory
//   - Absolute paths are returned as-is
//   - Relative paths are resolved relative to workDir
func resolvePath(path, workDir string) string {
	expanded := ExpandPath(path)
	if filepath.IsAbs(expanded) {
		return filepath.Clean(expanded)
	}
	return filepath.Clean(filepath.Join(workDir, expanded))
}

// ExpandPath expands environment variables and ~ prefix in a path.
// Use resolvePath when relative paths also need to be resolved against a working directory.
func ExpandPath(path string) string {
	// Step 1: Expand environment variables first.
	// This ensures $HOME/.env becomes /home/user/.env before the tilde
	// check, and handles ${VAR} in any position (including after ~/).
	path = os.ExpandEnv(path)

	// Step 2: Expand tilde prefix to home directory.
	// After env var expansion, any remaining ~ is a genuine tilde.
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	if path == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return home
	}

	return path
}

// isFilePath checks if a string looks like a file path (vs inline command).
func isFilePath(s string) bool {
	return strings.HasPrefix(s, "/") ||
		strings.HasPrefix(s, "~/") ||
		strings.HasPrefix(s, "./") ||
		strings.HasPrefix(s, "../") ||
		strings.HasPrefix(s, "~")
}

// getToolInlineEnv returns shell export commands for inline env vars from [tools.X].env.
// Returns empty string if the tool has no inline env vars defined.
// Keys are sorted for deterministic output. Single quotes in values are escaped.
func (i *Instance) getToolInlineEnv() string {
	def := GetToolDef(i.Tool)
	if def == nil || len(def.Env) == 0 {
		return ""
	}

	// Sort keys for deterministic ordering
	keys := make([]string, 0, len(def.Env))
	for k := range def.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Build export statements with single-quote escaping
	exports := make([]string, 0, len(keys))
	for _, k := range keys {
		v := def.Env[k]
		// Escape single quotes: replace ' with '\'' (end quote, escaped quote, start quote)
		escaped := strings.ReplaceAll(v, "'", "'\\''")
		exports = append(exports, fmt.Sprintf("export %s='%s'", k, escaped))
	}

	return strings.Join(exports, " && ")
}

// getToolEnvFile returns the env_file setting for the current tool.
func (i *Instance) getToolEnvFile() string {
	config, _ := LoadUserConfig()
	if config == nil {
		return ""
	}

	switch i.Tool {
	case "claude":
		return config.Claude.EnvFile
	case "gemini":
		return config.Gemini.EnvFile
	default:
		// Check custom tools
		if def := GetToolDef(i.Tool); def != nil {
			return def.EnvFile
		}
	}
	return ""
}

// getConductorEnv returns shell export commands for conductor-specific env vars.
// Checks if this session is a conductor (title starts with "conductor-") and loads
// env and env_file from the conductor's meta.json.
func (i *Instance) getConductorEnv(ignoreMissing bool) string {
	name := strings.TrimPrefix(i.Title, "conductor-")
	if name == "" || name == i.Title {
		return "" // not a conductor session
	}
	meta, err := LoadConductorMeta(name)
	if err != nil {
		sessionLog.Warn("conductor_env_load_failed",
			slog.String("conductor", name),
			slog.String("error", err.Error()))
		return ""
	}

	var parts []string

	// Conductor env_file
	if meta.EnvFile != "" {
		resolved := resolvePath(meta.EnvFile, i.ProjectPath)
		parts = append(parts, buildSourceCmd(resolved, ignoreMissing))
	}

	// Conductor inline env vars
	if len(meta.Env) > 0 {
		keys := make([]string, 0, len(meta.Env))
		for k := range meta.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if !isValidEnvKey(k) {
				continue // skip invalid env var names
			}
			parts = append(parts, fmt.Sprintf("export %s='%s'", k, strings.ReplaceAll(meta.Env[k], "'", "'\\''")))
		}
	}

	return strings.Join(parts, " && ")
}

// isValidEnvKey checks that a string is a valid environment variable name.
func isValidEnvKey(key string) bool {
	if key == "" {
		return false
	}
	for i, c := range key {
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c == '_' {
			continue
		}
		if i > 0 && c >= '0' && c <= '9' {
			continue
		}
		return false
	}
	return true
}
