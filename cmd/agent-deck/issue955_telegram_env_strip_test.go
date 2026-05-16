package main

// Issue #955 regression — TELEGRAM_STATE_DIR env-inheritance leak on
// `agent-deck launch` from a conductor session.
//
// #941 (closed) fixed the GLOBAL_ANTIPATTERN angle (enabledPlugins.telegram
// = true in settings.json). #955 is a DIFFERENT angle:
//
//	When a conductor session spawns a child via `agent-deck launch`, the
//	agent-deck CLI process inherits the conductor's TELEGRAM_STATE_DIR.
//	That env var is then propagated to the tmux server that hosts the
//	child session (tmux inherits the launching process env on first
//	`new-session`). Even though the S8 layer wraps the final `claude`
//	exec in `env -u TELEGRAM_STATE_DIR`, every other process in the
//	pane — restart-respawn, fork claudes, Bash-tool subprocesses, any
//	future plugin-loading shell — still sees TSD and can race the
//	conductor for the same Telegram bot lock (HTTP 409, dropped
//	inbound messages).
//
// Real fix: strip TELEGRAM_STATE_DIR from the agent-deck CLI process env
// before the new session is started, IF the child is not a legitimate
// telegram channel owner. The strip predicate is the same one the S8
// exec-layer uses (telegramStateDirStripExpr); we just lift it from the
// shell-command layer up to the os.Environ layer so the tmux server is
// born without TSD in the first place.
//
// Reporter: asheshgoplani (issue #955).

import (
	"os"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

const issue955TSDPath = "/fake/conductor/tsd"

// Non-conductor, non-channel-owner child launches MUST scrub
// TELEGRAM_STATE_DIR from the agent-deck process env before starting
// the tmux session. Without the strip the tmux server (and every
// descendant of the pane) inherits TSD, the Claude Code telegram
// plugin loads inside any non-S8-prefixed subprocess, and a duplicate
// `bun telegram` poller races the conductor.
func TestLaunch_DoesNotInheritTelegramStateDir_RegressionFor955(t *testing.T) {
	t.Setenv("TELEGRAM_STATE_DIR", issue955TSDPath)

	child := &session.Instance{
		ID:          "955-child",
		Tool:        "claude",
		Title:       "launch-child",
		ProjectPath: t.TempDir(),
	}

	session.ScrubProcessEnvForChildLaunch(child)

	if got := os.Getenv("TELEGRAM_STATE_DIR"); got != "" {
		t.Fatalf("issue #955 regression: non-channel-owning child launch must scrub TELEGRAM_STATE_DIR from the agent-deck process env before tmux new-session inherits it; got %q", got)
	}
}

// Conductor sessions LEGITIMATELY own the telegram bot token. The
// strip must NOT fire for them — otherwise the conductor's own
// poller loses its state-dir handoff and the channel breaks.
func TestLaunch_Conductor_KeepsTelegramStateDir_RegressionFor955(t *testing.T) {
	t.Setenv("TELEGRAM_STATE_DIR", issue955TSDPath)

	conductor := &session.Instance{
		ID:          "955-conductor",
		Tool:        "claude",
		Title:       "conductor-test955",
		ProjectPath: t.TempDir(),
	}

	session.ScrubProcessEnvForChildLaunch(conductor)

	if got := os.Getenv("TELEGRAM_STATE_DIR"); got != issue955TSDPath {
		t.Fatalf("conductor sessions own the telegram bot — TELEGRAM_STATE_DIR must be preserved; got %q want %q", got, issue955TSDPath)
	}
}

// Sessions launched with an explicit telegram channel (--channel
// plugin:telegram@…) ARE the channel owner for their bot, so the
// strip must not fire. Mirrors S8's exec-layer carve-out.
func TestLaunch_TelegramChannelOwner_KeepsTelegramStateDir_RegressionFor955(t *testing.T) {
	t.Setenv("TELEGRAM_STATE_DIR", issue955TSDPath)

	owner := &session.Instance{
		ID:          "955-owner",
		Tool:        "claude",
		Title:       "bot-owner",
		ProjectPath: t.TempDir(),
		Channels:    []string{"plugin:telegram@claude-plugins-official"},
	}

	session.ScrubProcessEnvForChildLaunch(owner)

	if got := os.Getenv("TELEGRAM_STATE_DIR"); got != issue955TSDPath {
		t.Fatalf("telegram channel owner sessions must preserve TELEGRAM_STATE_DIR (they own the bot); got %q want %q", got, issue955TSDPath)
	}
}

// Non-claude tools must not be touched. TELEGRAM_STATE_DIR is a
// Claude Code plugin env var — codex / gemini / shell sessions
// have no interaction with it, so the strip must leave their
// process env alone (defense against future env-coupling bugs in
// other tools).
func TestLaunch_NonClaudeTool_LeavesEnvAlone_RegressionFor955(t *testing.T) {
	t.Setenv("TELEGRAM_STATE_DIR", issue955TSDPath)

	codexChild := &session.Instance{
		ID:          "955-codex",
		Tool:        "codex",
		Title:       "launch-child",
		ProjectPath: t.TempDir(),
	}

	session.ScrubProcessEnvForChildLaunch(codexChild)

	if got := os.Getenv("TELEGRAM_STATE_DIR"); got != issue955TSDPath {
		t.Fatalf("non-claude child launches must not touch TELEGRAM_STATE_DIR; got %q want %q", got, issue955TSDPath)
	}
}

// When TELEGRAM_STATE_DIR is not set at all, the scrub must be a
// no-op (no spurious errors, idempotent).
func TestLaunch_NoTelegramStateDir_NoOp_RegressionFor955(t *testing.T) {
	// Explicitly clear in case the host has it set.
	t.Setenv("TELEGRAM_STATE_DIR", "")
	_ = os.Unsetenv("TELEGRAM_STATE_DIR")

	child := &session.Instance{
		ID:          "955-noenv",
		Tool:        "claude",
		Title:       "launch-child",
		ProjectPath: t.TempDir(),
	}

	session.ScrubProcessEnvForChildLaunch(child) // must not panic

	if got, ok := os.LookupEnv("TELEGRAM_STATE_DIR"); ok {
		t.Fatalf("scrub on an unset env must remain unset; got %q", got)
	}
}
