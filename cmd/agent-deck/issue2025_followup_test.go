package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These two consent cases were explicitly called out as NOT covered by
// #2055's fix (see #2025 and the #2055 PR description: "the remaining
// no-operand daemon and remote-add consent cases require separate
// coordinated work"). Both dispatchers accept no legitimate positional
// value that could collide with the literal string "help", so bare
// trailing "help" must be read-only there, matching the hooksHelpRequested
// / costs-sync precedent already established for that class of command.

func TestNotifyDaemonBareHelpDoesNotStartDaemon(t *testing.T) {
	home := t.TempDir()
	before := snapshotTree(t, home)

	out, err := runIssue2025Helper(t, home, []string{"notify-daemon", "help"})
	if err != nil {
		t.Fatalf("notify-daemon help failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Usage: agent-deck notify-daemon") {
		t.Fatalf("bare help did not print usage:\n%s", out)
	}
	if after := snapshotTree(t, home); len(after) != len(before) {
		t.Fatalf("notify-daemon help changed HOME state: before=%v after=%v", before, after)
	}
}

func TestRemoteAddTrailingBareHelpDoesNotAddRemote(t *testing.T) {
	home := t.TempDir()

	out, err := runIssue2025Helper(t, home, []string{"remote", "add", "myremote", "example.invalid", "help"})
	if err != nil {
		t.Fatalf("remote add trailing help failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Usage: agent-deck remote add") {
		t.Fatalf("trailing help did not print usage:\n%s", out)
	}
	// The remote must not have been written to config.toml anywhere under HOME.
	_ = filepath.WalkDir(home, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, "config.toml") {
			contents, readErr := os.ReadFile(path)
			if readErr == nil && strings.Contains(string(contents), "myremote") {
				t.Fatalf("remote add trailing help mutated config: %s", contents)
			}
		}
		return nil
	})
}

func TestRemoteAddRejectsUnexpectedExtraOperand(t *testing.T) {
	home := t.TempDir()

	out, err := runIssue2025Helper(t, home, []string{"remote", "add", "myremote", "example.invalid", "bogus"})
	if err == nil {
		t.Fatalf("expected non-zero exit for unexpected operand, got success:\n%s", out)
	}
	_ = filepath.WalkDir(home, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, "config.toml") {
			contents, readErr := os.ReadFile(path)
			if readErr == nil && strings.Contains(string(contents), "myremote") {
				t.Fatalf("unexpected operand mutated config: %s", contents)
			}
		}
		return nil
	})
}
