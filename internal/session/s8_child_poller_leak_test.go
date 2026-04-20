package session

// S8 — child claude sessions launched via `agent-deck launch` inherit
// TELEGRAM_STATE_DIR from the conductor's process env. With the telegram
// plugin globally enabled in the profile's settings.json (required per
// the v3 topology — see memory telegram_topology_supported.md), every
// non-conductor child loads the plugin, reads the conductor's .env via
// the inherited TSD, and spawns a bun poller on the same bot token.
// Telegram Bot API rejects the duplicate poller with 409 Conflict.
//
// Issue #680's strip is narrow: it fires only when the child's group
// has a paired [conductors.<group>] block AND that group has an
// env_file. `agent-deck launch` routinely creates children outside
// that triangle (different group, no group, no env_file). Those
// children still leak pollers.
//
// S8 fix: broaden the strip to fire for ANY non-channel-owning claude
// session, regardless of group, regardless of whether an env_file was
// sourced. Sessions that explicitly carry a telegram plugin channel
// (conductor or explicit opt-in) keep TSD. Everyone else loses it.

import (
	"strings"
	"testing"
)

// Child session with no Channels and no group/conductor config at all
// MUST still strip TELEGRAM_STATE_DIR. This is the `agent-deck launch`
// common path: a child spawned into an unrelated group or a bare
// project with no env_file in play.
func TestS8_ChildNoChannels_NoConfig_StripsTSD(t *testing.T) {
	cfg := &UserConfig{
		MCPs:       make(map[string]MCPDef),
		Conductors: map[string]ConductorOverrides{},
		Groups:     map[string]GroupSettings{},
	}
	defer resetUserConfigCache(t, cfg)()

	child := &Instance{
		Title:       "launch-child",
		GroupPath:   "",
		Tool:        "claude",
		ProjectPath: "/tmp",
	}

	got := child.buildEnvSourceCommand()

	if !strings.Contains(got, "unset TELEGRAM_STATE_DIR") {
		t.Errorf("non-channel-owning child must strip TELEGRAM_STATE_DIR even with no env_file\nbuildEnvSourceCommand() = %q", got)
	}
}

// Child session in an unrelated group (no conductor pairing) but still
// non-channel-owning MUST strip. This is the common S8 case: a
// conductor calls `agent-deck launch -g work ...` and the work group
// has no [conductors.work] block.
func TestS8_ChildNoChannels_UnrelatedGroup_StripsTSD(t *testing.T) {
	cfg := &UserConfig{
		MCPs:       make(map[string]MCPDef),
		Conductors: map[string]ConductorOverrides{},
		Groups: map[string]GroupSettings{
			"work": {}, // no env_file, no conductor pairing
		},
	}
	defer resetUserConfigCache(t, cfg)()

	child := &Instance{
		Title:       "launch-child",
		GroupPath:   "work",
		Tool:        "claude",
		ProjectPath: "/tmp",
	}

	got := child.buildEnvSourceCommand()

	if !strings.Contains(got, "unset TELEGRAM_STATE_DIR") {
		t.Errorf("non-channel-owning child in unrelated group must strip TELEGRAM_STATE_DIR\nbuildEnvSourceCommand() = %q", got)
	}
}

// A session whose Channels include a telegram plugin channel is a
// channel-owning session (typically the conductor). It legitimately
// needs TELEGRAM_STATE_DIR — do NOT strip.
func TestS8_TelegramChannelOwner_KeepsTSD(t *testing.T) {
	cfg := &UserConfig{
		MCPs:       make(map[string]MCPDef),
		Conductors: map[string]ConductorOverrides{},
		Groups:     map[string]GroupSettings{},
	}
	defer resetUserConfigCache(t, cfg)()

	owner := &Instance{
		Title:       "conductor-travel",
		GroupPath:   "travel",
		Tool:        "claude",
		ProjectPath: "/tmp",
		Channels:    []string{"plugin:telegram@claude-plugins-official"},
	}

	got := owner.buildEnvSourceCommand()

	if strings.Contains(got, "unset TELEGRAM_STATE_DIR") {
		t.Errorf("channel-owning session (Channels contains telegram) must NOT strip TELEGRAM_STATE_DIR\nbuildEnvSourceCommand() = %q", got)
	}
}

// Non-claude sessions (codex, gemini, etc.) are out of scope — the
// telegram plugin is a Claude Code plugin. Don't mutate their env.
func TestS8_NonClaudeSession_NoStrip(t *testing.T) {
	cfg := &UserConfig{
		MCPs:       make(map[string]MCPDef),
		Conductors: map[string]ConductorOverrides{},
		Groups:     map[string]GroupSettings{},
	}
	defer resetUserConfigCache(t, cfg)()

	codex := &Instance{
		Title:       "codex-child",
		Tool:        "codex",
		ProjectPath: "/tmp",
	}

	got := codex.buildEnvSourceCommand()

	if strings.Contains(got, "unset TELEGRAM_STATE_DIR") {
		t.Errorf("non-claude session must NOT receive the TSD strip\nbuildEnvSourceCommand() = %q", got)
	}
}

// Channels that are not telegram (e.g. discord, slack) do not
// legitimize the telegram state dir — strip still fires because
// TELEGRAM_STATE_DIR is a telegram-plugin-only variable.
func TestS8_NonTelegramChannelOwner_StripsTSD(t *testing.T) {
	cfg := &UserConfig{
		MCPs:       make(map[string]MCPDef),
		Conductors: map[string]ConductorOverrides{},
		Groups:     map[string]GroupSettings{},
	}
	defer resetUserConfigCache(t, cfg)()

	discordOwner := &Instance{
		Title:       "discord-bot",
		Tool:        "claude",
		ProjectPath: "/tmp",
		Channels:    []string{"plugin:discord@claude-plugins-official"},
	}

	got := discordOwner.buildEnvSourceCommand()

	if !strings.Contains(got, "unset TELEGRAM_STATE_DIR") {
		t.Errorf("non-telegram channel owner should still strip TELEGRAM_STATE_DIR (telegram-only env var)\nbuildEnvSourceCommand() = %q", got)
	}
}

// Any telegram channel id variant (forks, repo renames) qualifies as
// an owner. Match by the "plugin:telegram@" prefix, consistent with
// the existing TelegramValidator.
func TestS8_TelegramChannelOwner_ForkVariant_KeepsTSD(t *testing.T) {
	cfg := &UserConfig{
		MCPs:       make(map[string]MCPDef),
		Conductors: map[string]ConductorOverrides{},
		Groups:     map[string]GroupSettings{},
	}
	defer resetUserConfigCache(t, cfg)()

	owner := &Instance{
		Title:       "conductor-work",
		Tool:        "claude",
		ProjectPath: "/tmp",
		Channels:    []string{"plugin:telegram@acme/telegram-fork"},
	}

	got := owner.buildEnvSourceCommand()

	if strings.Contains(got, "unset TELEGRAM_STATE_DIR") {
		t.Errorf("fork-variant telegram channel owner must NOT strip\nbuildEnvSourceCommand() = %q", got)
	}
}

// Conductor session (title "conductor-*") without explicit Channels
// set still must NOT have its TSD stripped — the conductor is the
// legitimate owner of the bot token and may set Channels later via
// agent-deck CLI. Preserves the issue #680 invariant.
func TestS8_ConductorSession_NoChannels_KeepsTSD(t *testing.T) {
	cfg := &UserConfig{
		MCPs:       make(map[string]MCPDef),
		Conductors: map[string]ConductorOverrides{},
		Groups:     map[string]GroupSettings{},
	}
	defer resetUserConfigCache(t, cfg)()

	conductor := &Instance{
		Title:       "conductor-travel",
		GroupPath:   "travel",
		Tool:        "claude",
		ProjectPath: "/tmp",
	}

	got := conductor.buildEnvSourceCommand()

	if strings.Contains(got, "unset TELEGRAM_STATE_DIR") {
		t.Errorf("conductor-* session must NOT strip TSD (owner of the bot)\nbuildEnvSourceCommand() = %q", got)
	}
}
