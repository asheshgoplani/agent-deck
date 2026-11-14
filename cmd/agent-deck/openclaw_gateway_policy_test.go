package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// writeOpenClawConfig points the sandboxed CLI at a gateway URL.
func writeOpenClawConfig(t *testing.T, body string) {
	t.Helper()
	path, err := session.GetUserConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestOpenClawCLI_PlainWSToRemoteHostNeedsInsecure drives the real binary: a
// ws:// gateway that is not loopback or tailnet is refused with the exact
// message before any connection, and --insecure or allow_insecure lets it
// through to the dial. 0.0.0.0 is not a loopback address for the policy but
// dials locally, so the allowed run fails fast with a connection error.
func TestOpenClawCLI_PlainWSToRemoteHostNeedsInsecure(t *testing.T) {
	home := watcherCLIHome(t)
	const refusal = "openclaw: refusing to send the gateway password over unencrypted ws:// to 0.0.0.0; " +
		"use wss://, a loopback or tailnet address, or pass --insecure"
	env := []string{"OPENCLAW_PASSWORD=pw-test"}

	writeOpenClawConfig(t, "[openclaw]\ngateway_url = \"ws://0.0.0.0:1\"\n")
	for _, sub := range [][]string{{"list"}, {"status"}, {"sync"}, {"send", "--agent", "a", "hi"}, {"bridge", "--agent", "a"}} {
		_, stderr, code := runAgentDeckEnv(t, home, "", env, append([]string{"openclaw"}, sub...)...)
		if code == 0 || !strings.Contains(stderr, "Error: "+refusal) {
			t.Errorf("openclaw %v: exit %d, stderr %q; want the refusal", sub, code, stderr)
		}
	}

	_, stderr, code := runAgentDeckEnv(t, home, "", env, "openclaw", "list", "--insecure")
	if code == 0 || strings.Contains(stderr, "refusing") || !strings.Contains(stderr, "Failed to connect to gateway") {
		t.Errorf("--insecure: exit %d, stderr %q; want a dial failure, not the refusal", code, stderr)
	}
	_, stderr, code = runAgentDeckEnv(t, home, "", env, "openclaw", "status", "--insecure")
	if code == 0 || strings.Contains(stderr, "refusing") || !strings.Contains(stderr, "Gateway: OFFLINE") {
		t.Errorf("status --insecure: exit %d, stderr %q; want OFFLINE, not the refusal", code, stderr)
	}

	writeOpenClawConfig(t, "[openclaw]\ngateway_url = \"ws://0.0.0.0:1\"\nallow_insecure = true\n")
	_, stderr, code = runAgentDeckEnv(t, home, "", env, "openclaw", "list")
	if code == 0 || strings.Contains(stderr, "refusing") || !strings.Contains(stderr, "Failed to connect to gateway") {
		t.Errorf("allow_insecure: exit %d, stderr %q; want a dial failure, not the refusal", code, stderr)
	}
}
