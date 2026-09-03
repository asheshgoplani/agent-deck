package main

import (
	"errors"
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

func alwaysResolveOK(string) (send.ClaudeSocketTarget, error) {
	return send.ClaudeSocketTarget{SocketPath: "/tmp/whatever.sock", Pid: 1, SessionID: "sid"}, nil
}

// --- jsonFields() for the two new delivery values -------------------------

func TestSendDeliveryResult_JSONFields_QueuedSocket(t *testing.T) {
	r := sendDeliveryResult{delivery: deliveryQueuedSocket, transport: "socket", socketMsgID: "msg-123"}
	fields := r.jsonFields()
	if fields["delivery"] != deliveryQueuedSocket {
		t.Errorf("delivery = %v, want %q", fields["delivery"], deliveryQueuedSocket)
	}
	if fields["submitted"] != true {
		t.Errorf("submitted = %v, want true (a socket enqueue is a stronger signal than any pane heuristic)", fields["submitted"])
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

	res, err := performSend(claudeInst("sid"), mock, "hello", false, defaultSendTuning(), "auto", alwaysResolveOK, failingSend)
	if err == nil {
		t.Fatal("expected an error from a failing socket write")
	}
	if res.delivery != deliverySocketWriteFailed {
		t.Errorf("delivery = %q, want %q", res.delivery, deliverySocketWriteFailed)
	}
	if res.transport != "socket" {
		t.Errorf("transport = %q, want %q", res.transport, "socket")
	}
	// The socket branch never touches the tmux target at all.
	if mock.sendKeysCalls != 0 || mock.sendChunkedCalls != 0 || mock.sendEnterCalls != 0 || mock.sendCtrlCCalls != 0 {
		t.Errorf("tmux target was touched on a socket-path failure: keys=%d chunked=%d enter=%d ctrlc=%d",
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

	res, err := performSend(claudeInst("sid"), mock, "hello", false, defaultSendTuning(), "tmux", alwaysResolveOK, nil)
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

	res, err := performSend(claudeInst("sid"), mock, "hello", false, defaultSendTuning(), "auto", alwaysResolveOK, dialFailed)
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

	res, err := performSend(claudeInst("sid"), mock, "hello", false, defaultSendTuning(), "auto", alwaysResolveOK, tooLarge)
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
