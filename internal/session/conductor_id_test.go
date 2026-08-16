package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeUserConfig writes config.toml under the test's XDG config home and
// resets the user-config cache so the next LoadUserConfig reads it fresh.
func writeUserConfig(t *testing.T, xdgConfigHome, contents string) {
	t.Helper()
	cfgDir := filepath.Join(xdgConfigHome, "agent-deck")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", cfgDir, err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile(config.toml): %v", err)
	}
	ClearUserConfigCache()
}

// readUserConfigFile returns the raw on-disk config.toml, for the round-trip
// tests that assert on the textual representation rather than the decoded value.
func readUserConfigFile(t *testing.T, xdgConfigHome string) string {
	t.Helper()
	path := filepath.Join(xdgConfigHome, "agent-deck", "config.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	return string(data)
}

// TestLoadUserConfig_NonNumericConductorID is the regression guard for the bug
// where a single non-numeric conductor bot ID in config.toml poisoned the whole
// TOML parse. Because user_id/guild_id/channel_id were plain int64 fields, a
// value like `user_id = "not-a-number"` made toml.DecodeFile fail, which made
// LoadUserConfig error out and took down completely unrelated commands (the
// config here also declares a remote, mirroring how `remote list` broke).
//
// After the fix these IDs decode tolerantly: the text is preserved and reads as
// unusable (the bridge treats it as unset) instead of failing the load, so
// unrelated config (the remote) still parses.
func TestLoadUserConfig_NonNumericConductorID(t *testing.T) {
	_, xdgConfigHome, _ := setupSessionXDGPathEnv(t)

	const cfg = `
[remotes.example]
host = "user@host"

[conductor.telegram]
token = "dummy-token"
user_id = "not-a-number"

[conductor.discord]
bot_token = "dummy-token"
guild_id = "also-not-a-number"
channel_id = "nope"
user_id = "still-not-numeric"
`
	writeUserConfig(t, xdgConfigHome, cfg)

	config, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig must not fail on a non-numeric conductor ID: %v", err)
	}
	if _, ok := config.Remotes["example"]; !ok {
		t.Fatalf("unrelated [remotes.example] must still parse; got remotes: %v", config.Remotes)
	}

	for _, tc := range []struct {
		field string
		got   ConductorID
		want  ConductorID
	}{
		{"telegram user_id", config.Conductor.Telegram.UserID, "not-a-number"},
		{"discord guild_id", config.Conductor.Discord.GuildID, "also-not-a-number"},
		{"discord channel_id", config.Conductor.Discord.ChannelID, "nope"},
		{"discord user_id", config.Conductor.Discord.UserID, "still-not-numeric"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s: want text preserved as %q, got %q", tc.field, tc.want, tc.got)
		}
		if n, ok := tc.got.Int64(); ok {
			t.Errorf("%s: malformed value must not read as a usable number; got %d", tc.field, n)
		}
	}
}

// TestLoadUserConfig_ConductorIDForms verifies the tolerant decoder still
// accepts the well-formed shapes: a bare TOML integer and a quoted numeric
// string (which users routinely write for these large IDs). Both must yield the
// same usable number at the consumer boundary.
func TestLoadUserConfig_ConductorIDForms(t *testing.T) {
	_, xdgConfigHome, _ := setupSessionXDGPathEnv(t)

	const cfg = `
[conductor.telegram]
token = "dummy-token"
user_id = 12345

[conductor.discord]
bot_token = "dummy-token"
guild_id = "67890"
channel_id = 24680
user_id = "13579"
`
	writeUserConfig(t, xdgConfigHome, cfg)

	config, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}

	for _, tc := range []struct {
		field string
		got   ConductorID
		want  int64
	}{
		{"telegram user_id (bare int)", config.Conductor.Telegram.UserID, 12345},
		{"discord guild_id (quoted numeric)", config.Conductor.Discord.GuildID, 67890},
		{"discord channel_id (bare int)", config.Conductor.Discord.ChannelID, 24680},
		{"discord user_id (quoted numeric)", config.Conductor.Discord.UserID, 13579},
	} {
		n, ok := tc.got.Int64()
		if !ok {
			t.Errorf("%s: want a usable number, got unusable text %q", tc.field, tc.got)
			continue
		}
		if n != tc.want {
			t.Errorf("%s: want %d, got %d", tc.field, tc.want, n)
		}
	}
}

// TestConductorID_RoundTripPreservesEnvRefs is round-trip requirement 1: the
// bridge resolves telegram.user_id / discord.user_id through _resolve_secret, so
// "$TELEGRAM_USER_ID" and "${DISCORD_USER_ID}" are supported values. Under the
// old int64 field they normalized to 0 in memory, and because the field is
// omitempty the next unrelated SaveUserConfig DELETED the line from config.toml.
// Load → save → reload must leave both references untouched.
func TestConductorID_RoundTripPreservesEnvRefs(t *testing.T) {
	_, xdgConfigHome, _ := setupSessionXDGPathEnv(t)

	const cfg = `
[conductor.telegram]
token = "dummy-token"
user_id = "$TELEGRAM_USER_ID"

[conductor.discord]
bot_token = "dummy-token"
guild_id = 111
channel_id = 222
user_id = "${DISCORD_USER_ID}"
`
	writeUserConfig(t, xdgConfigHome, cfg)

	config, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	if got := config.Conductor.Telegram.UserID; got != "$TELEGRAM_USER_ID" {
		t.Fatalf("telegram user_id after load: want %q, got %q", "$TELEGRAM_USER_ID", got)
	}
	if got := config.Conductor.Discord.UserID; got != "${DISCORD_USER_ID}" {
		t.Fatalf("discord user_id after load: want %q, got %q", "${DISCORD_USER_ID}", got)
	}

	if err := SaveUserConfig(config); err != nil {
		t.Fatalf("SaveUserConfig: %v", err)
	}

	onDisk := readUserConfigFile(t, xdgConfigHome)
	for _, want := range []string{`user_id = "$TELEGRAM_USER_ID"`, `user_id = "${DISCORD_USER_ID}"`} {
		if !strings.Contains(onDisk, want) {
			t.Errorf("saved config.toml must still contain %s; got:\n%s", want, onDisk)
		}
	}

	reloaded, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig after save: %v", err)
	}
	if got := reloaded.Conductor.Telegram.UserID; got != "$TELEGRAM_USER_ID" {
		t.Errorf("telegram user_id after reload: want %q, got %q", "$TELEGRAM_USER_ID", got)
	}
	if got := reloaded.Conductor.Discord.UserID; got != "${DISCORD_USER_ID}" {
		t.Errorf("discord user_id after reload: want %q, got %q", "${DISCORD_USER_ID}", got)
	}
}

// TestConductorID_RoundTripPreservesNumericForms is round-trip requirement 2:
// a quoted numeric ID and a bare numeric ID keep the same usable value across
// load → save → reload. The bare form must also stay bare on disk, so upgrading
// does not rewrite every existing config.
//
// A quoted numeric is the one shape that is normalized rather than preserved: a
// text-backed ID cannot carry "was quoted in the source" alongside its value, so
// MarshalTOML emits the canonical integer form. The value is unchanged and the
// bridge coerces both shapes with int(), so nothing is lost.
func TestConductorID_RoundTripPreservesNumericForms(t *testing.T) {
	_, xdgConfigHome, _ := setupSessionXDGPathEnv(t)

	const cfg = `
[conductor.telegram]
token = "dummy-token"
user_id = 12345

[conductor.discord]
bot_token = "dummy-token"
guild_id = "67890"
channel_id = 24680
user_id = "13579"
`
	writeUserConfig(t, xdgConfigHome, cfg)

	config, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	if err := SaveUserConfig(config); err != nil {
		t.Fatalf("SaveUserConfig: %v", err)
	}

	onDisk := readUserConfigFile(t, xdgConfigHome)
	if !strings.Contains(onDisk, "user_id = 12345") {
		t.Errorf("a bare-integer telegram user_id must be re-emitted bare; got:\n%s", onDisk)
	}
	if !strings.Contains(onDisk, "channel_id = 24680") {
		t.Errorf("a bare-integer discord channel_id must be re-emitted bare; got:\n%s", onDisk)
	}
	if !strings.Contains(onDisk, "guild_id = 67890") {
		t.Errorf("a quoted-numeric discord guild_id must keep its value, normalized to the canonical integer form; got:\n%s", onDisk)
	}

	reloaded, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig after save: %v", err)
	}
	for _, tc := range []struct {
		field string
		got   ConductorID
		want  int64
	}{
		{"telegram user_id", reloaded.Conductor.Telegram.UserID, 12345},
		{"discord guild_id", reloaded.Conductor.Discord.GuildID, 67890},
		{"discord channel_id", reloaded.Conductor.Discord.ChannelID, 24680},
		{"discord user_id", reloaded.Conductor.Discord.UserID, 13579},
	} {
		n, ok := tc.got.Int64()
		if !ok {
			t.Errorf("%s: want a usable number after round-trip, got %q", tc.field, tc.got)
			continue
		}
		if n != tc.want {
			t.Errorf("%s: want %d after round-trip, got %d", tc.field, tc.want, n)
		}
	}
}

// TestConductorID_SaveDoesNotRewriteMalformedValue is round-trip requirement 3:
// a malformed ID sitting alongside unrelated config must survive a save that was
// triggered by an edit elsewhere. Under the old int64 field the value normalized
// to 0, and since these fields are omitempty the encoder dropped the key
// entirely — the operator's line disappeared from disk without a word.
func TestConductorID_SaveDoesNotRewriteMalformedValue(t *testing.T) {
	_, xdgConfigHome, _ := setupSessionXDGPathEnv(t)

	const cfg = `
[remotes.example]
host = "user@host"

[conductor.telegram]
token = "dummy-token"
user_id = "not-a-number"
`
	writeUserConfig(t, xdgConfigHome, cfg)

	config, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}

	// The save is provoked by an edit to an unrelated section, the way a normal
	// settings write would be.
	config.Remotes["other"] = RemoteConfig{Host: "user@other"}
	if err := SaveUserConfig(config); err != nil {
		t.Fatalf("SaveUserConfig: %v", err)
	}

	onDisk := readUserConfigFile(t, xdgConfigHome)
	if !strings.Contains(onDisk, `user_id = "not-a-number"`) {
		t.Errorf("the malformed telegram user_id must be preserved verbatim on save; got:\n%s", onDisk)
	}
	if strings.Contains(onDisk, "user_id = 0") {
		t.Errorf("the malformed telegram user_id must not be rewritten to 0; got:\n%s", onDisk)
	}

	reloaded, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig after save: %v", err)
	}
	if got := reloaded.Conductor.Telegram.UserID; got != "not-a-number" {
		t.Errorf("telegram user_id after reload: want %q, got %q", "not-a-number", got)
	}
	if _, ok := reloaded.Remotes["other"]; !ok {
		t.Errorf("the unrelated edit that provoked the save must be persisted; got remotes: %v", reloaded.Remotes)
	}
	if _, ok := reloaded.Remotes["example"]; !ok {
		t.Errorf("the pre-existing remote must survive the save; got remotes: %v", reloaded.Remotes)
	}
}

// TestConductorID_WarnsOnUnusableValue covers the warning contract, including
// the empty-string case raised in review: `user_id = ""` is treated as unset
// like a malformed value is, so it warns the same way rather than being
// silently accepted. A supported env-var reference must NOT warn — it is a
// documented value shape, not an operator mistake.
func TestConductorID_WarnsOnUnusableValue(t *testing.T) {
	for _, tc := range []struct {
		name     string
		value    string
		wantWarn string
	}{
		{"empty string", `user_id = ""`, "conductor_id_empty"},
		{"malformed", `user_id = "not-a-number"`, "conductor_id_unparseable"},
		{"wrong TOML type", `user_id = 1.5`, "conductor_id_unexpected_type"},
		{"env-var reference", `user_id = "$TELEGRAM_USER_ID"`, ""},
		{"bare integer", `user_id = 12345`, ""},
		{"quoted numeric", `user_id = "12345"`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, xdgConfigHome, _ := setupSessionXDGPathEnv(t)
			logs := captureSessionLog(t)

			writeUserConfig(t, xdgConfigHome, "[conductor.telegram]\ntoken = \"dummy-token\"\n"+tc.value+"\n")
			if _, err := LoadUserConfig(); err != nil {
				t.Fatalf("LoadUserConfig(%s): %v", tc.value, err)
			}

			got := logs.String()
			if tc.wantWarn == "" {
				if strings.Contains(got, "conductor_id_") {
					t.Errorf("%s must not warn; got log output:\n%s", tc.value, got)
				}
				return
			}
			if !strings.Contains(got, tc.wantWarn) {
				t.Errorf("%s must warn with %q; got log output:\n%s", tc.value, tc.wantWarn, got)
			}
		})
	}
}

// TestConductorID_MarshalTOMLEmission pins the encoder side directly. The bare
// form is only safe when the text is exactly what the TOML encoder would print
// for that integer: `user_id = 0123` is not valid TOML, so a leading-zero value
// must be quoted or the saved config would no longer parse.
func TestConductorID_MarshalTOMLEmission(t *testing.T) {
	for _, tc := range []struct {
		id   ConductorID
		want string
	}{
		{"12345", "12345"},
		{"-12345", "-12345"},
		{"0", "0"},
		{"0123", `"0123"`},
		{"+123", `"+123"`},
		{" 123 ", `" 123 "`},
		{"1_23", `"1_23"`},
		{"$TELEGRAM_USER_ID", `"$TELEGRAM_USER_ID"`},
		{"keychain:telegram-user-id", `"keychain:telegram-user-id"`},
		{`quote"and\slash`, `"quote\"and\\slash"`},
		{"9223372036854775808", `"9223372036854775808"`}, // overflows int64
	} {
		got, err := tc.id.MarshalTOML()
		if err != nil {
			t.Errorf("MarshalTOML(%q): %v", tc.id, err)
			continue
		}
		if string(got) != tc.want {
			t.Errorf("MarshalTOML(%q): want %s, got %s", tc.id, tc.want, got)
		}
	}
}
