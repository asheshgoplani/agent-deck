package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Regression coverage for issue #881 (profile divergence): GetEffectiveProfile
// is the single profile resolver for the TUI, CLI, web, storage, push and
// costs. A regression that re-orders its precedence silently routes every
// read to the wrong profile, which looks like lost sessions.
//
// These tests moved here from the removed internal/profile package, whose
// DetectCurrentProfile only wrapped GetEffectiveProfile(""). They rely on
// this package's TestMain (HOME+XDG and tmux socket isolation). TestMain sets
// AGENTDECK_PROFILE=_test process-wide, so each test clears it with
// t.Setenv rather than os.Unsetenv, which would leak into later tests.
//
// profileFromClaudeConfigDir parsing is covered directly in
// profile_resolver_test.go; source classification in
// issue1790_profile_autocreate_test.go.

// writeDefaultProfileConfig seeds ~/.agent-deck/config.json under the
// test-scoped HOME with the given default_profile.
func writeDefaultProfileConfig(t *testing.T, defaultProfile string) {
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

// TestProfileResolution_LiteralDefaultFloor: with no env and no config.json,
// or an empty default_profile, the literal "default" is the floor. A floor of
// "" would produce empty profile-dir paths in storage.
func TestProfileResolution_LiteralDefaultFloor(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENTDECK_PROFILE", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")

	if got := GetEffectiveProfile(""); got != DefaultProfile {
		t.Fatalf("no config: want %q got %q", DefaultProfile, got)
	}

	writeDefaultProfileConfig(t, "")
	if got := GetEffectiveProfile(""); got != DefaultProfile {
		t.Fatalf("empty config default: want %q got %q", DefaultProfile, got)
	}
}

// TestProfileResolution_PrecedenceLadder pins the full order documented on
// GetEffectiveProfile:
//  1. explicit (the -p flag)
//  2. AGENTDECK_PROFILE
//  3. inferred from CLAUDE_CONFIG_DIR
//  4. config.json default_profile
//  5. literal "default"
func TestProfileResolution_PrecedenceLadder(t *testing.T) {
	type env struct {
		explicit        string
		agentdeckProf   string
		claudeConfigDir string // relative to the test HOME; "" means unset
		configDefault   string // "" means do not write config.json
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
			// The exact #881 shape: `cdw` alias, no AGENTDECK_PROFILE, no
			// config.json. Must be "work", not the "default" floor.
			name: "CLAUDE_CONFIG_DIR .claude-work with no config infers work",
			env: env{
				claudeConfigDir: ".claude-work",
			},
			want: "work",
		},
		{
			name: "CLAUDE_CONFIG_DIR without leading dot uses last dash segment",
			env: env{
				claudeConfigDir: "claude-production",
			},
			want: "production",
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
			want: DefaultProfile,
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
			t.Setenv("AGENTDECK_PROFILE", tc.env.agentdeckProf)
			if tc.env.claudeConfigDir == "" {
				t.Setenv("CLAUDE_CONFIG_DIR", "")
			} else {
				t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(tmp, tc.env.claudeConfigDir))
			}
			if tc.env.configDefault != "" {
				writeDefaultProfileConfig(t, tc.env.configDefault)
			}

			if got := GetEffectiveProfile(tc.env.explicit); got != tc.want {
				t.Errorf("GetEffectiveProfile(%q): want %q got %q", tc.env.explicit, tc.want, got)
			}
		})
	}
}
