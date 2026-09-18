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

func TestCredsRefreshBareHelpDoesNotStartDaemon(t *testing.T) {
	home := t.TempDir()
	configDir := filepath.Join(home, "claude-config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	credPath := filepath.Join(configDir, ".credentials.json")
	credBody := `{"claudeAiOauth":{"accessToken":"tok","refreshToken":"rt","clientId":"c","expiresAt":9999999999999}}`
	if err := os.WriteFile(credPath, []byte(credBody), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(credPath)
	if err != nil {
		t.Fatal(err)
	}

	out, err := runIssue2025Helper(t, home, []string{"creds-refresh", "--config-dir", configDir, "help"})
	if err != nil {
		t.Fatalf("creds-refresh bare trailing help failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Usage: agent-deck creds-refresh") {
		t.Fatalf("bare trailing help did not print usage:\n%s", out)
	}
	if strings.Contains(string(out), "keeping") {
		t.Fatalf("bare trailing help entered the keep-warm daemon loop:\n%s", out)
	}
	after, err := os.Stat(credPath)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("bare trailing help mutated the credentials file: before=%v after=%v", before.ModTime(), after.ModTime())
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
