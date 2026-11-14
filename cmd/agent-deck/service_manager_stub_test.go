package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// serviceManagerCommands are the host service managers agent-deck shells out
// to when it installs or removes its daemons.
var serviceManagerCommands = []string{"launchctl", "systemctl"}

const serviceManagerStubMessage = "stubbed out in agent-deck cmd tests"

// stubServiceManagers puts failing launchctl and systemctl stubs first on
// PATH: a temp HOME does not isolate launchd or systemd --user, so a test
// running the real `conductor setup` replaces the developer's live daemons.
func stubServiceManagers() (cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "agent-deck-service-stubs-*")
	if err != nil {
		return nil, err
	}
	for _, name := range serviceManagerCommands {
		script := fmt.Sprintf("#!/bin/sh\necho %q >&2\nexit 1\n", name+": "+serviceManagerStubMessage)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			os.RemoveAll(dir)
			return nil, err
		}
	}
	if err := os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH")); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	return func() { os.RemoveAll(dir) }, nil
}

// TestServiceManagersStubbedForCLI checks that the binary under test resolves
// launchctl and systemctl to the failing stubs, never the host's.
func TestServiceManagersStubbedForCLI(t *testing.T) {
	env := agentDeckTestEnv(t.TempDir(), nil)
	for _, name := range serviceManagerCommands {
		cmd := exec.Command("/bin/sh", "-c", `command -v "$0"; "$0" list`, name)
		cmd.Env = env
		var stdout, stderr strings.Builder
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		exitErr, ok := err.(*exec.ExitError)
		if !ok || exitErr.ExitCode() != 1 {
			t.Fatalf("%s list: err = %v, want exit 1 from the stub\nstdout: %s\nstderr: %s", name, err, stdout.String(), stderr.String())
		}
		resolved := strings.TrimSpace(stdout.String())
		if !strings.HasPrefix(filepath.Base(filepath.Dir(resolved)), "agent-deck-service-stubs-") {
			t.Errorf("%s resolves to %q, want the stub dir", name, resolved)
		}
		if !strings.Contains(stderr.String(), serviceManagerStubMessage) {
			t.Errorf("%s stderr = %q, want the stub message", name, stderr.String())
		}
	}
}
