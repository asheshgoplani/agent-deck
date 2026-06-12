// Package session — regression suite for the lost-channels conductor drop.
//
// Background. Every telegram defense added by #1136/#1137/#1163 keys off
// Instance.Channels containing "plugin:telegram@…": the channel-owner
// scratch gate (needsScratchForTelegramChannelOwner), the `--channels`
// flag emission (buildClaudeExtraFlags), the force-correct scratch pass,
// and the drift warning (VerifyTelegramChannelEnabled returns vacuously
// OK when Channels is empty). The persisted Channels list is therefore a
// single upstream point of failure: when a conductor's DB record loses
// the field — index wipe + manual rebuild, migration, hand edit — the
// conductor silently respawns as a plain `claude --resume` with no
// channel wiring at all, and every downstream defense disarms without a
// single warning.
//
// Observed live 2026-06-11: 4 of 7 conductor records (si, sherif,
// innotrade, opengraphdb) had no "channels" key in tool_data after the
// 2026-06-04 index wipe + rebuild; their claude processes ran without
// --channels and without a scratch CLAUDE_CONFIG_DIR, while the three
// records that kept the field (agent-deck, personal, ryan) spawned
// correctly. The deaf bots were then "revived" by flipping the global
// antipattern (enabledPlugins.telegram=true in the shared profile, #941)
// back on, recreating the duplicate-poller 409 class.
//
// Fix under test. The conductor's config — an env_file declaring
// TELEGRAM_STATE_DIR under [conductors.<name>].claude — is the durable
// source of truth for channel ownership; the persisted Channels list is
// a cache. reconcileConductorTelegramChannel restores the telegram
// channel id onto conductor-titled claude instances whose Channels lost
// it, and is wired into prepareWorkerScratchConfigDirForSpawn and
// buildClaudeExtraFlags so the heal runs on every spawn path.
//
// All tests in this file MUST fail on the pre-fix tree and pass after
// the fix lands. Grep tag: telegram-channel-restore.

package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withConductorEnvFile points the conductorTelegramEnvFile seam at a temp
// .envrc with the given content for the named conductor, restoring the
// real loader on cleanup. Empty content means "conductor has no env_file".
func withConductorEnvFile(t *testing.T, conductor, content string) {
	t.Helper()
	orig := conductorTelegramEnvFile
	t.Cleanup(func() { conductorTelegramEnvFile = orig })

	if content == "" {
		conductorTelegramEnvFile = func(name string) string { return "" }
		return
	}
	path := filepath.Join(t.TempDir(), ".envrc")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp envrc: %v", err)
	}
	conductorTelegramEnvFile = func(name string) string {
		if name != conductor {
			return ""
		}
		return path
	}
}

// lostChannelsConductor reproduces the live regression shape: a conductor
// record whose tool_data lost the "channels" key (si/sherif/innotrade/
// opengraphdb after the 2026-06-04 index wipe).
func lostChannelsConductor(name string) *Instance {
	return &Instance{
		ID:          name + "-test",
		Tool:        "claude",
		Title:       "conductor-" + name,
		IsConductor: true,
		Channels:    nil, // the lost field
	}
}

// TestReconcileRestoresLostConductorChannel is the core regression test:
// conductor config declares TELEGRAM_STATE_DIR, persisted Channels is
// empty → the channel id must be restored.
func TestReconcileRestoresLostConductorChannel(t *testing.T) {
	withConductorEnvFile(t, "si",
		"export TELEGRAM_STATE_DIR=/home/u/.claude-work/channels/telegram-si\n")
	inst := lostChannelsConductor("si")

	if !reconcileConductorTelegramChannel(inst) {
		t.Fatal("reconcileConductorTelegramChannel returned false; want restore")
	}
	want := "plugin:" + telegramPluginID
	if len(inst.Channels) != 1 || inst.Channels[0] != want {
		t.Fatalf("Channels = %v; want [%s]", inst.Channels, want)
	}

	// Idempotent: a second pass must not duplicate.
	if reconcileConductorTelegramChannel(inst) {
		t.Fatal("second reconcile reported a restore; want no-op")
	}
	if len(inst.Channels) != 1 {
		t.Fatalf("Channels after second pass = %v; want exactly 1", inst.Channels)
	}
}

// TestReconcileSkipsNonConductorAndUnconfigured pins the negative space:
// no restore for worker sessions, conductors without env_file, env_files
// without TELEGRAM_STATE_DIR, non-claude tools, or explicit opt-out.
func TestReconcileSkipsNonConductorAndUnconfigured(t *testing.T) {
	cases := []struct {
		name string
		inst *Instance
		env  string // envrc content; "" = no env_file
	}{
		{
			name: "worker session title",
			inst: &Instance{ID: "w1", Tool: "claude", Title: "fix-telegram-drops"},
			env:  "export TELEGRAM_STATE_DIR=/tmp/x\n",
		},
		{
			name: "conductor without env_file",
			inst: lostChannelsConductor("si"),
			env:  "",
		},
		{
			name: "env_file without TELEGRAM_STATE_DIR",
			inst: lostChannelsConductor("si"),
			env:  "export RAILWAY_API_TOKEN=abc\n",
		},
		{
			name: "non-claude tool",
			inst: &Instance{ID: "h1", Tool: "hermes", Title: "conductor-si"},
			env:  "export TELEGRAM_STATE_DIR=/tmp/x\n",
		},
		{
			name: "explicit opt-out",
			inst: func() *Instance {
				i := lostChannelsConductor("si")
				i.PluginChannelLinkDisabled = true
				return i
			}(),
			env: "export TELEGRAM_STATE_DIR=/tmp/x\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withConductorEnvFile(t, "si", tc.env)
			if reconcileConductorTelegramChannel(tc.inst) {
				t.Fatalf("reconcile restored a channel for %q; want skip", tc.name)
			}
			if len(tc.inst.Channels) != 0 {
				t.Fatalf("Channels = %v; want empty", tc.inst.Channels)
			}
		})
	}
}

// TestBuildClaudeExtraFlagsEmitsRestoredChannel verifies the heal is
// wired into the command-build chokepoint: a lost-channels conductor
// must still get `--channels plugin:telegram@…` on every Start/Restart/
// resume, because every command build flows through
// buildClaudeExtraFlags.
func TestBuildClaudeExtraFlagsEmitsRestoredChannel(t *testing.T) {
	withConductorEnvFile(t, "sherif",
		"export TELEGRAM_STATE_DIR=/home/u/.claude-work/channels/telegram-sherif\n")
	inst := lostChannelsConductor("sherif")

	flags := inst.buildClaudeExtraFlags(nil)
	want := "--channels plugin:" + telegramPluginID
	if !strings.Contains(flags, want) {
		t.Fatalf("buildClaudeExtraFlags = %q; want it to contain %q", flags, want)
	}
}

// TestScratchGateArmsAfterRestore verifies the restored channel re-arms
// the #1138 channel-owner scratch gate, so the conductor regains its
// force-corrected scratch CLAUDE_CONFIG_DIR instead of trusting the
// ambient profile (the topology that pushed the operator back to the
// #941 global antipattern).
func TestScratchGateArmsAfterRestore(t *testing.T) {
	withConductorEnvFile(t, "innotrade",
		"export TELEGRAM_STATE_DIR=/home/u/.claude-work/channels/telegram-innotrade\n")
	inst := lostChannelsConductor("innotrade")

	if needsScratchForTelegramChannelOwner(inst) {
		t.Fatal("gate armed before restore; test precondition broken")
	}
	if !reconcileConductorTelegramChannel(inst) {
		t.Fatal("reconcile did not restore the channel")
	}
	if !needsScratchForTelegramChannelOwner(inst) {
		t.Fatal("channel-owner scratch gate did not arm after restore")
	}
}
