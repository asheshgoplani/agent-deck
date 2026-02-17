package session

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// buildEnvSourceCommand builds shell commands to source .env files before the main command.
// Returns empty string if no env files are configured.
// Order of sourcing (later overrides earlier):
//  1. Global [shell].env_files (in order)
//  2. [shell].init_script (for direnv, nvm, etc.)
//  3. Tool-specific env_file ([claude].env_file, [gemini].env_file, [tools.X].env_file)
//  4. Inline env vars from [tools.X].env (highest priority)
func (i *Instance) buildEnvSourceCommand() string {
	var sources []string
	config, _ := LoadUserConfig()
	if config == nil {
		return ""
	}

	ignoreMissing := config.Shell.GetIgnoreMissingEnvFiles()

	// 1. Global env_files from [shell] section
	for _, envFile := range config.Shell.EnvFiles {
		resolved := resolvePath(envFile, i.ProjectPath)
		sources = append(sources, buildSourceCmd(resolved, ignoreMissing))
	}

	// 2. Shell init script (direnv, nvm, pyenv, etc.)
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

	// 3. Tool-specific env_file
	toolEnvFile := i.getToolEnvFile()
	if toolEnvFile != "" {
		resolved := resolvePath(toolEnvFile, i.ProjectPath)
		sources = append(sources, buildSourceCmd(resolved, ignoreMissing))
	}

	// 4. Inline env vars from [tools.X].env (highest priority)
	if inlineEnv := i.getToolInlineEnv(); inlineEnv != "" {
		sources = append(sources, inlineEnv)
	}

	if len(sources) == 0 {
		return ""
	}

	// Join all sources with && and add trailing && for the main command
	return strings.Join(sources, " && ") + " && "
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
