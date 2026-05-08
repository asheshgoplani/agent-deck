package profile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Phase 1 v1.9 regression coverage for issue #881 (profile divergence).
//
// The existing parity_test.go pins TUI↔web parity for two env shapes only:
//   * CLAUDE_CONFIG_DIR set, AGENTDECK_PROFILE unset  → "work"
//   * AGENTDECK_PROFILE explicit                       → wins over CLAUDE_CONFIG_DIR
//
// Three holes remain. Each is a v1.9 ship-blocker because a regression
// silently re-routes every read to the wrong profile (= "I deleted all my
// sessions" support thread):
//
//   prof-001: priority 4 (config.json default_profile) when no env vars set.
//   prof-002: full precedence ladder explicit > env > CLAUDE_CONFIG_DIR > config > "default".
//   prof-003: profileFromClaudeConfigDir behavior on every documented variant.
//
// Tests live in internal/profile so the public DetectCurrentProfile() — the
// shim TUI consumers still import — is exercised end-to-end. Cross-package
// boundary catches re-imports that bypass the unification done in #881.

// writeConfigJSON seeds ~/.agent-deck/config.json under the test-scoped HOME
// with a chosen default_profile. session.SaveConfig honors HOME via
// session.GetAgentDeckDir, so the file lands inside t.TempDir().
func writeConfigJSON(t *testing.T, defaultProfile string) {
	t.Helper()
	home := os.Getenv("HOME")
	if home == "" {
		t.Fatal("HOME unset; test must seed it")
	}
	dir := filepath.Join(home, ".agent-deck")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir .agent-deck: %v", err)
	}
	body := map[string]any{"default_profile": defaultProfile, "version": 1}
	data, _ := json.MarshalIndent(body, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o600); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
}

// prof-001: priority 4 — config.json default_profile.
//
// Symptom this guards: an empty-env machine boots, both TUI and web read
// from the user's chosen non-"default" profile (e.g. they `cdw`'d once,
// set default to "work" via CLI, then logged in next day with no env).
// Today only priorities 1–3 have direct coverage.
func TestProfileResolution_ConfigDefaultFallback(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	os.Unsetenv("AGENTDECK_PROFILE")
	os.Unsetenv("CLAUDE_CONFIG_DIR")

	writeConfigJSON(t, "alpha")

	if got := DetectCurrentProfile(); got != "alpha" {
		t.Fatalf("TUI fallback: want %q got %q", "alpha", got)
	}
	if got := session.GetEffectiveProfile(""); got != "alpha" {
		t.Fatalf("web fallback: want %q got %q", "alpha", got)
	}
}

// prof-001 (continued): when even config.json is absent OR has an empty
// default_profile, the literal "default" string is the floor. Without this
// guard, a bug that flips the floor to "" would silently produce empty
// session-dir paths in storage.go:157.
func TestProfileResolution_LiteralDefaultFloor(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	os.Unsetenv("AGENTDECK_PROFILE")
	os.Unsetenv("CLAUDE_CONFIG_DIR")

	// No config.json at all.
	if got := session.GetEffectiveProfile(""); got != session.DefaultProfile {
		t.Fatalf("no config: want %q got %q", session.DefaultProfile, got)
	}

	// Empty default_profile in config.json.
	writeConfigJSON(t, "")
	if got := session.GetEffectiveProfile(""); got != session.DefaultProfile {
		t.Fatalf("empty config default: want %q got %q", session.DefaultProfile, got)
	}
}

// prof-002: the full precedence ladder.
//
// Documented in config.go:301-336:
//   1. explicit (passed-through arg, e.g. -p flag)
//   2. AGENTDECK_PROFILE env
//   3. CLAUDE_CONFIG_DIR-inferred
//   4. config.json default_profile
//   5. literal "default"
//
// A regression that re-orders any of these has the same visible symptom
// (wrong profile) but very different blast radii. This table-driven case
// nails every transition so a re-order can't go in unnoticed.
func TestProfileResolution_PrecedenceLadder(t *testing.T) {
	type env struct {
		explicit       string
		agentdeckProf  string
		claudeConfigDir string
		configDefault  string // "" means do not write config.json
	}
	cases := []struct {
		name string
		env  env
		want string
	}{
		{
			name: "explicit wins over everything",
			env: env{
				explicit:        "explicit",
				agentdeckProf:   "envprof",
				claudeConfigDir: ".claude-dirprof",
				configDefault:   "configprof",
			},
			want: "explicit",
		},
		{
			name: "AGENTDECK_PROFILE wins over CLAUDE_CONFIG_DIR",
			env: env{
				agentdeckProf:   "envprof",
				claudeConfigDir: ".claude-dirprof",
				configDefault:   "configprof",
			},
			want: "envprof",
		},
		{
			name: "CLAUDE_CONFIG_DIR wins over config default",
			env: env{
				claudeConfigDir: ".claude-dirprof",
				configDefault:   "configprof",
			},
			want: "dirprof",
		},
		{
			name: "config default used when no env",
			env: env{
				configDefault: "configprof",
			},
			want: "configprof",
		},
		{
			name: "literal default when nothing is set",
			env:  env{},
			want: session.DefaultProfile,
		},
		{
			name: "CLAUDE_CONFIG_DIR=~/.claude does not infer (no suffix)",
			env: env{
				claudeConfigDir: ".claude",
				configDefault:   "configprof",
			},
			want: "configprof",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			t.Setenv("HOME", tmp)

			if tc.env.agentdeckProf == "" {
				os.Unsetenv("AGENTDECK_PROFILE")
			} else {
				t.Setenv("AGENTDECK_PROFILE", tc.env.agentdeckProf)
			}
			if tc.env.claudeConfigDir == "" {
				os.Unsetenv("CLAUDE_CONFIG_DIR")
			} else {
				t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(tmp, tc.env.claudeConfigDir))
			}
			if tc.env.configDefault != "" {
				writeConfigJSON(t, tc.env.configDefault)
			}

			if got := session.GetEffectiveProfile(tc.env.explicit); got != tc.want {
				t.Errorf("GetEffectiveProfile(%q): want %q got %q", tc.env.explicit, tc.want, got)
			}
			// TUI surface MUST agree (this is the #881 invariant).
			if tc.env.explicit == "" {
				if got := DetectCurrentProfile(); got != tc.want {
					t.Errorf("DetectCurrentProfile: want %q got %q", tc.want, got)
				}
			}
		})
	}
}
