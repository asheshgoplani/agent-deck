package agentpaths

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const AppDirName = "agent-deck"

// S4 data-loss safeguard (2026-06-04 incident).
//
// Path resolution uses $HOME and XDG_* env vars. Under go test, a package that
// forgot testutil.IsolateHome() can otherwise resolve config, data, cache, or
// legacy paths under the developer's real home and write live user data.
//
// The guard below is intentionally test-only. Production binaries keep normal
// XDG and legacy fallback behavior; tests fail closed when a resolved
// agent-deck path lands under the OS user's real home.
var (
	unsafeTestPathWarnOnce sync.Once
	unsafeTestPathWarnMu   sync.Mutex
	unsafeTestPathWarnSink io.Writer = os.Stderr
)

func setUnsafeTestPathWarnSink(w io.Writer) func() {
	unsafeTestPathWarnMu.Lock()
	prev := unsafeTestPathWarnSink
	unsafeTestPathWarnSink = w
	unsafeTestPathWarnMu.Unlock()
	return func() {
		unsafeTestPathWarnMu.Lock()
		unsafeTestPathWarnSink = prev
		unsafeTestPathWarnMu.Unlock()
	}
}

func resetUnsafeTestPathWarnOnce() {
	unsafeTestPathWarnMu.Lock()
	unsafeTestPathWarnOnce = sync.Once{}
	unsafeTestPathWarnMu.Unlock()
}

func homeDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	return home, nil
}

func osUserRealHome() string {
	if u, err := user.Current(); err == nil && u.HomeDir != "" {
		return filepath.Clean(u.HomeDir)
	}
	return ""
}

func pathUnderRealHome(path, realHome string) bool {
	if realHome == "" {
		return false
	}
	clean := filepath.Clean(path)
	return clean == realHome || strings.HasPrefix(clean, realHome+string(os.PathSeparator))
}

func warnUnsafeTestPathOnce(resolved string) {
	unsafeTestPathWarnMu.Lock()
	once := &unsafeTestPathWarnOnce
	sink := unsafeTestPathWarnSink
	unsafeTestPathWarnMu.Unlock()
	once.Do(func() {
		fmt.Fprintf(sink,
			"agentpaths: resolved agent-deck path under the real home (%s); "+
				"this touches REAL user data. If this is a test, it is NOT sandboxed; "+
				"call testutil.IsolateHome() in TestMain and run with a temp HOME+XDG "+
				"(2026-06-04 data-loss incident, S4).\n",
			resolved)
	})
}

func ensureSafeForTest(resolved string) error {
	if !testing.Testing() {
		return nil
	}
	if realHome := osUserRealHome(); pathUnderRealHome(resolved, realHome) {
		warnUnsafeTestPathOnce(resolved)
		return fmt.Errorf(
			"agentpaths: refusing to resolve agent-deck path under the real home %q "+
				"(resolved %q) while running under test; call testutil.IsolateHome() "+
				"in TestMain (2026-06-04 data-loss incident, S4)",
			realHome, resolved)
	}
	return nil
}

func LegacyDir() (string, error) {
	home, err := homeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".agent-deck")
	if err := ensureSafeForTest(dir); err != nil {
		return "", err
	}
	return dir, nil
}

func xdgDir(envName string, fallbackParts ...string) (string, error) {
	if value := strings.TrimSpace(os.Getenv(envName)); value != "" {
		if filepath.IsAbs(value) {
			dir := filepath.Join(value, AppDirName)
			if err := ensureSafeForTest(dir); err != nil {
				return "", err
			}
			return dir, nil
		}
	}

	home, err := homeDir()
	if err != nil {
		return "", err
	}

	parts := append([]string{home}, fallbackParts...)
	parts = append(parts, AppDirName)
	dir := filepath.Join(parts...)
	if err := ensureSafeForTest(dir); err != nil {
		return "", err
	}
	return dir, nil
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
