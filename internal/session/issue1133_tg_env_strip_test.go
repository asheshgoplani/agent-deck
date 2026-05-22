package session

// Issue #1133 — broaden the telegram env strip to cover TELEGRAM_BOT_TOKEN
// (and any future TELEGRAM_* plugin env var) and add an explicit opt-in
// `--inherit-telegram-env` escape hatch.
//
// Background: #680 + #955 + S8 strip TELEGRAM_STATE_DIR at three layers
// (shell `unset`, exec `env -u`, agent-deck CLI `os.Unsetenv`). A
// conductor that exports both TELEGRAM_STATE_DIR *and* TELEGRAM_BOT_TOKEN
// still leaks the latter to children — the plugin re-derives state from
// the token and spawns the same duplicate `bun telegram` poller that
// fights the conductor for getUpdates (Telegram 409 Conflict, dropped
// inbound messages).
//
// Real fix: every non-channel-owning claude child loses ALL TELEGRAM_*
// env vars, not just TELEGRAM_STATE_DIR. Sessions that legitimately need
// the conductor's telegram env (rare: a tool-only fork debugging the
// poller) can opt in via the new InheritTelegramEnv flag, exposed on the
// CLI as `--inherit-telegram-env`.

import (
	"os"
	"strings"
	"testing"
)

// Test 1: parent has TELEGRAM_STATE_DIR set → spawn child → child env
// does NOT have TELEGRAM_STATE_DIR. Asserts the shell-source strip
// covers TELEGRAM_STATE_DIR (regression coverage; mirrors #680/#955).
func TestIssue1133_Child_StripsTelegramStateDir(t *testing.T) {
	cfg := &UserConfig{
		MCPs:       make(map[string]MCPDef),
		Conductors: map[string]ConductorOverrides{},
		Groups:     map[string]GroupSettings{},
	}
	defer resetUserConfigCache(t, cfg)()

	child := &Instance{
		Title:       "launch-child",
		Tool:        "claude",
		ProjectPath: "/tmp",
	}

	got := child.buildEnvSourceCommand()

	if !strings.Contains(got, "TELEGRAM_STATE_DIR") {
		t.Errorf("child must strip TELEGRAM_STATE_DIR\nbuildEnvSourceCommand() = %q", got)
	}
	if !strings.Contains(got, "unset ") {
		t.Errorf("child must emit an unset clause\nbuildEnvSourceCommand() = %q", got)
	}
}

// Test 2: parent has TELEGRAM_BOT_TOKEN set → child env does NOT have it
// either. This is the #1133 broadening — STATE_DIR alone isn't enough.
func TestIssue1133_Child_StripsTelegramBotToken(t *testing.T) {
	cfg := &UserConfig{
		MCPs:       make(map[string]MCPDef),
		Conductors: map[string]ConductorOverrides{},
		Groups:     map[string]GroupSettings{},
	}
	defer resetUserConfigCache(t, cfg)()

	child := &Instance{
		Title:       "launch-child",
		Tool:        "claude",
		ProjectPath: "/tmp",
	}

	got := child.buildEnvSourceCommand()

	if !strings.Contains(got, "TELEGRAM_BOT_TOKEN") {
		t.Errorf("child must strip TELEGRAM_BOT_TOKEN (#1133 broadening)\nbuildEnvSourceCommand() = %q", got)
	}
}

// Process-env scrub variant: ScrubProcessEnvForChildLaunch must remove
// TELEGRAM_BOT_TOKEN from os.Environ for a non-channel-owning child so
// the tmux server isn't born with the token.
func TestIssue1133_Scrub_RemovesTelegramBotToken(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "1234:fake-token-1133")

	child := &Instance{
		ID:          "1133-child",
		Tool:        "claude",
		Title:       "launch-child",
		ProjectPath: t.TempDir(),
	}

	ScrubProcessEnvForChildLaunch(child)

	if got, ok := os.LookupEnv("TELEGRAM_BOT_TOKEN"); ok {
		t.Fatalf("non-channel-owning child launch must scrub TELEGRAM_BOT_TOKEN from process env; got %q", got)
	}
}

// Process-env scrub variant: arbitrary TELEGRAM_* vars must also be
// removed so the strip covers future plugin env additions without
// requiring code changes here.
func TestIssue1133_Scrub_RemovesOtherTelegramPrefixedVars(t *testing.T) {
	t.Setenv("TELEGRAM_API_HASH", "deadbeef")
	t.Setenv("TELEGRAM_APP_ID", "42")

	child := &Instance{
		ID:          "1133-prefix",
		Tool:        "claude",
		Title:       "launch-child",
		ProjectPath: t.TempDir(),
	}

	ScrubProcessEnvForChildLaunch(child)

	if got, ok := os.LookupEnv("TELEGRAM_API_HASH"); ok {
		t.Fatalf("scrub must remove TELEGRAM_API_HASH; got %q", got)
	}
	if got, ok := os.LookupEnv("TELEGRAM_APP_ID"); ok {
		t.Fatalf("scrub must remove TELEGRAM_APP_ID; got %q", got)
	}
}

// Test 3: child explicitly opts in via `--inherit-telegram-env` flag →
// child DOES inherit TELEGRAM_STATE_DIR and TELEGRAM_BOT_TOKEN. The opt-in
// is the documented escape hatch; without it there'd be no way to test
// the poller from a forked session.
func TestIssue1133_OptIn_InheritsTelegramEnv(t *testing.T) {
	cfg := &UserConfig{
		MCPs:       make(map[string]MCPDef),
		Conductors: map[string]ConductorOverrides{},
		Groups:     map[string]GroupSettings{},
	}
	defer resetUserConfigCache(t, cfg)()

	child := &Instance{
		Title:              "launch-child",
		Tool:               "claude",
		ProjectPath:        "/tmp",
		InheritTelegramEnv: true,
	}

	got := child.buildEnvSourceCommand()

	if strings.Contains(got, "unset TELEGRAM_") {
		t.Errorf("--inherit-telegram-env opt-in child must NOT emit a telegram unset\nbuildEnvSourceCommand() = %q", got)
	}
}

// Process-env scrub variant of Test 3: opt-in child must leave os.Environ
// intact so the conductor's TELEGRAM_* propagates into tmux + claude.
func TestIssue1133_OptIn_ScrubIsNoOp(t *testing.T) {
	t.Setenv("TELEGRAM_STATE_DIR", "/tmp/tg-1133")
	t.Setenv("TELEGRAM_BOT_TOKEN", "1234:opt-in-1133")

	child := &Instance{
		ID:                 "1133-optin",
		Tool:               "claude",
		Title:              "launch-child",
		ProjectPath:        t.TempDir(),
		InheritTelegramEnv: true,
	}

	ScrubProcessEnvForChildLaunch(child)

	if got, ok := os.LookupEnv("TELEGRAM_STATE_DIR"); !ok || got != "/tmp/tg-1133" {
		t.Fatalf("opt-in child must preserve TELEGRAM_STATE_DIR; got ok=%v %q", ok, got)
	}
	if got, ok := os.LookupEnv("TELEGRAM_BOT_TOKEN"); !ok || got != "1234:opt-in-1133" {
		t.Fatalf("opt-in child must preserve TELEGRAM_BOT_TOKEN; got ok=%v %q", ok, got)
	}
}

// Test 4: regression — non-telegram env vars (PATH, HOME, etc.) DO
// propagate as before. The strip must be tightly scoped to the
// TELEGRAM_ prefix; broadening it to other vars would break every
// child session.
func TestIssue1133_NonTelegramVars_Propagate(t *testing.T) {
	cfg := &UserConfig{
		MCPs:       make(map[string]MCPDef),
		Conductors: map[string]ConductorOverrides{},
		Groups:     map[string]GroupSettings{},
	}
	defer resetUserConfigCache(t, cfg)()

	child := &Instance{
		Title:       "launch-child",
		Tool:        "claude",
		ProjectPath: "/tmp",
	}

	got := child.buildEnvSourceCommand()

	// PATH / HOME must NOT be in the unset list.
	if strings.Contains(got, "unset PATH") {
		t.Errorf("strip must not touch PATH\nbuildEnvSourceCommand() = %q", got)
	}
	if strings.Contains(got, "unset HOME") {
		t.Errorf("strip must not touch HOME\nbuildEnvSourceCommand() = %q", got)
	}

	// Process-env scrub: must not unset PATH or HOME.
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("HOME", "/home/test")
	ScrubProcessEnvForChildLaunch(child)
	if got := os.Getenv("PATH"); got != "/usr/bin:/bin" {
		t.Fatalf("scrub must preserve PATH; got %q", got)
	}
	if got := os.Getenv("HOME"); got != "/home/test" {
		t.Fatalf("scrub must preserve HOME; got %q", got)
	}
}

// Boundary: conductor session keeps both TELEGRAM_STATE_DIR AND
// TELEGRAM_BOT_TOKEN — it owns the bot, the whole point of the carve-out.
func TestIssue1133_Conductor_KeepsAllTelegramVars(t *testing.T) {
	cfg := &UserConfig{
		MCPs:       make(map[string]MCPDef),
		Conductors: map[string]ConductorOverrides{},
		Groups:     map[string]GroupSettings{},
	}
	defer resetUserConfigCache(t, cfg)()

	conductor := &Instance{
		Title:       "conductor-personal",
		Tool:        "claude",
		ProjectPath: "/tmp",
	}

	got := conductor.buildEnvSourceCommand()

	if strings.Contains(got, "unset TELEGRAM_") {
		t.Errorf("conductor session must NOT strip any TELEGRAM_* var\nbuildEnvSourceCommand() = %q", got)
	}
}

// Boundary: non-claude tool (codex, gemini) does not get the strip —
// telegram plugin is Claude Code only, so other tools must be left
// alone to avoid surprise breakage.
func TestIssue1133_NonClaudeTool_NoStrip(t *testing.T) {
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

	if strings.Contains(got, "unset TELEGRAM_") {
		t.Errorf("non-claude session must NOT receive the strip\nbuildEnvSourceCommand() = %q", got)
	}
}
