package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runPrelude runs an env prelude (which ends in "&& ") under bash with one
// extra inherited variable and returns the value of name afterwards.
func runPrelude(t *testing.T, prelude, inherited, name string) string {
	t.Helper()
	cmd := exec.Command("bash", "-c", prelude+`printf %s "$`+name+`"`)
	cmd.Env = append(os.Environ(), inherited)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("prelude failed: %v: %s", err, out)
	}
	return string(out)
}

// The field report (17.10, a shared box): conductor "ops" runs in its
// conductor dir with the title "conductor-team-ops" and no stored
// ConductorName (created before the stable key existed). After an account
// switch its restarted process had the group env (TEAM_*) but not
// TELEGRAM_STATE_DIR from [conductors.ops.claude.env]: the title parses to
// "team-ops", so every conductor lookup missed. The conductor dir, not the
// title, is the identity setup stamped.
const conductorSwitchConfig = `
[profiles.alpha.claude]
config_dir = "~/.claude-alpha"

[profiles.beta.claude]
config_dir = "~/.claude-beta"

[groups."team/ops".claude]
config_dir = "~/.claude-alpha"
[groups."team/ops".claude.env]
TEAM_ACCOUNT = "alpha"
TEAM_MEMBER = "ops"
`

func setupSwitchConductor(t *testing.T, conductorBlock string) (home string, inst *Instance) {
	t.Helper()
	home = withTempAgentDeckHome(t, conductorSwitchConfig+conductorBlock)
	if err := SetupConductor("ops", "default", false, true, "", "", "", "", nil, ""); err != nil {
		t.Fatalf("SetupConductor: %v", err)
	}
	dir, err := ConductorNameDir("ops")
	if err != nil {
		t.Fatalf("ConductorNameDir: %v", err)
	}
	inst = NewInstanceWithGroupAndTool("conductor-team-ops", dir, "team/ops", "claude")
	inst.IsConductor = true
	inst.Account = "alpha"
	inst.Channels = []string{"plugin:telegram@claude-plugins-official"}
	return home, inst
}

func switchConductorTo(t *testing.T, inst *Instance, account string) *AccountSwitchResult {
	t.Helper()
	cfg, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	result, err := SwitchAccount(cfg, inst, account, AccountSwitchOptions{})
	if err != nil {
		t.Fatalf("SwitchAccount: %v", err)
	}
	if inst.Account != account {
		t.Fatalf("account = %q, want %q", inst.Account, account)
	}
	return result
}

func writeChannelState(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("TELEGRAM_BOT_TOKEN=test-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Cause 1: the conductor env must follow the stable identity through a switch.
func TestConductorSwitch_ConductorEnvSurvivesAccountSwitch(t *testing.T) {
	home, inst := setupSwitchConductor(t, `
[conductors.ops.claude]
config_dir = "~/.claude-alpha"
[conductors.ops.claude.env]
TELEGRAM_STATE_DIR = "/srv/state/ops/telegram"
`)
	if got := inst.ResolveConductorName(); got != "ops" {
		t.Fatalf("ResolveConductorName = %q, want the conductor dir's name %q", got, "ops")
	}

	switchConductorTo(t, inst, "beta")

	env := inst.buildEnvSourceCommand()
	if !strings.Contains(env, "export TELEGRAM_STATE_DIR='/srv/state/ops/telegram'") {
		t.Errorf("after the switch the conductor env TELEGRAM_STATE_DIR is missing; prelude = %q", env)
	}
	if !strings.Contains(env, "export TEAM_MEMBER='ops'") {
		t.Errorf("group env missing; prelude = %q", env)
	}
	if strings.Contains(env, "unset TELEGRAM_STATE_DIR") {
		t.Errorf("conductor must keep TELEGRAM_* (it owns the bot); prelude = %q", env)
	}
	// The account still decides the login after a switch.
	if got, want := GetClaudeConfigDirForInstance(inst), filepath.Join(home, ".claude-beta"); got != want {
		t.Errorf("config dir = %q, want the switched account %q", got, want)
	}
	if inst.storedConductorName() != "ops" {
		t.Errorf("stable conductor key not adopted: %q", inst.storedConductorName())
	}
}

// The path rule is for the conductor itself only: a worker launched inside the
// conductor dir (unflagged) must not become the bot owner.
func TestConductorSwitch_WorkerInConductorDirIsNotConductor(t *testing.T) {
	_, cond := setupSwitchConductor(t, `
[conductors.ops.claude.env]
TELEGRAM_STATE_DIR = "/srv/state/ops/telegram"
`)
	worker := NewInstanceWithGroupAndTool("worker-1", cond.ProjectPath, "team/ops", "claude")
	if got := worker.ResolveConductorName(); got != "" {
		t.Fatalf("unflagged worker in the conductor dir resolved to conductor %q", got)
	}
	env := worker.buildEnvSourceCommand()
	if strings.Contains(env, "/srv/state/ops/telegram") || !strings.Contains(env, "unset TELEGRAM_STATE_DIR") {
		t.Errorf("worker must not get the conductor's telegram env and must be stripped; prelude = %q", env)
	}
}

// Every by-name lookup (setup re-run, teardown, status, session send
// conductor-ops) must find the conductor by its dir too.
func TestConductorSwitch_FindConductorInstanceByDir(t *testing.T) {
	_, cond := setupSwitchConductor(t, "")
	other := NewInstanceWithGroupAndTool("conductor-team-ops-copy", t.TempDir(), "team/ops", "claude")
	other.IsConductor = true
	if got := FindConductorInstance([]*Instance{other, cond}, "ops"); got != cond {
		t.Fatalf("FindConductorInstance(ops) = %v, want the session running in conductor/ops", got)
	}
	if !cond.IsConductorNamed("ops", cond.Title) {
		t.Error("IsConductorNamed(ops) = false for the session in conductor/ops")
	}
}

// Cause 2: the old account held the telegram state under channels/telegram
// and the conductor has no TELEGRAM_STATE_DIR configured. After the switch the
// plugin must still find its token: the conductor is pointed at the same state
// via TELEGRAM_STATE_DIR. Never a copy, and never a link into the new
// account's shared config dir (every other session on that account would find
// the token there and start a second poller).
func TestConductorSwitch_ChannelStateFollowsConductor(t *testing.T) {
	home, inst := setupSwitchConductor(t, "")
	oldState := filepath.Join(home, ".claude-alpha", "channels", "telegram")
	writeChannelState(t, oldState)
	if w := inst.ChannelStateWarnings(); len(w) != 0 {
		t.Fatalf("before the switch the state is in place; warnings = %v", w)
	}

	result := switchConductorTo(t, inst, "beta")

	newChannels := filepath.Join(home, ".claude-beta", "channels", "telegram")
	if _, err := os.Lstat(newChannels); !os.IsNotExist(err) {
		t.Errorf("the new account's shared config dir must not get the bot state (second poller risk); lstat err = %v", err)
	}
	condDir, _ := ConductorNameDir("ops")
	anchor := filepath.Join(condDir, "telegram")
	fi, err := os.Lstat(anchor)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("conductor/ops/telegram must be a link to the old state, lstat = %v, %v", fi, err)
	}
	if target, _ := os.Readlink(anchor); target != realPath(t, oldState) {
		t.Errorf("link target = %q, want %q", target, oldState)
	}
	env := inst.buildEnvSourceCommand()
	if !strings.Contains(env, "export TELEGRAM_STATE_DIR='"+anchor+"'") {
		t.Errorf("switched conductor is not pointed at its telegram state; prelude = %q", env)
	}
	// The pointer comes before every configured source (so an env_file or
	// inline env still overrides it) and beats an inherited value (another
	// conductor's state dir left in the tmux server env).
	if strings.Index(env, "TELEGRAM_STATE_DIR") > strings.Index(env, "TEAM_ACCOUNT") {
		t.Errorf("state-dir export must precede the configured env; prelude = %q", env)
	}
	if got := runPrelude(t, env, "TELEGRAM_STATE_DIR="+filepath.Join(t.TempDir(), "other-conductor"), "TELEGRAM_STATE_DIR"); got != anchor {
		t.Errorf("prelude run under an inherited TELEGRAM_STATE_DIR left %q, want %q", got, anchor)
	}
	if w := inst.ChannelStateWarnings(); len(w) != 0 {
		t.Errorf("state is reachable after the switch; warnings = %v", w)
	}
	noted := false
	for _, w := range result.Warnings {
		if strings.Contains(w, "telegram") {
			noted = true
		}
	}
	if !noted {
		t.Errorf("switch result must say what it did with the telegram state; warnings = %v", result.Warnings)
	}

	// No token copy anywhere: the only .env holding it is the original.
	var copies []string
	_ = filepath.Walk(home, func(p string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() && info.Name() == ".env" && p != filepath.Join(oldState, ".env") {
			copies = append(copies, p)
		}
		return nil
	})
	if len(copies) != 0 {
		t.Errorf("token copied to %v", copies)
	}

	// A plain session on the new account still loses TELEGRAM_* and gets no
	// pointer: single consumer.
	plain := NewInstanceWithGroupAndTool("plain", t.TempDir(), "team/ops", "claude")
	plain.Account = "beta"
	if penv := plain.buildEnvSourceCommand(); strings.Contains(penv, anchor) || !strings.Contains(penv, "unset TELEGRAM_STATE_DIR") {
		t.Errorf("plain session on the new account must not reach the bot state; prelude = %q", penv)
	}
}

// Layout already on the shared box: the state lives in conductor/<name>/telegram.
// A conductor without a configured TELEGRAM_STATE_DIR whose account dir has no
// state must be pointed there on every start, whatever the account.
func TestConductorSwitch_ConductorDirStateIsUsedOnStart(t *testing.T) {
	_, inst := setupSwitchConductor(t, "")
	inst.Account = "beta"
	condDir, _ := ConductorNameDir("ops")
	writeChannelState(t, filepath.Join(condDir, "telegram"))

	env := inst.buildEnvSourceCommand()
	if !strings.Contains(env, "export TELEGRAM_STATE_DIR='"+filepath.Join(condDir, "telegram")+"'") {
		t.Errorf("conductor not pointed at conductor/ops/telegram; prelude = %q", env)
	}
}

// A configured TELEGRAM_STATE_DIR is the operator's word: no pointer, no link.
func TestConductorSwitch_ConfiguredStateDirIsLeftAlone(t *testing.T) {
	home, inst := setupSwitchConductor(t, `
[conductors.ops.claude.env]
TELEGRAM_STATE_DIR = "/srv/state/ops/telegram"
`)
	writeChannelState(t, filepath.Join(home, ".claude-alpha", "channels", "telegram"))
	switchConductorTo(t, inst, "beta")
	condDir, _ := ConductorNameDir("ops")
	if _, err := os.Lstat(filepath.Join(condDir, "telegram")); !os.IsNotExist(err) {
		t.Errorf("configured state dir: no link may be made; lstat err = %v", err)
	}
	env := inst.buildEnvSourceCommand()
	if strings.Count(env, "TELEGRAM_STATE_DIR=") != 1 {
		t.Errorf("only the configured TELEGRAM_STATE_DIR may be exported; prelude = %q", env)
	}
}

// An existing conductor/<name>/<plugin> entry is never replaced.
func TestConductorSwitch_ExistingAnchorIsNotReplaced(t *testing.T) {
	home, inst := setupSwitchConductor(t, "")
	writeChannelState(t, filepath.Join(home, ".claude-alpha", "channels", "telegram"))
	condDir, _ := ConductorNameDir("ops")
	anchor := filepath.Join(condDir, "telegram")
	if err := os.MkdirAll(anchor, 0o700); err != nil {
		t.Fatal(err)
	}
	switchConductorTo(t, inst, "beta")
	fi, err := os.Lstat(anchor)
	if err != nil || fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		t.Fatalf("existing anchor dir was replaced: %v %v", fi, err)
	}
	if w := inst.ChannelStateWarnings(); len(w) == 0 {
		t.Error("an empty anchor and no account state must warn")
	}
}

// A plugin without a known state-dir variable cannot be pointed by env, and a
// link in the new account's shared config dir would hand the token to every
// session on that account. Nothing is linked; the switch says so.
func TestConductorSwitch_UnknownPluginIsNotLinkedIntoSharedDir(t *testing.T) {
	home, inst := setupSwitchConductor(t, "")
	inst.Channels = []string{"plugin:pager@example"}
	writeChannelState(t, filepath.Join(home, ".claude-alpha", "channels", "pager"))
	result := switchConductorTo(t, inst, "beta")
	if _, err := os.Lstat(filepath.Join(home, ".claude-beta", "channels", "pager")); !os.IsNotExist(err) {
		t.Fatalf("unknown plugin state linked into the shared account dir; lstat err = %v", err)
	}
	if !strings.Contains(strings.Join(result.Warnings, "\n"), "NOT linked") {
		t.Errorf("switch must say the pager state was not carried; warnings = %v", result.Warnings)
	}
	if w := inst.ChannelStateWarnings(); len(w) != 1 || !strings.Contains(w[0], "pager") {
		t.Errorf("ChannelStateWarnings = %v, want one pager warning", w)
	}
}

// The new account already has its own channels/telegram (another session's
// bot). The conductor must keep its own state, not take over that token.
func TestConductorSwitch_ConductorStateWinsOverAccountState(t *testing.T) {
	home, inst := setupSwitchConductor(t, "")
	oldState := filepath.Join(home, ".claude-alpha", "channels", "telegram")
	writeChannelState(t, oldState)
	writeChannelState(t, filepath.Join(home, ".claude-beta", "channels", "telegram"))
	switchConductorTo(t, inst, "beta")
	condDir, _ := ConductorNameDir("ops")
	anchor := filepath.Join(condDir, "telegram")
	if target, err := os.Readlink(anchor); err != nil || target != realPath(t, oldState) {
		t.Fatalf("anchor = %q, %v; want link -> %s", target, err, oldState)
	}
	if env := inst.buildEnvSourceCommand(); !strings.Contains(env, "export TELEGRAM_STATE_DIR='"+anchor+"'") {
		t.Errorf("conductor would read the new account's bot state; prelude = %q", env)
	}
}

// session show / conductor status: a conductor whose channel plugin has no
// state warns loudly.
func TestConductorSwitch_WarnsWhenChannelStateMissing(t *testing.T) {
	_, inst := setupSwitchConductor(t, "")
	inst.Account = "beta"
	w := inst.ChannelStateWarnings()
	if len(w) != 1 || !strings.Contains(w[0], "telegram") || !strings.Contains(w[0], "TELEGRAM_STATE_DIR") {
		t.Fatalf("ChannelStateWarnings = %v, want one telegram warning naming TELEGRAM_STATE_DIR", w)
	}
	plain := NewInstanceWithTool("plain", t.TempDir(), "claude")
	plain.Channels = inst.Channels
	if w := plain.ChannelStateWarnings(); len(w) != 0 {
		t.Errorf("non-conductor sessions are not checked; got %v", w)
	}
}

// An alias dir (conductor/<alias> -> conductor/<name>) must still name the
// real conductor: meta.json, not the path's last element, is the name.
// Otherwise a conductor running in its alias dir would adopt the alias and
// lose its [conductors.<name>] env, the very bug this guards against.
func TestConductorSwitch_AliasDirResolvesToMetaName(t *testing.T) {
	withTempAgentDeckHome(t, conductorSwitchConfig+`
[conductors.team-ops.claude.env]
TELEGRAM_STATE_DIR = "/srv/state/team-ops/telegram"
`)
	if err := SetupConductor("team-ops", "default", false, true, "", "", "", "", nil, ""); err != nil {
		t.Fatalf("SetupConductor: %v", err)
	}
	real, _ := ConductorNameDir("team-ops")
	alias := filepath.Join(filepath.Dir(real), "ops")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	inst := NewInstanceWithGroupAndTool("conductor-team-ops", alias, "team/ops", "claude")
	inst.IsConductor = true
	if got := inst.ResolveConductorName(); got != "team-ops" {
		t.Fatalf("ResolveConductorName in alias dir = %q, want %q", got, "team-ops")
	}
	if inst.storedConductorName() != "team-ops" {
		t.Errorf("adopted %q, want team-ops", inst.storedConductorName())
	}
	if env := inst.buildEnvSourceCommand(); !strings.Contains(env, "export TELEGRAM_STATE_DIR='/srv/state/team-ops/telegram'") {
		t.Errorf("conductor in its alias dir lost its env; prelude = %q", env)
	}
}

// DISCORD_* is not stripped from child sessions, so agent-deck must not
// export DISCORD_STATE_DIR on its own (a child of the conductor would connect
// a second client to the bot). The switch reports what to configure instead.
func TestConductorSwitch_DiscordIsReportedNotPointed(t *testing.T) {
	home, inst := setupSwitchConductor(t, "")
	inst.Channels = []string{"plugin:discord@claude-plugins-official"}
	oldState := filepath.Join(home, ".claude-alpha", "channels", "discord")
	writeChannelState(t, oldState)
	result := switchConductorTo(t, inst, "beta")
	condDir, _ := ConductorNameDir("ops")
	if _, err := os.Lstat(filepath.Join(condDir, "discord")); !os.IsNotExist(err) {
		t.Errorf("discord state anchored although it is never exported; lstat err = %v", err)
	}
	if env := inst.buildEnvSourceCommand(); strings.Contains(env, "DISCORD_STATE_DIR") {
		t.Errorf("DISCORD_STATE_DIR exported automatically; prelude = %q", env)
	}
	if !strings.Contains(strings.Join(result.Warnings, "\n"), "DISCORD_STATE_DIR") {
		t.Errorf("switch must say to configure DISCORD_STATE_DIR; warnings = %v", result.Warnings)
	}
	if w := inst.ChannelStateWarnings(); len(w) != 1 || !strings.Contains(w[0], "DISCORD_STATE_DIR") {
		t.Errorf("ChannelStateWarnings = %v, want one discord warning", w)
	}
}

// The anchor links into the old account's dir. If that state later goes away
// the link dangles; the conductor must still be pointed at its anchor (the
// plugin then fails closed) and must not fall back to the new account's
// channels/telegram, another session's bot. And it must warn.
func TestConductorSwitch_DanglingAnchorFailsClosedAndWarns(t *testing.T) {
	home, inst := setupSwitchConductor(t, "")
	oldState := filepath.Join(home, ".claude-alpha", "channels", "telegram")
	writeChannelState(t, oldState)
	writeChannelState(t, filepath.Join(home, ".claude-beta", "channels", "telegram"))
	switchConductorTo(t, inst, "beta")
	if err := os.RemoveAll(oldState); err != nil {
		t.Fatal(err)
	}
	condDir, _ := ConductorNameDir("ops")
	anchor := filepath.Join(condDir, "telegram")
	if env := inst.buildEnvSourceCommand(); !strings.Contains(env, "export TELEGRAM_STATE_DIR='"+anchor+"'") {
		t.Errorf("dangling anchor: conductor falls back to the account's bot state; prelude = %q", env)
	}
	if w := inst.ChannelStateWarnings(); len(w) != 1 || !strings.Contains(w[0], anchor) {
		t.Errorf("ChannelStateWarnings = %v, want one warning naming the dangling anchor %s", w, anchor)
	}
}
