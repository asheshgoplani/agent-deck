package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodexMCPWaitsForConfigWriter(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "config.toml")
	lock, err := AcquireConfigFileLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	done := make(chan error, 1)
	go func() { done <- WriteCodexMCPConfig(home, nil) }()
	// Negative-timing check: the writer must still be blocked after 100ms.
	// A slow machine can only make this pass more easily, never fail it
	// spuriously, because "early" means the lock was ignored.
	var early bool
	select {
	case err = <-done:
		early = true
	case <-time.After(100 * time.Millisecond):
	}
	// Model a hook writer publishing its independent change while holding the
	// shared lock. The MCP writer must read this version after taking the lock.
	writeErr := os.WriteFile(path, []byte("notify = [\"agent-deck\", \"codex-notify\"]\n"), 0o600)
	lock.Release()
	if !early {
		err = <-done
	}
	if early {
		t.Fatal("MCP writer completed while another config transaction held the lock")
	}
	if writeErr != nil || err != nil {
		t.Fatalf("hook write: %v; MCP write: %v", writeErr, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "codex-notify") {
		t.Fatalf("MCP update lost notify: %s, %v", data, err)
	}
}
