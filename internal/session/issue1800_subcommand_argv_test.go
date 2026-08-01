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
	inst.SubcommandPassthrough = true

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
		Title:                 "launch-child",
		Tool:                  "shell",
		Command:               "claude remote-control --name rc-test",
		ProjectPath:           "/tmp",
		SubcommandPassthrough: true,
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

	// Same Command text as inst above, but SubcommandPassthrough is unset —
	// e.g. a pre-existing/TUI-created custom-command session that merely
	// happens to run `claude remote-control ...` and was never routed
	// through resolveSessionCommand's #1821 passthrough. This must NOT be
	// treated as a claude spawn either: instInvokesClaudeBinary's whole-line
	// MatchTool match previously misfired here too (Claude review, PR #1821
	// MEDIUM #3 / same root cause as HIGH #1's retroactive-capture finding).
	untaggedSameCommand := &Instance{
		Title:       "untagged",
		Tool:        "shell",
		Command:     "claude remote-control --name rc-test",
		ProjectPath: "/tmp",
	}
	if got := untaggedSameCommand.buildEnvSourceCommand(); strings.Contains(got, "TELEGRAM_STATE_DIR") {
		t.Fatalf("a Tool==shell instance whose Command merely mentions claude, but was never "+
			"routed through resolveSessionCommand's passthrough (SubcommandPassthrough unset), "+
			"must not get the claude-only TELEGRAM strip\nbuildEnvSourceCommand() = %q", got)
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
	inst.SubcommandPassthrough = true

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
// careless user) set to something like `x$(touch <sentinel>)` would run
// arbitrary code at every spawn of a shell-routed codex subcommand
// session. shellescape.Quote (single-quoted) closes that: inside single
// quotes POSIX shells never expand $(...), so the substring is still
// present in the built command text (that alone proves nothing) — this
// test actually EXECUTES the built command through `sh -c`, exactly as
// prepareCommand's real dispatch would, and asserts the embedded
// $(touch <sentinel>) never ran.
func TestIssue1800_ShellPassthroughCodex_ShellEscapesTitle(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	binDir, logPath := buildFakeBinArgvCapture(t, "codex")
	sentinel := filepath.Join(t.TempDir(), "pwned")

	inst := NewInstanceWithTool("codex-sub", t.TempDir(), "shell")
	inst.Title = "x$(touch " + sentinel + ")"
	inst.Command = "codex mcp list"
	inst.SubcommandPassthrough = true

	cmd := inst.buildShellPassthroughCommand(inst.Command)
	wrapped, _, err := inst.prepareCommand(cmd)
	if err != nil {
		t.Fatalf("prepareCommand: %v", err)
	}

	runBuiltCommandCapturingArgv(t, wrapped, binDir, logPath)

	if _, statErr := os.Stat(sentinel); statErr == nil {
		t.Fatalf("session title's $(...) executed via command substitution — AGENTDECK_TITLE "+
			"injection is NOT shell-safe; command was: %s", wrapped)
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

// TestIssue1821_ShellPassthroughClaude_HonorsConfiguredCommand is the
// regression test for the Codex bot P1 finding on the allowlist-scoped fix
// (PR #1821, reviewed commit 520407c): the normal claude spawn path
// (buildClaudeCommandWithMessage) resolves a configured account-switcher
// command (conductor/group/global [claude].command, e.g. "cdw"/"cdp") even
// when the user literally typed "claude" — losing that for a
// subcommand-shaped --cmd would silently run the wrong binary/account for
// e.g. `claude remote-control`. buildShellPassthroughCommand must swap only
// the leading "claude" word for the resolved override, leaving the
// subcommand and its args untouched.
func TestIssue1821_ShellPassthroughClaude_HonorsConfiguredCommand(t *testing.T) {
	cfg := &UserConfig{
		MCPs:       make(map[string]MCPDef),
		Conductors: map[string]ConductorOverrides{},
		Groups:     map[string]GroupSettings{},
		Claude:     ClaudeSettings{Command: "cdw"},
	}
	defer resetUserConfigCache(t, cfg)()

	inst := &Instance{
		Title:                 "launch-child",
		Tool:                  "shell",
		Command:               "claude remote-control --name rc-test",
		ProjectPath:           "/tmp",
		SubcommandPassthrough: true,
	}

	got := inst.buildShellPassthroughCommand(inst.Command)
	if !strings.HasSuffix(got, "cdw remote-control --name rc-test") {
		t.Fatalf("expected the configured [claude].command override (\"cdw\") to replace the "+
			"literal \"claude\" the user typed while preserving the subcommand tail; got: %s", got)
	}
	if strings.Contains(got, "--session-id") {
		t.Fatalf("resolving the configured command must not resurrect flag injection; got: %s", got)
	}
}

// TestIssue1821_ShellPassthroughCodex_HonorsConfiguredCommand is the codex
// half of the same finding.
func TestIssue1821_ShellPassthroughCodex_HonorsConfiguredCommand(t *testing.T) {
	cfg := &UserConfig{
		MCPs:       make(map[string]MCPDef),
		Conductors: map[string]ConductorOverrides{},
		Groups:     map[string]GroupSettings{},
		Codex:      CodexSettings{Command: "codex-nightly"},
	}
	defer resetUserConfigCache(t, cfg)()

	inst := &Instance{
		Title:                 "codex-sub",
		Tool:                  "shell",
		Command:               "codex mcp list",
		ProjectPath:           "/tmp",
		SubcommandPassthrough: true,
	}

	got := inst.buildShellPassthroughCommand(inst.Command)
	if !strings.HasSuffix(got, "codex-nightly mcp list") {
		t.Fatalf("expected the configured [codex].command override to replace the literal "+
			"\"codex\" the user typed; got: %s", got)
	}
}

// TestIssue1821_ShellPassthroughClaude_NeverRewritesExplicitBinary is the
// regression test for Codex bot P1 on the follow-up review of PR #1821:
// substituteResolvedBinary must never overwrite an explicitly configured
// non-canonical executable (e.g. "/opt/canary/claude") with the resolved
// [claude].command override, even though MatchTool classifies the line as
// claude-shaped (it substring-matches "claude"). On this maintainer's
// machines the configured override is often itself an account-selection
// wrapper, so substituting it in place of an operator-chosen path can
// silently start the subcommand under the wrong account. Only the bare
// literal "claude" is eligible for substitution.
func TestIssue1821_ShellPassthroughClaude_NeverRewritesExplicitBinary(t *testing.T) {
	cfg := &UserConfig{
		MCPs:       make(map[string]MCPDef),
		Conductors: map[string]ConductorOverrides{},
		Groups:     map[string]GroupSettings{},
		Claude:     ClaudeSettings{Command: "cdw"},
	}
	defer resetUserConfigCache(t, cfg)()

	inst := &Instance{
		Title:                 "canary-child",
		Tool:                  "shell",
		Command:               "/opt/canary/claude remote-control --name rc-test",
		ProjectPath:           "/tmp",
		SubcommandPassthrough: true,
	}

	got := inst.buildShellPassthroughCommand(inst.Command)
	if !strings.HasSuffix(got, "/opt/canary/claude remote-control --name rc-test") {
		t.Fatalf("expected the explicit /opt/canary/claude path to survive untouched "+
			"despite a configured [claude].command override (\"cdw\"); got: %s", got)
	}
	if strings.Contains(got, "cdw ") {
		t.Fatalf("configured override must not have been substituted for the explicit path; got: %s", got)
	}
}

// TestIssue1821_ShellPassthroughCodex_NeverRewritesExplicitBinary is the
// codex half of the same finding.
func TestIssue1821_ShellPassthroughCodex_NeverRewritesExplicitBinary(t *testing.T) {
	cfg := &UserConfig{
		MCPs:       make(map[string]MCPDef),
		Conductors: map[string]ConductorOverrides{},
		Groups:     map[string]GroupSettings{},
		Codex:      CodexSettings{Command: "codex-nightly"},
	}
	defer resetUserConfigCache(t, cfg)()

	inst := &Instance{
		Title:                 "canary-codex",
		Tool:                  "shell",
		Command:               "codex-canary mcp list",
		ProjectPath:           "/tmp",
		SubcommandPassthrough: true,
	}

	got := inst.buildShellPassthroughCommand(inst.Command)
	if !strings.HasSuffix(got, "codex-canary mcp list") {
		t.Fatalf("expected the explicit codex-canary binary to survive untouched despite "+
			"a configured [codex].command override (\"codex-nightly\"); got: %s", got)
	}
	if strings.Contains(got, "codex-nightly") {
		t.Fatalf("configured override must not have been substituted for the explicit binary; got: %s", got)
	}
}

// TestIssue1821_ShellPassthroughCodex_ExplicitBinarySkipsConfiguredHome pins
// the follow-up finding from the Codex CLI review of the P1-A fix itself:
// CODEX_HOME selects which account's Codex config/credentials the spawned
// process reads, so injecting it ahead of an explicitly configured
// non-canonical binary (e.g. "codex-nightly", itself potentially an
// account-selection wrapper) risks starting under the wrong account —
// the same class of bug as substituteResolvedBinary above.
// buildCodexCommand's own custom-command early-return already skips this
// injection for an explicit command; buildShellPassthroughCommand's codex
// branch must match that, injecting CODEX_HOME only for the bare literal
// "codex".
func TestIssue1821_ShellPassthroughCodex_ExplicitBinarySkipsConfiguredHome(t *testing.T) {
	cfg := &UserConfig{
		MCPs:       make(map[string]MCPDef),
		Conductors: map[string]ConductorOverrides{},
		Groups:     map[string]GroupSettings{},
		Codex:      CodexSettings{ConfigDir: "/opt/configured-codex-home"},
	}
	defer resetUserConfigCache(t, cfg)()

	explicit := &Instance{
		Title:                 "canary-codex-home",
		Tool:                  "shell",
		Command:               "codex-canary mcp list",
		ProjectPath:           "/tmp",
		SubcommandPassthrough: true,
	}
	got := explicit.buildShellPassthroughCommand(explicit.Command)
	if strings.Contains(got, "CODEX_HOME=") {
		t.Fatalf("configured CODEX_HOME must not be injected ahead of an explicit "+
			"non-canonical codex binary; got: %s", got)
	}

	literal := &Instance{
		Title:                 "literal-codex-home",
		Tool:                  "shell",
		Command:               "codex mcp list",
		ProjectPath:           "/tmp",
		SubcommandPassthrough: true,
	}
	got = literal.buildShellPassthroughCommand(literal.Command)
	if !strings.Contains(got, "CODEX_HOME=") {
		t.Fatalf("configured CODEX_HOME must still be injected for the bare literal "+
			"\"codex\" invocation; got: %s", got)
	}
}

// TestIssue1821_ShellPassthroughClaude_IncludesResolvedAccountHints is the
// regression test for the Codex bot P2 finding: buildShellPassthroughCommand
// must promise the same AGENTDECK_RESOLVED_CONFIG_DIR/GROUP/SOURCE hint vars
// the normal claude spawn path exports (buildBashExportPrefix ->
// buildResolvedAccountHintExports), so statusline/hook/fleet-placement
// consumers don't silently lose the resolved-account label for a
// shell-routed claude subcommand session.
func TestIssue1821_ShellPassthroughClaude_IncludesResolvedAccountHints(t *testing.T) {
	cfg := &UserConfig{
		MCPs:       make(map[string]MCPDef),
		Conductors: map[string]ConductorOverrides{},
		Groups:     map[string]GroupSettings{},
	}
	defer resetUserConfigCache(t, cfg)()

	inst := &Instance{
		Title:                 "launch-child",
		Tool:                  "shell",
		Command:               "claude remote-control --name rc-test",
		ProjectPath:           "/tmp",
		GroupPath:             "fleet/workers",
		SubcommandPassthrough: true,
	}

	got := inst.buildShellPassthroughCommand(inst.Command)
	for _, want := range []string{
		"AGENTDECK_RESOLVED_CONFIG_DIR=",
		"AGENTDECK_RESOLVED_GROUP=",
		"AGENTDECK_RESOLVED_SOURCE=",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %s in shell-routed claude subcommand command; got: %s", want, got)
		}
	}
}

// TestIssue1821_ShellPassthroughClaude_RetroactiveCaptureDenied is the
// regression test for the Claude review HIGH #1 finding on PR #1821:
// buildShellPassthroughCommand must return a Tool=="shell" instance's
// Command byte-identical when SubcommandPassthrough is unset — regardless
// of what the command text contains — because that's the only signal
// distinguishing "resolveSessionCommand's #1800/#1821 passthrough actually
// validated this" from "a pre-existing or TUI-created custom-command
// session whose text happens to mention claude/codex". Before this fix, a
// session created via the TUI's free-text custom-command dialog with
// e.g. `claude --resume <id>` (persisted as Tool="shell" long before this
// field existed) would be silently reclassified as a claude spawn on every
// restart: its leading word "claude" would get swapped for a configured
// account-switcher alias, and CLAUDE_CONFIG_DIR would be injected — running
// the session under a DIFFERENT account than the one it was created under,
// and breaking `--resume <id>` against the old account's session store.
func TestIssue1821_ShellPassthroughClaude_RetroactiveCaptureDenied(t *testing.T) {
	cfg := &UserConfig{
		MCPs:       make(map[string]MCPDef),
		Conductors: map[string]ConductorOverrides{},
		Groups:     map[string]GroupSettings{},
		Claude:     ClaudeSettings{Command: "cdw", ConfigDir: "/opt/configured-claude-home"},
	}
	defer resetUserConfigCache(t, cfg)()

	// SubcommandPassthrough is deliberately left unset (false): this
	// instance was never routed through resolveSessionCommand's passthrough
	// branch, exactly like a session created before that field existed or
	// via the TUI's free-text custom-command dialog.
	inst := &Instance{
		Title:       "pre-existing-custom-command",
		Tool:        "shell",
		Command:     "claude --resume abc123",
		ProjectPath: "/tmp",
	}

	got := inst.buildShellPassthroughCommand(inst.Command)
	if got != inst.Command {
		t.Fatalf("a Tool==shell instance with SubcommandPassthrough unset must be returned "+
			"byte-identical, regardless of what its Command mentions — no binary substitution, "+
			"no CLAUDE_CONFIG_DIR, no AGENTDECK_* injection; got: %q, want: %q", got, inst.Command)
	}
}

// TestIssue1821_ShellPassthroughSubcommand_WrapsInBashCForNonBashShells is
// the regression test for the Claude review HIGH #2 finding on PR #1821: a
// SubcommandPassthrough instance's built command contains bash/POSIX-only
// syntax (inline `VAR=val` assignments, `export ... && `, `[ -f x ] && . x`)
// that instance.prepareCommand must wrap in `bash -c '...'` — Tool=="shell"
// instances are delivered via tmux send-keys into the user's actual
// interactive login shell, which is not necessarily bash (e.g. fish, which
// has no `export` builtin and rejects bare `VAR=val` assignment syntax).
// Without the wrap, a fish user's pane received a fish syntax error instead
// of running the subcommand. This test proves both the wrap shape AND that
// the wrapped command still functionally reaches the target binary intact.
func TestIssue1821_ShellPassthroughSubcommand_WrapsInBashCForNonBashShells(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	binDir, logPath := buildFakeBinArgvCapture(t, "codex")

	inst := NewInstanceWithTool("fish-safety", t.TempDir(), "shell")
	inst.Command = "codex mcp list"
	inst.SubcommandPassthrough = true

	cmd := inst.buildShellPassthroughCommand(inst.Command)
	wrapped, _, err := inst.prepareCommand(cmd)
	if err != nil {
		t.Fatalf("prepareCommand: %v", err)
	}

	if !strings.HasPrefix(wrapped, "bash -c '") {
		t.Fatalf("expected a SubcommandPassthrough instance's command to be wrapped in "+
			"bash -c '...' so its bash-only injected syntax runs correctly under any login "+
			"shell (e.g. fish); got: %s", wrapped)
	}

	got := runBuiltCommandCapturingArgv(t, wrapped, binDir, logPath)
	want := "ARGV: mcp list"
	if got != want {
		t.Fatalf("fake codex received wrong argv through the bash -c wrap.\n  got:  %s\n  want: %s\n(command was: %s)", got, want, wrapped)
	}
}

// TestIssue1821_ShellPassthroughClaude_ConfigDirNotForcedOntoAlias is the
// regression test for the CodeRabbit review finding on PR #1821: when a
// configured [claude].command (conductor/group/global, e.g. "cdw") is a
// custom alias rather than the literal "claude" binary,
// buildShellPassthroughCommand must NOT inject CLAUDE_CONFIG_DIR even if an
// explicit config_dir also resolves for the instance — the alias is
// designed to manage its own config-dir selection, mirroring
// buildClaudeCommandWithMessage's own "the alias handles it" gate. Forcing
// CLAUDE_CONFIG_DIR onto the alias broke parity between the direct claude
// spawn path and this shell-passthrough path for any session combining a
// configured account-switcher command with an explicit config_dir.
func TestIssue1821_ShellPassthroughClaude_ConfigDirNotForcedOntoAlias(t *testing.T) {
	cfg := &UserConfig{
		MCPs:       make(map[string]MCPDef),
		Conductors: map[string]ConductorOverrides{},
		Groups:     map[string]GroupSettings{},
		Claude:     ClaudeSettings{Command: "cdw", ConfigDir: "/opt/configured-claude-home"},
	}
	defer resetUserConfigCache(t, cfg)()

	aliased := &Instance{
		Title:                 "aliased-config-dir",
		Tool:                  "shell",
		Command:               "claude remote-control --name rc-test",
		ProjectPath:           "/tmp",
		SubcommandPassthrough: true,
	}
	got := aliased.buildShellPassthroughCommand(aliased.Command)
	if strings.Contains(got, "CLAUDE_CONFIG_DIR=") {
		t.Fatalf("configured CLAUDE_CONFIG_DIR must not be injected ahead of a configured "+
			"[claude].command alias (\"cdw\") — the alias manages its own config dir; got: %s", got)
	}
	if !strings.HasSuffix(got, "cdw remote-control --name rc-test") {
		t.Fatalf("expected the configured alias to still replace the literal \"claude\"; got: %s", got)
	}

	// Regression guard: with no configured alias (literal "claude" resolves
	// to itself), an explicit config_dir must still be injected exactly as
	// before.
	cfg2 := &UserConfig{
		MCPs:       make(map[string]MCPDef),
		Conductors: map[string]ConductorOverrides{},
		Groups:     map[string]GroupSettings{},
		Claude:     ClaudeSettings{ConfigDir: "/opt/configured-claude-home"},
	}
	defer resetUserConfigCache(t, cfg2)()

	literal := &Instance{
		Title:                 "literal-config-dir",
		Tool:                  "shell",
		Command:               "claude remote-control --name rc-test",
		ProjectPath:           "/tmp",
		SubcommandPassthrough: true,
	}
	got = literal.buildShellPassthroughCommand(literal.Command)
	if !strings.Contains(got, "CLAUDE_CONFIG_DIR=") {
		t.Fatalf("configured CLAUDE_CONFIG_DIR must still be injected for the bare literal "+
			"\"claude\" invocation with no configured alias; got: %s", got)
	}
}
