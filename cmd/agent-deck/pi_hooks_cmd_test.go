// pi_hooks_cmd_test.go covers the pi (pi-coding-agent) hook integration added
// for issue #2222: the event mapping `agent-deck hook-handler` applies to pi's
// lifecycle event names, and the `agent-deck pi-hooks` CLI wiring.
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestMapEventToStatus_PiEvents pins pi's native event vocabulary onto the
// shared normalized event map. The extension emits pi's own names, not
// Claude's, so the mapping (not the extension) is where the translation lives.
func TestMapEventToStatus_PiEvents(t *testing.T) {
	tests := []struct {
		event  string
		expect string
	}{
		{"turn_start", "running"},
		{"turn_end", "waiting"},
		{"session_start", "waiting"},
		{"session_shutdown", "dead"},
		// The normalizer folds separators and case, so these are the same
		// events however a future pi version spells them.
		{"turnStart", "running"},
		{"turn-end", "waiting"},
		{"sessionShutdown", "dead"},
	}
	for _, tt := range tests {
		t.Run(tt.event, func(t *testing.T) {
			if got := mapEventToStatus(tt.event); got != tt.expect {
				t.Errorf("mapEventToStatus(%q) = %q, want %q", tt.event, got, tt.expect)
			}
		})
	}
}

func setupPiHooksTest(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "pi")
	t.Setenv("PI_CODING_AGENT_DIR", dir)
	return filepath.Join(dir, "extensions")
}

func TestHandlePiHooks_InstallStatusUninstall(t *testing.T) {
	extDir := setupPiHooksTest(t)

	out := captureStdout(t, func() { handlePiHooks([]string{"status"}) })
	if !strings.Contains(out, "Status: NOT INSTALLED") {
		t.Errorf("status before install = %q, want NOT INSTALLED", out)
	}

	out = captureStdout(t, func() { handlePiHooks([]string{"install"}) })
	if !strings.Contains(out, "pi hooks installed successfully.") {
		t.Errorf("install output = %q", out)
	}
	if !session.CheckPiHooksInstalled(extDir) {
		t.Fatal("extension not installed under PI_CODING_AGENT_DIR")
	}
	if _, err := os.Stat(session.PiHookExtensionPath(extDir)); err != nil {
		t.Fatalf("extension file missing: %v", err)
	}

	out = captureStdout(t, func() { handlePiHooks([]string{"status"}) })
	if !strings.Contains(out, "Status: INSTALLED") {
		t.Errorf("status after install = %q, want INSTALLED", out)
	}

	out = captureStdout(t, func() { handlePiHooks([]string{"install"}) })
	if !strings.Contains(out, "pi hooks are already installed.") {
		t.Errorf("second install output = %q", out)
	}

	out = captureStdout(t, func() { handlePiHooks([]string{"uninstall"}) })
	if !strings.Contains(out, "pi hooks removed successfully.") {
		t.Errorf("uninstall output = %q", out)
	}

	out = captureStdout(t, func() { handlePiHooks([]string{"uninstall"}) })
	if !strings.Contains(out, "No agent-deck pi hooks found to remove.") {
		t.Errorf("second uninstall output = %q", out)
	}
}

func TestHandlePiHooks_StatusReportsDrift(t *testing.T) {
	extDir := setupPiHooksTest(t)
	if _, err := session.InstallPiHooks(extDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(session.PiHookExtensionPath(extDir),
		[]byte("// AGENTDECK PI HOOK EXTENSION v0\n"), 0644); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() { handlePiHooks([]string{"status"}) })
	if !strings.Contains(out, "Status: OUTDATED") {
		t.Errorf("status with drift = %q, want OUTDATED", out)
	}
}

// TestHandlePiHooks_HelpIsReadOnly: a help request anywhere in the argument
// list prints usage and touches nothing (#1993).
func TestHandlePiHooks_HelpIsReadOnly(t *testing.T) {
	extDir := setupPiHooksTest(t)

	for _, args := range [][]string{
		{"help"}, {"--help"}, {"-h"},
		{"install", "--help"}, {"uninstall", "-h"}, {"status", "help"},
	} {
		out := captureStdout(t, func() { handlePiHooks(args) })
		if !strings.Contains(out, "Usage: agent-deck pi-hooks <command>") {
			t.Errorf("pi-hooks %v printed no usage: %q", args, out)
		}
		if _, err := os.Stat(session.PiHookExtensionPath(extDir)); !os.IsNotExist(err) {
			t.Fatalf("pi-hooks %v wrote the extension file", args)
		}
	}
}
