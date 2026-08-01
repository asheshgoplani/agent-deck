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
// choice, once built into an actual shell command by
// buildClaudeCommand/prepareCommand, delivers the subcommand to the binary
// untouched: no --session-id, no --dangerously-skip-permissions spliced in
// before "remote-control".
//
// Before the fix, resolveSessionCommand instead produced Command="claude",
// Wrapper="{command} remote-control --name rc-test", and
// buildClaudeCommandWithMessage's {command} substitution landed the
// subcommand AFTER the injected root flags — reproduced independently below
// in TestIssue1800_PreFix_WrapperShape_DemotesSubcommand.
func TestIssue1800_ClaudeSubcommand_RunsWithoutFlagInjection(t *testing.T) {
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", t.TempDir())
	os.Unsetenv("CLAUDE_CONFIG_DIR")
	ClearUserConfigCache()
	t.Cleanup(func() {
		os.Setenv("HOME", origHome)
		ClearUserConfigCache()
	})

	binDir, logPath := buildFakeBinArgvCapture(t, "claude")

	// Mirrors resolveSessionCommand's fixed output for
	// `-c "claude remote-control --name rc-test"`.
	inst := NewInstanceWithTool("rc-collide", t.TempDir(), "shell")
	inst.Command = "claude remote-control --name rc-test"
	inst.Wrapper = ""

	cmd := inst.buildClaudeCommand(inst.Command)
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
	os.Setenv("HOME", t.TempDir())
	os.Unsetenv("CLAUDE_CONFIG_DIR")
	ClearUserConfigCache()
	t.Cleanup(func() {
		os.Setenv("HOME", origHome)
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

// Note: the REFUSE-path coverage for an unparseable --cmd (unterminated
// quote) lives in cmd/agent-deck/cli_utils_test.go's
// TestResolveSessionCommand_RefusesUnparseableExtraArgs — that's the
// package that owns resolveSessionCommand and the tokenizer.
