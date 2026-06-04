package agentpaths

import (
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacyDir_DefaultsToHomeAgentDeck(t *testing.T) {
	home := setupHome(t)

	legacyDir, err := LegacyDir()
	if err != nil {
		t.Fatalf("LegacyDir() error = %v", err)
	}
	if want := filepath.Join(home, ".agent-deck"); legacyDir != want {
		t.Fatalf("LegacyDir() = %q, want %q", legacyDir, want)
	}
}

func TestXDGDirs_DefaultToHomeFallbacks(t *testing.T) {
	home := setupHome(t)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")

	configDir, err := ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir() error = %v", err)
	}
	if want := filepath.Join(home, ".config", AppDirName); configDir != want {
		t.Fatalf("ConfigDir() = %q, want %q", configDir, want)
	}

	dataDir, err := DataDir()
	if err != nil {
		t.Fatalf("DataDir() error = %v", err)
	}
	if want := filepath.Join(home, ".local", "share", AppDirName); dataDir != want {
		t.Fatalf("DataDir() = %q, want %q", dataDir, want)
	}

	cacheDir, err := CacheDir()
	if err != nil {
		t.Fatalf("CacheDir() error = %v", err)
	}
	if want := filepath.Join(home, ".cache", AppDirName); cacheDir != want {
		t.Fatalf("CacheDir() = %q, want %q", cacheDir, want)
	}
}

func TestXDGDirs_EnvOverrides(t *testing.T) {
	setupHome(t)
	root := t.TempDir()
	configHome := filepath.Join(root, "config")
	dataHome := filepath.Join(root, "data")
	cacheHome := filepath.Join(root, "cache")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("XDG_CACHE_HOME", cacheHome)

	configDir, err := ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir() error = %v", err)
	}
	if want := filepath.Join(configHome, AppDirName); configDir != want {
		t.Fatalf("ConfigDir() = %q, want %q", configDir, want)
	}

	dataDir, err := DataDir()
	if err != nil {
		t.Fatalf("DataDir() error = %v", err)
	}
	if want := filepath.Join(dataHome, AppDirName); dataDir != want {
		t.Fatalf("DataDir() = %q, want %q", dataDir, want)
	}

	cacheDir, err := CacheDir()
	if err != nil {
		t.Fatalf("CacheDir() error = %v", err)
	}
	if want := filepath.Join(cacheHome, AppDirName); cacheDir != want {
		t.Fatalf("CacheDir() = %q, want %q", cacheDir, want)
	}
}

func osRealHome(t *testing.T) string {
	t.Helper()
	u, err := user.Current()
	if err != nil || u.HomeDir == "" {
		t.Skip("cannot determine real home directory from OS user database")
	}
	return filepath.Clean(u.HomeDir)
}

func TestConfigDir_RefusesUnderTestOnRealHome(t *testing.T) {
	real := osRealHome(t)
	t.Setenv("HOME", real)
	t.Setenv("XDG_CONFIG_HOME", "")

	dir, err := ConfigDir()
	if err == nil {
		t.Fatalf("ConfigDir should refuse real-home path under test, got %s nil error", dir)
	}
	if !strings.Contains(err.Error(), "real home") {
		t.Fatalf("error should mention real-home guard, got %v", err)
	}
}

func TestLegacyDir_RefusesUnderTestOnRealHome(t *testing.T) {
	real := osRealHome(t)
	t.Setenv("HOME", real)

	dir, err := LegacyDir()
	if err == nil {
		t.Fatalf("LegacyDir should refuse real-home path under test, got %s nil error", dir)
	}
	if !strings.Contains(err.Error(), "real home") {
		t.Fatalf("error should mention real-home guard, got %v", err)
	}
}

func TestUnsafeTestPathWarningDebounced(t *testing.T) {
	real := osRealHome(t)
	t.Setenv("HOME", real)
	t.Setenv("XDG_DATA_HOME", "")

	var buf strings.Builder
	restore := setUnsafeTestPathWarnSink(&buf)
	defer restore()
	resetUnsafeTestPathWarnOnce()

	for i := 0; i < 5; i++ {
		_, _ = DataDir()
	}

	got := buf.String()
	if n := strings.Count(got, "real home"); n != 1 {
		t.Fatalf("warning should be debounced to exactly 1 emission, got %d:\n%s", n, got)
	}
	if !strings.Contains(got, "testutil.IsolateHome") {
		t.Fatalf("warning should explain the sandbox fix, got %q", got)
	}
}

func TestEffectiveConfigPath_LegacyWinsOnlyWhenXDGFileMissing(t *testing.T) {
	home := setupHome(t)
	t.Setenv("XDG_CONFIG_HOME", "")

	legacyPath := filepath.Join(home, ".agent-deck", "config.toml")
	xdgPath := filepath.Join(home, ".config", AppDirName, "config.toml")

	got, err := EffectiveConfigPath("config.toml")
	if err != nil {
		t.Fatalf("EffectiveConfigPath() error = %v", err)
	}
	if got != xdgPath {
		t.Fatalf("EffectiveConfigPath() = %q, want XDG path %q", got, xdgPath)
	}

	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(legacyPath), err)
	}
	if err := os.WriteFile(legacyPath, []byte("legacy = true\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", legacyPath, err)
	}

	got, err = EffectiveConfigPath("config.toml")
	if err != nil {
		t.Fatalf("EffectiveConfigPath() error = %v", err)
	}
	if got != legacyPath {
		t.Fatalf("EffectiveConfigPath() = %q, want legacy path %q", got, legacyPath)
	}

	if err := os.MkdirAll(filepath.Dir(xdgPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(xdgPath), err)
	}
	if err := os.WriteFile(xdgPath, []byte("xdg = true\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", xdgPath, err)
	}

	got, err = EffectiveConfigPath("config.toml")
	if err != nil {
		t.Fatalf("EffectiveConfigPath() error = %v", err)
	}
	if got != xdgPath {
		t.Fatalf("EffectiveConfigPath() = %q, want XDG path %q", got, xdgPath)
	}
}

func TestEffectiveConfigPath_XDGStatErrorDoesNotFallBackToLegacy(t *testing.T) {
	home := setupHome(t)
	t.Setenv("XDG_CONFIG_HOME", "")

	legacyPath := filepath.Join(home, ".agent-deck", "config.toml")
	xdgPath := filepath.Join(home, ".config", AppDirName, "config.toml")

	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(legacyPath), err)
	}
	if err := os.WriteFile(legacyPath, []byte("legacy = true\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", legacyPath, err)
	}
	if err := os.MkdirAll(filepath.Dir(xdgPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(xdgPath), err)
	}
	if err := os.Symlink("config.toml", xdgPath); err != nil {
		t.Fatalf("Symlink(%q) error = %v", xdgPath, err)
	}

	if got, err := EffectiveConfigPath("config.toml"); err == nil {
		t.Fatalf("EffectiveConfigPath() = %q, want stat error", got)
	}
}

func TestEffectiveDataDir_LegacyWinsOnlyWhenXDGDataMissing(t *testing.T) {
	home := setupHome(t)
	t.Setenv("XDG_DATA_HOME", "")

	legacyDir := filepath.Join(home, ".agent-deck")
	xdgDataDir := filepath.Join(home, ".local", "share", AppDirName)
	legacyMarker := filepath.Join(legacyDir, "profiles", "default")
	xdgMarker := filepath.Join(xdgDataDir, "profiles")

	got, err := EffectiveDataDir("profiles")
	if err != nil {
		t.Fatalf("EffectiveDataDir() error = %v", err)
	}
	if got != xdgDataDir {
		t.Fatalf("EffectiveDataDir() = %q, want XDG data dir %q", got, xdgDataDir)
	}

	if err := os.MkdirAll(legacyMarker, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", legacyMarker, err)
	}

	got, err = EffectiveDataDir("profiles")
	if err != nil {
		t.Fatalf("EffectiveDataDir() error = %v", err)
	}
	if got != legacyDir {
		t.Fatalf("EffectiveDataDir() = %q, want legacy dir %q", got, legacyDir)
	}

	if err := os.MkdirAll(xdgMarker, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", xdgMarker, err)
	}

	got, err = EffectiveDataDir("profiles")
	if err != nil {
		t.Fatalf("EffectiveDataDir() error = %v", err)
	}
	if got != xdgDataDir {
		t.Fatalf("EffectiveDataDir() = %q, want XDG data dir %q", got, xdgDataDir)
	}
}

func TestEffectiveDataDir_LegacyMarkerWinsWhenXDGBaseExistsWithoutMarker(t *testing.T) {
	home := setupHome(t)
	t.Setenv("XDG_DATA_HOME", "")

	legacyDir := filepath.Join(home, ".agent-deck")
	xdgDataDir := filepath.Join(home, ".local", "share", AppDirName)
	legacyMarker := filepath.Join(legacyDir, "profiles", "default")

	if err := os.MkdirAll(xdgDataDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", xdgDataDir, err)
	}
	if err := os.MkdirAll(legacyMarker, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", legacyMarker, err)
	}

	got, err := EffectiveDataDir("profiles")
	if err != nil {
		t.Fatalf("EffectiveDataDir() error = %v", err)
	}
	if got != legacyDir {
		t.Fatalf("EffectiveDataDir() = %q, want legacy dir %q", got, legacyDir)
	}
}

func TestEffectiveDataDir_RejectsNonLocalMarkers(t *testing.T) {
	setupHome(t)
	t.Setenv("XDG_DATA_HOME", "")

	if got, err := EffectiveDataDir("../profiles"); err == nil {
		t.Fatalf("EffectiveDataDir() = %q, want error", got)
	}
}

func TestEffectiveDataPath_RejectsNonLocalName(t *testing.T) {
	setupHome(t)
	t.Setenv("XDG_DATA_HOME", "")

	if got, err := EffectiveDataPath("../escape"); err == nil {
		t.Fatalf("EffectiveDataPath() = %q, want error", got)
	}
}

func TestCachePath_RejectsNonLocalName(t *testing.T) {
	setupHome(t)
	t.Setenv("XDG_CACHE_HOME", "")

	if got, err := CachePath("../escape"); err == nil {
		t.Fatalf("CachePath() = %q, want error", got)
	}
}

func setupHome(t *testing.T) string {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}
