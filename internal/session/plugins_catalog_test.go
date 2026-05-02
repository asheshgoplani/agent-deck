package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// Phase 1 RED tests for the plugin catalog (RFC docs/rfc/PLUGIN_ATTACH.md).
// These cover:
//   - PluginDef TOML round-trip (config.toml -> LoadUserConfig -> map)
//   - GetAvailablePlugins / GetAvailablePluginNames / GetPluginDef accessors
//   - PluginDef helper methods (ID, ChannelID)
//   - v1 §6 telegram-official refusal at catalog-load level (the entry is
//     filtered out so callers cannot accidentally enable it through the
//     legitimate catalog path)
//   - JSON persistence of Instance.Plugins through the same shape that
//     state.db round-trips today (mirrors Channels)
//
// Pattern mirrors userconfig_test.go's HOME-redirect setup.

// withTempHome redirects HOME to a fresh tempdir, clears the userconfig
// cache before and after, and returns the tempdir for further setup.
func withTempHome(t *testing.T) string {
	t.Helper()
	temp := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", temp)
	t.Cleanup(func() { os.Setenv("HOME", originalHome) })
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)
	return temp
}

// writeConfig drops a config.toml with the given content into the test
// HOME's .agent-deck dir.
func writeConfig(t *testing.T, home, content string) {
	t.Helper()
	dir := filepath.Join(home, ".agent-deck")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir agent-deck: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
}

// TestPluginDef_TOMLRoundTrip asserts that `[plugins.<name>]` tables in
// config.toml decode into UserConfig.Plugins with all fields preserved.
func TestPluginDef_TOMLRoundTrip(t *testing.T) {
	home := withTempHome(t)
	writeConfig(t, home, `
[plugins.octopus]
name = "octopus"
source = "nyldn/claude-octopus"
emits_channel = false
auto_install = true
description = "Multi-LLM code review"

[plugins.discord]
name = "discord"
source = "claude-plugins-official"
emits_channel = true
auto_install = true
description = "Discord channel bridge"
`)

	cfg, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	if len(cfg.Plugins) != 2 {
		t.Fatalf("Plugins len: got %d, want 2 (entries: %v)", len(cfg.Plugins), cfg.Plugins)
	}

	octopus, ok := cfg.Plugins["octopus"]
	if !ok {
		t.Fatalf("Plugins[\"octopus\"] missing")
	}
	if octopus.Name != "octopus" || octopus.Source != "nyldn/claude-octopus" {
		t.Errorf("octopus name/source: got (%q, %q), want (\"octopus\", \"nyldn/claude-octopus\")", octopus.Name, octopus.Source)
	}
	if octopus.EmitsChannel {
		t.Errorf("octopus.EmitsChannel: got true, want false")
	}
	if !octopus.AutoInstall {
		t.Errorf("octopus.AutoInstall: got false, want true")
	}
	if octopus.Description != "Multi-LLM code review" {
		t.Errorf("octopus.Description: got %q", octopus.Description)
	}

	discord := cfg.Plugins["discord"]
	if !discord.EmitsChannel {
		t.Errorf("discord.EmitsChannel: got false, want true")
	}
}

// TestPluginDef_ID asserts the runtime id construction is the canonical
// "<name>@<source>" form. Worker_scratch.go and the channel auto-link both
// depend on this string shape.
func TestPluginDef_ID(t *testing.T) {
	cases := []struct {
		name string
		def  PluginDef
		want string
	}{
		{
			name: "marketplace-name",
			def:  PluginDef{Name: "octopus", Source: "claude-plugins-official"},
			want: "octopus@claude-plugins-official",
		},
		{
			name: "owner-repo",
			def:  PluginDef{Name: "octopus", Source: "nyldn/claude-octopus"},
			want: "octopus@nyldn/claude-octopus",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.def.ID(); got != tc.want {
				t.Errorf("ID: got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestPluginDef_ChannelID asserts the auto-link channel id format.
// "plugin:<name>@<source>" — must be exactly compatible with
// telegram_validator.go's telegramChannelPrefix and the claude-code CLI
// --channels flag value.
func TestPluginDef_ChannelID(t *testing.T) {
	def := PluginDef{Name: "telegram", Source: "acme/telegram-fork"}
	if got, want := def.ChannelID(), "plugin:telegram@acme/telegram-fork"; got != want {
		t.Errorf("ChannelID: got %q, want %q", got, want)
	}
}

// TestGetAvailablePlugins_FiltersTelegramOfficial asserts the v1 §6 refusal
// policy: a config.toml entry pointing at telegram@claude-plugins-official is
// silently filtered from the catalog so the rest of the system cannot
// accidentally enable it through the catalog path. The entry must still be
// parseable (no toml decode error), only the read-side accessors hide it.
func TestGetAvailablePlugins_FiltersTelegramOfficial(t *testing.T) {
	home := withTempHome(t)
	writeConfig(t, home, `
[plugins.telegram-official]
name = "telegram"
source = "claude-plugins-official"
emits_channel = true
auto_install = true

[plugins.telegram-fork]
name = "telegram"
source = "acme/telegram-fork"
emits_channel = true
auto_install = true

[plugins.octopus]
name = "octopus"
source = "nyldn/claude-octopus"
auto_install = true
`)

	plugins := GetAvailablePlugins()
	if _, refused := plugins["telegram-official"]; refused {
		t.Errorf("GetAvailablePlugins must filter telegram@claude-plugins-official entries; got: %v", plugins)
	}
	if _, ok := plugins["telegram-fork"]; !ok {
		t.Errorf("GetAvailablePlugins must keep telegram fork entries (different source); got: %v", plugins)
	}
	if _, ok := plugins["octopus"]; !ok {
		t.Errorf("GetAvailablePlugins must keep non-telegram entries; got: %v", plugins)
	}

	names := GetAvailablePluginNames()
	wantNames := []string{"octopus", "telegram-fork"}
	if !reflect.DeepEqual(names, wantNames) {
		t.Errorf("GetAvailablePluginNames: got %v, want %v (sorted, refusal-filtered)", names, wantNames)
	}

	if def := GetPluginDef("telegram-official"); def != nil {
		t.Errorf("GetPluginDef(\"telegram-official\") must return nil for refused entry; got %+v", def)
	}
	if def := GetPluginDef("telegram-fork"); def == nil {
		t.Errorf("GetPluginDef(\"telegram-fork\") must return the entry (fork is allowed)")
	}
}

// TestIsTelegramOfficialRefusal locks the predicate so a future code-shuffle
// can't loosen the refusal silently.
func TestIsTelegramOfficialRefusal(t *testing.T) {
	cases := []struct {
		name, source string
		want         bool
	}{
		{"telegram", "claude-plugins-official", true},
		{"telegram", "acme/telegram-fork", false},
		{"telegram", "user/repo", false},
		{"discord", "claude-plugins-official", false},
		{"Telegram", "claude-plugins-official", false}, // case-sensitive
	}
	for _, tc := range cases {
		got := IsTelegramOfficialRefusal(tc.name, tc.source)
		if got != tc.want {
			t.Errorf("IsTelegramOfficialRefusal(%q, %q): got %v, want %v", tc.name, tc.source, got, tc.want)
		}
	}
}

// TestGetAvailablePlugins_EmptyWhenNoConfig asserts a nil-safe empty map when
// config.toml is absent. Mirrors GetAvailableMCPs's contract — callers can
// range over the result without nil-checks.
func TestGetAvailablePlugins_EmptyWhenNoConfig(t *testing.T) {
	withTempHome(t)
	plugins := GetAvailablePlugins()
	if plugins == nil {
		t.Fatalf("GetAvailablePlugins must never return nil")
	}
	if len(plugins) != 0 {
		t.Errorf("GetAvailablePlugins on empty config: got %v, want empty map", plugins)
	}
	if names := GetAvailablePluginNames(); len(names) != 0 {
		t.Errorf("GetAvailablePluginNames on empty config: got %v, want empty slice", names)
	}
	if def := GetPluginDef("anything"); def != nil {
		t.Errorf("GetPluginDef on empty config must return nil; got %+v", def)
	}
}

// TestUserConfig_PluginsNilSafePostDecode asserts the post-decode init
// guarantees a non-nil map even when [plugins] is absent from config.toml,
// matching the Tools and MCPs invariants at userconfig.go:1558-1565.
func TestUserConfig_PluginsNilSafePostDecode(t *testing.T) {
	home := withTempHome(t)
	writeConfig(t, home, `
[claude]
config_dir = "~/.claude-work"
`)
	cfg, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	if cfg.Plugins == nil {
		t.Fatalf("UserConfig.Plugins must be non-nil after LoadUserConfig — caller-side range/lookups depend on this invariant")
	}
}

// TestInstance_PluginsJSONPersistence asserts the JSON tag on the new
// Instance.Plugins field round-trips through marshal/unmarshal — this is
// the same shape state.db serializes and the CLAUDE.md persistence
// mandate's TestPersistence_PluginsSurviveRestart will rely on.
func TestInstance_PluginsJSONPersistence(t *testing.T) {
	in := &Instance{
		ID:      "test-id",
		Title:   "test",
		Tool:    "claude",
		Plugins: []string{"octopus", "discord"},
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	// JSON tag MUST be "plugins" (matches the channels convention so consumer
	// tooling reads both fields with the same lowercase pattern).
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal raw: %v", err)
	}
	if _, ok := raw["plugins"]; !ok {
		t.Fatalf("Instance JSON missing \"plugins\" key; got keys: %v", keysOf(raw))
	}
	var out Instance
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("Unmarshal Instance: %v", err)
	}
	if !reflect.DeepEqual(out.Plugins, in.Plugins) {
		t.Errorf("Plugins round-trip: got %v, want %v", out.Plugins, in.Plugins)
	}
}

// TestInstance_PluginsOmittedWhenEmpty asserts the omitempty tag works as
// expected — empty slice produces no "plugins" key, matching the Channels
// convention. Important so existing snapshots (sessions created before this
// release) don't grow a dangling "plugins": [] entry.
func TestInstance_PluginsOmittedWhenEmpty(t *testing.T) {
	in := &Instance{
		ID:    "test-id",
		Title: "test",
		Tool:  "claude",
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal raw: %v", err)
	}
	if _, ok := raw["plugins"]; ok {
		t.Errorf("Instance with empty Plugins must omit the JSON key (omitempty); got keys: %v", keysOf(raw))
	}
}

// TestValidatePluginDef_RejectsUnsafeCharsets locks the security
// invariant that plugin Name and Source go through a strict charset
// filter before reaching exec / filesystem ops / settings.json
// mutations. RFC PLUGIN_ATTACH.md security findings S5/S6/M1 (path
// traversal, lock-path escape, argv injection) all share this root
// cause and this single regex is the consolidated fix.
func TestValidatePluginDef_RejectsUnsafeCharsets(t *testing.T) {
	cases := []struct {
		name      string
		def       PluginDef
		wantError bool
	}{
		// Valid baseline.
		{"valid-marketplace", PluginDef{Name: "octopus", Source: "claude-plugins-official"}, false},
		{"valid-owner-repo", PluginDef{Name: "octopus", Source: "nyldn/claude-octopus"}, false},
		{"valid-with-dots", PluginDef{Name: "tg.fork", Source: "acme/tg.fork-1.2"}, false},
		{"valid-with-dashes", PluginDef{Name: "discord-bot", Source: "user/repo-name"}, false},

		// Path traversal — Source with ".." segments.
		{"traversal-source", PluginDef{Name: "octopus", Source: "../../tmp/evil"}, true},
		{"traversal-source-relative", PluginDef{Name: "octopus", Source: "../etc/passwd"}, true},

		// Path traversal — Name with ".." segments.
		{"traversal-name", PluginDef{Name: "../../tmp/evil", Source: "claude-plugins-official"}, true},

		// Argv injection — leading dash interpreted by claude as a flag.
		{"argv-source-leading-dash", PluginDef{Name: "x", Source: "--config=/tmp/evil"}, true},
		{"argv-name-leading-dash", PluginDef{Name: "--dangerous", Source: "x"}, true},
		{"argv-source-leading-dot", PluginDef{Name: "x", Source: ".evil"}, true},

		// Multiple slashes (only single owner/repo allowed).
		{"too-many-slashes", PluginDef{Name: "x", Source: "owner/repo/extra"}, true},

		// Whitespace, special chars.
		{"whitespace-name", PluginDef{Name: "octo pus", Source: "x"}, true},
		{"null-byte-source", PluginDef{Name: "x", Source: "evil\x00source"}, true},
		{"shell-meta-source", PluginDef{Name: "x", Source: "evil;rm-rf"}, true},
		{"absolute-path-source", PluginDef{Name: "x", Source: "/etc/passwd"}, true},

		// Empty.
		{"empty-name", PluginDef{Name: "", Source: "x"}, true},
		{"empty-source", PluginDef{Name: "x", Source: ""}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validatePluginDef(tc.name, tc.def)
			if tc.wantError && err == nil {
				t.Errorf("validatePluginDef(%+v): expected error, got nil", tc.def)
			}
			if !tc.wantError && err != nil {
				t.Errorf("validatePluginDef(%+v): expected nil, got %v", tc.def, err)
			}
		})
	}
}

// TestGetAvailablePlugins_FiltersInvalidCharsets asserts the public
// accessor drops catalog entries failing validatePluginDef — so unsafe
// values written to config.toml never reach exec, filesystem, or
// settings.json mutation paths.
func TestGetAvailablePlugins_FiltersInvalidCharsets(t *testing.T) {
	home := withTempHome(t)
	writeConfig(t, home, `
[plugins.evil-traversal]
name = "octopus"
source = "../../tmp/evil"
auto_install = true

[plugins.evil-argv]
name = "octopus"
source = "--config=/tmp/owned"

[plugins.legit]
name = "octopus"
source = "nyldn/claude-octopus"
`)
	plugins := GetAvailablePlugins()
	if _, leaked := plugins["evil-traversal"]; leaked {
		t.Errorf("evil-traversal must be filtered out by GetAvailablePlugins; got: %v", plugins)
	}
	if _, leaked := plugins["evil-argv"]; leaked {
		t.Errorf("evil-argv must be filtered out by GetAvailablePlugins; got: %v", plugins)
	}
	if _, ok := plugins["legit"]; !ok {
		t.Errorf("legit entry must pass through validation; got: %v", plugins)
	}
	if def := GetPluginDef("evil-traversal"); def != nil {
		t.Errorf("GetPluginDef must return nil for invalid entry; got %+v", def)
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
