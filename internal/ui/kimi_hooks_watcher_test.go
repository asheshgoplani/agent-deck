package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

func TestInstalledKimiHooksAreWatchedWhenAutomaticPromptDisabled(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("KIMI_CODE_HOME", filepath.Join(homeDir, ".kimi-code"))
	t.Setenv("AGENTDECK_PROFILE", "kimi-hooks-watch")
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "kimi"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	configPath, err := session.GetUserConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("[kimi]\nhooks_enabled = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)
	if _, err := session.InjectKimiHooks(session.GetKimiConfigDir()); err != nil {
		t.Fatal(err)
	}
	previousWorkers := homeBackgroundWorkersEnabled
	homeBackgroundWorkersEnabled = true
	t.Cleanup(func() { homeBackgroundWorkersEnabled = previousWorkers })
	stubLogMaintenance(t)
	previousDB := statedb.GetGlobal()
	statedb.SetGlobal(nil)
	t.Cleanup(func() { statedb.SetGlobal(previousDB) })

	home := NewHome()
	t.Cleanup(func() { cleanupKimiHooksHome(t, home) })
	if home.hookWatcher == nil {
		t.Fatal("installed Kimi hooks are not watched when automatic prompting is disabled")
	}
	if home.pendingKimiHooksPrompt {
		t.Fatal("installed Kimi hooks triggered a consent prompt")
	}
}

func TestKimiHookWatcherInitFailureIsLogged(t *testing.T) {
	homeDir := setXDGTestHome(t)
	t.Setenv("KIMI_CODE_HOME", filepath.Join(homeDir, ".kimi-code"))
	t.Setenv("AGENTDECK_PROFILE", "kimi-hooks-watch-error")
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "kimi"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	if _, err := session.InjectKimiHooks(session.GetKimiConfigDir()); err != nil {
		t.Fatal(err)
	}
	hooksDir := session.GetHooksDir()
	if err := os.MkdirAll(filepath.Dir(hooksDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hooksDir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	previousWorkers := homeBackgroundWorkersEnabled
	homeBackgroundWorkersEnabled = true
	t.Cleanup(func() { homeBackgroundWorkersEnabled = previousWorkers })
	stubLogMaintenance(t)
	previousDB := statedb.GetGlobal()
	statedb.SetGlobal(nil)
	t.Cleanup(func() { statedb.SetGlobal(previousDB) })
	logs := captureUILog(t)

	home := NewHome()
	t.Cleanup(func() {
		cleanupKimiHooksHome(t, home)
		<-home.statusWorkerDone
	})
	if !strings.Contains(logs.String(), `"msg":"hook_watcher_init_failed"`) {
		t.Fatalf("Kimi watcher initialization error was not logged: %s", logs.String())
	}
}
