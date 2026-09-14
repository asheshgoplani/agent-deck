package session

import (
	"os"
	"path/filepath"
	"testing"
)

// The native-SSH TUI acceptance (cmd/agent-deck/native_tui_ssh_test.go) makes
// itself release-independent by writing `[updates]\ncheck_enabled = false` into
// its fixture config: without it, promptForUpdate() prints a startup
// "Update available" banner into the TUI pane the moment a newer release
// exists, displacing the SESSIONS/PREVIEW divider the test asserts (this is
// what broke it after v1.16.7 shipped). This guards the exact config key that
// fix relies on — if `check_enabled` is renamed or the parse breaks, the
// determinism fix would silently stop working and this fails first.
func TestUpdateCheckEnabled_FixtureConfigDisablesStartupCheck(t *testing.T) {
	writeConfig := func(t *testing.T, body string) {
		t.Helper()
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
		t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
		t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
		dir := filepath.Join(home, ".config", "agent-deck")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		ClearUserConfigCache()
		t.Cleanup(ClearUserConfigCache)
	}

	// The exact update stanza the acceptance fixture writes disables the check.
	writeConfig(t, "[telemetry]\ndisabled = true\n[updates]\ncheck_enabled = false\n[claude]\nhooks_enabled = false\n")
	if GetUpdateSettings().GetCheckEnabled() {
		t.Fatal("check_enabled = false must disable the startup update check (fixture would still banner)")
	}

	// Without the stanza, the check defaults on — proving the line is load-bearing.
	writeConfig(t, "[telemetry]\ndisabled = true\n[claude]\nhooks_enabled = false\n")
	if !GetUpdateSettings().GetCheckEnabled() {
		t.Fatal("update check defaults on; the fixture's [updates] check_enabled=false is what makes the acceptance deterministic")
	}
}
