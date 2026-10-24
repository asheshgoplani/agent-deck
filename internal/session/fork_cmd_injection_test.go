package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runForkCommandWithFakeClaude executes a built fork command in a real bash
// with a stub `claude` first on PATH. The stub records the directory it ran
// in and its argv (one per line) instead of launching anything, so the test
// observes exactly what the shell made of the command.
func runForkCommandWithFakeClaude(t *testing.T, cmdStr, runDir string) (pwd string, args []string) {
	t.Helper()
	binDir := t.TempDir()
	record := filepath.Join(t.TempDir(), "record")
	stub := "#!/bin/sh\n{ printf '%s\\n' \"$PWD\"; for a in \"$@\"; do printf '%s\\n' \"$a\"; done; } > " +
		shellQuote(record) + "\n"
	if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte(stub), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	cmd := exec.Command("bash", "-c", cmdStr)
	cmd.Dir = runDir
	cmd.Env = append(os.Environ(), "PATH="+binDir+":"+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fork command failed: %v (%s)\ncmd: %s", err, out, cmdStr)
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("stub claude never ran: %v\ncmd: %s", err, cmdStr)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	return lines[0], lines[1:]
}

// TestFork_ProjectPathWithQuoteDoesNotInject (LOC-7): the fork command used to
// interpolate the working directory into `cd '%s'` unescaped, so a project or
// worktree path containing a single quote broke out of the quoting and ran
// shell. The path must reach cd as ONE argument and nothing may execute.
func TestFork_ProjectPathWithQuoteDoesNotInject(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	base := t.TempDir()
	evil := filepath.Join(base, "x'; touch PWNED; '")
	if err := os.MkdirAll(evil, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	inst := &Instance{
		ID:               "test-loc7",
		Title:            "src",
		ProjectPath:      evil,
		Tool:             "claude",
		ClaudeSessionID:  "session-abc-123",
		ClaudeDetectedAt: time.Now(),
	}
	cmdStr, err := inst.Fork("forked", "")
	if err != nil {
		t.Fatalf("Fork() error = %v", err)
	}

	pwd, args := runForkCommandWithFakeClaude(t, cmdStr, base)

	if _, err := os.Stat(filepath.Join(base, "PWNED")); err == nil {
		t.Errorf("injected command executed: PWNED created\ncmd: %s", cmdStr)
	}
	if !sameClaudeCWD(pwd, evil) {
		t.Errorf("fork ran in %q, want %q\ncmd: %s", pwd, evil, cmdStr)
	}
	if got := strings.Join(args, " "); !strings.Contains(got, "--resume session-abc-123 --fork-session") {
		t.Errorf("claude argv = %q, want --resume session-abc-123 --fork-session", args)
	}
}

// TestFork_SessionIDWithShellMetacharsDoesNotInject (LOC-7): the parent's
// conversation id comes off disk/hook payloads and was spliced bare after
// --resume; it must reach claude as one literal argument.
func TestFork_SessionIDWithShellMetacharsDoesNotInject(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	base := t.TempDir()
	sid := "abc; touch PWNED"

	inst := &Instance{
		ID:               "test-loc7-sid",
		Title:            "src",
		ProjectPath:      base,
		Tool:             "claude",
		ClaudeSessionID:  sid,
		ClaudeDetectedAt: time.Now(),
	}
	cmdStr, err := inst.Fork("forked", "")
	if err != nil {
		t.Fatalf("Fork() error = %v", err)
	}

	_, args := runForkCommandWithFakeClaude(t, cmdStr, base)

	if _, err := os.Stat(filepath.Join(base, "PWNED")); err == nil {
		t.Errorf("injected command executed: PWNED created\ncmd: %s", cmdStr)
	}
	found := false
	for k := 0; k+1 < len(args); k++ {
		if args[k] == "--resume" && args[k+1] == sid {
			found = true
		}
	}
	if !found {
		t.Errorf("claude argv = %q, want --resume %q as one argument", args, sid)
	}
}

// TestFork_OrdinaryPathCommandUnchanged pins that hardening does not alter the
// command for an ordinary path and id.
func TestFork_OrdinaryPathCommandUnchanged(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	base := t.TempDir()
	inst := &Instance{
		ID:               "test-loc7-plain",
		Title:            "src",
		ProjectPath:      base,
		Tool:             "claude",
		ClaudeSessionID:  "session-abc-123",
		ClaudeDetectedAt: time.Now(),
	}
	cmdStr, err := inst.Fork("forked", "")
	if err != nil {
		t.Fatalf("Fork() error = %v", err)
	}
	if !strings.HasPrefix(cmdStr, "cd '"+base+"' && ") {
		t.Errorf("ordinary path cd prefix changed: %s", cmdStr)
	}
	if !strings.Contains(cmdStr, " --resume session-abc-123 --fork-session") {
		t.Errorf("ordinary resume id formatting changed: %s", cmdStr)
	}
	pwd, _ := runForkCommandWithFakeClaude(t, cmdStr, t.TempDir())
	if !sameClaudeCWD(pwd, base) {
		t.Errorf("fork ran in %q, want %q", pwd, base)
	}
}
