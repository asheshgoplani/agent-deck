package tmux

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"testing"
)

// socket_waitdelay_test.go pins the RUNTIME half of the WaitDelay contract:
// that Cmd.WaitDelay fires on a lingering-child fd and that the bytes written
// before the I/O goroutine was abandoned survive in the captured stdout.
//
// This file pins the CALLER half, which nothing covered. tmuxSubprocessWaitDelay
// documents it: "when errors.Is(err, exec.ErrWaitDelay) and the captured stdout
// looks valid (non-empty, parses cleanly), treat it as success." Under the
// bridged-stdio setups that motivated WaitDelay (Claude Code /remote-control,
// ssh ControlMaster), ignoring that turns every pane-PID probe into a failure
// even though the PID is sitting in the buffer — which now decides whether the
// SIGTERM->SIGKILL escalation runs at all, so orphans would accumulate on every
// kill in exactly those environments.

func TestParsePanePID_AcceptsWaitDelayWithValidOutput(t *testing.T) {
	pid, err := parsePanePID([]byte("4242\n"), fmt.Errorf("wrapped: %w", exec.ErrWaitDelay))
	if err != nil {
		t.Fatalf("ErrWaitDelay with a parseable pane_pid must be treated as success; got err %v", err)
	}
	if pid != 4242 {
		t.Fatalf("pane_pid = %d, want 4242", pid)
	}
}

func TestParsePanePID_RejectsWaitDelayWithUnusableOutput(t *testing.T) {
	if _, err := parsePanePID([]byte("  \n"), exec.ErrWaitDelay); err == nil {
		t.Fatal("ErrWaitDelay with no usable pane_pid must stay an error: a " +
			"buffer holding nothing is indeterminate, not 'no processes'")
	}
}

func TestParsePanePID_RejectsGenuineFailure(t *testing.T) {
	// A client killed at the deadline reports a plain ExitError. That is a real
	// failure and must not be rescued just because the buffer has bytes.
	if _, err := parsePanePID([]byte("4242\n"), errors.New("signal: killed")); err == nil {
		t.Fatal("a non-WaitDelay error must propagate")
	}
}

func TestParsePanePID_RejectsNonPositivePID(t *testing.T) {
	if _, err := parsePanePID([]byte("0\n"), nil); err == nil {
		t.Fatal("pane_pid 0 must be an error: it is the value the old code " +
			"degraded to, and it matches no live process")
	}
}

func TestParsePanePID_TakesFirstLineOnly(t *testing.T) {
	pid, err := parsePanePID([]byte("101\n202\n303\n"), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pid != 101 {
		t.Fatalf("multi-pane output must yield the first pane_pid; got %d", pid)
	}
}

// annotateDeadline is what lets a caller tell "we gave up on tmux" from "tmux
// told us no". Without it every timeout looks like a hard tmux failure, and
// SwitchAttachedClients cannot honor its documented fallback contract.

func TestAnnotateDeadline_MarksDeadlineFailures(t *testing.T) {
	cause := errors.New("signal: killed")
	err := annotateDeadline(context.DeadlineExceeded, cause)
	if !errors.Is(err, errTmuxTimeout) {
		t.Fatalf("a run that failed while its context deadline had fired must be "+
			"identifiable as a timeout; got %v", err)
	}
	// The underlying cause must stay readable for logs.
	if !errors.Is(err, cause) {
		t.Fatalf("the original error must be preserved alongside the sentinel; got %q", err)
	}
}

func TestAnnotateDeadline_LeavesGenuineFailuresAlone(t *testing.T) {
	cause := errors.New("can't find session: agentdeck-nope")
	err := annotateDeadline(nil, cause)
	if errors.Is(err, errTmuxTimeout) {
		t.Fatal("a tmux-reported failure must not be disguised as a timeout: " +
			"callers treat timeouts as benign and would swallow a real error")
	}
	if !errors.Is(err, cause) {
		t.Fatalf("cause must pass through unchanged; got %v", err)
	}
}

func TestAnnotateDeadline_SuccessStaysSuccess(t *testing.T) {
	// A context can be past its deadline by the time the command returns and
	// still have succeeded; only an actual failure gets annotated.
	if err := annotateDeadline(context.DeadlineExceeded, nil); err != nil {
		t.Fatalf("nil run error must stay nil; got %v", err)
	}
}
