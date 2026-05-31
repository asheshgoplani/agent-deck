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

	if err := sendWakeNudgeNoWait("", "parent-x"); err != nil {
		t.Fatalf("send: %v", err)
	}
	if !hadDeadline {
		t.Fatal("wake-nudge send must run under a context deadline (hung-binary guard)")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 || remaining > wakeNudgeSendTimeout+time.Second {
		t.Fatalf("deadline = %v from now, want within (0, %v]", remaining, wakeNudgeSendTimeout)
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

	if err := sendWakeNudgeNoWait("myprofile", "parent-y"); err != nil {
		t.Fatalf("send: %v", err)
	}
	want := []string{"-p", "myprofile", "session", "send", "parent-y", wakeNudgeMessage, "--no-wait", "-q"}
	if len(gotArgs) != len(want) {
		t.Fatalf("args = %v, want %v", gotArgs, want)
	}
	for i := range want {
		if gotArgs[i] != want[i] {
			t.Fatalf("args = %v, want %v (differ at %d)", gotArgs, want, i)
		}
	}
}
