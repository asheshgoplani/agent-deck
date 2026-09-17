package main

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Messaging audit P2-2 (#2104): `session send` had no per-target lock, so the
// daemon's [INBOX] nudge, the heartbeat, the bridge and sibling sessions all
// typed into the same pane at once. performSend now holds a per-target flock
// through the readiness guard, the paste and the Enter (and the socket write),
// with a bounded wait and a distinct "target busy with another send" verdict.

func sendLockTestHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
}

// TestPerformSend_ConcurrentSendsToOneTargetSerialize runs the #2104 race
// through performSend: sender B arrives while A's body is still landing.
// With the per-target lock B waits, and BOTH messages reach Claude intact,
// in order, with no refusal.
func TestPerformSend_ConcurrentSendsToOneTargetSerialize(t *testing.T) {
	sendLockTestHome(t)
	const (
		bodyA = "[SENDER A] lane timer: review the waiting sessions and report back"
		bodyB = "[SENDER B] inbox nudge: a new transition notification is waiting"
	)
	pane := &racingClaudePane{
		chunkSize:   4,
		chunkDelay:  10 * time.Millisecond,
		exitWindow:  time.Second,
		redrawDelay: 40 * time.Millisecond,
	}
	tuning := testGuardTuning(sendRetryOptions{maxRetries: 20, checkDelay: 5 * time.Millisecond, verifyDelivery: true})
	tuning.guardHold = 20 * time.Millisecond
	tuning.guardPoll = 5 * time.Millisecond
	tuning.guardClearWait = 30 * time.Millisecond
	inst := &session.Instance{ID: "target-2104-lock", Title: "conductor", Tool: "claude"}

	type outcome struct {
		res sendDeliveryResult
		err error
	}
	var wg sync.WaitGroup
	outcomes := make([]outcome, 2)
	wg.Add(1)
	go func() {
		defer wg.Done()
		res, err := performSend(inst, pane, bodyA, true, tuning, "tmux", false, nil, nil, nil)
		outcomes[0] = outcome{res, err}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for !pane.composerShowsText() {
		if time.Now().After(deadline) {
			t.Fatal("sender A never started typing")
		}
		time.Sleep(time.Millisecond)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		res, err := performSend(inst, pane, bodyB, true, tuning, "tmux", false, nil, nil, nil)
		outcomes[1] = outcome{res, err}
	}()
	wg.Wait()

	submitted, exited, ctrlCTimes := pane.snapshot()
	if exited || len(ctrlCTimes) != 0 {
		t.Fatalf("racing sends must never interrupt the pane: exited=%v ctrlC=%d", exited, len(ctrlCTimes))
	}
	for i, o := range outcomes {
		if o.err != nil {
			t.Errorf("sender %d must be delivered once the lock serializes the sends, got delivery=%q err=%v", i, o.res.delivery, o.err)
		}
	}
	if len(submitted) != 2 || strings.TrimSpace(submitted[0]) != bodyA || strings.TrimSpace(submitted[1]) != bodyB {
		t.Fatalf("submissions must be both bodies intact and in lock order, got %q", submitted)
	}
}

func TestPerformSend_TargetHeldByAnotherSendReportsBusy(t *testing.T) {
	sendLockTestHome(t)
	prev := sendTargetLockWait
	sendTargetLockWait = 200 * time.Millisecond
	t.Cleanup(func() { sendTargetLockWait = prev })

	inst := &session.Instance{ID: "target-2104-busy", Title: "conductor", Tool: "claude"}
	held, err := session.AcquireSendLock(inst.ID, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()

	mock := &mockSendRetryTarget{statuses: []string{"active"}, panes: []string{""}}
	start := time.Now()
	res, err := performSend(inst, mock, "hello", false, defaultSendTuning(), "tmux", false, nil, nil, nil)
	if err == nil || !errors.Is(err, session.ErrConfigLockBusy) || !strings.Contains(err.Error(), "busy with another send") {
		t.Fatalf("a held target must yield the busy verdict, got res=%+v err=%v", res, err)
	}
	if res.delivery != deliveryTargetBusy {
		t.Fatalf("delivery = %q, want %q", res.delivery, deliveryTargetBusy)
	}
	if time.Since(start) < sendTargetLockWait {
		t.Fatal("busy verdict returned before the bounded wait elapsed")
	}
	if mock.sendKeysCalls != 0 || mock.sendChunkedCalls != 0 || mock.sendEnterCalls != 0 {
		t.Fatalf("nothing may be typed into a target another send holds: keys=%d chunked=%d enter=%d", mock.sendKeysCalls, mock.sendChunkedCalls, mock.sendEnterCalls)
	}
	if deliveryConfirmation(deliveryTargetBusy) != "failed" {
		t.Fatalf("target_busy must classify as a failed confirmation, got %q", deliveryConfirmation(deliveryTargetBusy))
	}
}
