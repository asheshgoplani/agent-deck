package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// sandboxConductorHome points HOME and the XDG roots at a fresh temp dir so
// conductor state never touches a real home.
func sandboxConductorHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
}

// runConductorSetupAgentResolution mirrors the agent-selection part of
// handleConductorSetup: parse the CLI args, resolve the agent, then run the
// on-disk setup with it. It returns the agent setup ran with.
func runConductorSetupAgentResolution(t *testing.T, args ...string) string {
	t.Helper()
	fs := newConductorSetupFlagSet()
	name, extras, err := parseConductorSetupArgs(fs, args)
	if err != nil || len(extras) > 0 {
		t.Fatalf("parseConductorSetupArgs(%v): name=%q extras=%v err=%v", args, name, extras, err)
	}
	agent, err := resolveConductorSetupAgent(fs, name)
	if err != nil {
		t.Fatalf("resolveConductorSetupAgent(%v): %v", args, err)
	}
	if err := session.SetupConductorWithAgent(name, "default", agent, true, true, "", "", "", "", nil, ""); err != nil {
		t.Fatalf("SetupConductorWithAgent(%q, agent=%q): %v", name, agent, err)
	}
	return agent
}

func conductorMetaAgent(t *testing.T, name string) string {
	t.Helper()
	meta, err := session.LoadConductorMeta(name)
	if err != nil {
		t.Fatalf("LoadConductorMeta(%q): %v", name, err)
	}
	return meta.Agent
}

// Issue #2434: a bare `conductor setup <name>` re-run (the documented way to
// add channels later) must keep the conductor's existing agent instead of
// resetting it to the claude default, and must therefore leave the
// conductor's AGENTS.md alone. An explicit --agent still switches.
func TestConductorSetup_BareRerunKeepsAgent_Issue2434(t *testing.T) {
	sandboxConductorHome(t)
	const name = "pi-ops"

	if got := runConductorSetupAgentResolution(t, "--agent", "pi", name); got != session.ConductorAgentPi {
		t.Fatalf("initial setup agent = %q, want pi", got)
	}
	dir, err := session.ConductorNameDir(name)
	if err != nil {
		t.Fatal(err)
	}
	agentsPath := filepath.Join(dir, "AGENTS.md")
	before, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatalf("AGENTS.md missing after pi setup: %v", err)
	}

	// Bare re-run: no --agent flag at all.
	if got := runConductorSetupAgentResolution(t, name); got != session.ConductorAgentPi {
		t.Errorf("bare re-run resolved agent %q, want the existing pi", got)
	}
	if got := conductorMetaAgent(t, name); got != session.ConductorAgentPi {
		t.Errorf("meta agent after bare re-run = %q, want pi", got)
	}
	after, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatalf("AGENTS.md deleted by bare re-run: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("AGENTS.md changed by bare re-run")
	}
	if _, err := os.Stat(filepath.Join(dir, "CLAUDE.md")); !os.IsNotExist(err) {
		t.Errorf("bare re-run of a pi conductor wrote CLAUDE.md (err=%v)", err)
	}

	// Explicit --agent claude still switches the runtime.
	if got := runConductorSetupAgentResolution(t, "--agent", "claude", name); got != session.ConductorAgentClaude {
		t.Fatalf("explicit --agent claude resolved %q, want claude", got)
	}
	if got := conductorMetaAgent(t, name); got != session.ConductorAgentClaude {
		t.Errorf("meta agent after explicit switch = %q, want claude", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "CLAUDE.md")); err != nil {
		t.Errorf("CLAUDE.md missing after explicit switch to claude: %v", err)
	}
}

// A bare setup of a brand-new conductor still defaults to claude.
func TestConductorSetup_BareFreshSetupDefaultsToClaude_Issue2434(t *testing.T) {
	sandboxConductorHome(t)
	if got := runConductorSetupAgentResolution(t, "fresh"); got != session.ConductorAgentClaude {
		t.Fatalf("fresh bare setup agent = %q, want claude", got)
	}
}

// A bare re-run must not guess when the stored agent is one this build does
// not recognize (a newer release's runtime): falling back to claude would
// clobber that conductor exactly like #2434. It must ask for --agent instead.
func TestConductorSetup_BareRerunRefusesUnknownStoredAgent_Issue2434(t *testing.T) {
	sandboxConductorHome(t)
	const name = "future"
	// SaveConductorMeta rejects agents this build does not know, so write the
	// newer release's meta.json by hand.
	dir, err := session.ConductorNameDir(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := `{"name":"future","agent":"someday-agent","profile":"default","heartbeat_enabled":true}`
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
	fs := newConductorSetupFlagSet()
	if _, _, err := parseConductorSetupArgs(fs, []string{name}); err != nil {
		t.Fatal(err)
	}
	agent, err := resolveConductorSetupAgent(fs, name)
	if err == nil {
		t.Fatalf("bare re-run over unrecognized stored agent resolved %q, want an error asking for --agent", agent)
	}
	if !strings.Contains(err.Error(), "--agent") {
		t.Errorf("error %q should tell the user to pass --agent", err)
	}

	// An explicit --agent is still honoured.
	fs = newConductorSetupFlagSet()
	if _, _, err := parseConductorSetupArgs(fs, []string{"--agent", "codex", name}); err != nil {
		t.Fatal(err)
	}
	if agent, err := resolveConductorSetupAgent(fs, name); err != nil || agent != session.ConductorAgentCodex {
		t.Fatalf("explicit --agent codex over unknown stored agent = (%q, %v), want (codex, nil)", agent, err)
	}
}
