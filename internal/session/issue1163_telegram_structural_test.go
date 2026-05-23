// Issue #1163 — structural defenses so telegram can never leak into a
// conductor-spawned child session.
//
// Root cause (see /tmp/exec-fix-tg-launcher-structural/EVIDENCE-env.md):
// conductor children inherited CLAUDE_CONFIG_DIR pointing at the
// conductor's worker-scratch dir (telegram=true), because the scratch-pin
// gate hostHasTelegramConductor() read only the legacy single-bot
// [conductor.telegram].token, which is empty under the 7-bot env_file
// topology. These tests lock in the three structural repairs:
//
//	Change 1 — gate detects telegram via the modern env_file topology.
//	Change 2 — ChildLaunchEnv strips CLAUDE_CONFIG_DIR + TELEGRAM_* (a
//	           child must never inherit the parent's config dir).
//	Change 3 — process-group reaping (see issue1163_procgroup_unix_test.go).
package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Change 1 — the gate must detect a telegram conductor declared via the
// modern per-conductor env_file (TELEGRAM_STATE_DIR), not only via the
// legacy [conductor.telegram].token field.
func TestHostHasTelegramConductor_EnvFileTopology(t *testing.T) {
	dir := t.TempDir()

	// .envrc with the modern marker, exactly as a real conductor declares it.
	envrc := filepath.Join(dir, "personal.envrc")
	require.NoError(t, os.WriteFile(envrc,
		[]byte("export TELEGRAM_STATE_DIR=~/.claude/channels/telegram-personal/\n"), 0o644))

	cfg := &UserConfig{
		// Legacy single-bot token is empty — the 7-bot topology never sets it.
		Conductors: map[string]ConductorOverrides{
			"personal": {Claude: ConductorClaudeSettings{EnvFile: envrc}},
		},
	}

	require.True(t, configDeclaresTelegram(cfg),
		"gate must detect telegram via conductor env_file containing TELEGRAM_STATE_DIR")
}

// Boundary: a conductor whose env_file exists but defines no telegram state
// must NOT arm the gate (avoids breaking per-group config_dir isolation, #759).
func TestHostHasTelegramConductor_EnvFileWithoutTelegram(t *testing.T) {
	dir := t.TempDir()
	envrc := filepath.Join(dir, "work.envrc")
	require.NoError(t, os.WriteFile(envrc, []byte("export FOO=bar\n"), 0o644))

	cfg := &UserConfig{
		Conductors: map[string]ConductorOverrides{
			"work": {Claude: ConductorClaudeSettings{EnvFile: envrc}},
		},
	}

	require.False(t, configDeclaresTelegram(cfg),
		"env_file without TELEGRAM_STATE_DIR must not arm the gate")
}

// Backward compat: the legacy single-bot token field still arms the gate.
func TestHostHasTelegramConductor_LegacyTokenStillWorks(t *testing.T) {
	cfg := &UserConfig{}
	cfg.Conductor.Telegram.Token = "123:abc"
	require.True(t, configDeclaresTelegram(cfg),
		"legacy [conductor.telegram].token must still arm the gate")
}

// Failure mode: nil config and missing env_file never panic and report false.
func TestHostHasTelegramConductor_NilAndMissingFile(t *testing.T) {
	require.False(t, configDeclaresTelegram(nil))

	cfg := &UserConfig{
		Conductors: map[string]ConductorOverrides{
			"ghost": {Claude: ConductorClaudeSettings{EnvFile: "/no/such/file.envrc"}},
		},
	}
	require.False(t, configDeclaresTelegram(cfg),
		"missing env_file must be treated as no-telegram, not an error")
}

func joinEnv(env []string) string { return strings.Join(env, "\n") }

// Change 2 — ChildLaunchEnv guarantees a child claude never inherits the
// conductor's CLAUDE_CONFIG_DIR or any TELEGRAM_* var, and pins the child's
// own config dir when one is supplied.
func TestChildLaunchEnv_StripsCCDAndTelegram(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/parent/scratch")
	t.Setenv("TELEGRAM_STATE_DIR", "/parent/tg")
	t.Setenv("TELEGRAM_BOT_TOKEN", "secret")
	t.Setenv("PATH", "/usr/bin")

	env := ChildLaunchEnv(&Instance{}, "/child/scratch")

	assert.NotContains(t, env, "CLAUDE_CONFIG_DIR=/parent/scratch",
		"child must not inherit the parent's config dir")
	assert.Contains(t, env, "CLAUDE_CONFIG_DIR=/child/scratch",
		"child must get its own config dir")
	assert.NotContains(t, joinEnv(env), "TELEGRAM_",
		"every TELEGRAM_* var must be stripped (#1152 logic)")
	assert.Contains(t, env, "PATH=/usr/bin",
		"unrelated vars must pass through untouched")
}

// Boundary: empty childConfigDir strips the inherited CCD without adding one,
// so the child resolves its own (ambient ~/.claude or a freshly prepared scratch).
func TestChildLaunchEnv_EmptyChildDirDropsInheritedCCD(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/parent/scratch")
	t.Setenv("PATH", "/usr/bin")

	env := ChildLaunchEnv(&Instance{}, "")

	for _, kv := range env {
		assert.False(t, strings.HasPrefix(kv, "CLAUDE_CONFIG_DIR="),
			"no CLAUDE_CONFIG_DIR should remain when childConfigDir is empty, got %q", kv)
	}
	assert.Contains(t, env, "PATH=/usr/bin")
}

// Change 1 end-to-end — a child Instance under the env_file topology gets a
// scratch CLAUDE_CONFIG_DIR whose settings.json pins telegram OFF, instead of
// inheriting the conductor's telegram=true scratch.
func TestConductorSpawnsChild_ChildScratchHasTelegramFalse(t *testing.T) {
	// Arm the gate as the env_file topology would (legacy token empty).
	orig := hostHasTelegramConductor
	hostHasTelegramConductor = func() bool { return true }
	t.Cleanup(func() { hostHasTelegramConductor = orig })

	// Source profile whose settings.json enables telegram (the conductor shape).
	source := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(source, "settings.json"),
		[]byte(`{"enabledPlugins":{"telegram@claude-plugins-official":true}}`), 0o644))
	t.Setenv("HOME", t.TempDir())

	// A plain claude worker child (not a channel owner) under a telegram host.
	child := &Instance{ID: "00000000-0000-0000-0000-000000001163", Tool: "claude", Title: "worker-1163"}

	require.True(t, child.NeedsWorkerScratchConfigDir(),
		"with the gate armed, a claude worker must get its own scratch config dir")

	scratch, err := child.EnsureWorkerScratchConfigDir(source)
	require.NoError(t, err)
	t.Cleanup(func() { child.CleanupWorkerScratchConfigDir() })
	require.NotEmpty(t, scratch)
	require.NotEqual(t, source, scratch, "child must get a distinct scratch dir, not the conductor's")

	data, err := os.ReadFile(filepath.Join(scratch, "settings.json"))
	require.NoError(t, err)
	assert.Contains(t, string(data), telegramPluginID,
		"scratch settings.json must reference the telegram plugin id")
	assert.NotContains(t, strings.ReplaceAll(string(data), " ", ""),
		`"`+telegramPluginID+`":true`,
		"scratch settings.json must pin telegram OFF, not true")
}
