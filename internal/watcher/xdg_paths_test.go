package watcher

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func setupWatcherXDGPathEnv(t *testing.T) (home string, xdgDataHome string) {
	t.Helper()

	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)

	root := t.TempDir()
	home = filepath.Join(root, "home")
	xdgConfigHome := filepath.Join(root, "xdg-config")
	xdgDataHome = filepath.Join(root, "xdg-data")

	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", home, err)
	}

	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdgConfigHome)
	t.Setenv("XDG_DATA_HOME", xdgDataHome)
	t.Setenv("CLAUDE_CONFIG_DIR", "")

	return home, xdgDataHome
}

func TestXDGDataTask4_WatcherLayoutUsesXDGDataHomeForNewUser(t *testing.T) {
	_, xdgDataHome := setupWatcherXDGPathEnv(t)

	got, err := LayoutDir()
	if err != nil {
		t.Fatalf("LayoutDir(): %v", err)
	}
	want := filepath.Join(xdgDataHome, "agent-deck", "watcher")
	if got != want {
		t.Fatalf("LayoutDir() = %q, want %q", got, want)
	}

	gotName, err := WatcherDir("my-watcher")
	if err != nil {
		t.Fatalf("WatcherDir(): %v", err)
	}
	wantName := filepath.Join(want, "my-watcher")
	if gotName != wantName {
		t.Fatalf("WatcherDir() = %q, want %q", gotName, wantName)
	}
}

func TestXDGDataTask4_WatcherLayoutUsesCategorySpecificLegacyFallback(t *testing.T) {
	home, xdgDataHome := setupWatcherXDGPathEnv(t)

	legacyProfilesDir := filepath.Join(home, ".agent-deck", session.ProfilesDirName)
	if err := os.MkdirAll(legacyProfilesDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", legacyProfilesDir, err)
	}

	got, err := LayoutDir()
	if err != nil {
		t.Fatalf("LayoutDir(): %v", err)
	}
	wantXDG := filepath.Join(xdgDataHome, "agent-deck", "watcher")
	if got != wantXDG {
		t.Fatalf("legacy profiles marker must not move watcher layout to legacy: got %q want %q", got, wantXDG)
	}

	legacyPluralDir := filepath.Join(home, ".agent-deck", "watchers")
	if err := os.MkdirAll(legacyPluralDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", legacyPluralDir, err)
	}

	got, err = LayoutDir()
	if err != nil {
		t.Fatalf("LayoutDir(): %v", err)
	}
	wantLegacy := filepath.Join(home, ".agent-deck", "watcher")
	if got != wantLegacy {
		t.Fatalf("legacy watchers marker should keep watcher layout legacy: got %q want %q", got, wantLegacy)
	}
}

func TestXDGDataTask4_WatcherEngineDefaultsUseXDGDataHome(t *testing.T) {
	_, xdgDataHome := setupWatcherXDGPathEnv(t)

	engine := NewEngine(EngineConfig{})

	wantTriage := filepath.Join(xdgDataHome, "agent-deck", "triage")
	if engine.cfg.TriageDir != wantTriage {
		t.Fatalf("Engine TriageDir = %q, want %q", engine.cfg.TriageDir, wantTriage)
	}

	wantClients := filepath.Join(xdgDataHome, "agent-deck", "watcher", "clients.json")
	if engine.cfg.ClientsPath != wantClients {
		t.Fatalf("Engine ClientsPath = %q, want %q", engine.cfg.ClientsPath, wantClients)
	}
}
