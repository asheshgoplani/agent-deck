//go:build eval_smoke

package session_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/tests/eval/harness"
)

// TestEval_Session_StatusUnderBridgedStdio_NoHang is the user-observable
// guard for the v1.7.56 fix to issue: agent-deck CLI subcommands hang
// forever under Claude Code /remote-control (and any other environment
// that proxies stdio in a way that keeps subprocess pipe fds alive past
// process exit).
//
// Pure Go unit tests can't reach this — they assert on Cmd struct fields
// (TestTmuxExec_SetsWaitDelay) or on Go's runtime contract for WaitDelay
// (TestExecWaitDelay_AbandonsLingeringChildPipe). Neither proves the
// real `agent-deck status` binary completes when its tmux subprocess
// exits but a lingering child holds the stdout pipe — the exact pattern
// /remote-control's bridge produces. This eval drives the actual
// compiled binary against a tmux shim that simulates the lingering-fd
// condition; if the WaitDelay fix in internal/tmux/socket.go regresses,
// `status` blocks past the deadline and this test fails.
//
// Without the fix: cmd.Output() blocks indefinitely because the
// lingering bash child keeps the pipe's write end open, so the I/O
// goroutine never sees EOF — and the 3s context timeout on
// RefreshSessionCache doesn't help because cmd.Wait waits for the
// goroutine, not the process. Test would time out at the 10s ceiling.
//
// With the fix: each tmux subprocess returns within ~2s of its exit
// (cmd.WaitDelay = 2s in internal/tmux/socket.go), so `status` finishes
// well under the 10s ceiling even when it makes multiple tmux calls
// (RefreshSessionCache + RefreshPaneInfoCache).
func TestEval_Session_StatusUnderBridgedStdio_NoHang(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH (shim requires it)")
	}

	sb := harness.NewSandbox(t)
	installLingeringChildTmuxShim(t, sb)

	// Register a session so `status` has something to count. We don't
	// `session start` it — `status` should work whether or not the tmux
	// session is live, and we want to keep the test fast and hermetic.
	workDir := filepath.Join(sb.Home, "project")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}
	runBin(t, sb, "add", "-c", "bash", "-t", "rc", "-g", "evalgrp", workDir)

	// Now the assertion: `agent-deck status` must return within the
	// ceiling. The shim makes every tmux invocation orphan a 10-second
	// sleeping child that inherits stdout/stderr — exactly reproducing
	// the EOF hang pattern that breaks /remote-control.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, sb.BinPath, "status")
	cmd.Env = sb.Env()
	cmd.Dir = sb.Home

	start := time.Now()
	out, err := cmd.CombinedOutput()
	elapsed := time.Since(start)

	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("agent-deck status hung past 10s under lingering-child "+
			"tmux shim — the WaitDelay regression has returned. "+
			"Without cmd.WaitDelay in internal/tmux/socket.go, "+
			"cmd.Output() blocks on the I/O goroutine reading from a "+
			"pipe whose write end is held open by the orphaned bash "+
			"child. Elapsed: %v\nPartial output: %s",
			elapsed, string(out))
	}
	if err != nil {
		// Non-zero exit is acceptable for this test — `status` may
		// legitimately complain about the unstartable session. What we
		// care about is that it RETURNED instead of hanging.
		t.Logf("status returned in %v with non-zero exit (acceptable): %v\noutput: %s",
			elapsed, err, string(out))
	} else {
		t.Logf("status returned cleanly in %v", elapsed)
	}
}

// installLingeringChildTmuxShim writes a bash tmux wrapper into the
// sandbox shim dir that, BEFORE forwarding to real tmux, spawns a
// 10-second sleep that inherits stdout/stderr. This is the minimal
// reproduction of the /remote-control hang condition: a forked child
// keeps the subprocess's stdio pipe write end open after the named
// process (real tmux here, the tmux server normally) has exited. Go's
// cmd.Output() then blocks on its read goroutine waiting for EOF that
// never arrives.
//
// This shim deliberately differs from harness.InstallTmuxShim — that
// one only forces -S <socket>. We need the lingering-child injection
// AND the -S forcing, so both behaviors live here.
func installLingeringChildTmuxShim(t *testing.T, sb *harness.Sandbox) {
	t.Helper()
	shimPath := filepath.Join(sb.ShimDir, "tmux")

	script := fmt.Sprintf(`#!/usr/bin/env bash
# Eval shim: simulate the /remote-control bridged-stdio EOF hang by
# spawning a child that holds stdout/stderr open past tmux's exit.
# Then forward to the real tmux against the per-sandbox socket.
set -u

SOCK=%q
SHIM_DIR=%q

# Orphan a sleeping child that inherits our stdio. After we exec real
# tmux below, this child still holds the stdout/stderr pipe write ends,
# so EOF never arrives to whoever is reading the pipes (agent-deck).
# 10s is comfortably more than the WaitDelay (2s) so the bug clearly
# wins without the fix.
sleep 10 &
disown

real=""
IFS=":" read -ra parts <<< "$PATH"
for p in "${parts[@]}"; do
  [ "$p" = "$SHIM_DIR" ] && continue
  if [ -x "$p/tmux" ]; then
    real="$p/tmux"
    break
  fi
done
if [ -z "$real" ]; then
  echo "eval-tmux-shim: no real tmux found on PATH (excluding shim dir)" >&2
  exit 127
fi

exec "$real" -S "$SOCK" "$@"
`, sb.TmuxSocket(), sb.ShimDir)

	if err := os.WriteFile(shimPath, []byte(script), 0o755); err != nil {
		t.Fatalf("lingering-child tmux shim write: %v", err)
	}
}
