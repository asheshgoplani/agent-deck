// Phase 2 tests for the worker-scratch deny+allow generalization
// (RFC docs/rfc/PLUGIN_ATTACH.md §4.3-4.4 and §7).
//
// These cover:
//   - needsScratchForExplicitPlugins fires on non-TG-conductor hosts
//     when Instance.Plugins is non-empty
//   - The deny+allow writer overlays both sets onto the existing
//     enabledPlugins block, allow wins on key collision
//   - Catalog resolution skips unknown names rather than erroring
//   - macOS warning emission is one-shot per source profile and
//     no-ops on non-darwin
//   - hostHasTelegramConductor=false + Plugins=empty yields no scratch
//     (issue #759 invariant preserved on macOS for non-plugin sessions)

package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestNeedsScratchForExplicitPlugins_FiresOnNonTGConductorHost asserts
// the new RFC §4.4 gate: any claude session with non-empty Plugins gets
// a scratch dir even when the host has no TG conductor. This is the gap
// the new flag fills.
func TestNeedsScratchForExplicitPlugins_FiresOnNonTGConductorHost(t *testing.T) {
	// Force hostHasTelegramConductor=false so only the plugin gate can fire.
	orig := hostHasTelegramConductor
	hostHasTelegramConductor = func() bool { return false }
	t.Cleanup(func() { hostHasTelegramConductor = orig })

	inst := &Instance{
		ID:      "test-id",
		Tool:    "claude",
		Title:   "worker",
		Plugins: []string{"octopus"},
	}
	if !inst.NeedsWorkerScratchConfigDir() {
		t.Errorf("NeedsWorkerScratchConfigDir must return true when Plugins is non-empty, even on a non-TG-conductor host (RFC §4.4)")
	}
	if !needsScratchForExplicitPlugins(inst) {
		t.Errorf("needsScratchForExplicitPlugins must fire on non-empty Plugins")
	}
	if needsScratchForTelegram(inst) {
		t.Errorf("needsScratchForTelegram must NOT fire on a host without TG conductor")
	}
}

// TestNeedsScratch_NoPluginsNoTelegramHostStaysAmbient is the issue #759
// regression guard: a session WITHOUT plugins on a host WITHOUT a TG
// conductor must NOT get a scratch dir. macOS users without telegram or
// plugins continue to see no behavior change.
func TestNeedsScratch_NoPluginsNoTelegramHostStaysAmbient(t *testing.T) {
	orig := hostHasTelegramConductor
	hostHasTelegramConductor = func() bool { return false }
	t.Cleanup(func() { hostHasTelegramConductor = orig })

	inst := &Instance{ID: "x", Tool: "claude", Title: "worker"}
	if inst.NeedsWorkerScratchConfigDir() {
		t.Errorf("issue #759 invariant: worker without Plugins on non-TG host must use ambient profile (no scratch)")
	}
}

// TestNeedsScratchForExplicitPlugins_NonClaudeSkipped asserts the
// claude-only gate. Plugins on a shell/gemini session must NOT trigger
// scratch — they have no enabledPlugins concept.
func TestNeedsScratchForExplicitPlugins_NonClaudeSkipped(t *testing.T) {
	inst := &Instance{
		ID:      "test-id",
		Tool:    "shell",
		Title:   "worker",
		Plugins: []string{"octopus"},
	}
	if needsScratchForExplicitPlugins(inst) {
		t.Errorf("non-claude session must not trigger plugin-driven scratch; got fired for tool=%q", inst.Tool)
	}
}

// TestComputeAllowList_ResolvesViaCatalog asserts the resolver maps
// catalog short names to fully-qualified ids in declaration order.
func TestComputeAllowList_ResolvesViaCatalog(t *testing.T) {
	home := withTempHome(t)
	writeConfig(t, home, `
[plugins.octopus]
name = "octopus"
source = "nyldn/claude-octopus"

[plugins.discord]
name = "discord"
source = "claude-plugins-official"
emits_channel = true
`)

	inst := &Instance{
		ID:      "test-id",
		Tool:    "claude",
		Plugins: []string{"octopus", "discord"},
	}
	got := computeAllowList(inst)
	want := []string{"octopus@nyldn/claude-octopus", "discord@claude-plugins-official"}
	if len(got) != len(want) {
		t.Fatalf("allowList length: got %d (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("allowList[%d]: got %q, want %q", i, got[i], want[i])
		}
	}
}

// TestComputeAllowList_SkipsUnknownNames asserts unknown catalog names
// are silently dropped (CLI-level validation already rejects them at
// flag parse time; spawn-time guard is defensive).
func TestComputeAllowList_SkipsUnknownNames(t *testing.T) {
	home := withTempHome(t)
	writeConfig(t, home, `
[plugins.octopus]
name = "octopus"
source = "nyldn/claude-octopus"
`)
	inst := &Instance{
		ID:      "test-id",
		Tool:    "claude",
		Plugins: []string{"octopus", "ghost-plugin", "unknown-too"},
	}
	got := computeAllowList(inst)
	if len(got) != 1 || got[0] != "octopus@nyldn/claude-octopus" {
		t.Errorf("unknown plugin names must be dropped; got %v", got)
	}
}

// TestComputeAllowList_TelegramOfficialFilteredAtCatalog asserts the
// catalog-level refusal (RFC §6) prevents telegram-official ids from
// reaching the allow list at all.
func TestComputeAllowList_TelegramOfficialFilteredAtCatalog(t *testing.T) {
	home := withTempHome(t)
	writeConfig(t, home, `
[plugins.tg-official]
name = "telegram"
source = "claude-plugins-official"

[plugins.tg-fork]
name = "telegram"
source = "acme/telegram-fork"
`)
	inst := &Instance{
		ID:      "test-id",
		Tool:    "claude",
		Plugins: []string{"tg-official", "tg-fork"},
	}
	got := computeAllowList(inst)
	if len(got) != 1 || got[0] != "telegram@acme/telegram-fork" {
		t.Errorf("telegram-official must be filtered at catalog read; tg-fork must pass; got %v", got)
	}
}

// TestComputeDenyList_FiresOnlyWhenTelegramScratchNeeded asserts the
// deny list is populated EXACTLY when needsScratchForTelegram fires.
// On non-TG-conductor hosts, even sessions getting a scratch for
// plugin reasons must NOT have telegram pinned off (host has no TG
// poller to defend against).
func TestComputeDenyList_FiresOnlyWhenTelegramScratchNeeded(t *testing.T) {
	cases := []struct {
		name          string
		hostHasTG     bool
		stripExpr     bool // simulates telegramStateDirStripExpr != ""
		wantDenyCount int
	}{
		{"tg-host-eligible-session", true, true, 1},
		{"tg-host-ineligible-session", true, false, 0},
		{"non-tg-host-eligible-session", false, true, 0},
		{"non-tg-host-ineligible-session", false, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			orig := hostHasTelegramConductor
			hostHasTelegramConductor = func() bool { return tc.hostHasTG }
			defer func() { hostHasTelegramConductor = orig }()

			inst := &Instance{ID: "x", Tool: "claude", Title: "worker"}
			if !tc.stripExpr {
				// Make telegramStateDirStripExpr return "" by adding a TG channel.
				inst.Channels = []string{"plugin:telegram@claude-plugins-official"}
			}
			deny := computeDenyList(inst)
			if len(deny) != tc.wantDenyCount {
				t.Errorf("denyList: got %v (%d), want %d entries", deny, len(deny), tc.wantDenyCount)
			}
			if tc.wantDenyCount == 1 && deny[0] != telegramPluginID {
				t.Errorf("deny entry: got %q, want %q", deny[0], telegramPluginID)
			}
		})
	}
}

// TestEnsureWorkerScratch_AllowAndDenyCoexist asserts the deny+allow
// overlay writes both kinds onto the same enabledPlugins block when a
// TG-conductor host runs a worker that explicitly enables OTHER
// plugins (not telegram). Verifies the §4.3 invariant that both
// concerns are preserved layered.
func TestEnsureWorkerScratch_AllowAndDenyCoexist(t *testing.T) {
	home := withTempHome(t)
	t.Setenv("HOME", home)
	withTelegramConductorPresent(t)

	writeConfig(t, home, `
[plugins.octopus]
name = "octopus"
source = "nyldn/claude-octopus"
`)

	source := t.TempDir()
	srcSettings := `{"enabledPlugins":{"superpowers@claude-plugins-official":true}}`
	if err := os.WriteFile(filepath.Join(source, "settings.json"), []byte(srcSettings), 0o644); err != nil {
		t.Fatal(err)
	}

	inst := &Instance{
		ID:      "00000000-0000-0000-0000-aaaaaaaaaaaa",
		Tool:    "claude",
		Title:   "worker",
		Plugins: []string{"octopus"},
	}

	scratch, err := inst.EnsureWorkerScratchConfigDir(source)
	if err != nil {
		t.Fatalf("EnsureWorkerScratchConfigDir: %v", err)
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

	// Telegram MUST be pinned off (deny).
	if v, ok := plugins[telegramPluginID]; !ok || v != false {
		t.Errorf("telegram-official must be denied (false); got %v", plugins[telegramPluginID])
	}
	// Octopus MUST be enabled (allow).
	if v, ok := plugins["octopus@nyldn/claude-octopus"]; !ok || v != true {
		t.Errorf("octopus@... must be allowed (true); got %v", plugins["octopus@nyldn/claude-octopus"])
	}
	// Source's superpowers entry MUST be preserved untouched.
	if v, ok := plugins["superpowers@claude-plugins-official"]; !ok || v != true {
		t.Errorf("source enabledPlugins entries must be preserved; got %v", plugins["superpowers@claude-plugins-official"])
	}
}

// TestEnsureWorkerScratch_PluginsOnNonTGHost asserts scratch is created
// and allow-list applied on a host WITHOUT a TG conductor — and the
// telegram deny is NOT applied (no defense needed).
func TestEnsureWorkerScratch_PluginsOnNonTGHost(t *testing.T) {
	home := withTempHome(t)
	t.Setenv("HOME", home)
	orig := hostHasTelegramConductor
	hostHasTelegramConductor = func() bool { return false }
	t.Cleanup(func() { hostHasTelegramConductor = orig })

	writeConfig(t, home, `
[plugins.octopus]
name = "octopus"
source = "nyldn/claude-octopus"
`)

	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "settings.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	inst := &Instance{
		ID:      "00000000-0000-0000-0000-bbbbbbbbbbbb",
		Tool:    "claude",
		Title:   "worker",
		Plugins: []string{"octopus"},
	}

	scratch, err := inst.EnsureWorkerScratchConfigDir(source)
	if err != nil {
		t.Fatalf("EnsureWorkerScratchConfigDir: %v", err)
	}
	if scratch == "" {
		t.Fatal("scratch dir must be created when Plugins is non-empty (even on non-TG host)")
	}

	data, _ := os.ReadFile(filepath.Join(scratch, "settings.json"))
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("parse: %v", err)
	}
	plugins := parsed["enabledPlugins"].(map[string]interface{})

	if v, ok := plugins["octopus@nyldn/claude-octopus"]; !ok || v != true {
		t.Errorf("octopus must be enabled in scratch on non-TG host; got %v", plugins)
	}
	if _, present := plugins[telegramPluginID]; present {
		t.Errorf("telegram-official must NOT be touched on non-TG host (no defense needed); got plugins=%v", plugins)
	}
}

// TestEnsureWorkerScratch_DetachedCatalogPluginsForcedFalse asserts
// that catalog plugins NOT attached to this instance are explicitly
// pinned to `false` in scratch enabledPlugins, even if the source
// profile has them globally enabled.
//
// Bug-D extension: omitting the key is NOT enough — Claude Code
// auto-loads any installed plugin whose enabledPlugins entry is missing
// (it scans `~/.claude/plugins/cache/` and treats absence as enabled).
// A previous global `/plugin install fakechat` therefore bled through
// into every worker after detach, defeating per-session isolation.
func TestEnsureWorkerScratch_DetachedCatalogPluginsForcedFalse(t *testing.T) {
	home := withTempHome(t)
	t.Setenv("HOME", home)
	orig := hostHasTelegramConductor
	hostHasTelegramConductor = func() bool { return false }
	t.Cleanup(func() { hostHasTelegramConductor = orig })

	// Two catalog plugins — only `octopus` is attached. `fakechat` is in
	// the catalog but NOT in inst.Plugins (i.e. detached).
	writeConfig(t, home, `
[plugins.octopus]
name = "octopus"
source = "nyldn/claude-octopus"

[plugins.fakechat]
name = "fakechat"
source = "claude-plugins-official"
`)

	source := t.TempDir()
	// Source profile has BOTH globally enabled (e.g., from prior
	// `/plugin install` runs).
	srcSettings := `{"enabledPlugins":{
		"fakechat@claude-plugins-official": true,
		"octopus@nyldn/claude-octopus": true,
		"superpowers@claude-plugins-official": true
	}}`
	if err := os.WriteFile(filepath.Join(source, "settings.json"), []byte(srcSettings), 0o644); err != nil {
		t.Fatal(err)
	}

	inst := &Instance{
		ID:      "00000000-0000-0000-0000-cccccccccccc",
		Tool:    "claude",
		Title:   "worker",
		Plugins: []string{"octopus"}, // fakechat detached
	}

	scratch, err := inst.EnsureWorkerScratchConfigDir(source)
	if err != nil {
		t.Fatalf("EnsureWorkerScratchConfigDir: %v", err)
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

	// octopus (attached) → true
	if v, ok := plugins["octopus@nyldn/claude-octopus"]; !ok || v != true {
		t.Errorf("attached catalog plugin must be true; got plugins[%q]=%v (present=%v)", "octopus@nyldn/claude-octopus", v, ok)
	}

	// fakechat (detached, but in catalog and globally enabled in source)
	// MUST be explicitly false. Omission is the bug we are fixing.
	v, present := plugins["fakechat@claude-plugins-official"]
	if !present {
		t.Errorf("detached catalog plugin must have explicit false in scratch (omission lets claude auto-load it); fakechat key absent. plugins=%v", plugins)
	} else if v != false {
		t.Errorf("detached catalog plugin must be false; got fakechat=%v", v)
	}

	// Non-catalog source entries pass through unchanged.
	if v, ok := plugins["superpowers@claude-plugins-official"]; !ok || v != true {
		t.Errorf("non-catalog source entries must pass through; got superpowers=%v (present=%v)", v, ok)
	}
}

// TestMacOSScratchWarning_OneShot asserts the warning emitter fires
// exactly once per source profile dir, gated on darwin. Subsequent
// invocations short-circuit via the persisted state file.
func TestMacOSScratchWarning_OneShot(t *testing.T) {
	withTempHome(t)

	// Pretend to be on darwin.
	origGOOS := runtimeGOOS
	runtimeGOOS = func() string { return "darwin" }
	t.Cleanup(func() { runtimeGOOS = origGOOS })

	// Track emission via a stub.
	var emitCount int
	origEmit := macOSScratchWarningEmitter
	macOSScratchWarningEmitter = func(src string) { emitCount++ }
	t.Cleanup(func() { macOSScratchWarningEmitter = origEmit })

	const srcA = "/Users/test/.claude"
	const srcB = "/Users/test/.claude-work"

	maybeEmitMacOSScratchWarning(srcA)
	maybeEmitMacOSScratchWarning(srcA) // second call must short-circuit
	maybeEmitMacOSScratchWarning(srcA) // and third
	maybeEmitMacOSScratchWarning(srcB) // different profile gets its own one-shot
	maybeEmitMacOSScratchWarning(srcB)

	if emitCount != 2 {
		t.Errorf("emit count: got %d, want 2 (one per distinct source profile dir)", emitCount)
	}
}

// TestMacOSScratchWarning_NonDarwinIsNoOp asserts the warning never
// fires on Linux/etc. — issue #759's path-keyed OAuth issue is
// macOS-specific.
func TestMacOSScratchWarning_NonDarwinIsNoOp(t *testing.T) {
	withTempHome(t)
	origGOOS := runtimeGOOS
	runtimeGOOS = func() string { return "linux" }
	t.Cleanup(func() { runtimeGOOS = origGOOS })

	var emitCount int
	origEmit := macOSScratchWarningEmitter
	macOSScratchWarningEmitter = func(src string) { emitCount++ }
	t.Cleanup(func() { macOSScratchWarningEmitter = origEmit })

	maybeEmitMacOSScratchWarning("/Users/test/.claude")
	maybeEmitMacOSScratchWarning("/Users/test/.claude")

	if emitCount != 0 {
		t.Errorf("emit count on linux: got %d, want 0 (no-op on non-darwin)", emitCount)
	}
}
