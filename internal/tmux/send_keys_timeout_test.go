// Regression tests for the unbounded `tmux send-keys` wake-delivery hang.
//
// The raw key-delivery primitives ran cmd.Run() with no deadline. Against a pane
// whose program transiently stops draining its input pty, `tmux send-keys`
// blocks in Run() forever; when an outer bound (the --no-wait wake-nudge's 5s
// context, or a daemon poll cycle) kills the agent-deck process, the blocked
// send-keys GRANDCHILD is reparented to launchd and hangs indefinitely. Observed
// in production as 9h-old send-keys zombies — one a launchd heartbeat that held
// its slot and killed a conductor's heartbeat.
//
// The fix routes every raw send-keys exec through runSendKeysBounded: a
// per-call deadline (tmuxSendKeysTimeout) plus a process-GROUP SIGKILL on
// timeout, so a wedged send-keys and any grandchild it forked are reaped, never
// orphaned. A timeout returns errSendKeysTimeout (wraps context.DeadlineExceeded)
// — a benign dropped nudge the durable pull model redelivers next turn.
package tmux

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// withShortSendKeysTimeout shrinks the live send-keys deadline for the duration
// of a test so the timeout path is exercised in milliseconds, and restores it.
func withShortSendKeysTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	orig := tmuxSendKeysTimeout
	tmuxSendKeysTimeout = d
	t.Cleanup(func() { tmuxSendKeysTimeout = orig })
}

// pidAlive reports whether pid is still a live (non-reaped) process.
// kill(pid, 0) returns ESRCH once the process is gone.
func pidAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// waitGone polls until pid is reaped or the deadline elapses; returns true if
// the process is gone.
func waitGone(pid int, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if !pidAlive(pid) {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return !pidAlive(pid)
}

// TestRunSendKeysBounded_FastCommandSucceeds: a command that exits immediately
// returns nil with no spurious timeout — the deadline must not penalize the
// healthy <50ms send-keys that dominates production.
func TestRunSendKeysBounded_FastCommandSucceeds(t *testing.T) {
	withShortSendKeysTimeout(t, 500*time.Millisecond)
	start := time.Now()
	if err := runSendKeysBounded(exec.Command("true")); err != nil {
		t.Fatalf("fast command must succeed, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("fast command took %v — should return promptly, not wait out the timer", elapsed)
	}
}

// TestRunSendKeysBounded_PropagatesNonTimeoutError: a command that exits nonzero
// returns its OWN error (so a real tmux failure is still surfaced), NOT the
// timeout sentinel.
func TestRunSendKeysBounded_PropagatesNonTimeoutError(t *testing.T) {
	withShortSendKeysTimeout(t, 500*time.Millisecond)
	err := runSendKeysBounded(exec.Command("false"))
	if err == nil {
		t.Fatal("a nonzero exit must surface an error")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a nonzero exit must NOT be classified as a timeout, got %v", err)
	}
}

// TestRunSendKeysBounded_NilCommand: defensive — a nil command is a no-op.
func TestRunSendKeysBounded_NilCommand(t *testing.T) {
	if err := runSendKeysBounded(nil); err != nil {
		t.Fatalf("nil command must be a no-op, got %v", err)
	}
}

// TestRunSendKeysBounded_TimesOutAndKillsGroup is the core regression. A wedged
// send-keys that forks a grandchild (the exact orphan-zombie shape) must:
//  1. return within ~tmuxSendKeysTimeout (+ reap grace) — never hang, AND
//  2. have its ENTIRE process group SIGKILL'd, so the grandchild is reaped too,
//     not orphaned. (A process-only kill would leave the grandchild alive — the
//     production bug.)
func TestRunSendKeysBounded_TimesOutAndKillsGroup(t *testing.T) {
	withShortSendKeysTimeout(t, 150*time.Millisecond)

	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	// `sh` is the child (made a group leader by runSendKeysBounded's Setpgid).
	// The backgrounded `sleep` is the grandchild; it inherits sh's pgid, so a
	// group-kill must reach it. sh records the grandchild pid then blocks on it.
	cmd := exec.Command("sh", "-c", "sleep 300 & echo $! > "+pidFile+"; wait")

	start := time.Now()
	err := runSendKeysBounded(cmd)
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wedged send-keys must return errSendKeysTimeout (wraps DeadlineExceeded), got %v", err)
	}
	if elapsed > tmuxSendKeysTimeout+sendKeysReapGrace+500*time.Millisecond {
		t.Fatalf("bounded run took %v — must return within timeout (%v) + grace (%v)",
			elapsed, tmuxSendKeysTimeout, sendKeysReapGrace)
	}

	// Read the grandchild pid (written at sh startup, well before the deadline).
	var grandPID int
	for i := 0; i < 100; i++ {
		b, rerr := os.ReadFile(pidFile)
		if rerr == nil {
			if p, perr := strconv.Atoi(strings.TrimSpace(string(b))); perr == nil && p > 0 {
				grandPID = p
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	if grandPID == 0 {
		t.Fatal("grandchild pid never recorded — test scaffold failed")
	}
	if !waitGone(grandPID, 2*time.Second) {
		// The grandchild outlived the group-kill: this is the orphan-zombie bug.
		_ = syscall.Kill(grandPID, syscall.SIGKILL) // cleanup the leak
		t.Fatalf("grandchild pid %d survived the timeout — process group was NOT killed (orphan-zombie regressed)", grandPID)
	}
}

// blockingKeySender swaps keySenderExec for one that builds a REAL blocking
// `sleep` subprocess, so runSendKeysBounded's deadline + group-kill actually
// fire (the recorder seam used by tmux_vim_mode_test.go returns `true`, which
// exits instantly and would never exercise the timeout). Restores on cleanup.
func blockingKeySender(t *testing.T) {
	t.Helper()
	original := keySenderExec
	keySenderExec = func(socketName string, args ...string) *exec.Cmd {
		return exec.Command("sleep", "300")
	}
	t.Cleanup(func() { keySenderExec = original })
}

// TestSendKeysPrimitives_TimeOutBenignly proves the key-delivery primitives that
// honor the keySenderExec seam (SendKeys, SendEnter, SendNamedKey — and through
// them SendKeysChunked / SendKeysAndEnter, the wake-nudge & send-verify paths)
// inherit the bound: against a wedged (blocking) send-keys, each returns within
// ~timeout with the benign DeadlineExceeded classification and no panic, instead
// of hanging the caller. SendCtrlC/SendCtrlU route through the same
// runSendKeysBounded helper (proven directly above); they build via tmuxCmd, not
// this seam, so they are excluded from the deterministic blocking-seam table.
func TestSendKeysPrimitives_TimeOutBenignly(t *testing.T) {
	withShortSendKeysTimeout(t, 120*time.Millisecond)

	cases := []struct {
		name string
		call func(s *Session) error
	}{
		{"SendKeys", func(s *Session) error { return s.SendKeys("hi") }},
		{"SendEnter", func(s *Session) error { return s.SendEnter() }}, // non-vim: single Enter
		{"SendNamedKey", func(s *Session) error { return s.SendNamedKey("BSpace") }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			blockingKeySender(t)

			done := make(chan error, 1)
			s := &Session{Name: "wedged"}
			start := time.Now()
			go func() {
				defer func() {
					if r := recover(); r != nil {
						done <- errors.New("panic in primitive")
					}
				}()
				done <- tc.call(s)
			}()

			select {
			case err := <-done:
				elapsed := time.Since(start)
				if elapsed > tmuxSendKeysTimeout+sendKeysReapGrace+time.Second {
					t.Fatalf("%s took %v — primitive is not bounded", tc.name, elapsed)
				}
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("%s against a wedged send-keys must return the benign timeout sentinel, got %v", tc.name, err)
				}
			case <-time.After(tmuxSendKeysTimeout + sendKeysReapGrace + 3*time.Second):
				t.Fatalf("%s HUNG past the bound — the unbounded send-keys regressed", tc.name)
			}
		})
	}
}

// TestErrSendKeysTimeout_ClassifiesAsDeadline pins the sentinel contract the
// callers depend on: errors.Is(errSendKeysTimeout, context.DeadlineExceeded).
func TestErrSendKeysTimeout_ClassifiesAsDeadline(t *testing.T) {
	if !errors.Is(errSendKeysTimeout, context.DeadlineExceeded) {
		t.Fatal("errSendKeysTimeout must wrap context.DeadlineExceeded for caller classification")
	}
}
