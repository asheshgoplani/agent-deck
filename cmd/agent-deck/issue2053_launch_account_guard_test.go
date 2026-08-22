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
func TestIssue2053LaunchRejectsFlagShapedAccountBeforeSideEffects(t *testing.T) {
	if os.Getenv("AGENT_DECK_ISSUE2053_HELPER") == "1" {
		handleLaunch("_test", []string{".", "--account", "--no-wait"})
		os.Exit(0)
	}

	home := t.TempDir()
	marker := filepath.Join(home, "tmux-invoked")
	binDir := t.TempDir()
	tmuxPath := filepath.Join(binDir, "tmux")
	if err := os.WriteFile(tmuxPath, []byte("#!/bin/sh\ntouch \"$AGENT_DECK_ISSUE2053_MARKER\"\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestIssue2053LaunchRejectsFlagShapedAccountBeforeSideEffects$")
	cmd.Env = append(os.Environ(),
		"AGENT_DECK_TASK6_HELPER_PROCESS=1",
		"AGENT_DECK_ISSUE2053_HELPER=1",
		"AGENT_DECK_ISSUE2053_MARKER="+marker,
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, "config"),
		"XDG_DATA_HOME="+filepath.Join(home, "data"),
		"XDG_STATE_HOME="+filepath.Join(home, "state"),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("launch accepted a flag-shaped account value; output:\n%s", out)
	}
	for _, want := range []string{"account", "--no-wait", "needs a value"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("error output %q does not explain %q", out, want)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("launch reached tmux after rejecting argv (stat error %v)", err)
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("launch/account fallback wrote state before rejecting argv: %v", entries)
	}
}
