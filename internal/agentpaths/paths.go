package agentpaths

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const AppDirName = "agent-deck"

func homeDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	return home, nil
}

func LegacyDir() (string, error) {
	home, err := homeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".agent-deck"), nil
}

func xdgDir(envName string, fallbackParts ...string) (string, error) {
	if value := strings.TrimSpace(os.Getenv(envName)); value != "" {
		return filepath.Join(value, AppDirName), nil
	}

	home, err := homeDir()
	if err != nil {
		return "", err
	}

	parts := append([]string{home}, fallbackParts...)
	parts = append(parts, AppDirName)
	return filepath.Join(parts...), nil
}

func ConfigDir() (string, error) {
	return xdgDir("XDG_CONFIG_HOME", ".config")
}

func DataDir() (string, error) {
	return xdgDir("XDG_DATA_HOME", ".local", "share")
}

func CacheDir() (string, error) {
	return xdgDir("XDG_CACHE_HOME", ".cache")
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func EffectiveConfigPath(name string) (string, error) {
	configDir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	xdgPath := filepath.Join(configDir, filepath.Base(name))
	if exists(xdgPath) {
		return xdgPath, nil
	}

	legacyDir, err := LegacyDir()
	if err != nil {
		return "", err
	}
	legacyPath := filepath.Join(legacyDir, filepath.Base(name))
	if exists(legacyPath) {
		return legacyPath, nil
	}

	return xdgPath, nil
}

func EffectiveDataDir(markers ...string) (string, error) {
	dataDir, err := DataDir()
	if err != nil {
		return "", err
	}
	if exists(dataDir) {
		return dataDir, nil
	}

	legacyDir, err := LegacyDir()
	if err != nil {
		return "", err
	}
	for _, marker := range markers {
		if marker == "" {
			continue
		}
		if exists(filepath.Join(legacyDir, filepath.Clean(marker))) {
			return legacyDir, nil
		}
	}

	return dataDir, nil
}

func EffectiveDataPath(name string, markers ...string) (string, error) {
	dataDir, err := EffectiveDataDir(markers...)
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, filepath.Clean(name)), nil
}

func CachePath(name string) (string, error) {
	cacheDir, err := CacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cacheDir, filepath.Clean(name)), nil
}
