//go:build !windows

package ui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	deckterminal "github.com/asheshgoplani/agent-deck/internal/terminal"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
	"github.com/charmbracelet/x/ansi"
)

// Regression tests for the rc.5 remote-parity defect (g14, tmux 3.4): the
// embedded attach view to a remote session showed
//
//	/home/ashesh/.tmux.conf:25: invalid option: extended-keys-format   [1/1]
//	...
//	[agentdeck_parity-…] 0:[tmux]*                     ctrl+q detach │ …
//
// and never became live. The whole remote path is exercised here: the
// embedded PTY runs `ssh -tt …`, a fake `ssh` on PATH stands in for the
// remote host (printing whatever leading output the test wants: a warning, a
// MOTD banner, or nothing) and then re-executes this test binary in
// attach-helper mode, which runs the same tmux.Session.Attach the remote
// `agent-deck session attach` runs, against a server born from a broken
// tmux.conf.

const (
	attachHelperSocketEnv  = "AGENTDECK_TEST_ATTACH_HELPER_SOCKET"
	attachHelperSessionEnv = "AGENTDECK_TEST_ATTACH_HELPER_SESSION"
	leadingOutputMarker    = "PANE-LIVE-MARKER"
)

// runAttachHelper is the "remote agent-deck session attach" half of the
// fake SSH endpoint. It runs inside the embedded PTY, so os.Stdin is the
// terminal the attach expects.
func runAttachHelper(socket, name string) int {
	sess := &tmux.Session{Name: name, SocketName: socket}
	if err := sess.Attach(context.Background(), 0x11); err != nil {
		fmt.Fprintf(os.Stderr, "attach helper: %v\n", err)
		return 1
	}
	return 0
}

type leadingOutputCase struct {
	name    string
	leading string
}

func TestEmbeddedRemoteAttach_ToleratesConfigErrorAndLeadingOutput(t *testing.T) {
	skipIfNoTmuxBinaryUI(t)
	cases := []leadingOutputCase{
		{name: "tmux_conf_warning", leading: "/home/ashesh/.tmux.conf:25: invalid option: extended-keys-format\r\n"},
		{name: "motd_banner", leading: "Welcome to g14 (Ubuntu 24.04 LTS)\r\n * Documentation: https://example.invalid\r\n\r\nLast login: Thu Sep 18 03:10:11 2026\r\n"},
		{name: "silent_remote", leading: ""},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runLeadingOutputCase(t, i, tc)
		})
	}
}

func runLeadingOutputCase(t *testing.T, index int, tc leadingOutputCase) {
	t.Helper()
	// TestMain isolated TMUX_TMPDIR, so the -L name cannot reach a live server.
	socket := fmt.Sprintf("lead-%d-%d", os.Getpid(), index)
	name := "agentdeck_parity_lead"
	tmuxCtl := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("tmux", append([]string{"-L", socket}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("tmux %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() })

	// g14's server: one invalid option in the config it was started from.
	badConf := filepath.Join(t.TempDir(), "tmux.conf")
	if err := os.WriteFile(badConf, []byte("set -g extended-keys-format-typo csi-u\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	spawn := exec.Command("tmux", "-L", socket, "-f", badConf, "new-session", "-d", "-x", "80", "-y", "24", "-s", name, "sh")
	if out, err := spawn.CombinedOutput(); err != nil {
		t.Fatalf("spawn tmux server: %v\n%s", err, out)
	}
	tmuxCtl("set-option", "-g", "status", "on")

	// Fake SSH endpoint: prints the leading output, then becomes the remote
	// `agent-deck session attach` by re-executing this binary in helper mode.
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s' %s\n%s=%s %s=%s exec %s\n",
		shellQuoteForTest(tc.leading),
		attachHelperSocketEnv, shellQuoteForTest(socket),
		attachHelperSessionEnv, shellQuoteForTest(name),
		shellQuoteForTest(self))
	if err := os.WriteFile(filepath.Join(binDir, "ssh"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	req := deckterminal.AttachRequest{
		Name:   "remote-session-id",
		Remote: &deckterminal.RemoteAttach{Host: "fake@g14", AgentDeckPath: "agent-deck"},
	}
	term, err := startEmbeddedTerminal(context.Background(), req, embeddedTerminalSize{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("start embedded remote attach: %v", err)
	}
	t.Cleanup(func() { _ = term.Close() })

	paneState := func() string {
		return tmuxCtl("display-message", "-p", "-t", name, "#{session_attached}|#{pane_in_mode}|#{pane_mode}")
	}
	frame := func() string { return strings.TrimRight(ansi.Strip(term.Render()), "\n") }

	// The client must attach and the pane must be usable (not parked in
	// tmux's config-error view) within the attach watchdog window.
	if !waitFor(5*time.Second, func() bool { return paneState() == "1|0|" }) {
		t.Fatalf("pane never became live after attach: state=%q\n--- frozen frame ---\n%s\n--- end ---", paneState(), frame())
	}
	// Keys reach the agent pane, not a copy/view key table.
	if _, err := term.Write([]byte("printf " + leadingOutputMarker + "\r")); err != nil {
		t.Fatal(err)
	}
	if !waitFor(5*time.Second, func() bool { return strings.Contains(frame(), leadingOutputMarker) }) {
		t.Fatalf("typed command never rendered in the attach view\n--- frame ---\n%s\n--- end ---", frame())
	}
	t.Logf("live attach frame (%s):\n%s", tc.name, frame())
	if strings.Contains(frame(), "invalid option") {
		t.Fatalf("attach view still shows tmux's config-error view:\n%s", frame())
	}
}

func waitFor(timeout time.Duration, condition func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return condition()
}

func shellQuoteForTest(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
