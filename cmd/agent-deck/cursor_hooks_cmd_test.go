// cursor_hooks_cmd_test.go covers the `agent-deck cursor-hooks` CLI wiring
// (issue #1675, follow-up to #1673 which added session-layer coverage for
// session.AutoInstallCursorHooks / SetCursorHooksEnabled but left the CLI
// handlers in this file and the top-level subcommand dispatcher untested).
//
// Covered:
//   - handleCursorHooksInstall / handleCursorHooksUninstall actually drive
//     session.InjectCursorHooks / RemoveCursorHooks and SetCursorHooksEnabled
//     together, in the right order (install clears the durable opt-out;
//     uninstall persists it before removing hooks).
//   - handleCursorHooksStatus reports INSTALLED/NOT INSTALLED and the
//     Auto-install DISABLED line correctly for each config/install
//     combination.
//   - handleCursorHooks's subcommand dispatch: valid subcommands
//     (install/uninstall/status/help/--help/-h) route to the right handler,
//     and invalid input (no args, unknown subcommand) exits 1 with a usage
//     message on stderr. Since handleCursorHooks calls os.Exit on those
//     paths, the exit-path cases run in a helper subprocess (same pattern as
//     TestXDGTask6HelperProcess in xdg_task6_test.go).
package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// setupCursorHooksTest sandboxes HOME to a fresh temp dir and clears the
// package-level user-config cache so LoadUserConfig/SaveUserConfig in this
// test don't see state left behind by another test (the config cache keys
// on mtime, not path — see cursor_hooks_test.go's TestSetCursorHooksEnabled_RoundTrip
// for the same precaution at the session-package level).
func setupCursorHooksTest(t *testing.T) (home string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)
	return home
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	// Read on a separate goroutine while fn runs, rather than after it
	// returns: the OS pipe buffer is bounded (~64KB), so a sequential
	// "run fn, then read" would deadlock if fn ever printed more than that
	// in one call (fn blocks on the full pipe with nobody draining it yet).
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r)
		done <- buf.String()
	}()
	// Close w even if fn panics, so the reader goroutine always sees EOF
	// and this helper never leaks it (avoids the fd leak on a panicking fn).
	defer func() { _ = w.Close() }()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("close stdout writer: %v", err)
	}
	return <-done
}

func TestHandleCursorHooksInstall_InjectsHooksAndClearsOptOut(t *testing.T) {
	setupCursorHooksTest(t)

	// Seed a prior opt-out (as `cursor-hooks uninstall` would have left it),
	// so install's job of clearing it is actually exercised.
	if err := session.SetCursorHooksEnabled(false); err != nil {
		t.Fatalf("seed opt-out: %v", err)
	}

	out := captureStdout(t, handleCursorHooksInstall)

	if !strings.Contains(out, "Cursor hooks installed successfully.") {
		t.Fatalf("install output missing success message, got: %q", out)
	}

	configDir := getCursorConfigDirForHooks()
	if !session.CheckCursorHooksInstalled(configDir) {
		t.Fatal("hooks not present in hooks.json after handleCursorHooksInstall")
	}

	session.ClearUserConfigCache()
	cfg, err := session.LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	if !cfg.Cursor.GetHooksEnabled() {
		t.Fatal("handleCursorHooksInstall did not clear the [cursor] hooks_enabled opt-out")
	}
}

func TestHandleCursorHooksInstall_NoopMessageWhenAlreadyInstalled(t *testing.T) {
	setupCursorHooksTest(t)

	configDir := getCursorConfigDirForHooks()
	if _, err := session.InjectCursorHooks(configDir); err != nil {
		t.Fatalf("seed install: %v", err)
	}

	out := captureStdout(t, handleCursorHooksInstall)
	if !strings.Contains(out, "already installed") {
		t.Fatalf("expected already-installed message, got: %q", out)
	}
}

func TestHandleCursorHooksUninstall_PersistsOptOutAndRemovesHooks(t *testing.T) {
	setupCursorHooksTest(t)

	configDir := getCursorConfigDirForHooks()
	if _, err := session.InjectCursorHooks(configDir); err != nil {
		t.Fatalf("seed install: %v", err)
	}
	if !session.CheckCursorHooksInstalled(configDir) {
		t.Fatal("seed install did not take effect")
	}

	out := captureStdout(t, handleCursorHooksUninstall)

	if !strings.Contains(out, "Auto-install disabled") {
		t.Fatalf("uninstall output missing disabled message, got: %q", out)
	}
	if !strings.Contains(out, "Cursor hooks removed successfully.") {
		t.Fatalf("uninstall output missing removal message, got: %q", out)
	}

	if session.CheckCursorHooksInstalled(configDir) {
		t.Fatal("hooks still present in hooks.json after handleCursorHooksUninstall")
	}

	session.ClearUserConfigCache()
	cfg, err := session.LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	if cfg.Cursor.GetHooksEnabled() {
		t.Fatal("handleCursorHooksUninstall did not persist the [cursor] hooks_enabled opt-out")
	}
}

func TestHandleCursorHooksUninstall_NoHooksFoundMessage(t *testing.T) {
	setupCursorHooksTest(t)

	out := captureStdout(t, handleCursorHooksUninstall)
	if !strings.Contains(out, "No agent-deck Cursor hooks found to remove.") {
		t.Fatalf("expected no-hooks-found message, got: %q", out)
	}

	// The opt-out must still be persisted even when there was nothing to
	// remove — TUI startup must not reinstall on the next launch.
	session.ClearUserConfigCache()
	cfg, err := session.LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	if cfg.Cursor.GetHooksEnabled() {
		t.Fatal("opt-out not persisted when no hooks were present to remove")
	}
}

func TestHandleCursorHooksStatus_ReportsNotInstalled(t *testing.T) {
	setupCursorHooksTest(t)

	out := captureStdout(t, handleCursorHooksStatus)

	if !strings.Contains(out, "Status: NOT INSTALLED") {
		t.Fatalf("expected NOT INSTALLED status, got: %q", out)
	}
	if strings.Contains(out, "Auto-install: DISABLED") {
		t.Fatalf("did not expect DISABLED line with default config, got: %q", out)
	}
}

func TestHandleCursorHooksStatus_ReportsInstalled(t *testing.T) {
	setupCursorHooksTest(t)

	configDir := getCursorConfigDirForHooks()
	if _, err := session.InjectCursorHooks(configDir); err != nil {
		t.Fatalf("seed install: %v", err)
	}

	out := captureStdout(t, handleCursorHooksStatus)
	if !strings.Contains(out, "Status: INSTALLED") {
		t.Fatalf("expected INSTALLED status, got: %q", out)
	}
	if !strings.Contains(out, filepath.Join(configDir, "hooks.json")) {
		t.Fatalf("expected config path in status output, got: %q", out)
	}
}

func TestHandleCursorHooksStatus_ReportsDisabledAutoInstall(t *testing.T) {
	setupCursorHooksTest(t)

	if err := session.SetCursorHooksEnabled(false); err != nil {
		t.Fatalf("seed opt-out: %v", err)
	}
	session.ClearUserConfigCache()

	out := captureStdout(t, handleCursorHooksStatus)
	if !strings.Contains(out, "Auto-install: DISABLED") {
		t.Fatalf("expected DISABLED auto-install line, got: %q", out)
	}
	if !strings.Contains(out, "[cursor] hooks_enabled = false") {
		t.Fatalf("expected opt-out explanation in status output, got: %q", out)
	}
}

func TestHandleCursorHooks_HelpVariantsPrintUsage(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"--help"}, {"-h"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			out := captureStdout(t, func() { handleCursorHooks(args) })
			if !strings.Contains(out, "Usage: agent-deck cursor-hooks <command>") {
				t.Fatalf("help output for %v missing usage header, got: %q", args, out)
			}
			for _, want := range []string{"install", "uninstall", "status"} {
				if !strings.Contains(out, want) {
					t.Fatalf("help output for %v missing %q, got: %q", args, want, out)
				}
			}
		})
	}
}

func TestHandleCursorHooks_RoutesInstallSubcommand(t *testing.T) {
	setupCursorHooksTest(t)

	out := captureStdout(t, func() { handleCursorHooks([]string{"install"}) })
	if !strings.Contains(out, "Cursor hooks installed successfully.") {
		t.Fatalf("expected install routing to reach handleCursorHooksInstall, got: %q", out)
	}
	if !session.CheckCursorHooksInstalled(getCursorConfigDirForHooks()) {
		t.Fatal("dispatched install did not actually install hooks")
	}
}

func TestHandleCursorHooks_RoutesStatusSubcommand(t *testing.T) {
	setupCursorHooksTest(t)

	out := captureStdout(t, func() { handleCursorHooks([]string{"status"}) })
	if !strings.Contains(out, "Status: NOT INSTALLED") {
		t.Fatalf("expected status routing to reach handleCursorHooksStatus, got: %q", out)
	}
}

// --- exit-path coverage via helper subprocess ---
//
// handleCursorHooks calls os.Exit(1) for both empty args and an unknown
// subcommand. Testing that in-process would kill the whole test binary, so
// (mirroring TestXDGTask6HelperProcess) we re-exec the test binary itself
// with -test.run pinned to the helper entrypoint below.

func runCursorHooksHelperProcess(t *testing.T, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()

	cmdArgs := append([]string{"-test.run=TestCursorHooksHelperProcess", "--"}, args...)
	cmd := exec.Command(os.Args[0], cmdArgs...)
	cmd.Env = append(os.Environ(), "AGENT_DECK_CURSOR_HOOKS_HELPER=1")

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err := cmd.Run()
	code := 0
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("helper process failed to start/run: %v (stderr=%q)", err, errBuf.String())
		}
		code = exitErr.ExitCode()
	}
	return outBuf.String(), errBuf.String(), code
}

// TestCursorHooksHelperProcess is not a real test; go test invokes it (via
// -test.run) as a subprocess entrypoint that calls handleCursorHooks
// directly so its os.Exit calls terminate the subprocess, not the parent
// test binary. It is a no-op unless AGENT_DECK_CURSOR_HOOKS_HELPER=1 is set.
func TestCursorHooksHelperProcess(t *testing.T) {
	if os.Getenv("AGENT_DECK_CURSOR_HOOKS_HELPER") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) > 0 {
		args = args[1:]
	}
	handleCursorHooks(args)
	// Valid subcommands return normally; give the parent a clean signal.
	os.Exit(0)
}

func TestHandleCursorHooks_NoArgsExitsNonZeroWithUsageOnStderr(t *testing.T) {
	stdout, stderr, code := runCursorHooksHelperProcess(t)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (stdout=%q stderr=%q)", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "Usage: agent-deck cursor-hooks <command>") {
		t.Fatalf("stderr missing usage on empty args, got: %q", stderr)
	}
}

func TestHandleCursorHooks_UnknownSubcommandExitsNonZeroWithMessage(t *testing.T) {
	stdout, stderr, code := runCursorHooksHelperProcess(t, "bogus")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (stdout=%q stderr=%q)", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "Unknown cursor-hooks subcommand: bogus") {
		t.Fatalf("stderr missing unknown-subcommand message, got: %q", stderr)
	}
	if !strings.Contains(stderr, "Usage: agent-deck cursor-hooks <command>") {
		t.Fatalf("stderr missing usage fallback after unknown subcommand, got: %q", stderr)
	}
}
