package session

// Hardening (PR #1230 audit): the detached wake-nudge send must run under a
// bounded context so a hung agent-deck binary (e.g. wedged on SQLite/tmux)
// cannot leak the dispatch goroutine indefinitely. These tests pin the deadline
// behavior without spawning a real slow process.

import (
	"context"
	"testing"
	"time"
)

func TestWakeNudgeExecInheritsEnvWithMachineMarker(t *testing.T) {
	// This subprocess is agent-deck itself, so retain the conductor's env.
	t.Setenv("CLAUDE_CONFIG_DIR", "/test/conductor-config")
	t.Setenv("TELEGRAM_BOT_TOKEN", "test-inherited-token")
	t.Setenv(MachineSendEnv, "0")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := wakeNudgeExec(ctx, "sh", "-c", `
		test "$AGENTDECK_SEND_MACHINE" = 1 &&
		test "$CLAUDE_CONFIG_DIR" = /test/conductor-config &&
		test "$TELEGRAM_BOT_TOKEN" = test-inherited-token
	`); err != nil {
		t.Fatalf("wake subprocess environment: %v", err)
	}
}

// The wake-nudge send always runs under a context deadline, ~wakeNudgeSendTimeout
// out, so a stuck binary is reaped instead of leaking the goroutine forever.
func TestIssue1225_WakeNudgeSendHasTimeout(t *testing.T) {
	orig := wakeNudgeExec
	t.Cleanup(func() { wakeNudgeExec = orig })

	var deadline time.Time
	var hadDeadline bool
	wakeNudgeExec = func(ctx context.Context, bin string, args ...string) error {
		deadline, hadDeadline = ctx.Deadline()
		return nil
	}

	if err := sendWakeNudgeNoWait("", "parent-x", wakeNudgeMessage); err != nil {
		t.Fatalf("send: %v", err)
	}
	if !hadDeadline {
		t.Fatal("wake-nudge send must run under a context deadline (hung-binary guard)")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 || remaining > wakeNudgeSendTimeout+time.Second {
		t.Fatalf("deadline = %v from now, want within (0, %v]", remaining, wakeNudgeSendTimeout)
	}
	// Review round 2 (P3): the nudge may queue behind another sender for the
	// full per-target lock wait; the deadline must outlive that wait plus the
	// send itself, or the subprocess is killed while still waiting on the
	// lock and the wake is silently lost.
	if remaining <= SendTargetLockWait {
		t.Fatalf("deadline = %v from now, must exceed the send lock wait %v", remaining, SendTargetLockWait)
	}
	if wakeNudgeSendTimeout < SendTargetLockWait+wakeNudgeDeliveryBudget {
		t.Fatalf("wakeNudgeSendTimeout = %v, want >= lock wait %v + delivery budget %v", wakeNudgeSendTimeout, SendTargetLockWait, wakeNudgeDeliveryBudget)
	}
}

// The resolved command line is the expected `[-p profile] session send <ref>
// <msg> --no-wait -q` — proving the timeout wrapper didn't drop --no-wait (which
// is what keeps the send fire-and-forget independent of the context bound).
func TestIssue1225_WakeNudgeSendCommandShape(t *testing.T) {
	orig := wakeNudgeExec
	t.Cleanup(func() { wakeNudgeExec = orig })

	var gotArgs []string
	wakeNudgeExec = func(ctx context.Context, bin string, args ...string) error {
		gotArgs = args
		return nil
	}

	if err := sendWakeNudgeNoWait("myprofile", "parent-y", wakeNudgeMessage); err != nil {
		t.Fatalf("send: %v", err)
	}
	want := []string{"-p", "myprofile", "session", "send", "parent-y", wakeNudgeMessage, "--no-wait", "--no-tag", "-q"}
	if len(gotArgs) != len(want) {
		t.Fatalf("args = %v, want %v", gotArgs, want)
	}
	for i := range want {
		if gotArgs[i] != want[i] {
			t.Fatalf("args = %v, want %v (differ at %d)", gotArgs, want, i)
		}
	}
}
