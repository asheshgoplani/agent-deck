//go:build eval_smoke

// Package session holds behavioral eval cases for agent-deck's session
// lifecycle. This file owns the `inject_status_line = false` turn-OFF case:
// disabling injection must actively remove the tmux status bar, not just skip
// setting agent-deck's own. A unit test that asserts on the struct field or the
// argv it just built cannot see this — only real tmux can (#687, and the reason
// the eval lane exists per tests/eval/README.md).
package session_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/tests/eval/harness"
)

// TestEval_Session_BarOff_RealTmux is revert-sensitive: it fails
// without the `status off` emission in buildStatusBarArgs.
//
// The scenario mirrors what the user actually hits — a bar is already on the
// tmux server (tmux's own default `status on`, the user's config, or a prior run
// that had injection enabled), the user sets inject_status_line=false, and
// expects it to go away. tmux defaults every new session to `status on`, so a
// session started with injection disabled still starts from "on"; the test
// asserts real tmux reports it as "off", which only holds if agent-deck actively
// emits `status off` rather than merely skipping its own bar.
func TestEval_Session_BarOff_RealTmux(t *testing.T) {
	sb := harness.NewSandbox(t)
	sb.InstallTmuxShim(t) // force the binary's tmux calls onto the sandbox socket

	// 1. Config disables status-bar injection.
	cfgDir := filepath.Join(sb.Home, ".config", "agent-deck")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"),
		[]byte("[tmux]\ninject_status_line = false\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	workDir := filepath.Join(sb.Home, "project")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}

	// 2. Register + start the agent-deck session (display name "inj"). tmux's
	//    built-in default is `status on`, so the freshly created session starts
	//    with a bar — agent-deck must turn it off for the config to take effect.
	runBin(t, sb, "add", "-c", "bash", "-t", "inj", "-g", "evalgrp", workDir)
	runBin(t, sb, "session", "start", "inj")

	// 4. Find the agentdeck_* session tmux created.
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

	// 5. Ask REAL tmux what the session's status option resolves to. With
	//    injection disabled, agent-deck must have emitted `status off`; the
	//    seeded global `status on` must not win.
	status := strings.TrimSpace(
		sb.Tmux("display-message", "-p", "-t", sessName, "#{status}"))

	if status != "off" {
		t.Fatalf("inject_status_line=false did not turn the status bar off.\n"+
			"session=%q\n#{status}=%q (want \"off\")\n"+
			"tmux defaults new sessions to `status on`; if agent-deck only skips "+
			"setting its bar (returns nil from buildStatusBarArgs) instead of "+
			"emitting `status off`, that default bar survives — exactly the "+
			"#687 symptom.", sessName, status)
	}

	// Clean teardown; non-fatal (Sandbox.teardown kill-servers as a fallback).
	_, _ = runBinTry(sb, "session", "stop", "inj")
}
