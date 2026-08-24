package main

import (
	"strings"
	"testing"
)

func TestSessionApprovalCommandIsExposed(t *testing.T) {
	home := t.TempDir()
	stdout, stderr, code := runAgentDeck(t, home, "session", "approval", "--help")
	if code != 0 {
		t.Fatalf("session approval --help exited %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "session approval <id|title>") {
		t.Fatalf("session approval help missing command usage: %s", stdout)
	}
}

func TestSessionRejectCommandIsExposed(t *testing.T) {
	home := t.TempDir()
	stdout, stderr, code := runAgentDeck(t, home, "session", "reject", "--help")
	if code != 0 {
		t.Fatalf("session reject --help exited %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "session reject <id|title>") {
		t.Fatalf("session reject help missing command usage: %s", stdout)
	}
}
