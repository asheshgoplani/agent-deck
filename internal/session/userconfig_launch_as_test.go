// Tests for TmuxSettings.LaunchAs + GetLaunchAs (v1.7.21 defense-in-depth).
//
// LaunchAs is the new config-driven spawn-mode selector that agent-deck
// passes through to tmux.Session. These tests pin:
//   - toml decodes [tmux].launch_as = "..." into TmuxSettings.LaunchAs
//   - GetLaunchAs normalizes case + trims whitespace
//   - Unknown values return "" (defer to legacy LaunchInUserScope)
//   - nil pointer (absent field) returns "" — zero-behavior-change guarantee
//
// See .planning/v1721-scope-to-service/PLAN.md.
package session

import (
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTmuxSettings_GetLaunchAs_Unset returns empty string so
// downstream resolveLaunchMode falls through to LaunchInUserScope
// (the zero-behavior-change contract for v1.7.20 users).
func TestTmuxSettings_GetLaunchAs_Unset(t *testing.T) {
	s := TmuxSettings{}
	assert.Equal(t, "", s.GetLaunchAs(), "unset launch_as must return empty string")
}

// TestTmuxSettings_GetLaunchAs_ValidValues ensures each documented
// value round-trips through GetLaunchAs unchanged (lowercase canonical).
func TestTmuxSettings_GetLaunchAs_ValidValues(t *testing.T) {
	for _, v := range []string{"scope", "service", "direct", "auto"} {
		t.Run(v, func(t *testing.T) {
			val := v
			s := TmuxSettings{LaunchAs: &val}
			assert.Equal(t, v, s.GetLaunchAs())
		})
	}
}

// TestTmuxSettings_GetLaunchAs_CaseInsensitive ensures "Service" and
// "SERVICE" and " service " all normalize to "service" so config typos
// don't silently opt the user out of the feature they requested.
func TestTmuxSettings_GetLaunchAs_CaseInsensitive(t *testing.T) {
	cases := map[string]string{
		"Service":   "service",
		"SERVICE":   "service",
		" service ": "service",
		"\tSCOPE\n": "scope",
		"Auto":      "auto",
	}
	for input, want := range cases {
		t.Run(input, func(t *testing.T) {
			v := input
			s := TmuxSettings{LaunchAs: &v}
			assert.Equal(t, want, s.GetLaunchAs())
		})
	}
}

// TestTmuxSettings_GetLaunchAs_UnknownValueReturnsEmpty protects users
// from typos. A misspelling like "servise" must NOT put them on a
// random spawn path — it returns "" so downstream resolveLaunchMode
// uses legacy LaunchInUserScope behavior.
func TestTmuxSettings_GetLaunchAs_UnknownValueReturnsEmpty(t *testing.T) {
	for _, bad := range []string{"servise", "foo", "scope-ish", "SERVIC", "bin"} {
		t.Run(bad, func(t *testing.T) {
			v := bad
			s := TmuxSettings{LaunchAs: &v}
			assert.Equal(t, "", s.GetLaunchAs(),
				"unknown value %q must return empty so legacy fallback kicks in", bad)
		})
	}
}

// TestTmuxSettings_LaunchAs_TomlRoundTrip exercises the toml decode
// path explicitly, because that's the one that runs at agent-deck
// startup. A regression that breaks toml tagging would silently make
// the new config key inert.
func TestTmuxSettings_LaunchAs_TomlRoundTrip(t *testing.T) {
	doc := `
[tmux]
launch_as = "service"
`
	var cfg struct {
		Tmux TmuxSettings `toml:"tmux"`
	}
	_, err := toml.Decode(doc, &cfg)
	require.NoError(t, err)
	require.NotNil(t, cfg.Tmux.LaunchAs, "launch_as must decode into a non-nil pointer")
	assert.Equal(t, "service", *cfg.Tmux.LaunchAs)
	assert.Equal(t, "service", cfg.Tmux.GetLaunchAs())
}

// TestTmuxSettings_LaunchAs_TomlAbsent ensures that leaving the field
// out of config.toml leaves the pointer nil (the "unset" sentinel
// GetLaunchAs consults). A regression where toml synthesizes an empty
// string would confuse downstream logic that distinguishes "absent"
// from "explicit empty" — we never want that distinction to drift.
func TestTmuxSettings_LaunchAs_TomlAbsent(t *testing.T) {
	doc := `
[tmux]
inject_status_line = true
`
	var cfg struct {
		Tmux TmuxSettings `toml:"tmux"`
	}
	_, err := toml.Decode(doc, &cfg)
	require.NoError(t, err)
	assert.Nil(t, cfg.Tmux.LaunchAs, "absent launch_as must leave the pointer nil")
	assert.Equal(t, "", cfg.Tmux.GetLaunchAs())
}
