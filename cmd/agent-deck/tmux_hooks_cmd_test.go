// tmux_hooks_cmd_test.go covers the `agent-deck tmux-hooks` CLI wiring: help
// is side-effect free, and install/status/uninstall act on the reserved
// after-new-window slot of the configured tmux server only when it is ours.
package main

import (
	"crypto/sha256"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

func TestHandleTmuxHooks_HelpVariantsPrintUsage(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"--help"}, {"-h"}, {"uninstall", "--help"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			out := captureStdout(t, func() { handleTmuxHooks(args) })
			if !strings.Contains(out, "Usage: agent-deck tmux-hooks <command>") {
				t.Fatalf("help output for %v missing usage header, got: %q", args, out)
			}
			for _, want := range []string{"install", "uninstall", "status", "after-new-window[2259]"} {
				if !strings.Contains(out, want) {
					t.Fatalf("help output for %v missing %q, got: %q", args, want, out)
				}
			}
		})
	}
}

// privateTmuxServer starts a tmux server on a per-test socket, points the
// process-wide default socket at it for the test's duration, and returns a
// runner for tmux commands against it.
func privateTmuxServer(t *testing.T) func(args ...string) string {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux binary not on PATH; skipping")
	}
	socket := fmt.Sprintf("th%x", sha256.Sum256([]byte(t.Name())))[:14]
	ctl := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("tmux", append([]string{"-L", socket}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("tmux %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	ctl("new-session", "-d", "-x", "80", "-y", "24", "-s", "tgt", "bash")
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() })
	previous := tmux.DefaultSocketName()
	tmux.SetDefaultSocketName(socket)
	t.Cleanup(func() { tmux.SetDefaultSocketName(previous) })
	return ctl
}

func TestHandleTmuxHooks_InstallStatusUninstallRoundTrip(t *testing.T) {
	ctl := privateTmuxServer(t)
	ctl("set-hook", "-g", "after-new-window[0]", "set-option -w @user_hook_ran yes")

	out := captureStdout(t, func() { handleTmuxHooks([]string{"status"}) })
	if !strings.Contains(out, "Status: NOT INSTALLED") {
		t.Fatalf("fresh server must report not installed, got: %q", out)
	}
	out = captureStdout(t, func() { handleTmuxHooks([]string{"install"}) })
	if !strings.Contains(out, "hook installed") {
		t.Fatalf("install: %q", out)
	}
	if got := ctl("show-options", "-gqv", "after-new-window[2259]"); !strings.Contains(got, "@agentdeck_window_size") {
		t.Fatalf("slot after install: %q", got)
	}
	out = captureStdout(t, func() { handleTmuxHooks([]string{"status"}) })
	if !strings.Contains(out, "Status: INSTALLED") {
		t.Fatalf("status after install: %q", out)
	}
	out = captureStdout(t, func() { handleTmuxHooks([]string{"uninstall"}) })
	if !strings.Contains(out, "hook removed") {
		t.Fatalf("uninstall: %q", out)
	}
	if got := ctl("show-options", "-gqv", "after-new-window[2259]"); got != "" {
		t.Fatalf("slot must be empty after uninstall, got: %q", got)
	}
	if got := ctl("show-options", "-gqv", "after-new-window[0]"); got != "set-option -w @user_hook_ran yes" {
		t.Fatalf("the user's own entry must survive, got: %q", got)
	}
	out = captureStdout(t, func() { handleTmuxHooks([]string{"uninstall"}) })
	if !strings.Contains(out, "No agent-deck hook found") {
		t.Fatalf("second uninstall: %q", out)
	}
}

func TestHandleTmuxHooks_ForeignSlotIsLeftAlone(t *testing.T) {
	ctl := privateTmuxServer(t)
	const foreign = "set-option -w @user_slot_2259 yes"
	ctl("set-hook", "-g", "after-new-window[2259]", foreign)

	for _, sub := range []string{"status", "install", "uninstall"} {
		out := captureStdout(t, func() { handleTmuxHooks([]string{sub}) })
		if !strings.Contains(strings.ToLower(out), "foreign") && !strings.Contains(out, "Left alone") {
			t.Fatalf("%s must report the foreign occupant, got: %q", sub, out)
		}
		if got := ctl("show-options", "-gqv", "after-new-window[2259]"); got != foreign {
			t.Fatalf("%s changed the foreign slot: %q", sub, got)
		}
	}
}
