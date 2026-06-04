// Package session — tests for the worker-scratch CLAUDE_CONFIG_DIR
// mechanism introduced in v1.7.68 to shut down the v1.7.40 regression
// root cause (issue #59 — rogue bun telegram pollers on the
// maintainer's host, 2026-04-22).
//
// v1.7.40 stripped TELEGRAM_STATE_DIR from child spawn env. The plugin
// then fell back to its *default* state dir (`~/.claude/channels/
// telegram/`) which is the conductor's real token dir. So every worker
// still spawned a second poller on the same bot token, generating the
// 409-Conflict storm that dropped messages.
//
// Fix A: for non-conductor claude workers, prepare an ephemeral scratch
// CLAUDE_CONFIG_DIR whose settings.json has the telegram plugin
// explicitly disabled. The rest of the profile is symlinked through so
// auth, commands, agents, other plugins keep working.
//
// Conductors and sessions that explicitly own a `plugin:telegram@...`
// channel keep the ambient profile — they are the legitimate bot
// owners.

package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withTelegramConductorPresent forces the host-conductor gate to
// return true for the duration of the test. Issue #759 narrowed
// `NeedsWorkerScratchConfigDir` to additionally require an active
// Telegram conductor; the existing scratch-dir invariants below
// pre-date that gate and exercise the dir's content/path properties
// in isolation, so they short-circuit the gate via this seam.
func withTelegramConductorPresent(t *testing.T) {
	t.Helper()
	orig := hostHasTelegramConductor
	hostHasTelegramConductor = func() bool { return true }
	t.Cleanup(func() { hostHasTelegramConductor = orig })
}

// A non-conductor claude session (title does not start with "conductor-",
// no telegram channel) MUST receive a scratch CLAUDE_CONFIG_DIR that:
//   - is a distinct directory from the source profile
//   - contains a settings.json with telegram plugin explicitly disabled
//     (enabledPlugins."telegram@claude-plugins-official" = false)
//   - preserves other enabled plugins
//   - makes the rest of the profile reachable (via symlink)
func TestEnsureWorkerScratchConfigDir_DisablesTelegramPlugin(t *testing.T) {
	withTelegramConductorPresent(t)
	source := t.TempDir()
	srcSettings := `{"enabledPlugins":{"telegram@claude-plugins-official":true,"superpowers@claude-plugins-official":true}}`
	if err := os.WriteFile(filepath.Join(source, "settings.json"), []byte(srcSettings), 0o644); err != nil {
		t.Fatal(err)
	}
	// Non-settings entry that must remain reachable through scratch
	// (profile customisations: commands, agents, plugins cache, etc.)
	commandsDir := filepath.Join(source, "commands")
	if err := os.MkdirAll(commandsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(commandsDir, "hi.md"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg-data"))

	inst := &Instance{
		ID:    "00000000-0000-0000-0000-000000000001",
		Tool:  "claude",
		Title: "my-worker",
	}

	got, err := inst.EnsureWorkerScratchConfigDir(source)
	if err != nil {
		t.Fatalf("EnsureWorkerScratchConfigDir: %v", err)
	}
	if got == "" {
		t.Fatal("expected non-empty scratch dir for a non-conductor claude worker")
	}
	if got == source {
		t.Fatalf("scratch dir must be distinct from source profile; got=%q source=%q", got, source)
	}

	// Scratch settings.json must disable telegram plugin while preserving
	// other plugin flags.
	data, err := os.ReadFile(filepath.Join(got, "settings.json"))
	if err != nil {
		t.Fatalf("read scratch settings.json: %v", err)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("parse scratch settings.json: %v", err)
	}
	plugins, _ := parsed["enabledPlugins"].(map[string]interface{})
	if plugins == nil {
		t.Fatalf("scratch settings.json missing enabledPlugins block: %s", string(data))
	}
	if v, ok := plugins["telegram@claude-plugins-official"]; !ok || v != false {
		t.Errorf("telegram plugin must be explicitly disabled in scratch; got %v", plugins)
	}
	if v, ok := plugins["superpowers@claude-plugins-official"]; !ok || v != true {
		t.Errorf("non-telegram plugins must be preserved; got %v", plugins)
	}

	// commands/hi.md must be reachable through the scratch dir (symlink
	// or direct copy — either is fine, the assertion is on reachability).
	commandsContent, err := os.ReadFile(filepath.Join(got, "commands", "hi.md"))
	if err != nil {
		t.Errorf("commands dir must be reachable via scratch dir: %v", err)
	} else if string(commandsContent) != "hi" {
		t.Errorf("commands content mismatch; got %q want %q", string(commandsContent), "hi")
	}
}

// A conductor session (title starts with "conductor-") MUST NOT get a
// scratch dir — the conductor is the legitimate telegram poller owner.
// Returning "" signals the caller to use the ambient profile.
func TestEnsureWorkerScratchConfigDir_ConductorKeepsAmbientProfile(t *testing.T) {
	withTelegramConductorPresent(t)
	source := t.TempDir()
	_ = os.WriteFile(filepath.Join(source, "settings.json"), []byte(`{"enabledPlugins":{"telegram@claude-plugins-official":true}}`), 0o644)

	inst := &Instance{ID: "c1", Tool: "claude", Title: "conductor-si"}

	got, err := inst.EnsureWorkerScratchConfigDir(source)
	if err != nil {
		t.Fatalf("EnsureWorkerScratchConfigDir: %v", err)
	}
	if got != "" {
		t.Errorf("conductor session must keep ambient profile; got scratch=%q", got)
	}
}

// Issue #1138 amendment (was: ChannelOwnerKeepsAmbientProfile).
//
// History: pre-#1138 a channel owner with v3 topology (global
// enabledPlugins.telegram=false, --channels as activation) kept the
// ambient profile because the scratch indirection seemed unnecessary —
// claude would supposedly read `--channels` and find the plugin
// already enabled globally.
//
// That assumption broke in production. With the ambient settings.json
// as the only source of truth for plugin enablement, ANY drift in the
// ambient (manual edit, Claude Code's `/plugin disable`, an out-of-band
// rewriter) silently disabled the channel transport. On the next
// restart there was no force-correct pass to heal it — channels
// silently dropped for hours until the maintainer noticed.
//
// Post-#1138: channel-owning sessions ALWAYS receive a scratch dir.
// The scratch is a shallow mirror of the ambient profile (everything
// is symlinked through), so the bot's own token files / commands /
// agents / plugins keep working. The only file the scratch OWNS is
// settings.json, where agent-deck force-writes
// enabledPlugins["telegram@claude-plugins-official"]=true on every
// spawn. That makes the scratch the heal point for drift.
func TestEnsureWorkerScratchConfigDir_ChannelOwner_AlwaysGetsScratch(t *testing.T) {
	withTelegramConductorPresent(t)
	source := t.TempDir()
	// v3 topology: global flag is unset, only --channels activates.
	_ = os.WriteFile(filepath.Join(source, "settings.json"), []byte(`{"enabledPlugins":{}}`), 0o644)

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg-data"))
	t.Setenv("CLAUDE_CONFIG_DIR", source)

	inst := &Instance{
		ID:       "bot1",
		Tool:     "claude",
		Title:    "my-bot",
		Channels: []string{"plugin:telegram@claude-plugins-official"},
	}

	got, err := inst.EnsureWorkerScratchConfigDir(source)
	if err != nil {
		t.Fatalf("EnsureWorkerScratchConfigDir: %v", err)
	}
	if got == "" {
		t.Fatalf("issue #1138: telegram channel owner MUST get a scratch dir for force-correct of channel-plugin enablement; got empty")
	}

	data, err := os.ReadFile(filepath.Join(got, "settings.json"))
	if err != nil {
		t.Fatalf("read scratch settings: %v", err)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("parse: %v", err)
	}
	plugins, _ := parsed["enabledPlugins"].(map[string]interface{})
	v, ok := plugins[telegramPluginID].(bool)
	if !ok || !v {
		t.Fatalf("scratch must force telegram=true for channel owner; got present=%v value=%v", ok, v)
	}
}

// Issue #941: when a channel-owning conductor's ambient profile has the
// GLOBAL_ANTIPATTERN (enabledPlugins.telegram=true), the worker-scratch
// guard MUST fire so the conductor's claude reads scratch settings (not
// the ambient profile) — that's how agent-deck owns the plugin
// enablement state per-session instead of leaking ambient changes.
//
// Issue #1134 amended the assertion: scratch must KEEP telegram ENABLED
// for channel-owning sessions (not pin it off). claude's `--channels`
// flag is a routing directive and requires the plugin's MCP stdio
// transport to already be live; pinning telegram=false breaks the
// transport and bun crashes in a respawn loop. The "sole-activation"
// reading of --channels was wrong; the correct reading is "use the
// already-enabled plugin to route channel events here."
func TestEnsureWorkerScratchConfigDir_ChannelOwner_GlobalAntipattern_GetsScratch(t *testing.T) {
	withTelegramConductorPresent(t)
	source := t.TempDir()
	_ = os.WriteFile(filepath.Join(source, "settings.json"), []byte(`{"enabledPlugins":{"telegram@claude-plugins-official":true}}`), 0o644)

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg-data"))
	t.Setenv("CLAUDE_CONFIG_DIR", source)

	inst := &Instance{
		ID:       "bot941",
		Tool:     "claude",
		Title:    "conductor-941",
		Channels: []string{"plugin:telegram@claude-plugins-official"},
	}

	scratch, err := inst.EnsureWorkerScratchConfigDir(source)
	if err != nil {
		t.Fatalf("EnsureWorkerScratchConfigDir: %v", err)
	}
	if scratch == "" {
		t.Fatalf("issue #941: channel owner with global enabledPlugins.telegram=true MUST get a scratch dir (got empty)")
	}
	data, err := os.ReadFile(filepath.Join(scratch, "settings.json"))
	if err != nil {
		t.Fatalf("read scratch settings: %v", err)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("parse: %v", err)
	}
	plugins := parsed["enabledPlugins"].(map[string]interface{})
	if v, ok := plugins[telegramPluginID].(bool); !ok || !v {
		t.Errorf("issue #1134: scratch settings.json must keep telegram ENABLED for channel-owning conductors so --channels has a live MCP transport to wire to; got %v", plugins[telegramPluginID])
	}
}

// Non-claude tools (codex, gemini, copilot, shell) MUST NOT get a
// scratch dir — TELEGRAM_STATE_DIR is a Claude Code plugin concept
// and other tools have no interaction with it.
func TestEnsureWorkerScratchConfigDir_NonClaudeToolSkipped(t *testing.T) {
	withTelegramConductorPresent(t)
	source := t.TempDir()
	_ = os.WriteFile(filepath.Join(source, "settings.json"), []byte(`{"enabledPlugins":{"telegram@claude-plugins-official":true}}`), 0o644)

	inst := &Instance{ID: "cx", Tool: "codex", Title: "my-worker"}
	got, err := inst.EnsureWorkerScratchConfigDir(source)
	if err != nil {
		t.Fatalf("EnsureWorkerScratchConfigDir: %v", err)
	}
	if got != "" {
		t.Errorf("non-claude tool must not get scratch dir; got %q", got)
	}
}

// A source profile that does NOT have telegram enabled globally still
// gets a scratch dir (defense-in-depth): the worker might otherwise
// have telegram flipped on behind its back by a concurrent conductor
// setup. The scratch settings.json always pins it false.
func TestEnsureWorkerScratchConfigDir_TelegramAbsentStillPinsDisabled(t *testing.T) {
	withTelegramConductorPresent(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg-data"))
	source := t.TempDir()
	_ = os.WriteFile(filepath.Join(source, "settings.json"), []byte(`{"enabledPlugins":{"superpowers@claude-plugins-official":true}}`), 0o644)

	inst := &Instance{ID: "w2", Tool: "claude", Title: "plain-worker"}
	got, err := inst.EnsureWorkerScratchConfigDir(source)
	if err != nil {
		t.Fatalf("EnsureWorkerScratchConfigDir: %v", err)
	}
	if got == "" {
		t.Fatal("worker must still receive a scratch dir even when telegram key absent in source")
	}
	data, _ := os.ReadFile(filepath.Join(got, "settings.json"))
	var parsed map[string]interface{}
	_ = json.Unmarshal(data, &parsed)
	plugins, _ := parsed["enabledPlugins"].(map[string]interface{})
	if v, ok := plugins["telegram@claude-plugins-official"]; !ok || v != false {
		t.Errorf("telegram must be explicitly pinned false; got %v", plugins)
	}
}

// buildClaudeCommand must route CLAUDE_CONFIG_DIR through the scratch
// dir once Instance.WorkerScratchConfigDir is set. This is the
// load-bearing wire: without it, the plugin still loads the ambient
// profile's settings.json and reads the conductor's bot token.
func TestBuildClaudeCommand_UsesWorkerScratchConfigDir(t *testing.T) {
	withTelegramConductorPresent(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg-data"))
	profile := filepath.Join(home, ".claude")
	if err := os.MkdirAll(profile, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile, "settings.json"), []byte(`{"enabledPlugins":{"telegram@claude-plugins-official":true}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Make CLAUDE_CONFIG_DIR explicit so the command builder actually
	// emits the prefix (IsClaudeConfigDirExplicitForInstance predicate).
	t.Setenv("CLAUDE_CONFIG_DIR", profile)

	inst := &Instance{
		ID:          "00000000-0000-0000-0000-000000000002",
		Tool:        "claude",
		Title:       "my-worker",
		ProjectPath: filepath.Join(home, "proj"),
	}

	scratch, err := inst.EnsureWorkerScratchConfigDir(profile)
	if err != nil {
		t.Fatal(err)
	}
	if scratch == "" {
		t.Fatal("setup: scratch dir should be non-empty for worker")
	}
	inst.WorkerScratchConfigDir = scratch

	cmd := inst.buildClaudeCommand("claude")

	// Accept either the inline form (`CLAUDE_CONFIG_DIR=<dir> claude`) or
	// the bash-export form (`export CLAUDE_CONFIG_DIR=<dir>;`). The
	// command builder picks between them per session mode — both must
	// point at the scratch dir.
	scratchInline := fmt.Sprintf("CLAUDE_CONFIG_DIR=%s ", scratch)
	scratchExport := fmt.Sprintf("CLAUDE_CONFIG_DIR=%s;", scratch)
	if !strings.Contains(cmd, scratchInline) && !strings.Contains(cmd, scratchExport) {
		t.Errorf("built command must point CLAUDE_CONFIG_DIR at scratch dir\n  want contains one of: %q | %q\n  got: %s", scratchInline, scratchExport, cmd)
	}
	profileInline := fmt.Sprintf("CLAUDE_CONFIG_DIR=%s ", profile)
	profileExport := fmt.Sprintf("CLAUDE_CONFIG_DIR=%s;", profile)
	if strings.Contains(cmd, profileInline) || strings.Contains(cmd, profileExport) {
		t.Errorf("built command must NOT use ambient profile when scratch is set\n  got: %s", cmd)
	}
}
