package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

func setupKimiHooksCommandTest(t *testing.T) (string, *statedb.StateDB) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("KIMI_CODE_HOME", filepath.Join(home, "kimi"))
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)
	db, err := statedb.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	previous := statedb.GetGlobal()
	statedb.SetGlobal(db)
	t.Cleanup(func() { statedb.SetGlobal(previous) })
	return filepath.Join(home, "kimi"), db
}

func TestHandleKimiHooksLifecycleAndConsent(t *testing.T) {
	configDir, db := setupKimiHooksCommandTest(t)

	out := captureStdout(t, func() { handleKimiHooks([]string{"status"}) })
	if !strings.Contains(out, "Status: NOT INSTALLED") || !strings.Contains(out, filepath.Join(configDir, "config.toml")) {
		t.Fatalf("initial status = %q", out)
	}
	out = captureStdout(t, func() { handleKimiHooks([]string{"install"}) })
	if !strings.Contains(out, "Kimi hooks installed successfully.") {
		t.Fatalf("install output = %q", out)
	}
	if decision, _ := db.GetMeta("kimi_hooks_prompted"); decision != "accepted" {
		t.Fatalf("explicit install consent = %q, want accepted", decision)
	}
	if !session.CheckKimiHooksInstalled(configDir) {
		t.Fatal("hooks missing after CLI install")
	}

	out = captureStdout(t, func() { handleKimiHooks([]string{"status"}) })
	if !strings.Contains(out, "Status: INSTALLED") {
		t.Fatalf("installed status = %q", out)
	}
	out = captureStdout(t, func() { handleKimiHooks([]string{"uninstall"}) })
	if !strings.Contains(out, "Kimi hooks removed successfully.") {
		t.Fatalf("uninstall output = %q", out)
	}
	if decision, _ := db.GetMeta("kimi_hooks_prompted"); decision != "declined" {
		t.Fatalf("uninstall consent = %q, want declined", decision)
	}
	if session.CheckKimiHooksInstalled(configDir) {
		t.Fatal("hooks still installed after CLI uninstall")
	}

	captureStdout(t, func() { handleKimiHooks([]string{"install"}) })
	if decision, _ := db.GetMeta("kimi_hooks_prompted"); decision != "accepted" {
		t.Fatalf("explicit reinstall consent = %q, want accepted", decision)
	}
}

func TestHandleKimiHooksHelpIsReadOnly(t *testing.T) {
	configDir, _ := setupKimiHooksCommandTest(t)
	for _, args := range [][]string{{"help"}, {"--help"}, {"-h"}, {"install", "--help"}, {"uninstall", "-h"}, {"status", "help"}} {
		out := captureStdout(t, func() { handleKimiHooks(args) })
		if !strings.Contains(out, "Usage: agent-deck kimi-hooks <command>") {
			t.Errorf("kimi-hooks %v printed no usage: %q", args, out)
		}
		if _, err := os.Stat(filepath.Join(configDir, "config.toml")); !os.IsNotExist(err) {
			t.Fatalf("kimi-hooks %v wrote config", args)
		}
	}
}
