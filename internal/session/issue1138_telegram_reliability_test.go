// Package session — issue #1138 regression suite for recurring telegram drops.
//
// Background. PR #1136 (issue #1134) made the channel-owning scratch
// settings.json set `enabledPlugins."telegram@claude-plugins-official"=true`,
// so `--channels` would have a live MCP transport to wire onto. That fix
// closed the issue on hosts whose ambient profile already carried the
// "global antipattern" (settings.telegram=true). But the maintainer kept
// observing the same drop pattern every few hours:
//
//	Bot reachable via Telegram API ✅
//	claude process running --channels plugin:telegram@…       ✅
//	bun-telegram plugin process: MISSING ❌
//	Scratch settings.json: enabledPlugins["telegram@…"] is
//	  either `false` OR absent ❌
//
// Root cause (multi-vector): scratch creation is gated on three signals
// (telegramStateDirStrip, explicit plugins, global antipattern). For the
// recommended post-#941 topology — channel-owning conductor with
// global enablement DISABLED, no extra plugins — none of those signals
// fire. NeedsWorkerScratchConfigDir returns false, no scratch is created,
// and the conductor depends entirely on the ambient settings.json carrying
// `telegram=true`. Any drift in the ambient (manual edit, `/plugin disable`,
// Claude Code rewrite) silently disables the channel transport, and on
// restart there is no force-correct pass to heal it.
//
// Fix. A channel-owning session ALWAYS needs the scratch indirection so
// agent-deck owns the enablement of its own channel plugin. The scratch
// settings.json is rewritten on every spawn (idempotent, force-correct).
// A post-spawn warning fires when --channels references a plugin that
// the effective settings.json does not enable, surfacing drift loudly.
//
// All 4 tests in this file MUST fail on the pre-fix tree and pass after
// the fix lands. Grep tag: issue1138, telegram-reliability.

package session

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// channelOwnerInstance constructs the canonical channel-owning conductor
// instance used by every test in this file. Centralising it keeps every
// case asserting the SAME minimal topology so we can't accidentally
// regress one case by tweaking another.
func channelOwnerInstance(id string) *Instance {
	return &Instance{
		ID:       id,
		Tool:     "claude",
		Title:    "conductor-personal",
		Channels: []string{"plugin:telegram@claude-plugins-official"},
	}
}

// readScratchTelegramState returns (present, value) for
// enabledPlugins."telegram@claude-plugins-official" in the given scratch
// settings.json. Both `false` and absence are bugs — distinguish them so
// the failure message can name which variant fired.
func readScratchTelegramState(t *testing.T, scratchDir string) (present bool, enabled bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(scratchDir, "settings.json"))
	if err != nil {
		t.Fatalf("read scratch settings.json: %v", err)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("parse scratch settings.json: %v", err)
	}
	plugins, _ := parsed["enabledPlugins"].(map[string]interface{})
	v, ok := plugins[telegramPluginID].(bool)
	return ok, v
}

// TestIssue1138_ScratchAlwaysCreatedForChannelOwner is the headline
// regression: a channel-owning conductor session must ALWAYS receive a
// scratch CLAUDE_CONFIG_DIR even when no other trigger fires. Without
// this gate, the ambient settings.json is the only source of plugin
// enablement, and any drift there silently disables the channel
// transport.
//
// Pre-fix: NeedsWorkerScratchConfigDir returns false on a host without
// a TG conductor (hostHasTelegramConductor=false), no global antipattern,
// no explicit plugins — even though Channels carries the telegram channel.
// Post-fix: needsScratchForTelegramChannelOwner fires, scratch is built,
// and computeChannelPluginAllowList pins telegram=true.
func TestIssue1138_ScratchAlwaysCreatedForChannelOwner(t *testing.T) {
	orig := hostHasTelegramConductor
	hostHasTelegramConductor = func() bool { return false }
	t.Cleanup(func() { hostHasTelegramConductor = orig })

	home := t.TempDir()
	t.Setenv("HOME", home)

	// Ambient profile: telegram NOT in enabledPlugins (the post-#941
	// recommended state). Without a scratch, --channels would have
	// nothing to wire to.
	source := filepath.Join(home, ".claude")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "settings.json"),
		[]byte(`{"enabledPlugins":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", source)

	inst := channelOwnerInstance("1138-always-create")

	if !inst.NeedsWorkerScratchConfigDir() {
		t.Fatalf("channel-owning conductor must always need a scratch (issue #1138); got NeedsWorkerScratchConfigDir=false")
	}

	scratch, err := inst.EnsureWorkerScratchConfigDir(source)
	if err != nil {
		t.Fatalf("EnsureWorkerScratchConfigDir: %v", err)
	}
	if scratch == "" {
		t.Fatalf("channel-owning conductor must receive a scratch even on a clean host; got empty")
	}

	present, enabled := readScratchTelegramState(t, scratch)
	if !present {
		t.Fatalf("scratch settings.json missing %q in enabledPlugins — --channels has nothing to wire (issue #1138 absent-variant)", telegramPluginID)
	}
	if !enabled {
		t.Fatalf("scratch settings.json has %q=false — --channels cannot activate a disabled plugin (issue #1138 false-variant)", telegramPluginID)
	}
}

// TestIssue1138_RestartHealsDriftedScratchSettings asserts the
// force-correct invariant: if the scratch settings.json drifts (manual
// edit, external rewrite, Claude Code's own /plugin disable), the next
// call to EnsureWorkerScratchConfigDir MUST overwrite it back to
// telegram=true. Idempotency is the contract — `EnsureWorkerScratchConfigDir`
// is the canonical heal point and must not assume prior state is sane.
func TestIssue1138_RestartHealsDriftedScratchSettings(t *testing.T) {
	orig := hostHasTelegramConductor
	hostHasTelegramConductor = func() bool { return false }
	t.Cleanup(func() { hostHasTelegramConductor = orig })

	home := t.TempDir()
	t.Setenv("HOME", home)
	source := filepath.Join(home, ".claude")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "settings.json"),
		[]byte(`{"enabledPlugins":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", source)

	inst := channelOwnerInstance("1138-heal")

	scratch, err := inst.EnsureWorkerScratchConfigDir(source)
	if err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	if scratch == "" {
		t.Fatalf("scratch must be created for channel owner (issue #1138)")
	}

	// Simulate drift: a 3rd party (or Claude Code itself) rewrites the
	// scratch settings.json with telegram pinned off.
	driftPath := filepath.Join(scratch, "settings.json")
	if err := os.WriteFile(driftPath,
		[]byte(`{"enabledPlugins":{"telegram@claude-plugins-official":false}}`),
		0o600); err != nil {
		t.Fatalf("inject drift: %v", err)
	}

	// Restart path: prepare ⇒ ensure. Force-correct must heal the drift.
	scratch2, err := inst.EnsureWorkerScratchConfigDir(source)
	if err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if scratch2 != scratch {
		t.Fatalf("scratch dir must be stable across restarts; got %q vs %q", scratch2, scratch)
	}

	present, enabled := readScratchTelegramState(t, scratch2)
	if !present || !enabled {
		t.Fatalf("restart must heal drifted scratch settings.json back to telegram=true; got present=%v enabled=%v", present, enabled)
	}
}

// TestIssue1138_HealsAbsentEntry covers the "completely absent" variant
// of the bug: a scratch settings.json that has the enabledPlugins block
// but no entry for telegram at all. Force-correct must inject it.
func TestIssue1138_HealsAbsentEntry(t *testing.T) {
	orig := hostHasTelegramConductor
	hostHasTelegramConductor = func() bool { return false }
	t.Cleanup(func() { hostHasTelegramConductor = orig })

	home := t.TempDir()
	t.Setenv("HOME", home)
	source := filepath.Join(home, ".claude")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "settings.json"),
		[]byte(`{"enabledPlugins":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", source)

	inst := channelOwnerInstance("1138-absent")

	scratch, err := inst.EnsureWorkerScratchConfigDir(source)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}

	// Drift: enabledPlugins block exists but telegram entry is absent.
	driftPath := filepath.Join(scratch, "settings.json")
	if err := os.WriteFile(driftPath,
		[]byte(`{"enabledPlugins":{"superpowers@claude-plugins-official":true}}`),
		0o600); err != nil {
		t.Fatalf("inject absent drift: %v", err)
	}

	// Restart heals.
	if _, err := inst.EnsureWorkerScratchConfigDir(source); err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	present, enabled := readScratchTelegramState(t, scratch)
	if !present {
		t.Fatalf("issue #1138 absent-variant: scratch must have explicit %q entry after restart; got absent", telegramPluginID)
	}
	if !enabled {
		t.Fatalf("issue #1138 absent-variant: scratch entry must be true; got false")
	}
}

// TestIssue1138_PostSpawnDriftWarning asserts the new diagnostic: when a
// session has `--channels plugin:telegram@…` but the effective
// settings.json (after scratch resolution) does NOT enable the plugin,
// agent-deck must log a CLEAR, structured warning so the user sees the
// silent failure mode. Without this, the drop is invisible until the
// user happens to send a telegram message and notice no response.
//
// The warning fires via VerifyTelegramChannelEnabled(configDir, channels)
// which inspects settings.json and returns a structured result. The
// session prepare path logs at WARN level when the check fails.
func TestIssue1138_PostSpawnDriftWarning(t *testing.T) {
	// Capture the session logger output via a buffer-backed handler so
	// we can assert on what was emitted.
	var buf bytes.Buffer
	origLog := sessionLog
	sessionLog = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	t.Cleanup(func() { sessionLog = origLog })

	// Setup: a config dir whose settings.json says telegram=false. This
	// is the silent-failure topology the warning must surface.
	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "settings.json"),
		[]byte(`{"enabledPlugins":{"telegram@claude-plugins-official":false}}`),
		0o600); err != nil {
		t.Fatal(err)
	}

	channels := []string{"plugin:telegram@claude-plugins-official"}
	result := VerifyTelegramChannelEnabled(configDir, channels)
	if result.OK {
		t.Fatalf("VerifyTelegramChannelEnabled must report not-OK when channel set but plugin=false; got OK")
	}
	if result.Reason == "" {
		t.Fatalf("VerifyTelegramChannelEnabled must return a non-empty Reason; got empty")
	}

	// Now exercise the diagnostic emitter — must log at WARN with a
	// stable, greppable code so operators / monitoring can detect it.
	EmitTelegramChannelDriftWarning("conductor-personal", "instance-id-x", configDir, channels, result)

	got := buf.String()
	if !strings.Contains(got, "telegram_channel_plugin_drift") {
		t.Fatalf("warning must use the stable code %q for grep/alerting; got %s", "telegram_channel_plugin_drift", got)
	}
	if !strings.Contains(got, "level=WARN") {
		t.Fatalf("warning must be at WARN level; got %s", got)
	}
	if !strings.Contains(got, telegramPluginID) {
		t.Fatalf("warning must name the plugin id (%s); got %s", telegramPluginID, got)
	}
	if !strings.Contains(got, "instance-id-x") {
		t.Fatalf("warning must name the instance id for triage; got %s", got)
	}
}
