package session

import (
	"log/slog"
	"os"
	"path/filepath"
)

// InjectGooseHooks injects goose-specific configuration for a session.
//
// Goose manages its own MCP extensions via config.yaml (~/.config/goose/).
// This function currently handles only lightweight pre-flight tasks:
//  1. Ensure the env file exists when settings.EnvFile is set.
//
// More sophisticated hook injection (config.yaml merge, MCP wiring) is
// deferred until the goose integration matures — goose already supports
// extensions natively through its own config.
//
// ponytail: minimal integration; goose's config.yaml is user-managed for now.
func InjectGooseHooks(sessionDir string, projectPath string, settings *GooseSettings) error {
	if settings == nil {
		return nil
	}

	// If an env file is specified, ensure it exists so goose doesn't fail
	// on startup trying to source a missing file.
	if settings.EnvFile != "" {
		envPath, err := expandGoosePath(settings.EnvFile)
		if err != nil {
			return err
		}
		if err := ensureFileExists(envPath); err != nil {
			sessionLog.Warn("goose_env_file_missing",
				slog.String("path", envPath),
				slog.String("session_dir", sessionDir),
			)
			// Not fatal — goose can run without an env file.
		}
	}

	return nil
}

// RemoveGooseHooks removes agent-deck injected configuration for goose.
// Currently a no-op since goose manages its own config.yaml, but exists
// for symmetry with InjectGooseHooks and future use.
func RemoveGooseHooks(configDir string) (bool, error) {
	// ponytail: no-op for now — goose config is user-managed
	return false, nil
}

// CheckGooseHooksInstalled reports whether agent-deck hooks are installed
// for goose. Currently always returns false since we don't inject into
// goose's config.yaml yet.
func CheckGooseHooksInstalled(configDir string) bool {
	// ponytail: no-op for now — goose config is user-managed
	return false
}

// expandGoosePath expands ~ and environment variables in a goose file path.
func expandGoosePath(p string) (string, error) {
	if p == "" {
		return "", nil
	}

	// Expand ~ to home directory
	if p[0] == '~' {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, p[1:])
	}

	// Expand environment variables ($VAR or ${VAR})
	expanded := os.ExpandEnv(p)
	return expanded, nil
}

// ensureFileExists creates the file (and parent directories) if it doesn't
// exist. If the file already exists, this is a no-op.
func ensureFileExists(path string) error {
	if path == "" {
		return nil
	}
	if _, err := os.Stat(path); err == nil {
		return nil // already exists
	}
	// Create parent directory
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// Create empty file
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	return f.Close()
}
