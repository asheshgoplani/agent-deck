package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/watcher"
)

// Issue #2532: `agent-deck watcher create webhook --port N` validated --port and
// then dropped it. Only github, ntfy and slack wrote a watcher.toml [source]
// table, and the engine reads adapter settings from nothing else
// (internal/ui/home.go loadWatcherSourceSettings), so WebhookAdapter.Setup saw
// an empty Settings["port"] and listened on its default 18460. Every webhook
// watcher therefore contended for the same port, whatever --port said.

// freeTCPPort returns a loopback port that was free a moment ago and is not
// the adapter's 18460 default, so a pass cannot come from the fallback.
func freeTCPPort(t *testing.T) int {
	t.Helper()
	for i := 0; i < 10; i++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("reserve a free port: %v", err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		_ = ln.Close()
		if port != 18460 {
			return port
		}
	}
	t.Fatal("could not find a free port other than 18460")
	return 0
}

// TestIssue2532_WebhookCreatePortReachesEngine drives the real binary, then
// starts the webhook adapter from the settings the engine would load, and
// posts to the port the user asked for.
func TestIssue2532_WebhookCreatePortReachesEngine(t *testing.T) {
	home := watcherCLIHome(t)
	const watcherName = "issue2532-webhook"
	port := freeTCPPort(t)

	if stdout, stderr, code := runAgentDeck(t, home,
		"-p", watcherCLIProfile, "watcher", "create", "webhook",
		"--name", watcherName, "--port", strconv.Itoa(port)); code != 0 {
		t.Fatalf("create webhook failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if row := loadWatcherRow(t, watcherName); row == nil {
		t.Fatalf("create webhook exited 0 but registered no watcher %q", watcherName)
	}

	dir, err := session.WatcherNameDir(watcherName)
	if err != nil {
		t.Fatalf("WatcherNameDir: %v", err)
	}
	tomlPath := filepath.Join(dir, "watcher.toml")
	if _, err := os.Stat(tomlPath); err != nil {
		t.Fatalf("create webhook --port %d wrote no watcher.toml, so the engine never sees the port "+
			"and the adapter listens on its default 18460: %v", port, err)
	}
	source := decodeWatcherSource(t, tomlPath)
	if got := source["port"]; got != strconv.Itoa(port) {
		t.Fatalf("[source].port = %q, want %q", got, strconv.Itoa(port))
	}
	if info, err := os.Stat(tomlPath); err == nil && info.Mode().Perm() != 0o600 {
		t.Errorf("watcher.toml mode = %o, want 600", info.Mode().Perm())
	}

	// Start the adapter from exactly those settings and prove it serves on the
	// requested port.
	adapter := &watcher.WebhookAdapter{}
	if err := adapter.Setup(context.Background(), watcher.AdapterConfig{
		Type: "webhook", Name: watcherName, Settings: source,
	}); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- adapter.Listen(ctx, make(chan watcher.Event, 1)) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("webhook adapter did not stop")
		}
	})

	url := fmt.Sprintf("http://127.0.0.1:%d/webhook", port)
	client := &http.Client{Timeout: 2 * time.Second}
	var lastErr error
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		resp, err := client.Post(url, "text/plain", strings.NewReader("issue 2532"))
		if err != nil {
			lastErr = err
			continue
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("POST %s = %d, want 202", url, resp.StatusCode)
		}
		return
	}
	t.Fatalf("nothing accepted a webhook on the requested port %d: %v", port, lastErr)
}

// TestIssue2532_WebhookCreateRefusesExistingConfig pins the name claim the
// [source] write now gives webhook, as it already did for the other types: an
// existing watcher.toml with no row behind it is never overwritten and no row
// is published over it.
func TestIssue2532_WebhookCreateRefusesExistingConfig(t *testing.T) {
	home := watcherCLIHome(t)
	const watcherName = "issue2532-leftover"

	dir, err := session.WatcherNameDir(watcherName)
	if err != nil {
		t.Fatalf("WatcherNameDir: %v", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir watcher dir: %v", err)
	}
	tomlPath := filepath.Join(dir, "watcher.toml")
	existing := "[source]\nport = \"19001\"\n\n[routing]\nconductor = \"client-a\"\n"
	if err := os.WriteFile(tomlPath, []byte(existing), 0o600); err != nil {
		t.Fatalf("seed watcher.toml: %v", err)
	}

	stdout, stderr, code := runAgentDeck(t, home,
		"-p", watcherCLIProfile, "watcher", "create", "webhook",
		"--name", watcherName, "--port", strconv.Itoa(freeTCPPort(t)))
	if code == 0 {
		t.Fatalf("create webhook succeeded over a watcher.toml it did not write\nstdout: %s\nstderr: %s", stdout, stderr)
	}
	if !strings.Contains(stderr, "already exists and was left untouched") {
		t.Fatalf("create failed before the config claim, so this proves nothing\nstdout: %s\nstderr: %s", stdout, stderr)
	}
	got, err := os.ReadFile(tomlPath)
	if err != nil {
		t.Fatalf("read watcher.toml: %v", err)
	}
	if string(got) != existing {
		t.Errorf("the existing watcher.toml was modified:\n--- got ---\n%s\n--- want ---\n%s", got, existing)
	}
	if row := loadWatcherRow(t, watcherName); row != nil {
		t.Errorf("watcher %q was registered over a config this command did not write", watcherName)
	}
}
