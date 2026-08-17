//go:build darwin && !cgo

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDesktopNotificationsRejectHelperWithoutCGO(t *testing.T) {
	if err := desktopNotificationHelperPrerequisite(); err == nil || !strings.Contains(err.Error(), "cgo enabled") {
		t.Fatalf("no-cgo helper prerequisite = %v, want cgo remediation", err)
	}
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Join(configRoot, "agent-deck"), 0o700); err != nil {
		t.Fatal(err)
	}
	out := captureDesktopNotificationsOutput(t, desktopNotificationsDoctor)
	for _, want := range []string{"native presentation: unavailable", "remediation: install or rebuild Agent Deck for macOS with cgo enabled"} {
		if !strings.Contains(out, want) {
			t.Fatalf("doctor output missing %q:\n%s", want, out)
		}
	}
	for _, misleading := range []string{"helper endpoint: healthy", "action routing: ready"} {
		if strings.Contains(out, misleading) {
			t.Fatalf("doctor output misleadingly reports %q:\n%s", misleading, out)
		}
	}
}
