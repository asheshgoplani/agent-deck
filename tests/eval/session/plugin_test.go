//go:build eval_smoke

// Behavioral eval for the `--plugin <name>` CLI flag and its
// downstream effects on Instance.Plugins / Channels persistence
// (RFC docs/rfc/PLUGIN_ATTACH.md). Mandate: CLAUDE.md:81-108
// requires eval coverage for any user-observable behavior change
// that pure Go tests cannot structurally express.
//
// The unit tests in internal/session/plugins_catalog_test.go and
// cmd/agent-deck/plugin_cli_test.go exercise the in-process state
// machinery. This eval drives the actual `ad-fork` binary against
// a sandbox HOME with a real config.toml and a real state.db, then
// inspects `session show --json` and the on-disk catalog to confirm:
//
//  1. `agent-deck add --plugin <name>` populates Instance.Plugins
//     in state.db (round-trip through the JSON serializer).
//  2. emits_channel=true catalog entries auto-link the channel into
//     Instance.Channels at creation time.
//  3. `--no-channel-link` suppresses the auto-link AND persists the
//     PluginChannelLinkDisabled flag (regression: previously the flag
//     was lost on Marshal/Unmarshal — RFC §4.7).
//  4. The catalog filters the v1-refused telegram-official entry, so
//     the CLI emits a sorted Available list that excludes it.
//
// What this DOESN'T cover (out of scope for this eval — covered by
// other layers):
//
//   - The actual scratch settings.json mutation under spawn — that
//     requires a stub claude binary and is exercised by the unit
//     tests in worker_scratch_plugins_test.go and the persistence
//     test TestPersistence_PluginsSurviveRestart.
//   - The Plugin Manager TUI dialog — covered by
//     internal/ui/plugin_dialog_test.go.

package session_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/tests/eval/harness"
)

// TestEval_PluginCLI_AddFlagPersistsAndAutoLinks is the core eval for
// the CLI flag plumbing. Drives a real `ad-fork add --plugin ... -c claude`
// against a sandbox, then asserts the JSON output of `session show`
// matches the expected schema.
func TestEval_PluginCLI_AddFlagPersistsAndAutoLinks(t *testing.T) {
	sb := harness.NewSandbox(t)

	// Catalog with two plugins (no auto_install — keeps the eval
	// hermetic, no shell-out to `claude plugin install`).
	catalog := `
[plugins.octopus]
name = "octopus"
source = "nyldn/claude-octopus"
emits_channel = false
auto_install = false

[plugins.discord]
name = "discord"
source = "claude-plugins-official"
emits_channel = true
auto_install = false

[plugins.tg-official]
name = "telegram"
source = "claude-plugins-official"
auto_install = false
`
	configPath := filepath.Join(sb.Home, ".agent-deck", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatalf("mkdir agent-deck: %v", err)
	}
	if err := os.WriteFile(configPath, []byte(catalog), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	workDir := filepath.Join(sb.Home, "project")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}

	// Subtest 1: octopus — no auto-link (emits_channel=false).
	t.Run("octopus_no_autolink", func(t *testing.T) {
		runBin(t, sb, "add", "-c", "claude", "-t", "octo-eval", "--plugin", "octopus", workDir)
		json := readSessionShowJSON(t, sb, "octo-eval")
		assertStringSlice(t, json, "plugins", []string{"octopus"})
		if ch, ok := json["channels"]; ok && ch != nil {
			t.Errorf("octopus has emits_channel=false, channels must be nil/absent; got %v", ch)
		}
	})

	// Subtest 2: discord — auto-link discord channel.
	t.Run("discord_autolinks_channel", func(t *testing.T) {
		runBin(t, sb, "add", "-c", "claude", "-t", "disc-eval", "--plugin", "discord", workDir)
		json := readSessionShowJSON(t, sb, "disc-eval")
		assertStringSlice(t, json, "plugins", []string{"discord"})
		assertStringSliceContains(t, json, "channels", "plugin:discord@claude-plugins-official")
	})

	// Subtest 3: --no-channel-link — flag persists in state.db (RFC §4.7
	// regression: previously dropped during Marshal/Unmarshal).
	t.Run("no_channel_link_persists", func(t *testing.T) {
		runBin(t, sb, "add", "-c", "claude", "-t", "nclnk-eval",
			"--plugin", "discord", "--no-channel-link", workDir)
		json := readSessionShowJSON(t, sb, "nclnk-eval")
		if v, ok := json["plugin_channel_link_disabled"].(bool); !ok || !v {
			t.Errorf("plugin_channel_link_disabled must persist as true; got %v", json["plugin_channel_link_disabled"])
		}
		if ch, ok := json["channels"]; ok && ch != nil {
			t.Errorf("--no-channel-link must suppress auto-link; got channels=%v", ch)
		}
	})

	// Subtest 4: telegram-official refusal at CLI layer (RFC §6).
	t.Run("telegram_official_refused_at_cli", func(t *testing.T) {
		out, err := runBinTry(sb, "add", "-c", "claude", "-t", "tg-eval",
			"--plugin", "telegram@claude-plugins-official", workDir)
		if err == nil {
			t.Fatalf("--plugin telegram@claude-plugins-official must error; got success: %s", out)
		}
		if !strings.Contains(out, "refused in v1") {
			t.Errorf("error message must mention refusal; got: %s", out)
		}
		if !strings.Contains(out, "channel") {
			t.Errorf("error message must point at --channel as alternative; got: %s", out)
		}
	})

	// Subtest 5: catalog filters tg-official from `plugin list`.
	t.Run("catalog_filters_tg_official", func(t *testing.T) {
		out, err := runBinTry(sb, "plugin", "list", "--json")
		if err != nil {
			t.Fatalf("plugin list --json: %v\nout: %s", err, out)
		}
		jsonStart := strings.Index(out, "{")
		if jsonStart < 0 {
			t.Fatalf("no JSON in plugin list output:\n%s", out)
		}
		var parsed struct {
			Plugins []map[string]interface{} `json:"plugins"`
		}
		if err := json.Unmarshal([]byte(out[jsonStart:]), &parsed); err != nil {
			t.Fatalf("parse plugin list JSON: %v\nraw: %s", err, out)
		}
		for _, p := range parsed.Plugins {
			name, _ := p["plugin_name"].(string)
			source, _ := p["source"].(string)
			if name == "telegram" && source == "claude-plugins-official" {
				t.Errorf("tg-official must be filtered from `plugin list`; got entry: %v", p)
			}
		}
	})
}

// readSessionShowJSON runs `ad-fork session show <id> --json` and
// returns the parsed top-level object.
//
// runBinTry uses CombinedOutput which mixes stderr (where the tmux 3.6
// heads-up warning is printed) into stdout. We compensate by extracting
// the first JSON object from the captured bytes — anything before the
// opening `{` is non-JSON noise (warnings/banner) and is discarded.
func readSessionShowJSON(t *testing.T, sb *harness.Sandbox, identifier string) map[string]interface{} {
	t.Helper()
	out, err := runBinTry(sb, "session", "show", identifier, "--json")
	if err != nil {
		t.Fatalf("session show %s --json: %v\nout: %s", identifier, err, out)
	}
	jsonStart := strings.Index(out, "{")
	if jsonStart < 0 {
		t.Fatalf("no JSON object in output:\n%s", out)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(out[jsonStart:]), &parsed); err != nil {
		t.Fatalf("parse session show JSON: %v\nraw: %s", err, out)
	}
	return parsed
}

// assertStringSlice asserts the JSON field at key is a []string equal
// to want.
func assertStringSlice(t *testing.T, m map[string]interface{}, key string, want []string) {
	t.Helper()
	raw, ok := m[key]
	if !ok {
		t.Fatalf("JSON missing key %q; got keys: %v", key, mapKeys(m))
	}
	arr, ok := raw.([]interface{})
	if !ok {
		t.Fatalf("JSON %q is %T, want []interface{}; got %v", key, raw, raw)
	}
	if len(arr) != len(want) {
		t.Fatalf("JSON %q length: got %d (%v), want %d (%v)", key, len(arr), arr, len(want), want)
	}
	for i, w := range want {
		got, _ := arr[i].(string)
		if got != w {
			t.Errorf("JSON %q[%d]: got %q, want %q", key, i, got, w)
		}
	}
}

// assertStringSliceContains asserts the JSON field at key contains the
// given string (order-independent).
func assertStringSliceContains(t *testing.T, m map[string]interface{}, key, want string) {
	t.Helper()
	raw, ok := m[key]
	if !ok {
		t.Fatalf("JSON missing key %q", key)
	}
	arr, _ := raw.([]interface{})
	for _, v := range arr {
		if s, _ := v.(string); s == want {
			return
		}
	}
	t.Errorf("JSON %q does not contain %q; got %v", key, want, arr)
}

func mapKeys(m map[string]interface{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
