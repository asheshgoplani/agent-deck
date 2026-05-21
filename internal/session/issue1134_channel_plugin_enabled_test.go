// Package session — issue #1134 regression.
//
// agent-deck v1.7.68 introduced a scratch CLAUDE_CONFIG_DIR for
// channel-owning conductors whose ambient profile has
// `enabledPlugins."telegram@claude-plugins-official" = true` (the
// "global antipattern" — issue #941). The scratch's settings.json
// pinned the plugin to *false* on the theory that `--channels` would
// re-activate it as the sole spawn source.
//
// That theory does not match real claude behavior. With the plugin
// disabled in settings.json, claude does not establish the MCP stdio
// transport for the channel server. The bun child either never spawns
// or spawns into task-mode (stdout redirected to a task output file,
// no MCP handshake) and dies in a crash-respawn loop. Net effect: a
// conductor on `--channels plugin:telegram@...` is *deaf* to Telegram
// inbound even though every other component (token, .env, plugin
// install, agent-deck channel record) is correct.
//
// Fix: when a session owns the telegram channel, the scratch
// settings.json must set `enabledPlugins."telegram@claude-plugins-official"
// = true`. `--channels` is a routing/wiring directive — the plugin
// must already be enabled for the wiring to reach a live MCP server.
// Workers (no telegram channel) continue to be pinned false: their
// "denied" path is unchanged.

package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Happy path: a channel-owning conductor with the global-antipattern
// topology (settings.telegram=true in source) gets a scratch dir whose
// settings.json ENABLES telegram. `--channels` then has a live MCP
// transport to wire its channel routing to.
func TestIssue1134_ScratchEnablesTelegramForChannelOwner(t *testing.T) {
	withTelegramConductorPresent(t)
	source := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(source, "settings.json"),
		[]byte(`{"enabledPlugins":{"telegram@claude-plugins-official":true}}`),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", source)

	inst := &Instance{
		ID:       "1134-happy",
		Tool:     "claude",
		Title:    "conductor-1134",
		Channels: []string{"plugin:telegram@claude-plugins-official"},
	}

	scratch, err := inst.EnsureWorkerScratchConfigDir(source)
	if err != nil {
		t.Fatalf("EnsureWorkerScratchConfigDir: %v", err)
	}
	if scratch == "" {
		t.Fatalf("channel-owning conductor with global antipattern MUST receive a scratch (issue #941 trigger)")
	}

	data, err := os.ReadFile(filepath.Join(scratch, "settings.json"))
	if err != nil {
		t.Fatalf("read scratch settings: %v", err)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("parse scratch settings: %v", err)
	}
	plugins, _ := parsed["enabledPlugins"].(map[string]interface{})
	v, ok := plugins[telegramPluginID].(bool)
	if !ok {
		t.Fatalf("scratch settings.json missing %q in enabledPlugins (got map=%v); --channels has nothing to wire", telegramPluginID, plugins)
	}
	if !v {
		t.Fatalf("scratch settings.json has %q=false — this is the issue #1134 root cause; --channels cannot activate a disabled plugin and bun crashes in a respawn loop", telegramPluginID)
	}
}

// Failure mode / regression guard: worker sessions (no telegram
// channel) MUST still get telegram=false. We're only flipping behavior
// for channel-owners; workers continue to be defended against the
// auto-poller leak (issue #941, issue #1133).
func TestIssue1134_WorkerWithoutChannelStillDenied(t *testing.T) {
	withTelegramConductorPresent(t)
	source := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(source, "settings.json"),
		[]byte(`{"enabledPlugins":{"telegram@claude-plugins-official":true}}`),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", source)

	inst := &Instance{
		ID:    "1134-worker",
		Tool:  "claude",
		Title: "background-worker",
		// no Channels — this is a plain worker, not a channel owner
	}

	scratch, err := inst.EnsureWorkerScratchConfigDir(source)
	if err != nil {
		t.Fatalf("EnsureWorkerScratchConfigDir: %v", err)
	}
	if scratch == "" {
		t.Fatal("worker on a host with a telegram conductor MUST get a scratch dir")
	}
	data, _ := os.ReadFile(filepath.Join(scratch, "settings.json"))
	var parsed map[string]interface{}
	_ = json.Unmarshal(data, &parsed)
	plugins, _ := parsed["enabledPlugins"].(map[string]interface{})
	v, ok := plugins[telegramPluginID].(bool)
	if !ok || v {
		t.Fatalf("worker scratch must pin telegram=false; got %v (workers must not auto-spawn a second poller)", plugins[telegramPluginID])
	}
}

// Boundary case: a session with a non-telegram channel (hypothetical
// future plugin, e.g. discord) MUST NOT have telegram enabled in scratch
// — the allow-for-channel-owner rule fires only on telegramChannelPrefix.
// This guards against an over-broad fix that enables telegram for any
// channel-owning session.
func TestIssue1134_NonTelegramChannelDoesNotEnableTelegram(t *testing.T) {
	withTelegramConductorPresent(t)
	source := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(source, "settings.json"),
		[]byte(`{"enabledPlugins":{"telegram@claude-plugins-official":true}}`),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", source)

	inst := &Instance{
		ID:       "1134-other-channel",
		Tool:     "claude",
		Title:    "discord-conductor",
		Channels: []string{"plugin:discord@some-other-marketplace"},
	}

	scratch, err := inst.EnsureWorkerScratchConfigDir(source)
	if err != nil {
		t.Fatalf("EnsureWorkerScratchConfigDir: %v", err)
	}
	// Whether or not a scratch is built for a non-telegram channel
	// owner is governed by other triggers (NeedsWorkerScratchConfigDir
	// fires only on telegram conflict + explicit plugins today). If a
	// scratch IS built — by any future trigger — telegram must remain
	// pinned false because this session does NOT own the telegram
	// channel.
	if scratch == "" {
		return // no scratch built → no settings.json to check; vacuously satisfied
	}
	data, _ := os.ReadFile(filepath.Join(scratch, "settings.json"))
	var parsed map[string]interface{}
	_ = json.Unmarshal(data, &parsed)
	plugins, _ := parsed["enabledPlugins"].(map[string]interface{})
	if v, ok := plugins[telegramPluginID].(bool); ok && v {
		t.Fatalf("scratch enabled telegram=true for a session that does NOT own the telegram channel — over-broad fix; got plugins=%v", plugins)
	}
}
