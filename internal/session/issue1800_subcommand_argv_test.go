package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Argv-capture harness for #1800: the fake-binary technique from the issue's
// own sandboxed reproduction. A stand-in `claude` script logs exactly the
// argv it received; running the command string agent-deck actually builds
// through it proves what the real binary would see, without needing a live
// Claude install or a tmux server.
//
// buildFakeBinArgvCapture writes an executable script named binName into a
// fresh temp dir that appends "ARGV: <args>" to logPath, and returns the
// dir (to prepend onto PATH) and logPath.
func buildFakeBinArgvCapture(t *testing.T, binName string) (binDir, logPath string) {
	t.Helper()
	binDir = t.TempDir()
	logPath = filepath.Join(t.TempDir(), "argv.log")

	script := "#!/bin/sh\necho \"ARGV: $*\" >> \"$CLAUDE_SHIM_LOG\"\n"
	binPath := filepath.Join(binDir, binName)
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write fake %s binary: %v", binName, err)
	}
	return binDir, logPath
}

// runBuiltCommandCapturingArgv executes cmd (a shell command string exactly
// as agent-deck would hand it to the tmux pane) via `sh -c`, with the fake
// binary directory prepended to PATH, and returns the captured ARGV log
// line. Fully sandboxed: isolated HOME/XDG so nothing touches the real
// environment, matching the repo-wide test-isolation rule.
func runBuiltCommandCapturingArgv(t *testing.T, cmd, binDir, logPath string) string {
	t.Helper()

	sandboxHome := t.TempDir()
	execCmd := exec.Command("sh", "-c", cmd)
	execCmd.Env = append(os.Environ(),
		"PATH="+binDir+":"+os.Getenv("PATH"),
		"CLAUDE_SHIM_LOG="+logPath,
		"HOME="+sandboxHome,
		"XDG_CONFIG_HOME=",
		"XDG_DATA_HOME=",
		"XDG_CACHE_HOME=",
	)

	done := make(chan error, 1)
	go func() { done <- execCmd.Run() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("built command failed: %v\ncommand: %s", err, cmd)
		}
	case <-time.After(10 * time.Second):
		if execCmd.Process != nil {
			_ = execCmd.Process.Kill()
		}
		t.Fatalf("built command did not exit within timeout (a real interactive claude session\n"+
			"would block forever here — this test's fake binary always exits immediately, so a\n"+
			"hang means the built command doesn't reach the fake binary at all)\ncommand: %s", cmd)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("fake binary never logged an ARGV line (log file missing/unreadable): %v\ncommand: %s", err, cmd)
	}
	return strings.TrimSpace(string(data))
}

// TestIssue1800_ClaudeSubcommand_RunsWithoutFlagInjection is the argv-capture
// regression test for #1800. resolveSessionCommand (cmd/agent-deck/cli_utils.go)
// routes "claude remote-control --name rc-test" to Tool="shell",
// Command=raw, Wrapper="" (see TestResolveSessionCommand's
// "claude subcommand runs as-is, no wrapper" case) — this test proves that
// choice, once built by the ACTUAL production dispatch a Tool=="shell"
// instance takes at spawn time (Start()'s default case →
// buildShellPassthroughCommand, not buildClaudeCommand — Tool=="shell"
// never reaches buildClaudeCommand in production, see
// IsClaudeCompatible(i.Tool) at instance.go's Start() switch), delivers the
// subcommand to the binary untouched: no --session-id, no
// --dangerously-skip-permissions spliced in before "remote-control".
//
// Before the fix, resolveSessionCommand instead produced Command="claude",
// Wrapper="{command} remote-control --name rc-test", and
// buildClaudeCommandWithMessage's {command} substitution landed the
// subcommand AFTER the injected root flags — reproduced independently below
// in TestIssue1800_PreFix_WrapperShape_DemotesSubcommand.
func TestIssue1800_ClaudeSubcommand_RunsWithoutFlagInjection(t *testing.T) {
	origHome := os.Getenv("HOME")
	origConfigDir, hadConfigDir := os.LookupEnv("CLAUDE_CONFIG_DIR")
	os.Setenv("HOME", t.TempDir())
	os.Unsetenv("CLAUDE_CONFIG_DIR")
	ClearUserConfigCache()
	t.Cleanup(func() {
		os.Setenv("HOME", origHome)
		if hadConfigDir {
			os.Setenv("CLAUDE_CONFIG_DIR", origConfigDir)
		} else {
			os.Unsetenv("CLAUDE_CONFIG_DIR")
		}
		ClearUserConfigCache()
	})

	binDir, logPath := buildFakeBinArgvCapture(t, "claude")

	// Mirrors resolveSessionCommand's fixed output for
	// `-c "claude remote-control --name rc-test"`.
	inst := NewInstanceWithTool("rc-collide", t.TempDir(), "shell")
	inst.Command = "claude remote-control --name rc-test"
	inst.Wrapper = ""

	// buildShellPassthroughCommand is what Start()'s default case actually
	// calls for a Tool=="shell" instance (instance.go) — exercising it here
	// (rather than buildClaudeCommand, which Tool=="shell" never reaches)
	// proves the production dispatch, not just the string shape.
	cmd := inst.buildShellPassthroughCommand(inst.Command)
	if !strings.Contains(cmd, "AGENTDECK_INSTANCE_ID="+inst.ID) {
		t.Fatalf("expected buildShellPassthroughCommand to restore the AGENTDECK_INSTANCE_ID "+
			"env prefix that Tool=\"shell\"'s bare default case (command = i.Command) dropped; got: %s", cmd)
	}
	if strings.Contains(cmd, "--session-id") {
		t.Fatalf("buildShellPassthroughCommand must never inject --session-id; got: %s", cmd)
	}

	wrapped, _, err := inst.prepareCommand(cmd)
	if err != nil {
		t.Fatalf("prepareCommand: %v", err)
	}

	got := runBuiltCommandCapturingArgv(t, wrapped, binDir, logPath)
	want := "ARGV: remote-control --name rc-test"
	if got != want {
		t.Fatalf("fake claude received wrong argv.\n  got:  %s\n  want: %s\n(command was: %s)", got, want, wrapped)
	}
}

// TestIssue1800_PreFix_WrapperShape_DemotesSubcommand pins the exact bug
// #1800 reported, independent of the resolveSessionCommand fix: if a caller
// still builds Tool="claude" with Wrapper="{command} remote-control --name
// rc-test" (the pre-fix wrapper-suffix shape), the subcommand lands AFTER
// agent-deck's injected --session-id, demoting it to a positional argument
// of plain interactive claude instead of a real subcommand invocation. This
// documents why the fix routes subcommands around the wrapper path entirely
// rather than trying to reorder flags within it.
func TestIssue1800_PreFix_WrapperShape_DemotesSubcommand(t *testing.T) {
	origHome := os.Getenv("HOME")
	origConfigDir, hadConfigDir := os.LookupEnv("CLAUDE_CONFIG_DIR")
	os.Setenv("HOME", t.TempDir())
	os.Unsetenv("CLAUDE_CONFIG_DIR")
	ClearUserConfigCache()
	t.Cleanup(func() {
		os.Setenv("HOME", origHome)
		if hadConfigDir {
			os.Setenv("CLAUDE_CONFIG_DIR", origConfigDir)
		} else {
			os.Unsetenv("CLAUDE_CONFIG_DIR")
		}
		ClearUserConfigCache()
	})

	binDir, logPath := buildFakeBinArgvCapture(t, "claude")

	inst := NewInstanceWithTool("rc-collide-prefix", t.TempDir(), "claude")
	inst.Command = "claude"
	inst.Wrapper = "{command} remote-control --name rc-test"

	cmd := inst.buildClaudeCommand(inst.Command)
	wrapped, _, err := inst.prepareCommand(cmd)
	if err != nil {
		t.Fatalf("prepareCommand: %v", err)
	}

	got := runBuiltCommandCapturingArgv(t, wrapped, binDir, logPath)

	if !strings.Contains(got, "--session-id") {
		t.Fatalf("expected the pre-fix wrapper shape to still inject --session-id ahead of the\n"+
			"subcommand (that's the bug being documented); got: %s", got)
	}
	sessionIdx := strings.Index(got, "--session-id")
	subcmdIdx := strings.Index(got, "remote-control")
	if subcmdIdx < 0 {
		t.Fatalf("expected argv to still contain remote-control (demoted to positional); got: %s", got)
	}
	if subcmdIdx < sessionIdx {
		t.Fatalf("subcommand unexpectedly precedes --session-id — the bug this test documents did not "+
			"reproduce; got: %s", got)
	}
}

// TestIssue1800_ShellPassthroughClaude_StripsTelegramEnv is the regression
// test for the review finding on #1821: a Tool=="shell" instance whose
// Command still execs claude (the #1800 subcommand-passthrough shape) must
// keep the #1133/S8 TELEGRAM_* strip. Before instInvokesClaudeBinary
// (env.go), telegramStateDirStripExpr's Tool=="claude" gate never fired for
// these instances, so a long-lived `claude remote-control` spawned from a
// conductor pane would inherit TELEGRAM_STATE_DIR / TELEGRAM_BOT_TOKEN and
// could start a duplicate poller against the same bot token (Telegram 409).
func TestIssue1800_ShellPassthroughClaude_StripsTelegramEnv(t *testing.T) {
	cfg := &UserConfig{
		MCPs:       make(map[string]MCPDef),
		Conductors: map[string]ConductorOverrides{},
		Groups:     map[string]GroupSettings{},
	}
	defer resetUserConfigCache(t, cfg)()

	inst := &Instance{
		Title:       "launch-child",
		Tool:        "shell",
		Command:     "claude remote-control --name rc-test",
		ProjectPath: "/tmp",
	}

	got := inst.buildEnvSourceCommand()
	if !strings.Contains(got, "TELEGRAM_STATE_DIR") || !strings.Contains(got, "unset ") {
		t.Fatalf("shell-routed claude subcommand must still strip TELEGRAM_STATE_DIR\nbuildEnvSourceCommand() = %q", got)
	}

	// A genuinely plain shell command must NOT be treated as a claude spawn
	// — the strip stays gated to commands that actually exec claude.
	plainShell := &Instance{Title: "plain", Tool: "shell", Command: "ls -la", ProjectPath: "/tmp"}
	if got := plainShell.buildEnvSourceCommand(); strings.Contains(got, "TELEGRAM_STATE_DIR") {
		t.Fatalf("a plain shell command must not get the claude-only TELEGRAM strip\nbuildEnvSourceCommand() = %q", got)
	}
}

// TestIssue1800_ShellPassthroughCodex_GetsAgentdeckEnv proves the codex half
// of the #1821 review finding: a Tool=="shell" subcommand-passthrough
// instance whose Command execs codex (e.g. `-c "codex mcp list"`) still gets
// the AGENTDECK_* env vars codex's hook subprocesses rely on to find this
// session's state — the same treatment buildCodexCommand's own
// `trimmed != "codex"` passthrough branch applies when Tool=="codex".
func TestIssue1800_ShellPassthroughCodex_GetsAgentdeckEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	inst := NewInstanceWithTool("codex-sub", t.TempDir(), "shell")
	inst.Command = "codex mcp list"

	cmd := inst.buildShellPassthroughCommand(inst.Command)
	if !strings.Contains(cmd, "AGENTDECK_INSTANCE_ID="+inst.ID) {
		t.Fatalf("expected AGENTDECK_INSTANCE_ID for a shell-routed codex subcommand; got: %s", cmd)
	}
	if !strings.HasSuffix(cmd, inst.Command) {
		t.Fatalf("expected the codex subcommand to run verbatim with no flag injection, got: %s", cmd)
	}
	// Review finding on PR #1821: AGENTDECK_TOOL must reflect the real
	// spawned binary ("codex", from MatchTool), not the routing tool
	// ("shell") — hook subprocesses that key off AGENTDECK_TOOL=="codex"
	// would otherwise silently stop matching for every shell-routed codex
	// subcommand.
	if !strings.Contains(cmd, "AGENTDECK_TOOL=codex") {
		t.Fatalf("expected AGENTDECK_TOOL=codex (the matched binary identity), not the routing "+
			"Tool=%q; got: %s", inst.Tool, cmd)
	}
	if strings.Contains(cmd, "AGENTDECK_TOOL=shell") {
		t.Fatalf("AGENTDECK_TOOL must not leak the routing tool name \"shell\"; got: %s", cmd)
	}
}

// TestIssue1800_ShellPassthroughCodex_ShellEscapesTitle is the regression
// test for the Major/Security review finding on PR #1821: an earlier
// version of buildShellPassthroughCommand's codex branch injected
// AGENTDECK_TITLE with Go's %q, which quotes for Go syntax, not shell
// syntax — a double-quoted string is still subject to shell $(...) /
// backtick command substitution, so a session title an attacker (or
// careless user) set to something like `x$(touch /tmp/pwned)` would run
// arbitrary code at every spawn of a shell-routed codex subcommand
// session. shellescape.Quote (single-quoted, escape-safe) closes that.
func TestIssue1800_ShellPassthroughCodex_ShellEscapesTitle(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	inst := NewInstanceWithTool("codex-sub", t.TempDir(), "shell")
	inst.Title = "x$(touch /tmp/pwned)"
	inst.Command = "codex mcp list"

	cmd := inst.buildShellPassthroughCommand(inst.Command)
	if strings.Contains(cmd, "$(touch") {
		t.Fatalf("session title's $(...) must be shell-escaped, not passed through live "+
			"inside a double-quoted AGENTDECK_TITLE; got: %s", cmd)
	}
}

// Note: a custom tool's subcommand-shaped --cmd (e.g. "reviewbot serve")
// never reaches buildShellPassthroughCommand at all — resolveSessionCommand
// (cmd/agent-deck/cli_utils.go) only routes claude/codex through the
// no-flag-injection passthrough (see its doc for why only those two
// builtins need it); a custom tool keeps the ordinary wrapper-suffix path,
// which already resolves through toolDef.Command correctly. That is
// covered by cli_utils_test.go's
// TestResolveSessionCommand_CustomToolSubcommand_UsesWrapperSuffix, in the
// package that owns resolveSessionCommand — this package only tests the
// Tool=="shell" dispatch a *claude/codex* subcommand-passthrough instance
// takes, so there is no custom-tool test here.

// Note: the REFUSE-path coverage for an unparseable --cmd (unterminated
// quote) lives in cmd/agent-deck/cli_utils_test.go's
// TestResolveSessionCommand_RefusesUnparseableExtraArgs — that's the
// package that owns resolveSessionCommand and the tokenizer.
