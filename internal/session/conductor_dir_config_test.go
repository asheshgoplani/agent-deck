package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeConductorDirConfig writes a config.toml carrying [conductor].dir under
// the test's XDG config home and resets the user-config cache so the next
// LoadUserConfig reads it fresh.
func writeConductorDirConfig(t *testing.T, xdgConfigHome, dir string) {
	t.Helper()
	cfgDir := filepath.Join(xdgConfigHome, "agent-deck")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", cfgDir, err)
	}
	cfg := "[conductor]\ndir = \"" + dir + "\"\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatalf("WriteFile(config.toml): %v", err)
	}
	ClearUserConfigCache()
}

func TestConductorDir_ConfigOverride(t *testing.T) {
	_, xdgConfigHome, _ := setupSessionXDGPathEnv(t)

	override := filepath.Join(t.TempDir(), "conductor homes")
	writeConductorDirConfig(t, xdgConfigHome, override)

	got, err := ConductorDir()
	if err != nil {
		t.Fatalf("ConductorDir(): %v", err)
	}
	if got != override {
		t.Fatalf("ConductorDir() = %q, want override %q", got, override)
	}

	// Named-conductor paths must compose with the override (every conductor
	// path helper routes through ConductorDir).
	nameDir, err := ConductorNameDir("alpha")
	if err != nil {
		t.Fatalf("ConductorNameDir(): %v", err)
	}
	if want := filepath.Join(override, "alpha"); nameDir != want {
		t.Fatalf("ConductorNameDir() = %q, want %q", nameDir, want)
	}
}

func TestConductorDir_ConfigOverrideExpandsTilde(t *testing.T) {
	home, xdgConfigHome, _ := setupSessionXDGPathEnv(t)

	writeConductorDirConfig(t, xdgConfigHome, "~/vault/conductor")

	got, err := ConductorDir()
	if err != nil {
		t.Fatalf("ConductorDir(): %v", err)
	}
	if want := filepath.Join(home, "vault", "conductor"); got != want {
		t.Fatalf("ConductorDir() = %q, want tilde-expanded %q", got, want)
	}
}

func TestConductorDir_ConfigOverrideExpandsEnvVar(t *testing.T) {
	_, xdgConfigHome, _ := setupSessionXDGPathEnv(t)

	rootDir := t.TempDir()
	t.Setenv("AGENT_DECK_TEST_CONDUCTOR_ROOT", rootDir)
	writeConductorDirConfig(t, xdgConfigHome, "$AGENT_DECK_TEST_CONDUCTOR_ROOT/conductor")

	got, err := ConductorDir()
	if err != nil {
		t.Fatalf("ConductorDir(): %v", err)
	}
	if want := filepath.Join(rootDir, "conductor"); got != want {
		t.Fatalf("ConductorDir() = %q, want env-expanded %q", got, want)
	}
}

// TestConductorDir_EmptyOverrideFallsThroughToXDG composes with the existing
// XDG coverage (TestXDGDataTask4_ConductorDirUsesXDGDataAndLegacyConductorFallback):
// an empty or whitespace-only [conductor].dir must leave the default
// resolution untouched.
func TestConductorDir_EmptyOverrideFallsThroughToXDG(t *testing.T) {
	_, xdgConfigHome, xdgDataHome := setupSessionXDGPathEnv(t)
	want := filepath.Join(xdgDataHome, "agent-deck", "conductor")

	for _, dir := range []string{"", "   "} {
		writeConductorDirConfig(t, xdgConfigHome, dir)
		got, err := ConductorDir()
		if err != nil {
			t.Fatalf("ConductorDir() with dir=%q: %v", dir, err)
		}
		if got != want {
			t.Fatalf("ConductorDir() with dir=%q = %q, want XDG default %q", dir, got, want)
		}
	}
}

// TestSetupConductorWithAgent_PreAcceptsClaudeTrustForOverrideDir composes the
// [conductor].dir override with the Claude trust pre-accept added upstream in
// #1393. SetupConductorWithAgent seeds projects[dir].hasTrustDialogAccepted in
// the root ~/.claude.json, where dir comes from ConductorNameDir — which routes
// through ConductorDir and therefore honors the override. The trust entry must
// be keyed under the OVERRIDDEN conductor home, not the default XDG location, or
// a configured conductor would still stall on Claude Code's trust prompt at
// first boot.
func TestSetupConductorWithAgent_PreAcceptsClaudeTrustForOverrideDir(t *testing.T) {
	_, xdgConfigHome, xdgDataHome := setupSessionXDGPathEnv(t)

	override := filepath.Join(t.TempDir(), "conductor homes")
	writeConductorDirConfig(t, xdgConfigHome, override)

	name := "trust-override"
	if err := SetupConductorWithAgent(name, "default", ConductorAgentClaude, true, true, "", "", "", "", nil, ""); err != nil {
		t.Fatalf("SetupConductorWithAgent: %v", err)
	}

	overrideDir, err := ConductorNameDir(name)
	if err != nil {
		t.Fatalf("ConductorNameDir: %v", err)
	}
	if want := filepath.Join(override, name); overrideDir != want {
		t.Fatalf("ConductorNameDir() = %q, want override-rooted %q", overrideDir, want)
	}

	entry := conductorTrustEntry(t, overrideDir)
	if entry == nil {
		t.Fatalf("no trust entry for overridden conductor dir %q in %s", overrideDir, GetUserMCPRootPath())
	}
	if entry["hasTrustDialogAccepted"] != true {
		t.Fatalf("hasTrustDialogAccepted = %v, want true", entry["hasTrustDialogAccepted"])
	}

	// The default XDG-rooted dir must NOT carry a trust entry — the pre-accept
	// targeted the configured dir, not the default.
	defaultDir := filepath.Join(xdgDataHome, "agent-deck", "conductor", name)
	if e := conductorTrustEntry(t, defaultDir); e != nil {
		t.Fatalf("unexpected trust entry for default dir %q: %v (pre-accept did not follow the override)", defaultDir, e)
	}
}

// TestRenderConductorHeartbeatScript_UsesConfigOverrideConductorRoot mirrors
// TestRenderConductorHeartbeatScript_UsesXDGConductorRoot for the
// [conductor].dir override: the rendered heartbeat script embeds the
// override as CONDUCTOR_ROOT (this is the install-time literal that goes
// stale if dir changes after setup — see InstallHeartbeatScript).
func TestRenderConductorHeartbeatScript_UsesConfigOverrideConductorRoot(t *testing.T) {
	_, xdgConfigHome, _ := setupSessionXDGPathEnv(t)

	override := filepath.Join(t.TempDir(), "conductor homes")
	writeConductorDirConfig(t, xdgConfigHome, override)

	script := renderConductorHeartbeatScript("alpha", "work")

	if !strings.Contains(script, `CONDUCTOR_ROOT="`+override+`"`) {
		t.Fatalf("heartbeat script should render override conductor root %q:\n%s", override, script)
	}
	if !strings.Contains(script, `"$CONDUCTOR_ROOT/alpha/HEARTBEAT_RULES.md"`) {
		t.Fatalf("heartbeat script should check per-conductor rules under override root:\n%s", script)
	}
	if !strings.Contains(script, `"$HOME/.agent-deck/conductor/alpha/HEARTBEAT_RULES.md"`) {
		t.Fatalf("heartbeat script should retain legacy fallback:\n%s", script)
	}
}
