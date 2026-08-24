package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The reported launch shape used to consume --no-wait as the account name,
// then continue into account fallback and session creation. Exercise the real
// command in a subprocess because its error path intentionally calls os.Exit.
func TestIssue2053AccountGuardCoversSessionCreationCommands(t *testing.T) {
	if command := os.Getenv("AGENT_DECK_ISSUE2053_HELPER"); command != "" {
		if command == "add" {
			handleAdd("_test", []string{".", "--account", "--quick"})
		} else {
			handleLaunch("_test", []string{".", "--account", "--no-wait"})
		}
		os.Exit(0)
	}

	commands := []struct {
		name     string
		nextFlag string
	}{
		{name: "add", nextFlag: "--quick"},
		{name: "launch", nextFlag: "--no-wait"},
	}
	for _, command := range commands {
		t.Run(command.name, func(t *testing.T) {
			home := t.TempDir()
			marker := filepath.Join(home, "tmux-invoked")
			binDir := t.TempDir()
			tmuxPath := filepath.Join(binDir, "tmux")
			if err := os.WriteFile(tmuxPath, []byte("#!/bin/sh\ntouch \"$AGENT_DECK_ISSUE2053_MARKER\"\nexit 0\n"), 0o755); err != nil {
				t.Fatal(err)
			}

			cmd := exec.Command(os.Args[0], "-test.run=^TestIssue2053AccountGuardCoversSessionCreationCommands$")
			cmd.Env = append(os.Environ(),
				"AGENT_DECK_TASK6_HELPER_PROCESS=1",
				"AGENT_DECK_ISSUE2053_HELPER="+command.name,
				"AGENT_DECK_ISSUE2053_MARKER="+marker,
				"HOME="+home,
				"XDG_CONFIG_HOME="+filepath.Join(home, "config"),
				"XDG_DATA_HOME="+filepath.Join(home, "data"),
				"XDG_STATE_HOME="+filepath.Join(home, "state"),
				"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
			)
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("%s accepted a flag-shaped account value; output:\n%s", command.name, out)
			}
			for _, want := range []string{"account", command.nextFlag, "needs a value"} {
				if !strings.Contains(string(out), want) {
					t.Errorf("error output %q does not explain %q", out, want)
				}
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("%s reached tmux after rejecting argv (stat error %v)", command.name, err)
			}
			entries, err := os.ReadDir(home)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("%s/account fallback wrote state before rejecting argv: %v", command.name, entries)
			}
		})
	}
}
