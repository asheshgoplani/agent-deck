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

// TestGenerateLaunchdPlist_InjectsConductorDirOverride asserts the bridge
// daemon's launchd plist exports AGENT_DECK_CONDUCTOR_DIR set to the resolved
// [conductor].dir override. The running Python bridge reads this env var (with
// the #1350 XDG resolver as fallback) so a relocated conductor is scanned where
// it actually lives. Mirrors TestRenderConductorHeartbeatScript_UsesConfigOverrideConductorRoot.
func TestGenerateLaunchdPlist_InjectsConductorDirOverride(t *testing.T) {
	_, xdgConfigHome, _ := setupSessionXDGPathEnv(t)

	override := filepath.Join(t.TempDir(), "conductor homes", "conductor")
	writeConductorDirConfig(t, xdgConfigHome, override)

	plist, err := GenerateLaunchdPlist()
	if err != nil {
		if strings.Contains(err.Error(), "not found in PATH") {
			t.Skipf("skipping: %v", err)
		}
		t.Fatalf("GenerateLaunchdPlist(): %v", err)
	}

	if strings.Contains(plist, "__CONDUCTOR_DIR__") {
		t.Errorf("plist still contains __CONDUCTOR_DIR__ placeholder:\n%s", plist)
	}
	wantKey := "<key>AGENT_DECK_CONDUCTOR_DIR</key>"
	wantVal := "<string>" + override + "</string>"
	if !strings.Contains(plist, wantKey) || !strings.Contains(plist, wantVal) {
		t.Errorf("plist should export AGENT_DECK_CONDUCTOR_DIR=%q, plist:\n%s", override, plist)
	}
}

// TestGenerateSystemdBridgeService_InjectsConductorDirOverride is the Linux
// counterpart: the systemd bridge unit must carry an
// Environment=AGENT_DECK_CONDUCTOR_DIR=<override> line.
func TestGenerateSystemdBridgeService_InjectsConductorDirOverride(t *testing.T) {
	_, xdgConfigHome, _ := setupSessionXDGPathEnv(t)

	override := filepath.Join(t.TempDir(), "conductor homes", "conductor")
	writeConductorDirConfig(t, xdgConfigHome, override)

	unit, err := GenerateSystemdBridgeService()
	if err != nil {
		if strings.Contains(err.Error(), "not found in PATH") {
			t.Skipf("skipping: %v", err)
		}
		t.Fatalf("GenerateSystemdBridgeService(): %v", err)
	}

	// The override path here contains a space ("conductor homes"); the unit must
	// double-quote the value so systemd does not split it (CodeRabbit #1429).
	want := `Environment="AGENT_DECK_CONDUCTOR_DIR=` + override + `"`
	if !strings.Contains(unit, want) {
		t.Errorf("systemd bridge unit should contain %q, unit:\n%s", want, unit)
	}
	// The bridge.py path lives under the space-bearing override, so the ExecStart
	// argument must be quoted too — otherwise systemd would split it.
	wantExec := `"` + filepath.Join(override, "bridge.py") + `"`
	if !strings.Contains(unit, wantExec) {
		t.Errorf("systemd bridge unit ExecStart should quote bridge path %q, unit:\n%s", wantExec, unit)
	}
}

// TestSystemdUnits_QuoteEnvironmentAndExecStart pins the systemd quoting fix
// (CodeRabbit #1429): systemd splits unquoted whitespace, so every Environment=
// value and every space-capable ExecStart argument must be double-quoted.
func TestSystemdUnits_QuoteEnvironmentAndExecStart(t *testing.T) {
	setupSessionXDGPathEnv(t)

	bridge, err := GenerateSystemdBridgeService()
	if err != nil {
		if strings.Contains(err.Error(), "not found in PATH") {
			t.Skipf("skipping: %v", err)
		}
		t.Fatalf("GenerateSystemdBridgeService(): %v", err)
	}
	for _, want := range []string{
		`Environment="PATH=`,
		`Environment="HOME=`,
		`Environment="XDG_DATA_HOME=`,
		`Environment="XDG_CONFIG_HOME=`,
		`Environment="AGENT_DECK_CONDUCTOR_DIR=`,
		"ExecStart=\"", // quoted python3 executable + bridge path
	} {
		if !strings.Contains(bridge, want) {
			t.Errorf("systemd bridge unit must contain %q, unit:\n%s", want, bridge)
		}
	}

	hb, err := GenerateSystemdHeartbeatService("test-conductor")
	if err != nil {
		t.Fatalf("GenerateSystemdHeartbeatService(): %v", err)
	}
	for _, want := range []string{
		`Environment="PATH=`,
		`Environment="HOME=`,
		`ExecStart=/bin/bash "`, // quoted script path
	} {
		if !strings.Contains(hb, want) {
			t.Errorf("systemd heartbeat unit must contain %q, unit:\n%s", want, hb)
		}
	}
}

// TestHeartbeatDaemonStale_DetectsDirChange pins the side-effect-free staleness
// detector that powers the honest '[migrated]' caveat: when an installed
// heartbeat plist references a script path other than the conductor's
// currently-resolved <ConductorNameDir>/heartbeat.sh (as happens after a
// [conductor].dir change), HeartbeatDaemonStale reports true; a fresh plist and
// a missing daemon both report false.
func TestHeartbeatDaemonStale_DetectsDirChange(t *testing.T) {
	_, _, _ = setupSessionXDGPathEnv(t)

	name := "alpha"

	// No daemon installed -> nothing to warn about.
	if HeartbeatDaemonStale(name) {
		t.Fatalf("HeartbeatDaemonStale(%q) = true with no daemon installed, want false", name)
	}

	plistPath, err := HeartbeatPlistPath(name)
	if err != nil {
		t.Fatalf("HeartbeatPlistPath(): %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", filepath.Dir(plistPath), err)
	}

	// A plist pointing at a STALE (old-root) script path -> stale.
	staleScript := filepath.Join(t.TempDir(), "old-root", name, "heartbeat.sh")
	if err := os.WriteFile(plistPath, []byte("<string>"+staleScript+"</string>"), 0o644); err != nil {
		t.Fatalf("WriteFile(plist): %v", err)
	}
	if !HeartbeatDaemonStale(name) {
		t.Fatalf("HeartbeatDaemonStale(%q) = false for plist referencing a stale script path, want true", name)
	}

	// A plist pointing at the currently-resolved script path -> fresh.
	dir, err := ConductorNameDir(name)
	if err != nil {
		t.Fatalf("ConductorNameDir(): %v", err)
	}
	freshScript := filepath.Join(dir, "heartbeat.sh")
	if err := os.WriteFile(plistPath, []byte("<string>"+freshScript+"</string>"), 0o644); err != nil {
		t.Fatalf("WriteFile(plist): %v", err)
	}
	if HeartbeatDaemonStale(name) {
		t.Fatalf("HeartbeatDaemonStale(%q) = true for plist referencing the current script path, want false", name)
	}
}
