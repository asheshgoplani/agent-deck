package main

import (
	"strings"
	"sync/atomic"
	"testing"
)

// ASK-95 lander review: once a busy Claude already shows "Press up to edit
// queued messages" from earlier input, that hint is no acknowledgement of a
// NEW send. A message that never reached Claude's queue must not be graded
// "queued" (a confirmed verdict) just because the old hint is still there.

// staleHintPane serves before until the message is typed, then after in turn
// (the last frame repeats).
type staleHintPane struct {
	mockSendRetryTarget
	typed  atomic.Bool
	idx    atomic.Int32
	before string
	after  []string
}

func (f *staleHintPane) SendKeysAndEnter(s string) error {
	f.typed.Store(true)
	return f.mockSendRetryTarget.SendKeysAndEnter(s)
}

func (f *staleHintPane) CapturePaneFresh() (string, error) {
	if !f.typed.Load() {
		return f.before, nil
	}
	i := int(f.idx.Add(1) - 1)
	if i >= len(f.after) {
		i = len(f.after) - 1
	}
	return f.after[i], nil
}

func hintPane(extra, composer string) string {
	div := strings.Repeat("─", 80)
	return busyClaudeHead + extra + div + "\n❯ " + composer + "\n" + div + "\n  ⏵⏵ bypass permissions on (shift+tab to cycle)\n"
}

func TestStaleQueuedHintIsNoQueueAcknowledgement(t *testing.T) {
	const msg = "STALE_HINT_PROBE second message body here"
	const hint = "Press up to edit queued messages"
	cases := map[string]*staleHintPane{
		// Typed, then cleared without queuing (operator Esc): the old hint returns.
		"typed then discarded": {before: hintPane("", hint), after: []string{hintPane("", msg), hintPane("", hint)}},
		// Keystrokes lost; the running turn's output quotes the text.
		"echo in output": {before: hintPane("", hint), after: []string{hintPane("⏺ quoting: "+msg+"\n", hint)}},
	}
	for name, f := range cases {
		t.Run(name, func(t *testing.T) {
			f.statuses = []string{"running"}
			tun := testGuardTuning(sendRetryOptions{maxRetries: 6, checkDelay: 0, verifyDelivery: true, targetBusyByHook: hookSeq(probeBusy)})
			res, _ := executeSend(f, "claude", msg, true, tun)
			if res.delivery == deliveryQueued || res.delivery == deliverySubmitted {
				t.Fatalf("delivery=%q: a hint left from earlier input confirmed a send it never saw", res.delivery)
			}
			if n := atomic.LoadInt32(&f.sendKeysCalls); n != 1 {
				t.Fatalf("message typed %d time(s), want 1", n)
			}
		})
	}
}

// The hint appearing for THIS send still confirms it as queued (#1978).
func TestFreshQueuedHintStillAcknowledgesSend(t *testing.T) {
	const msg = "FRESH_HINT_PROBE second message body here"
	f := &staleHintPane{before: hintPane("", ""), after: []string{hintPane("> "+msg+"\n", "Press up to edit queued messages")}}
	f.statuses = []string{"running"}
	tun := testGuardTuning(sendRetryOptions{maxRetries: 6, checkDelay: 0, verifyDelivery: true, targetBusyByHook: hookSeq(probeBusy)})
	if res, err := executeSend(f, "claude", msg, true, tun); err != nil || res.delivery != deliveryQueued {
		t.Fatalf("delivery=%q err=%v, want %q", res.delivery, err, deliveryQueued)
	}
}
