//go:build eval_smoke

// Package session holds behavioral eval cases for agent-deck's session
// lifecycle. See docs/rfc/EVALUATOR_HARNESS.md (issue #37) — this file
// owns the RFC §7 Example 3 case (inject_status_line under real tmux).
package session_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/tests/eval/harness"
)

// TestEval_Session_InjectStatusLine_RealTmux is the guard for RFC Bug 3
// (v1.7.x — `inject_status_line` unit tests asserted on struct fields and
// argv slices, so a regression that broke the actual tmux state would pass
// every Go test). A hotfix session ran `go test ./internal/tmux/... -race`,
// saw green, closed the report "no regression" — because no test inspected
// what tmux itself displays.
//
// This eval talks to a real tmux server on an isolated socket, drives the
// agent-deck binary through the full `add`+`session start` flow, and then
// asks tmux for the rendered status-right. The claim: the injected status
// bar actually reaches the tmux server, and there are no unexpanded format
// placeholders (no raw `#{...}` leaking through).
func TestEval_Session_InjectStatusLine_RealTmux(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH")
	}

	sb := harness.NewSandbox(t)
	sb.InstallTmuxShim(t)

	// A work directory inside the scratch HOME so the binary's path
	// canonicalization stays inside the sandbox.
	workDir := filepath.Join(sb.Home, "project")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}

	// 1. Register a shell session so we don't need the real `claude`
	//    binary on PATH. Display name "inj" keeps it short for the
	//    `status-right` substring check.
	runBin(t, sb, "add", "-c", "bash", "-t", "inj", "-g", "evalgrp", workDir)

	// 2. Start the session. This spawns tmux via the shim against the
	//    per-sandbox socket and calls ConfigureStatusBar.
	runBin(t, sb, "session", "start", "inj")

	// 3. tmux needs a beat to have the session registered and options
	//    set. Poll `list-sessions` briefly instead of sleeping blindly.
	deadline := time.Now().Add(5 * time.Second)
	var sessName string
	for time.Now().Before(deadline) {
		out, err := sb.TmuxTry("list-sessions", "-F", "#{session_name}")
		if err == nil {
			for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
				if strings.HasPrefix(line, "agentdeck_") {
					sessName = line
					break
				}
			}
		}
		if sessName != "" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if sessName == "" {
		out, _ := sb.TmuxTry("list-sessions")
		t.Fatalf("no agentdeck_* session appeared within 5s.\ntmux list-sessions: %s", out)
	}

	// 4. Query the REAL tmux-expanded status-right. If ConfigureStatusBar
	//    never ran, this is the default (empty or the user's global).
	//    If the format string leaked as-is, `#{...}` will be present.
	statusRight := strings.TrimSpace(
		sb.Tmux("display-message", "-p", "-t", sessName, "#{status-right}"))

	if !strings.Contains(statusRight, "inj") {
		t.Fatalf("status-right does not contain session display name 'inj'.\n"+
			"session=%q\nstatus-right=%q\n"+
			"If status-right is empty, ConfigureStatusBar likely never ran.\n"+
			"If it's non-empty but lacks 'inj', themedStatusRight() regressed.",
			sessName, statusRight)
	}
	if strings.Contains(statusRight, "#{") {
		t.Fatalf("status-right has unexpanded tmux format tokens: %q\n"+
			"This means agent-deck wrote the format string but tmux didn't "+
			"expand it — usually a quoting regression in set-option.", statusRight)
	}

	// Guarantee clean teardown: stop the session so the shared tmux server
	// exits cleanly. Failure here is non-fatal — Sandbox.teardown will
	// kill-server as a fallback.
	_, _ = runBinTry(sb, "session", "stop", "inj")
}

// runBin spawns the agent-deck binary with args and the sandbox env. Fails
// the test on non-zero exit.
func runBin(t *testing.T, sb *harness.Sandbox, args ...string) {
	t.Helper()
	cmd := exec.Command(sb.BinPath, args...)
	cmd.Env = sb.Env()
	cmd.Dir = sb.Home
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("agent-deck %v: %v\n%s", args, err, string(out))
	}
}

// runBinTry is the best-effort variant used during cleanup.
func runBinTry(sb *harness.Sandbox, args ...string) (string, error) {
	cmd := exec.Command(sb.BinPath, args...)
	cmd.Env = sb.Env()
	cmd.Dir = sb.Home
	out, err := cmd.CombinedOutput()
	return string(out), err
}
