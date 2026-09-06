package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/send"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// ---------------------------------------------------------------------------
// Issue #2089: `session send` over Claude Code's messaging socket.
//
// performSend is the delivery-leg core wired into handleSessionSend: it
// calls chooseSendTransport, then either executeSocketSend (write to
// Claude's socket, bypassing the pane) or executeSend (the historical tmux
// keystroke path). These tests exercise performSend directly with stub
// resolve/sendFn seams so nothing here touches a real live Claude process.
// ---------------------------------------------------------------------------

func claudeInst(sessionID string) *session.Instance {
	return &session.Instance{ID: "i1", Title: "target", Tool: "claude", ClaudeSessionID: sessionID}
}

// instResolveOK is performSend's resolve seam: per-INSTANCE since #2100, so
// tests never have to build the real config dir / pane-tree machinery.
func instResolveOK(*session.Instance) (send.ClaudeSocketTarget, error) {
	return send.ClaudeSocketTarget{SocketPath: "/tmp/whatever.sock", Pid: 1, SessionID: "sid"}, nil
}

// --- jsonFields() for the two new delivery values -------------------------

func TestSendDeliveryResult_JSONFields_QueuedSocket(t *testing.T) {
	r := sendDeliveryResult{delivery: deliveryQueuedSocket, transport: "socket", socketMsgID: "msg-123"}
	fields := r.jsonFields()
	if fields["delivery"] != deliveryQueuedSocket {
		t.Errorf("delivery = %v, want %q", fields["delivery"], deliveryQueuedSocket)
	}
	if fields["submitted"] != false {
		t.Errorf("submitted = %v, want false (a socket write is not evidence the inbox accepted the message)", fields["submitted"])
	}
	if fields["acknowledged"] != false {
		t.Errorf("acknowledged = %v, want false (Claude's inbox never confirms delivery)", fields["acknowledged"])
	}
	if fields["transport"] != "socket" {
		t.Errorf("transport = %v, want %q", fields["transport"], "socket")
	}
	if fields["msg_id"] != "msg-123" {
		t.Errorf("msg_id = %v, want %q", fields["msg_id"], "msg-123")
	}
	if _, present := fields["fallback_reason"]; present {
		t.Errorf("fallback_reason should not be present on a socket success, got %v", fields["fallback_reason"])
	}
}

func TestSendDeliveryResult_JSONFields_SocketWriteFailed(t *testing.T) {
	r := sendDeliveryResult{delivery: deliverySocketWriteFailed, transport: "socket"}
	fields := r.jsonFields()
	if fields["delivery"] != deliverySocketWriteFailed {
		t.Errorf("delivery = %v, want %q", fields["delivery"], deliverySocketWriteFailed)
	}
	if fields["submitted"] != false {
		t.Errorf("submitted = %v, want false", fields["submitted"])
	}
	if fields["acknowledged"] != false {
		t.Errorf("acknowledged = %v, want false", fields["acknowledged"])
	}
	if fields["transport"] != "socket" {
		t.Errorf("transport = %v, want %q", fields["transport"], "socket")
	}
	if _, present := fields["msg_id"]; present {
		t.Errorf("msg_id should not be present on a write failure, got %v", fields["msg_id"])
	}
}

func TestSendDeliveryResult_JSONFields_TmuxFallback_HasReason(t *testing.T) {
	r := sendDeliveryResult{delivery: deliverySubmitted, transport: "tmux", fallbackReason: send.ReasonDeadPid}
	fields := r.jsonFields()
	if fields["submitted"] != true {
		t.Errorf("submitted = %v, want true (tmux submit verification is positive evidence)", fields["submitted"])
	}
	if _, present := fields["acknowledged"]; present {
		t.Errorf("acknowledged is a socket-only field, got %v on tmux", fields["acknowledged"])
	}
	if fields["transport"] != "tmux" {
		t.Errorf("transport = %v, want %q", fields["transport"], "tmux")
	}
	if fields["fallback_reason"] != string(send.ReasonDeadPid) {
		t.Errorf("fallback_reason = %v, want %q", fields["fallback_reason"], send.ReasonDeadPid)
	}
}

func TestSendDeliveryResult_JSONFields_TmuxPin_NoFallbackReason(t *testing.T) {
	// An explicit send_transport=tmux pin is not a fallback: fallbackReason
	// stays empty even though transport is "tmux".
	r := sendDeliveryResult{delivery: deliverySubmitted, transport: "tmux", fallbackReason: ""}
	fields := r.jsonFields()
	if _, present := fields["fallback_reason"]; present {
		t.Errorf("fallback_reason should be absent for an explicit pin, got %v", fields["fallback_reason"])
	}
}

// --- performSend: socket-write-failure -> ErrCodeDeliveryFailed shape, no tmux call ---

func TestPerformSend_SocketWriteFailure_NoTmuxCall(t *testing.T) {
	mock := &mockSendRetryTarget{statuses: []string{"waiting"}, panes: []string{""}}
	failingSend := func(send.ClaudeSocketTarget, string) (string, error) {
		return "", &send.CommittedError{Err: errors.New("simulated write failure")}
	}

	res, err := performSend(claudeInst("sid"), mock, "hello", false, defaultSendTuning(), "auto", nil, instResolveOK, failingSend)
	if err == nil {
		t.Fatal("expected an error from a failing socket write")
	}
	if res.delivery != deliverySocketWriteFailed {
		t.Errorf("delivery = %q, want %q", res.delivery, deliverySocketWriteFailed)
	}
	if res.transport != "socket" {
		t.Errorf("transport = %q, want %q", res.transport, "socket")
	}
	// The socket branch never invokes a WRITE method on the tmux target. It
	// may read its status (the pre-write busy probe falls back to the pane
	// when no hook status is available), which is why this asserts on the
	// send methods rather than on the target being untouched.
	if mock.sendKeysCalls != 0 || mock.sendChunkedCalls != 0 || mock.sendEnterCalls != 0 || mock.sendCtrlCCalls != 0 {
		t.Errorf("tmux target was written to on a socket-path failure: keys=%d chunked=%d enter=%d ctrlc=%d",
			mock.sendKeysCalls, mock.sendChunkedCalls, mock.sendEnterCalls, mock.sendCtrlCCalls)
	}
}

// --- performSend: send_transport=tmux pin, otherwise-good record ----------

func TestPerformSend_TmuxPin_TakesTheTmuxPath(t *testing.T) {
	// Empty pane: the composer-draft guard (issue #1409) sees nothing to
	// hold for. Status "active": the verification loop (issue #876) treats
	// an active transition as immediate positive delivery evidence — same
	// shape as TestSendWithRetryTarget_StopsWhenActive in session_send_test.go.
	mock := &mockSendRetryTarget{statuses: []string{"active"}, panes: []string{""}}

	res, err := performSend(claudeInst("sid"), mock, "hello", false, defaultSendTuning(), "tmux", nil, instResolveOK, nil)
	if err != nil {
		t.Fatalf("performSend with a tmux pin: %v", err)
	}
	if res.transport != "tmux" {
		t.Errorf("transport = %q, want %q (explicit pin)", res.transport, "tmux")
	}
	if res.fallbackReason != "" {
		t.Errorf("fallbackReason = %q, want empty (a pin is not a fallback)", res.fallbackReason)
	}
	// The tmux path was actually exercised, not incidentally skipped.
	if mock.sendKeysCalls == 0 && mock.sendChunkedCalls == 0 {
		t.Errorf("expected the tmux target to be exercised for an explicit send_transport=tmux pin")
	}
}

// Note: an earlier version of this test suite also had an end-to-end test
// against a locally duplicated fake Unix-socket inbox (a second copy of
// internal/send's startFakeInbox, since it's unexported there). Dropped as
// redundant: the wire-format assertions in
// internal/send/claudesocket_test.go (TestSendOverClaudeSocket_WireFormat_*)
// already prove the exact bytes on the wire, and TestPerformSend_* above
// already prove the delivery-leg wiring (transport selection, tmux
// exclusivity, jsonFields shape) — the combination covered nothing the two
// don't already cover individually.

// --- performSend: a socket refusal discovered only at send time (not at
// chooseSendTransport's resolve()) still falls back to tmux, because
// nothing was written to the socket yet. ---

func TestPerformSend_SocketDialFailedAtSendTime_FallsBackToTmux(t *testing.T) {
	// Simulates the target's socket dying in the window between
	// chooseSendTransport's resolve() (which succeeded) and the actual
	// write — e.g. a stale socket file, ECONNREFUSED.
	dialFailed := func(send.ClaudeSocketTarget, string) (string, error) {
		return "", &send.Unavailable{Reason: send.ReasonDialFailed, Err: errors.New("connect: connection refused")}
	}
	mock := &mockSendRetryTarget{statuses: []string{"active"}, panes: []string{""}}

	res, err := performSend(claudeInst("sid"), mock, "hello", false, defaultSendTuning(), "auto", nil, instResolveOK, dialFailed)
	if err != nil {
		t.Fatalf("performSend should succeed via the tmux fallback, got: %v", err)
	}
	if res.transport != "tmux" {
		t.Errorf("transport = %q, want %q (fell back after a late dial failure)", res.transport, "tmux")
	}
	if res.fallbackReason != send.ReasonDialFailed {
		t.Errorf("fallbackReason = %q, want %q", res.fallbackReason, send.ReasonDialFailed)
	}
	if res.delivery != deliverySubmitted {
		t.Errorf("delivery = %q, want %q (a normal tmux send)", res.delivery, deliverySubmitted)
	}
	// Exercised exactly once: one SendKeysAndEnter for the fallback send.
	if mock.sendKeysCalls != 1 {
		t.Errorf("sendKeysCalls = %d, want exactly 1", mock.sendKeysCalls)
	}
}

func TestPerformSend_SocketMessageTooLargeAtSendTime_FallsBackToTmux_TmuxOwnVerdictSurfaces(t *testing.T) {
	// Simulates a message that only turned out to be oversize once
	// SendOverClaudeSocket computed the actual marshaled frame size — still
	// a pre-write refusal (§1.4's size guard runs before the dial), so
	// falling back is exactly as safe as any other resolve()-time refusal.
	tooLarge := func(send.ClaudeSocketTarget, string) (string, error) {
		return "", &send.Unavailable{Reason: send.ReasonTooLarge, Err: errors.New("message too large")}
	}
	// The tmux fallback runs the message through the REAL tmux delivery
	// path, which has its own independent line-length guard
	// (tmux.ErrCanonicalLineOverflow -> deliveryLineTooLong). Confirming
	// that verdict surfaces — not a socket-flavored status — is the point
	// of this test: the fallback must look exactly like an ordinary tmux
	// send that happened to be too long, not like a socket concept leaking
	// through.
	mock := &mockSendRetryTarget{statuses: []string{"active"}, panes: []string{""}, sendKeysErr: tmux.ErrCanonicalLineOverflow}

	res, err := performSend(claudeInst("sid"), mock, "hello", false, defaultSendTuning(), "auto", nil, instResolveOK, tooLarge)
	if err == nil {
		t.Fatal("expected an error: tmux's own line-length guard refuses this send")
	}
	if res.transport != "tmux" {
		t.Errorf("transport = %q, want %q (fell back after a late size refusal)", res.transport, "tmux")
	}
	if res.fallbackReason != send.ReasonTooLarge {
		t.Errorf("fallbackReason = %q, want %q", res.fallbackReason, send.ReasonTooLarge)
	}
	if res.delivery != deliveryLineTooLong {
		t.Errorf("delivery = %q, want %q — tmux's own verdict, not a socket-flavored one", res.delivery, deliveryLineTooLong)
	}
	if mock.sendKeysCalls != 1 {
		t.Errorf("sendKeysCalls = %d, want exactly 1", mock.sendKeysCalls)
	}
}

// --- waitForTurnStart: --wait on the socket path (the socket send returns
// before the target's turn necessarily starts, unlike tmux where
// executeSend's own verification loop already waited for "active") -------

func TestWaitForTurnStart_FlipsActiveAfterNCalls(t *testing.T) {
	mock := &mockStatusChecker{
		statuses: []string{"waiting", "waiting", "active"},
	}
	if !waitForTurnStart(mock, 5*time.Second) {
		t.Fatal("expected waitForTurnStart to return true once status flips to active")
	}
}

func TestWaitForTurnStart_NeverActive_ReturnsFalseWithinBound(t *testing.T) {
	mock := &mockStatusChecker{
		statuses: []string{"waiting"}, // stays "waiting" forever
	}
	const bound = 1200 * time.Millisecond
	start := time.Now()
	if waitForTurnStart(mock, bound) {
		t.Fatal("expected waitForTurnStart to return false: status never went active")
	}
	// Bounded, not hanging: allow slack over `bound` for the poll interval's
	// own granularity, but it must not run away.
	if elapsed := time.Since(start); elapsed > bound+time.Second {
		t.Errorf("waitForTurnStart took %s, want roughly the %s bound", elapsed, bound)
	}
}

// activeAfterChecker reports "waiting" until activeFrom has elapsed since it
// was constructed, "active" for a short window after that (long enough for
// waitForTurnStart's 500ms polling to catch it), then "waiting" again — a
// wall-clock-driven fake, unlike mockStatusChecker's call-count-driven one,
// needed here because waitAfterSend's fix is specifically about how two REAL
// time budgets (waitForTurnStart's and waitForCompletion's) interact. The
// window must end, not stay "active" forever: waitForCompletion treats
// "active" as "still processing, sleep a full poll interval and recheck",
// so a checker that never leaves "active" would add its own unrelated delay
// on top of whatever waitAfterSend does, defeating a test that is trying to
// isolate waitAfterSend's deadline-sharing arithmetic specifically.
type activeAfterChecker struct {
	start      time.Time
	activeFrom time.Duration
	activeFor  time.Duration
}

func (c *activeAfterChecker) GetStatus() (string, error) {
	elapsed := time.Since(c.start)
	if elapsed >= c.activeFrom && elapsed < c.activeFrom+c.activeFor {
		return "active", nil
	}
	return "waiting", nil
}

// TestWaitAfterSend_SocketPath_SharesOneDeadlineAcrossBothWaits is the
// regression test for the bug CodeRabbit caught: waitForTurnStart and
// waitForCompletion each getting their own full `timeout` budget let --wait
// run up to turnStartBound + timeout instead of honoring timeout as a single
// cap. With a 3s --timeout and a target that only goes active at 2s in, the
// old (buggy) shape would run ~2s (turn-start) + a fresh ~3s (completion,
// which itself has a 1s grace sleep plus a 2s poll interval) ≈ 5s+. The
// fixed shape shares one 3s deadline, so total wall time should stay well
// under that — asserted here at 3.5s. Takes ~3s of real wall-clock time
// (this codebase's existing waitForCompletion tests already do the same,
// e.g. TestWaitForCompletion_SessionDeath ~9s), so not skipped as flaky.
func TestWaitAfterSend_SocketPath_SharesOneDeadlineAcrossBothWaits(t *testing.T) {
	checker := &activeAfterChecker{start: time.Now(), activeFrom: 2 * time.Second, activeFor: 400 * time.Millisecond}
	start := time.Now()
	_, _ = waitAfterSend(checker, "socket", 3*time.Second)
	if elapsed := time.Since(start); elapsed > 3500*time.Millisecond {
		t.Errorf("waitAfterSend took %s, want well under ~3.5s for a 3s --timeout (a per-wait-fresh-budget bug would take ~5s+)", elapsed)
	}
}

// TestWaitAfterSend_SocketPath_TurnNeverStarts_ReturnsError is the
// regression test for the CodeRabbit finding: on the socket path, a turn
// that never starts within the bound must be a hard error, not a silent
// fall-through to waitForCompletion (which treats a non-"active" status as
// complete and would let --wait print stale output and exit 0 for a message
// the target never actually consumed).
func TestWaitAfterSend_SocketPath_TurnNeverStarts_ReturnsError(t *testing.T) {
	mock := &mockStatusChecker{statuses: []string{"waiting"}} // never goes active
	const timeout = 1500 * time.Millisecond
	start := time.Now()
	_, err := waitAfterSend(mock, "socket", timeout)
	if err == nil {
		t.Fatal("expected an error: the turn never started within the bound")
	}
	if elapsed := time.Since(start); elapsed > timeout+time.Second {
		t.Errorf("waitAfterSend took %s, want close to the %s timeout, not a further wait", elapsed, timeout)
	}
}

// TestWaitAfterSend_TmuxPath_UnchangedSingleBudget confirms the tmux
// transport skips the turn-start wait entirely and waitForCompletion gets
// the full, unreduced timeout — the "reduces to exactly waitForCompletion"
// claim in waitAfterSend's doc comment.
func TestWaitAfterSend_TmuxPath_UnchangedSingleBudget(t *testing.T) {
	mock := &mockStatusChecker{statuses: []string{"waiting"}}
	start := time.Now()
	status, err := waitAfterSend(mock, "tmux", 3*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "waiting" {
		t.Errorf("status = %q, want %q", status, "waiting")
	}
	// waitForCompletion's own initial grace sleep is 1s; a non-active status
	// resolves on the very first poll after that, so this should be fast —
	// nowhere near the full 3s budget, proving no turn-start wait ran first.
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("waitAfterSend(tmux) took %s, want close to waitForCompletion's ~1s grace period alone", elapsed)
	}
}

// --- busy-at-send probe and the unverified --wait outcomes ---------------

// hookStatusStub builds the probe's hook-driven seam.
func hookStatusStub(status string, err error) func() (string, error) {
	return func() (string, error) { return status, err }
}

// TestProbeTargetBusy covers the tri-state probe, including which source it
// prefers: the hook-driven status --defer-if-busy holds on, with the tmux
// pane only as a fallback for targets that signal cannot speak for.
func TestProbeTargetBusy(t *testing.T) {
	cases := []struct {
		name string
		hook func() (string, error)
		pane *mockSendRetryTarget
		want busyProbeResult
	}{
		{
			name: "hook says running: busy",
			hook: hookStatusStub("running", nil),
			pane: &mockSendRetryTarget{statuses: []string{"waiting"}},
			want: busyProbeBusy,
		},
		{
			name: "hook says starting: busy",
			hook: hookStatusStub("starting", nil),
			want: busyProbeBusy,
		},
		{
			name: "hook says waiting: idle, even though the pane says active",
			hook: hookStatusStub("waiting", nil),
			pane: &mockSendRetryTarget{statuses: []string{"active"}},
			want: busyProbeIdle,
		},
		{
			// The pane heuristic false-positives to idle mid-turn (#1578),
			// which is exactly why the hook signal must win when present.
			name: "hook wins over a pane that claims idle mid-turn",
			hook: hookStatusStub("running", nil),
			pane: &mockSendRetryTarget{statuses: []string{"waiting"}},
			want: busyProbeBusy,
		},
		{
			name: "hook errors: falls back to the pane, which says active",
			hook: hookStatusStub("", errors.New("no session")),
			pane: &mockSendRetryTarget{statuses: []string{"active"}},
			want: busyProbeBusy,
		},
		{
			name: "hook empty: falls back to the pane, which says waiting",
			hook: hookStatusStub("", nil),
			pane: &mockSendRetryTarget{statuses: []string{"waiting"}},
			want: busyProbeIdle,
		},
		{
			name: "no hook seam at all: pane only",
			pane: &mockSendRetryTarget{statuses: []string{"active"}},
			want: busyProbeBusy,
		},
		{
			name: "both sources fail: probe failed, never 'busy'",
			hook: hookStatusStub("", errors.New("no session")),
			pane: &mockSendRetryTarget{statuses: []string{"waiting"}, statusErrs: []error{errors.New("no such pane")}},
			want: busyProbeFailed,
		},
		{
			name: "no sources at all: probe failed",
			want: busyProbeFailed,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var pane statusChecker
			if tc.pane != nil {
				pane = tc.pane
			}
			if got := probeTargetBusy(tc.hook, pane); got != tc.want {
				t.Errorf("probeTargetBusy = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestPerformSend_SocketRecordsProbeOutcome confirms the probe result rides
// out on the delivery result as two independent flags, never conflated.
func TestPerformSend_SocketRecordsProbeOutcome(t *testing.T) {
	okSend := func(send.ClaudeSocketTarget, string) (string, error) { return "msg-1", nil }

	for _, tc := range []struct {
		name          string
		hook          func() (string, error)
		paneStatus    string
		paneErr       error
		wantBusy      bool
		wantProbeFail bool
	}{
		{name: "confirmed busy", hook: hookStatusStub("running", nil), paneStatus: "waiting", wantBusy: true},
		{name: "confirmed idle", hook: hookStatusStub("waiting", nil), paneStatus: "waiting"},
		{
			name: "probe failed", hook: hookStatusStub("", errors.New("gone")),
			paneStatus: "waiting", paneErr: errors.New("no such pane"), wantProbeFail: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockSendRetryTarget{statuses: []string{tc.paneStatus}, panes: []string{""}}
			if tc.paneErr != nil {
				mock.statusErrs = []error{tc.paneErr}
			}
			res, err := performSend(claudeInst("sid"), mock, "hello", false, defaultSendTuning(), "auto", tc.hook, instResolveOK, okSend)
			if err != nil {
				t.Fatalf("performSend: %v", err)
			}
			if res.transport != "socket" {
				t.Fatalf("transport = %q, want socket", res.transport)
			}
			if res.targetBusyAtSend != tc.wantBusy {
				t.Errorf("targetBusyAtSend = %v, want %v", res.targetBusyAtSend, tc.wantBusy)
			}
			if res.busyProbeFailed != tc.wantProbeFail {
				t.Errorf("busyProbeFailed = %v, want %v", res.busyProbeFailed, tc.wantProbeFail)
			}
			fields := res.jsonFields()
			if _, present := fields["target_busy_at_send"]; present != tc.wantBusy {
				t.Errorf("target_busy_at_send present = %v, want %v (absent unless confirmed busy)", present, tc.wantBusy)
			}
			if _, present := fields["busy_probe_failed"]; present != tc.wantProbeFail {
				t.Errorf("busy_probe_failed present = %v, want %v", present, tc.wantProbeFail)
			}
		})
	}
}

// TestSendSuccessData is the test of what the CLI actually emits:
// handleSessionSend builds its --json success payload with exactly this
// function, so the wait_outcome contract is pinned here rather than in a
// helper that reimplements the gate.
func TestSendSuccessData(t *testing.T) {
	inst := claudeInst("sid")

	cases := []struct {
		name            string
		res             sendDeliveryResult
		wait            bool
		wantOutcome     interface{} // nil means the key must be absent
		wantBusyPresent bool
		wantFailPresent bool
	}{
		{
			name:        "tmux send never carries a wait outcome",
			res:         sendDeliveryResult{delivery: deliverySubmitted, transport: "tmux"},
			wait:        true,
			wantOutcome: nil,
		},
		{
			name:        "idle socket send waits normally",
			res:         sendDeliveryResult{delivery: deliveryQueuedSocket, transport: "socket", socketMsgID: "m1"},
			wait:        true,
			wantOutcome: nil,
		},
		{
			name:            "busy socket send is unverified_busy_target",
			res:             sendDeliveryResult{delivery: deliveryQueuedSocket, transport: "socket", targetBusyAtSend: true},
			wait:            true,
			wantOutcome:     waitOutcomeUnverifiedBusyTarget,
			wantBusyPresent: true,
		},
		{
			name:            "probe-failed socket send is unverified_busy_probe_failed",
			res:             sendDeliveryResult{delivery: deliveryQueuedSocket, transport: "socket", busyProbeFailed: true},
			wait:            true,
			wantOutcome:     waitOutcomeUnverifiedBusyProbeFailed,
			wantFailPresent: true,
		},
		{
			name:            "without --wait there is no outcome to report",
			res:             sendDeliveryResult{delivery: deliveryQueuedSocket, transport: "socket", targetBusyAtSend: true},
			wait:            false,
			wantOutcome:     nil,
			wantBusyPresent: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := sendSuccessData(inst, "hello", tc.res, tc.wait)

			if data["success"] != true || data["session_id"] != inst.ID || data["session_title"] != inst.Title {
				t.Errorf("identity fields wrong: %+v", data)
			}
			got, present := data["wait_outcome"]
			if tc.wantOutcome == nil {
				if present {
					t.Errorf("wait_outcome = %v, want absent", got)
				}
			} else if got != tc.wantOutcome {
				t.Errorf("wait_outcome = %v, want %v", got, tc.wantOutcome)
			}
			if _, p := data["target_busy_at_send"]; p != tc.wantBusyPresent {
				t.Errorf("target_busy_at_send present = %v, want %v", p, tc.wantBusyPresent)
			}
			if _, p := data["busy_probe_failed"]; p != tc.wantFailPresent {
				t.Errorf("busy_probe_failed present = %v, want %v", p, tc.wantFailPresent)
			}
		})
	}
}

// TestSendSkippedWaitWarning: only the confirmed-busy outcome may claim the
// target was mid-turn. A failed probe established no such thing.
func TestSendSkippedWaitWarning(t *testing.T) {
	busy := sendSkippedWaitWarning("target", waitOutcomeUnverifiedBusyTarget)
	if !strings.Contains(busy, "was mid-turn") || !strings.Contains(busy, waitOutcomeUnverifiedBusyTarget) {
		t.Errorf("confirmed-busy warning = %q", busy)
	}

	failed := sendSkippedWaitWarning("target", waitOutcomeUnverifiedBusyProbeFailed)
	if !strings.Contains(failed, "could not be confirmed idle") {
		t.Errorf("probe-failed warning must say it could not be confirmed idle, got %q", failed)
	}
	if strings.Contains(failed, "mid-turn") {
		t.Errorf("probe-failed warning must not claim the target was mid-turn, got %q", failed)
	}
	if !strings.Contains(failed, waitOutcomeUnverifiedBusyProbeFailed) {
		t.Errorf("probe-failed warning should name its outcome, got %q", failed)
	}
}

// TestSkippedWaitOutcome is the --wait gate: only a socket send that could
// not be shown idle declines to wait. In particular the tmux path never
// consults the probe, so a busy tmux target waits exactly as it always has.
func TestSkippedWaitOutcome(t *testing.T) {
	cases := []struct {
		name string
		res  sendDeliveryResult
		want string
	}{
		{"socket, confirmed busy", sendDeliveryResult{transport: "socket", targetBusyAtSend: true}, waitOutcomeUnverifiedBusyTarget},
		{"socket, probe failed", sendDeliveryResult{transport: "socket", busyProbeFailed: true}, waitOutcomeUnverifiedBusyProbeFailed},
		{"socket, confirmed idle", sendDeliveryResult{transport: "socket"}, ""},
		{"tmux, busy flag set", sendDeliveryResult{transport: "tmux", targetBusyAtSend: true}, ""},
		{"tmux, probe-failed flag set", sendDeliveryResult{transport: "tmux", busyProbeFailed: true}, ""},
		{"tmux, idle", sendDeliveryResult{transport: "tmux"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := skippedWaitOutcome(tc.res)
			if got != tc.want {
				t.Errorf("skippedWaitOutcome = %q, want %q", got, tc.want)
			}
			if want := got != ""; shouldSkipWaitForBusyTarget(tc.res) != want {
				t.Errorf("shouldSkipWaitForBusyTarget disagrees with skippedWaitOutcome for %+v", tc.res)
			}
		})
	}
}
