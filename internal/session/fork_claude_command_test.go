package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Forks used to exec the literal `claude` binary whatever [claude].command
// said, so a session launched through a wrapper (a plugin injector, an
// account switcher) forked into a bare claude without it. The fork builder
// now uses the configured command when its first word is a real executable,
// and keeps the literal binary for an alias, which a non-interactive
// `bash -c` cannot see.

// writeRecordingWrapper writes an executable at path that records its argv
// (one per line) to record instead of launching anything.
func writeRecordingWrapper(t *testing.T, path, record string) {
	t.Helper()
	stub := "#!/bin/sh\n{ for a in \"$@\"; do printf '%s\\n' \"$a\"; done; } > " + shellQuote(record) + "\n"
	if err := os.WriteFile(path, []byte(stub), 0o755); err != nil {
		t.Fatalf("write wrapper: %v", err)
	}
}

func newForkSource(t *testing.T) *Instance {
	t.Helper()
	return &Instance{
		ID:               "test-fork-cmd",
		Title:            "src",
		ProjectPath:      t.TempDir(),
		Tool:             "claude",
		ClaudeSessionID:  "session-abc-123",
		ClaudeDetectedAt: time.Now(),
	}
}

// TestFork_ConfiguredWrapperPathIsExecuted: an absolute wrapper path in
// [claude].command is what the fork command runs, and running it hands the
// wrapper the fork's claude arguments.
func TestFork_ConfiguredWrapperPathIsExecuted(t *testing.T) {
	binDir := t.TempDir()
	wrapper := filepath.Join(binDir, "claude-stack")
	record := filepath.Join(t.TempDir(), "record")
	writeRecordingWrapper(t, wrapper, record)
	withIsolatedHomeAndConfig(t, "[claude]\ncommand = "+tomlQuote(wrapper)+"\n")

	src := newForkSource(t)
	cmdStr, err := src.Fork("forked", "")
	if err != nil {
		t.Fatalf("Fork() error = %v", err)
	}
	if !strings.Contains(cmdStr, "exec "+wrapper+" --session-id ") {
		t.Fatalf("fork command does not exec the configured wrapper %q:\n%s", wrapper, cmdStr)
	}
	if strings.Contains(cmdStr, "exec claude ") {
		t.Errorf("fork command still execs literal claude:\n%s", cmdStr)
	}

	cmd := exec.Command("bash", "-c", cmdStr)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fork command failed: %v (%s)\ncmd: %s", err, out, cmdStr)
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("wrapper never ran: %v\ncmd: %s", err, cmdStr)
	}
	if got := strings.Join(strings.Fields(string(data)), " "); !strings.Contains(got, "--resume session-abc-123 --fork-session") {
		t.Errorf("wrapper argv = %q, want --resume session-abc-123 --fork-session", got)
	}
}

// TestFork_ConfiguredCommandWithAssignmentsIsExecuted: leading VAR=value
// words in [claude].command reach the wrapper's environment, and the wrapper
// runs. The fork splices the command after `exec`, where a bare assignment
// word is taken for the program name; a value with a path separator must
// not hide the wrapper from the executable lookup either.
func TestFork_ConfiguredCommandWithAssignmentsIsExecuted(t *testing.T) {
	cases := []struct {
		name, variable, value string
	}{
		{"plain value", "FLAG", "1"},
		{"path value", "CLAUDE_CONFIG_DIR", "/tmp/account"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wrapper := filepath.Join(t.TempDir(), "claude-stack")
			record := filepath.Join(t.TempDir(), "record")
			stub := "#!/bin/sh\n{ printf '%s=%s\\n' " + tc.variable + " \"$" + tc.variable + "\"; " +
				"for a in \"$@\"; do printf '%s\\n' \"$a\"; done; } > " + shellQuote(record) + "\n"
			if err := os.WriteFile(wrapper, []byte(stub), 0o755); err != nil {
				t.Fatalf("write wrapper: %v", err)
			}
			command := tc.variable + "=" + tc.value + " " + wrapper
			withIsolatedHomeAndConfig(t, "[claude]\ncommand = "+tomlQuote(command)+"\n")

			cmdStr, err := newForkSource(t).Fork("forked", "")
			if err != nil {
				t.Fatalf("Fork() error = %v", err)
			}
			if strings.Contains(cmdStr, "exec claude ") {
				t.Fatalf("fork command fell back to literal claude:\n%s", cmdStr)
			}
			if out, err := exec.Command("bash", "-c", cmdStr).CombinedOutput(); err != nil {
				t.Fatalf("fork command failed: %v (%s)\ncmd: %s", err, out, cmdStr)
			}
			data, err := os.ReadFile(record)
			if err != nil {
				t.Fatalf("wrapper never ran: %v\ncmd: %s", err, cmdStr)
			}
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			if want := tc.variable + "=" + tc.value; lines[0] != want {
				t.Errorf("wrapper environment has %q, want %q\ncmd: %s", lines[0], want, cmdStr)
			}
			if got := strings.Join(lines[1:], " "); !strings.Contains(got, "--resume session-abc-123 --fork-session") {
				t.Errorf("wrapper argv = %q, want --resume session-abc-123 --fork-session", got)
			}
		})
	}
}

// TestFork_ConfiguredCommandLineOnPathIsExecuted: a command line whose first
// word is found on PATH keeps its own arguments ahead of the fork's.
func TestFork_ConfiguredCommandLineOnPathIsExecuted(t *testing.T) {
	binDir := t.TempDir()
	writeRecordingWrapper(t, filepath.Join(binDir, "fake-launcher"), filepath.Join(t.TempDir(), "record"))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	withIsolatedHomeAndConfig(t, "[claude]\ncommand = \"fake-launcher launch claude --\"\n")

	cmdStr, err := newForkSource(t).Fork("forked", "")
	if err != nil {
		t.Fatalf("Fork() error = %v", err)
	}
	if !strings.Contains(cmdStr, "exec fake-launcher launch claude -- --session-id ") {
		t.Errorf("fork command does not exec the configured command line:\n%s", cmdStr)
	}
}

// TestFork_ConfiguredAliasFallsBackToClaude: an alias is not an executable,
// and the fork command runs under a non-interactive bash that has no
// aliases, so the fork keeps the literal claude binary.
func TestFork_ConfiguredAliasFallsBackToClaude(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	withIsolatedHomeAndConfig(t, "[claude]\ncommand = \"cdw\"\n")

	cmdStr, err := newForkSource(t).Fork("forked", "")
	if err != nil {
		t.Fatalf("Fork() error = %v", err)
	}
	if !strings.Contains(cmdStr, "exec claude --session-id ") {
		t.Errorf("alias command must fall back to literal claude:\n%s", cmdStr)
	}
	if strings.Contains(cmdStr, "cdw") {
		t.Errorf("fork command must not reference the alias:\n%s", cmdStr)
	}
}

// TestFork_ConfiguredWrapperSkipsConfigDirExport: with an explicit
// config_dir, the fork follows the same #1822 F3 gate as a fresh start. A
// wrapper resolves the account itself, so neither exports CLAUDE_CONFIG_DIR;
// literal claude (alias fallback) still gets it.
func TestFork_ConfiguredWrapperSkipsConfigDirExport(t *testing.T) {
	binDir := t.TempDir()
	wrapper := filepath.Join(binDir, "claude-stack")
	writeRecordingWrapper(t, wrapper, filepath.Join(t.TempDir(), "record"))
	configDir := filepath.Join(t.TempDir(), "claude-work")

	cases := []struct {
		name       string
		command    string
		wantExport bool
	}{
		{"wrapper", wrapper, false},
		{"alias", "cdw-not-on-path", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withIsolatedHomeAndConfig(t, "[claude]\ncommand = "+tomlQuote(tc.command)+
				"\nconfig_dir = "+tomlQuote(configDir)+"\n")
			src := newForkSource(t)
			cmdStr, err := src.Fork("forked", "")
			if err != nil {
				t.Fatalf("Fork() error = %v", err)
			}
			if got := strings.Contains(cmdStr, "export CLAUDE_CONFIG_DIR="); got != tc.wantExport {
				t.Errorf("CLAUDE_CONFIG_DIR exported = %v, want %v:\n%s", got, tc.wantExport, cmdStr)
			}
			fresh := NewInstanceWithGroupAndTool("fresh", src.ProjectPath, "", "claude")
			if tc.command == wrapper {
				if got := strings.Contains(fresh.buildClaudeCommand("claude"), "export CLAUDE_CONFIG_DIR="); got != tc.wantExport {
					t.Errorf("fresh start disagrees with fork: CLAUDE_CONFIG_DIR exported = %v", got)
				}
			}
		})
	}
}

func tomlQuote(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}
