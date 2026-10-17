package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Issue #2556 (reporter @Djeeteg007): with exit_to_shell enabled, `/exit` in a
// pi session closed the pane instead of dropping to a shell, because "pi" was
// missing from builtinAgentTools, so wrapExitToShell skipped it. omp (Oh My
// Pi) launches through the same command shape and was missing for the same
// reason.
//
// These tests run the wrapped pane command through bash with a fake agent and
// a fake $SHELL, so they check what the pane actually does when the agent
// exits, not only the command string.

// exitToShellFakeEnv puts a fake agent binary named tool on PATH that prints
// its arguments, and a fake $SHELL that prints a marker, and returns the env
// to run a pane command with.
func exitToShellFakeEnv(t *testing.T, tool string) []string {
	t.Helper()
	bin := t.TempDir()
	agent := "#!/bin/sh\necho \"AGENT_RAN $*\"\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, tool), []byte(agent), 0o755); err != nil {
		t.Fatal(err)
	}
	fakeShell := filepath.Join(bin, "fake-shell")
	if err := os.WriteFile(fakeShell, []byte("#!/bin/sh\necho \"SHELL_FALLBACK $*\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return []string{
		"PATH=" + bin + ":/usr/local/bin:/usr/bin:/bin",
		"HOME=" + os.Getenv("HOME"),
		"SHELL=" + fakeShell,
	}
}

func runPaneCommand(t *testing.T, command, dir string, env []string) string {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	cmd := exec.Command("bash", "-c", command)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("pane command failed: %v\ncommand: %s\noutput:\n%s", err, command, out)
	}
	return string(out)
}

func TestExitToShell_PiFamilyFallsBackToShellOnExit(t *testing.T) {
	for _, tool := range []string{"pi", "omp"} {
		t.Run(tool, func(t *testing.T) {
			exitToShellTestEnv(t)

			dir := t.TempDir()
			inst := NewInstanceWithTool("e2s-"+tool, dir, tool)
			inst.ExitToShell = boolPtr(true)

			var raw string
			if tool == "pi" {
				raw = inst.buildPiCommand("pi")
			} else {
				raw = inst.buildOMPCommand("omp")
			}
			wrapped := inst.wrapExitToShell(raw)
			if !strings.HasSuffix(wrapped, exitToShellSuffix) {
				t.Fatalf("%s command must end with %q when exit_to_shell is on, got:\n%s", tool, exitToShellSuffix, wrapped)
			}

			out := runPaneCommand(t, wrapped, dir, exitToShellFakeEnv(t, tool))
			if !strings.Contains(out, "AGENT_RAN") {
				t.Fatalf("%s did not run, output:\n%s", tool, out)
			}
			if !strings.Contains(out, "SHELL_FALLBACK -i") {
				t.Fatalf("pane did not fall back to an interactive shell after %s exited, output:\n%s", tool, out)
			}
		})
	}
}

// The flag stays opt-in: with it off the pi command is byte-for-byte unchanged.
func TestExitToShell_PiDisabledLeavesCommandUnchanged(t *testing.T) {
	exitToShellTestEnv(t)

	inst := NewInstanceWithTool("e2s-pi-off", t.TempDir(), "pi")
	raw := inst.buildPiCommand("pi")
	if got := inst.wrapExitToShell(raw); got != raw {
		t.Fatalf("flag OFF must not alter the pi command.\n raw:     %s\n wrapped: %s", raw, got)
	}
}

// A pi fork's first start runs `find ... -exec ls -t {} +` to pick the parent
// session file. The launcher strip must only remove an `exec ` in command
// position, never the `exec ` inside `-exec`, or the fork finds no file.
func TestExitToShell_PiForkKeepsFindExec(t *testing.T) {
	exitToShellTestEnv(t)

	dir := t.TempDir()
	parent := NewInstanceWithTool("e2s-pi-parent", dir, "pi")
	home, _ := os.UserHomeDir()
	sessionDir := filepath.Join(home, ".pi", "agent-deck", parent.ID)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sessionFile := filepath.Join(sessionDir, "parent.jsonl")
	if err := os.WriteFile(sessionFile, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	forked, cmd, err := parent.CreateForkedPiInstance("e2s-pi-fork", "")
	if err != nil {
		t.Fatalf("CreateForkedPiInstance: %v", err)
	}
	forked.ExitToShell = boolPtr(true)
	wrapped := forked.wrapExitToShell(cmd)
	if !strings.Contains(wrapped, "-exec ls -t {} +") {
		t.Fatalf("wrap must keep find's -exec intact, got:\n%s", wrapped)
	}

	out := runPaneCommand(t, wrapped, dir, exitToShellFakeEnv(t, "pi"))
	if !strings.Contains(out, "AGENT_RAN --fork "+sessionFile) {
		t.Fatalf("fork did not resolve the parent session file, output:\n%s", out)
	}
	if !strings.Contains(out, "SHELL_FALLBACK -i") {
		t.Fatalf("forked pi pane did not fall back to a shell, output:\n%s", out)
	}
}

// Every built-in tool in the registry must be either an exit_to_shell agent or
// listed here with the reason it is not, so a newly added harness cannot be
// silently left out again.
func TestExitToShell_EveryBuiltinToolClassified(t *testing.T) {
	notExitToShell := map[string]string{
		"shell":    "not an agent; the pane already is a shell",
		"aider":    "no launch builder; started as a plain passthrough command",
		"deepseek": "headless profile answers one task and exits by design",
	}
	for _, bt := range builtinTools() {
		_, excluded := notExitToShell[bt.Name]
		if builtinAgentTools[bt.Name] == excluded {
			t.Errorf("built-in tool %q must be in exactly one of builtinAgentTools or the documented exclusions (agent=%v, excluded=%v)",
				bt.Name, builtinAgentTools[bt.Name], excluded)
		}
	}
}
