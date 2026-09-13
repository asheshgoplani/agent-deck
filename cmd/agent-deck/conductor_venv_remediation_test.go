// The PEP 668 remediation told the user to build a venv at a path the binary
// never looks at.
//
// findPython3 prefers <ConductorDir>/venv/bin/python3. The message printed
// ~/.agent-deck/conductor/.venv — dot-prefixed — so a user who followed it
// verbatim got a venv agent-deck could not see, and the message itself
// conceded the point by telling them to hand-edit the launchd plist.
//
// Worse, following it did not help even with the right path: installPythonDeps
// shelled out to "python3" from PATH, so re-running setup went straight back to
// the externally-managed interpreter, failed the same way, and refused to
// install the bridge daemon again.

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestFormatPipFailureMessage_VenvPathIsTheOneTheResolverPrefers(t *testing.T) {
	venvDir := "/home/u/.local/share/agent-deck/conductor/venv"
	msg := formatPipFailureMessage(pipFailureDiagnostic{
		Kind:            pipFailurePEP668,
		InterpreterPath: "/opt/homebrew/opt/python@3.14/bin/python3.14",
		InterpreterVer:  "3.14.7",
		Packages:        []string{"toml", "aiogram"},
		VenvDir:         venvDir,
	})

	for _, want := range []string{
		"python3 -m venv " + venvDir,
		filepath.Join(venvDir, "bin", "pip") + " install toml aiogram",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q\n----- full message -----\n%s", want, msg)
		}
	}

	// The specific regression: a dot-prefixed venv is not on findPython3's
	// search path, so the message must never name one.
	if strings.Contains(msg, "/.venv") {
		t.Errorf("message still points at a dot-prefixed venv findPython3 ignores\n----- full message -----\n%s", msg)
	}

	// The hand-edit instruction only existed because the advertised path was
	// undiscoverable. With the resolver's own path there is nothing to edit.
	if strings.Contains(strings.ToLower(msg), "edit ~/library/launchagents") {
		t.Errorf("message still tells the user to hand-edit the plist\n----- full message -----\n%s", msg)
	}
}

// The formatter is reachable from a path that does not populate VenvDir; it
// must still print a real venv path rather than an empty one.
func TestFormatPipFailureMessage_VenvPathFallsBackWhenUnset(t *testing.T) {
	msg := formatPipFailureMessage(pipFailureDiagnostic{
		Kind:     pipFailurePEP668,
		Packages: []string{"toml"},
	})

	if strings.Contains(msg, "python3 -m venv \n") {
		t.Fatalf("message printed an empty venv path\n----- full message -----\n%s", msg)
	}
	want := session.ConductorVenvDir()
	if want == "" {
		want = filepath.Join(os.Getenv("HOME"), ".agent-deck", "conductor", "venv")
	}
	for _, line := range strings.Split(msg, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "python3 -m venv ") {
			command := "python3() { printf '%s\\n' \"$@\"; }; " + line
			out, err := exec.Command("sh", "-c", command).CombinedOutput()
			if err != nil || string(out) != "-m\nvenv\n"+want+"\n" {
				t.Fatalf("fallback command does not target the resolver's directory: err=%v, got %q, want %q", err, out, want)
			}
			return
		}
	}
	t.Fatal("message contains no venv creation command")
}

// probePythonInterpreter reports the interpreter the installer actually used,
// so the "Python:" line names the interpreter that failed rather than whatever
// PATH resolves at render time.
func TestProbePythonInterpreter_UsesTheGivenInterpreter(t *testing.T) {
	path, version := probePythonInterpreter("")
	if path == "" && version == "" {
		t.Skip("no python3 on PATH in this environment")
	}
	if path == "" {
		t.Fatalf("probe returned empty path with version %q", version)
	}
}

// Exercise the printed commands with a real shell and harmless executables.
// A valid configured directory must remain one literal argument.
func TestFormatPipFailureMessage_VenvCommandsPreserveLiteralPaths(t *testing.T) {
	for _, name := range []string{"plain", "with spaces", "literal $EXPAND_ME", "single'quote"} {
		t.Run(name, func(t *testing.T) {
			venvDir := filepath.Join(t.TempDir(), name, "venv")
			binDir := filepath.Join(venvDir, "bin")
			if err := os.MkdirAll(binDir, 0o700); err != nil {
				t.Fatal(err)
			}
			pip := filepath.Join(binDir, "pip")
			if err := os.WriteFile(pip, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("EXPAND_ME", "wrong-directory")
			msg := formatPipFailureMessage(pipFailureDiagnostic{
				Kind: pipFailurePEP668, Packages: []string{"toml", "aiogram"}, VenvDir: venvDir,
			})
			commands := 0
			for _, line := range strings.Split(msg, "\n") {
				line = strings.TrimSpace(line)
				var want string
				if strings.HasPrefix(line, "python3 -m venv ") {
					want = "-m\nvenv\n" + venvDir + "\n"
					line = "python3() { printf '%s\\n' \"$@\"; }; " + line
				} else if strings.Contains(line, "/bin/pip") && strings.HasSuffix(line, " install toml aiogram") {
					want = "install\ntoml\naiogram\n"
				} else {
					continue
				}
				commands++
				out, err := exec.Command("sh", "-c", line).CombinedOutput()
				if err != nil || string(out) != want {
					t.Errorf("repair command did not preserve its arguments: err=%v, got %q, want %q", err, out, want)
				}
			}
			if commands != 2 {
				t.Fatalf("found %d repair commands, want 2", commands)
			}
		})
	}
}
