// Package profilefixture builds the controlled environment that
// profile-resolution parity tests require: a known AGENTDECK_PROFILE,
// CLAUDE_CONFIG_DIR, agent-deck config-dir override, and isolated tempdir.
//
// It implements the harness from TEST-PLAN.md §6.3 and TUI-TEST-PLAN.md
// §6.7 (profileFixture). The five-way probe and AssertParity are
// transport-agnostic — callers pass closures that exercise CLI, web
// /api/settings, web /api/profiles, /healthz, and a TUI snapshot.
//
// Usage:
//
//	f := profilefixture.New(t, profilefixture.Options{EnvProfile: "work"})
//	f.AssertParity(t, profilefixture.Probes{
//	    CLI:        func() string { return cliJSON("list").Profile },
//	    WebAPI:     func() string { return getJSON("/api/settings").Profile },
//	    WebProfile: func() string { return getJSON("/api/profiles").Current },
//	    Healthz:    func() string { return getJSON("/healthz").Profile },
//	    TUI:        func() string { return tuiSnapshot.Profile },
//	})
package profilefixture

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// Options controls how the fixture seeds the environment.
type Options struct {
	// EnvProfile, if non-empty, sets AGENTDECK_PROFILE. Highest precedence
	// in session.GetEffectiveProfile.
	EnvProfile string

	// ClaudeConfigDir, if non-empty, sets CLAUDE_CONFIG_DIR. Used to
	// exercise the "infer profile from claude config dir" branch.
	ClaudeConfigDir string

	// ConfigDefault, if non-empty, writes a config file at
	// <Home>/.agent-deck/config.json with `default_profile` set to this
	// value. Lowest precedence — only matters when env / inference are
	// both empty.
	ConfigDefault string

	// AgentDeckHome overrides the agent-deck config root via HOME. If
	// empty, a fresh t.TempDir() is used so the test never touches the
	// developer's real ~/.agent-deck.
	AgentDeckHome string
}

// Fixture is the active test scaffold. Cleanup is registered via
// t.Cleanup automatically.
type Fixture struct {
	Home string // resolved HOME (the scratch dir)
	Opts Options
}

// New seeds env vars and on-disk state, returning a Fixture. All
// originals are restored by t.Cleanup.
func New(t *testing.T, opts Options) *Fixture {
	t.Helper()

	home := opts.AgentDeckHome
	if home == "" {
		home = t.TempDir()
	}
	t.Setenv("HOME", home)

	if opts.EnvProfile != "" {
		t.Setenv("AGENTDECK_PROFILE", opts.EnvProfile)
	} else {
		t.Setenv("AGENTDECK_PROFILE", "")
		// t.Setenv to "" is treated as "set to empty"; clear instead.
		_ = os.Unsetenv("AGENTDECK_PROFILE")
	}

	if opts.ClaudeConfigDir != "" {
		t.Setenv("CLAUDE_CONFIG_DIR", opts.ClaudeConfigDir)
	} else {
		_ = os.Unsetenv("CLAUDE_CONFIG_DIR")
	}

	if opts.ConfigDefault != "" {
		writeConfigDefault(t, home, opts.ConfigDefault)
	}

	return &Fixture{Home: home, Opts: opts}
}

// Probes is the five-way probe suite from §6.7. Each field is a thunk
// that returns the profile string the relevant surface reports. Tests
// pass concrete closures bound to live CLI / HTTP clients / TUI models.
//
// Any field left nil is skipped during AssertParity (so a unit test can
// exercise a subset of surfaces).
type Probes struct {
	CLI        func() string // `agent-deck list --json | .profile`
	WebAPI     func() string // `GET /api/settings | .profile`
	WebProfile func() string // `GET /api/profiles | .current`
	Healthz    func() string // `GET /healthz | .profile`
	TUI        func() string // tuitest snapshot's profile field
}

// TB is the subset of testing.TB used by Assert helpers. We avoid
// pulling in *testing.T so the package can be exercised under stub Ts in
// its own unit tests.
type TB interface {
	Errorf(format string, args ...any)
	Helper()
}

// Probe runs every non-nil probe and returns a {name -> reported
// profile} map. The map is also useful for diagnostic dumps.
func (f *Fixture) Probe(p Probes) map[string]string {
	out := map[string]string{}
	if p.CLI != nil {
		out["CLI"] = p.CLI()
	}
	if p.WebAPI != nil {
		out["WebAPI"] = p.WebAPI()
	}
	if p.WebProfile != nil {
		out["WebProfile"] = p.WebProfile()
	}
	if p.Healthz != nil {
		out["Healthz"] = p.Healthz()
	}
	if p.TUI != nil {
		out["TUI"] = p.TUI()
	}
	return out
}

// AssertParity runs Probe and fails the test if any two probes return
// different values. The failure message lists every probe so the
// diagnosis is one glance.
func (f *Fixture) AssertParity(t TB, p Probes) {
	t.Helper()
	results := f.Probe(p)
	if len(results) < 2 {
		// Nothing meaningful to compare; defer to caller.
		return
	}

	// Pick a reference (any one — sort for stable error messages).
	keys := make([]string, 0, len(results))
	for k := range results {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	ref := results[keys[0]]

	diverged := false
	for _, k := range keys[1:] {
		if results[k] != ref {
			diverged = true
			break
		}
	}
	if diverged {
		t.Errorf("profilefixture: probe parity violation: %v", results)
	}
}

// writeConfigDefault writes a minimal agent-deck config.json with the
// requested default_profile. The on-disk shape mirrors what
// session.SaveConfig produces; tests that care about other fields
// should write their own config.
func writeConfigDefault(t *testing.T, home, profile string) {
	t.Helper()
	dir := filepath.Join(home, ".agent-deck")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("profilefixture: mkdir %s: %v", dir, err)
	}
	body, _ := json.Marshal(map[string]any{"default_profile": profile})
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("profilefixture: write %s: %v", path, err)
	}
}
