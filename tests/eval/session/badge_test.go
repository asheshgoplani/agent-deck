//go:build eval_smoke

// Package session — eval coverage for the iTerm2 badge integration. The
// `Attach` lifecycle emits OSC 1337 SetBadgeFormat directly to os.Stdout
// (which is the iTerm2 tty under normal use). The unit-level integration
// test in internal/tmux/chrome_test.go skips when stdin isn't a terminal,
// which is always the case under `go test`. PTY-spawning the binary here
// gives Attach() a real terminal so the OSC bytes land in our captured
// stream — exactly the gap the eval harness exists to close (RFC §7
// Bug 3, mirroring the inject_status_line case).
package session_test

import (
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/tests/eval/harness"
)

// TestEval_Session_ITermBadge_RealAttach drives `agent-deck session attach`
// under a real PTY and asserts that the SetBadgeFormat OSC for the
// session's title is written to stdout on attach entry, and the empty
// (clear) form is written on detach. Catches regressions where the call
// to emitITermBadge in pty.Attach is removed or reordered — a class of
// breakage the existing pure-Go tests cannot structurally detect because
// they exercise the helper in isolation, not the lifecycle wiring.
func TestEval_Session_ITermBadge_RealAttach(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH")
	}

	sb := harness.NewSandbox(t)
	sb.InstallTmuxShim(t)

	workDir := filepath.Join(sb.Home, "project")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}

	// Enable the feature via config — the env-var path doesn't reliably
	// propagate through every process boundary, but config is read from
	// disk in every spawn. Match the wiring users are documented to use.
	cfgDir := filepath.Join(sb.Home, ".agent-deck")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir agent-deck config dir: %v", err)
	}
	cfgPath := filepath.Join(cfgDir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("[terminal]\niterm_badge = true\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Register a shell session so we don't need claude on PATH. Title
	// "badge" is what we'll assert the SetBadgeFormat payload encodes.
	runBin(t, sb, "add", "-c", "bash", "-t", "badge", "-g", "evalgrp", workDir)
	runBin(t, sb, "session", "start", "badge")

	// Wait for the agentdeck_* session to register on tmux.
	deadline := time.Now().Add(5 * time.Second)
	registered := false
	for time.Now().Before(deadline) {
		out, err := sb.TmuxTry("list-sessions", "-F", "#{session_name}")
		if err == nil {
			for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
				if strings.HasPrefix(line, "agentdeck_") {
					registered = true
					break
				}
			}
		}
		if registered {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !registered {
		out, _ := sb.TmuxTry("list-sessions")
		t.Fatalf("no agentdeck_* session appeared within 5s.\ntmux list-sessions: %s", out)
	}

	// Cleanup the session even on failure.
	t.Cleanup(func() { _, _ = runBinTry(sb, "session", "stop", "badge") })

	// PTY-spawn the attach. TERM_PROGRAM=iTerm.app turns on the badge
	// emit gate. TERM=dumb (harness default) is intentional: under it,
	// the inner tmux attach process exits immediately with "terminal does
	// not support clear", which drives agent-deck's Attach() through its
	// full on-entry-then-cleanupAttach lifecycle in a single short-lived
	// spawn. We do not need to actually run an interactive session — we
	// just need both OSC emit boundaries to fire so we can assert on them.
	p := sb.SpawnWithEnv(
		[]string{"TERM_PROGRAM=iTerm.app"},
		"session", "attach", "badge",
	)
	defer p.Close()

	// Wait for the binary to exit naturally. Under TERM=dumb the inner
	// tmux attach exits with "terminal does not support clear", but
	// agent-deck's Attach normalises that into a clean exit (the cmdDone
	// branch treats exit codes 0/1 as detach equivalents). Both emit
	// sites fire before and after, which is what we're testing. A 10s
	// ceiling is generous for what's typically a sub-second flow.
	p.ExpectExit(0, 10*time.Second)

	captured := p.Output()

	// Set-on-entry: SetBadgeFormat=<base64("badge")> + BEL.
	wantSet := "\x1b]1337;SetBadgeFormat=" +
		base64.StdEncoding.EncodeToString([]byte("badge")) + "\a"
	if !strings.Contains(captured, wantSet) {
		t.Fatalf("attach did not emit SetBadgeFormat OSC for session title 'badge'.\n"+
			"want substring: %q\ncaptured (escaped): %q",
			wantSet, captured)
	}

	// Clear-on-detach: empty payload form.
	wantClear := "\x1b]1337;SetBadgeFormat=\a"
	clearIdx := strings.LastIndex(captured, wantClear)
	setIdx := strings.Index(captured, wantSet)
	if clearIdx < 0 {
		t.Fatalf("detach did not emit badge-clear OSC.\nwant substring: %q\ncaptured (escaped): %q",
			wantClear, captured)
	}
	if clearIdx <= setIdx {
		t.Fatalf("badge SET must precede badge CLEAR within a single Attach lifecycle.\n"+
			"set-idx=%d clear-idx=%d\ncaptured (escaped): %q",
			setIdx, clearIdx, captured)
	}
}
