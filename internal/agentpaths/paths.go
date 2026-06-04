package agentpaths

import (
	"errors"
	"fmt"
	"io/fs"
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

func statExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("stat %q: %w", path, err)
}

func cleanLocal(name string) (string, error) {
	cleaned := filepath.Clean(name)
	if cleaned == "." || !filepath.IsLocal(cleaned) {
		return "", fmt.Errorf("path must be local: %q", name)
	}
	return cleaned, nil
}

func EffectiveConfigPath(name string) (string, error) {
	configDir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	// Config paths are leaf filenames; callers should not pass nested paths here.
	xdgPath := filepath.Join(configDir, filepath.Base(name))
	ok, err := statExists(xdgPath)
	if err != nil {
		return "", err
	}
	if ok {
		return xdgPath, nil
	}

	legacyDir, err := LegacyDir()
	if err != nil {
		return "", err
	}
	legacyPath := filepath.Join(legacyDir, filepath.Base(name))
	ok, err = statExists(legacyPath)
	if err != nil {
		return "", err
	}
	if ok {
		return legacyPath, nil
	}

	return xdgPath, nil
}

func EffectiveDataDir(markers ...string) (string, error) {
	dataDir, err := DataDir()
	if err != nil {
		return "", err
	}

	cleanMarkers := make([]string, 0, len(markers))
	for _, marker := range markers {
		if marker == "" {
			continue
		}
		cleanMarker, err := cleanLocal(marker)
		if err != nil {
			return "", err
		}
		cleanMarkers = append(cleanMarkers, cleanMarker)
	}
	if len(cleanMarkers) == 0 {
		return dataDir, nil
	}

	for _, marker := range cleanMarkers {
		ok, err := statExists(filepath.Join(dataDir, marker))
		if err != nil {
			return "", err
		}
		if ok {
			return dataDir, nil
		}
	}

	legacyDir, err := LegacyDir()
	if err != nil {
		return "", err
	}
	for _, marker := range cleanMarkers {
		ok, err := statExists(filepath.Join(legacyDir, marker))
		if err != nil {
			return "", err
		}
		if ok {
			return legacyDir, nil
		}
	}

	return dataDir, nil
}

func EffectiveDataPath(name string, markers ...string) (string, error) {
	cleanName, err := cleanLocal(name)
	if err != nil {
		return "", err
	}
	dataDir, err := EffectiveDataDir(markers...)
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, cleanName), nil
}

func CachePath(name string) (string, error) {
	cleanName, err := cleanLocal(name)
	if err != nil {
		return "", err
	}
	cacheDir, err := CacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cacheDir, cleanName), nil
}
