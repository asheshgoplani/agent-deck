package session

import (
	"os"
	"path/filepath"
	"testing"
)

func setupSessionXDGPathEnv(t *testing.T) (home string, xdgConfigHome string, xdgDataHome string) {
	t.Helper()

	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	root := t.TempDir()
	home = filepath.Join(root, "home")
	xdgConfigHome = filepath.Join(root, "xdg-config")
	xdgDataHome = filepath.Join(root, "xdg-data")

	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", home, err)
	}

	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdgConfigHome)
	t.Setenv("XDG_DATA_HOME", xdgDataHome)
	t.Setenv("CLAUDE_CONFIG_DIR", "")

	return home, xdgConfigHome, xdgDataHome
}

func writeSessionPathFile(t *testing.T, path string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
}

func TestGetUserConfigPath_UsesXDGConfigHomeForNewUser(t *testing.T) {
	_, xdgConfigHome, _ := setupSessionXDGPathEnv(t)

	got, err := GetUserConfigPath()
	if err != nil {
		t.Fatalf("GetUserConfigPath(): %v", err)
	}

	want := filepath.Join(xdgConfigHome, "agent-deck", UserConfigFileName)
	if got != want {
		t.Fatalf("GetUserConfigPath() = %q, want %q", got, want)
	}
}

func TestGetUserConfigPath_LegacyFallbackWhenXDGMissing(t *testing.T) {
	home, _, _ := setupSessionXDGPathEnv(t)
	legacyPath := filepath.Join(home, ".agent-deck", UserConfigFileName)
	writeSessionPathFile(t, legacyPath)

	got, err := GetUserConfigPath()
	if err != nil {
		t.Fatalf("GetUserConfigPath(): %v", err)
	}

	if got != legacyPath {
		t.Fatalf("GetUserConfigPath() = %q, want %q", got, legacyPath)
	}
}

func TestGetUserConfigPath_XDGWinsWhenBothExist(t *testing.T) {
	home, xdgConfigHome, _ := setupSessionXDGPathEnv(t)
	legacyPath := filepath.Join(home, ".agent-deck", UserConfigFileName)
	xdgPath := filepath.Join(xdgConfigHome, "agent-deck", UserConfigFileName)
	writeSessionPathFile(t, legacyPath)
	writeSessionPathFile(t, xdgPath)

	got, err := GetUserConfigPath()
	if err != nil {
		t.Fatalf("GetUserConfigPath(): %v", err)
	}

	if got != xdgPath {
		t.Fatalf("GetUserConfigPath() = %q, want %q", got, xdgPath)
	}
}

func TestGetConfigPath_UsesXDGConfigHomeForNewUser(t *testing.T) {
	_, xdgConfigHome, _ := setupSessionXDGPathEnv(t)

	got, err := GetConfigPath()
	if err != nil {
		t.Fatalf("GetConfigPath(): %v", err)
	}

	want := filepath.Join(xdgConfigHome, "agent-deck", ConfigFileName)
	if got != want {
		t.Fatalf("GetConfigPath() = %q, want %q", got, want)
	}
}

func TestGetConfigPath_XDGWinsWhenBothExist(t *testing.T) {
	home, xdgConfigHome, _ := setupSessionXDGPathEnv(t)
	legacyPath := filepath.Join(home, ".agent-deck", ConfigFileName)
	xdgPath := filepath.Join(xdgConfigHome, "agent-deck", ConfigFileName)
	writeSessionPathFile(t, legacyPath)
	writeSessionPathFile(t, xdgPath)

	got, err := GetConfigPath()
	if err != nil {
		t.Fatalf("GetConfigPath(): %v", err)
	}

	if got != xdgPath {
		t.Fatalf("GetConfigPath() = %q, want %q", got, xdgPath)
	}
}

func TestGetProfilesDir_UsesXDGDataHomeForNewUser(t *testing.T) {
	_, _, xdgDataHome := setupSessionXDGPathEnv(t)

	got, err := GetProfilesDir()
	if err != nil {
		t.Fatalf("GetProfilesDir(): %v", err)
	}

	want := filepath.Join(xdgDataHome, "agent-deck", ProfilesDirName)
	if got != want {
		t.Fatalf("GetProfilesDir() = %q, want %q", got, want)
	}
}

func TestGetProfilesDir_LegacyFallbackWhenProfilesExist(t *testing.T) {
	home, _, _ := setupSessionXDGPathEnv(t)
	legacyProfilesDir := filepath.Join(home, ".agent-deck", ProfilesDirName)
	if err := os.MkdirAll(legacyProfilesDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", legacyProfilesDir, err)
	}

	got, err := GetProfilesDir()
	if err != nil {
		t.Fatalf("GetProfilesDir(): %v", err)
	}

	if got != legacyProfilesDir {
		t.Fatalf("GetProfilesDir() = %q, want %q", got, legacyProfilesDir)
	}
}

func TestGetProfilesDir_LegacyFallbackWhenSessionsJSONExists(t *testing.T) {
	home, _, _ := setupSessionXDGPathEnv(t)
	legacySessionsPath := filepath.Join(home, ".agent-deck", "sessions.json")
	writeSessionPathFile(t, legacySessionsPath)

	got, err := GetProfilesDir()
	if err != nil {
		t.Fatalf("GetProfilesDir(): %v", err)
	}

	want := filepath.Join(home, ".agent-deck", ProfilesDirName)
	if got != want {
		t.Fatalf("GetProfilesDir() = %q, want %q", got, want)
	}
}

func TestGetProfilesDir_XDGWinsWhenProfileMarkerExists(t *testing.T) {
	home, _, xdgDataHome := setupSessionXDGPathEnv(t)
	legacyProfilesDir := filepath.Join(home, ".agent-deck", ProfilesDirName)
	xdgProfilesDir := filepath.Join(xdgDataHome, "agent-deck", ProfilesDirName)
	if err := os.MkdirAll(legacyProfilesDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", legacyProfilesDir, err)
	}
	if err := os.MkdirAll(xdgProfilesDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", xdgProfilesDir, err)
	}

	got, err := GetProfilesDir()
	if err != nil {
		t.Fatalf("GetProfilesDir(): %v", err)
	}

	if got != xdgProfilesDir {
		t.Fatalf("GetProfilesDir() = %q, want %q", got, xdgProfilesDir)
	}
}

func TestNeedsMigration_LegacySessionsJSONWinsOverBroadXDGDataMarker(t *testing.T) {
	home, _, xdgDataHome := setupSessionXDGPathEnv(t)
	xdgLogsDir := filepath.Join(xdgDataHome, "agent-deck", "logs")
	if err := os.MkdirAll(xdgLogsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", xdgLogsDir, err)
	}
	legacySessionsPath := filepath.Join(home, ".agent-deck", "sessions.json")
	writeSessionPathFile(t, legacySessionsPath)

	needsMigration, err := NeedsMigration()
	if err != nil {
		t.Fatalf("NeedsMigration(): %v", err)
	}
	if !needsMigration {
		t.Fatalf("NeedsMigration() = false, want true for legacy sessions.json even when XDG data has logs marker")
	}
}

func TestGetDBPathForProfile_UsesXDGDataHomeForNewUser(t *testing.T) {
	_, _, xdgDataHome := setupSessionXDGPathEnv(t)

	got, err := GetDBPathForProfile("project/work")
	if err != nil {
		t.Fatalf("GetDBPathForProfile(): %v", err)
	}

	want := filepath.Join(xdgDataHome, "agent-deck", ProfilesDirName, "work", "state.db")
	if got != want {
		t.Fatalf("GetDBPathForProfile() = %q, want %q", got, want)
	}
}

func TestNewStorageWithProfile_UsesXDGDataHome(t *testing.T) {
	_, _, xdgDataHome := setupSessionXDGPathEnv(t)

	storage, err := NewStorageWithProfile("xdg-profile")
	if err != nil {
		t.Fatalf("NewStorageWithProfile(): %v", err)
	}
	t.Cleanup(func() {
		if err := storage.Close(); err != nil {
			t.Fatalf("Close(): %v", err)
		}
	})

	want := filepath.Join(xdgDataHome, "agent-deck", ProfilesDirName, "xdg-profile", "state.db")
	if got := storage.dbPath; got != want {
		t.Fatalf("NewStorageWithProfile db path = %q, want %q", got, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("Stat(%q): %v", want, err)
	}
}
