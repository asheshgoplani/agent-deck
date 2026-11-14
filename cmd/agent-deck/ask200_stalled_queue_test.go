package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// ASK-200: a Claude conductor whose turn had ended (Stop hook fired, hook
// status waiting) kept every automated send in its queued-messages list
// ("Press up to edit queued messages") for five hours. Each `session send
// --no-wait` typed the alert, its Enter put it in that queue, the attribution
// gate then refused every bare Enter at the queued hint, and the send exited
// 0 as "delivered". One Enter on the empty composer, pressed by a person,
// submitted the whole queue.

// fakeSubmittedContaining counts the user records the fake wrote whose
// content holds msg (a flushed queue is one record holding every prompt).
func fakeSubmittedContaining(t *testing.T, transcript, msg string) int {
	t.Helper()
	n := 0
	for _, line := range strings.Split(readGolden(t, transcript), "\n") {
		var rec struct {
			Type    string
			Message struct{ Content any }
		}
		if json.Unmarshal([]byte(line), &rec) != nil || rec.Type != "user" {
			continue
		}
		if s, ok := rec.Message.Content.(string); ok && strings.Contains(s, msg) {
			n++
		}
	}
	return n
}

func hookKnown(busy bool) func() (bool, bool) {
	return func() (bool, bool) { return busy, true }
}

// An idle Claude (hook says waiting) that parks the send behind its queued
// hint must have that queue flushed by the sender: the message is submitted.
func TestASK200StalledQueueOnIdleClaudeIsFlushed(t *testing.T) {
	sess, transcript := claudeLikeFakeControl(t, "idle", map[string]any{"stall_queue": true})
	msg := fmt.Sprintf("ASK200_STALLED_%d deckbox ALERT dashboard refresher failed", time.Now().UnixNano())
	tun := sendTuning(true, false)
	tun.retry.targetBusyByHook = hookKnown(false)

	res, err := executeSend(sess, "claude", msg, true, tun)
	if got := fakeSubmittedContaining(t, transcript, msg); got != 1 {
		t.Fatalf("message submitted %d time(s), want 1 (delivery=%q err=%v); queued=%d",
			got, res.delivery, err, fakeQueued(t, transcript, msg))
	}
	if err != nil {
		t.Fatalf("flushed send reported an error: delivery=%q err=%v", res.delivery, err)
	}
}

// A second send behind an earlier stalled one flushes both: the earlier
// prompt was already submitted by its sender and only waits on Claude.
func TestASK200StalledQueueFlushCarriesEarlierQueuedPrompt(t *testing.T) {
	sess, transcript := claudeLikeFakeControl(t, "idle", map[string]any{"stall_queue": true})
	earlier := fmt.Sprintf("ASK200_EARLIER_%d", time.Now().UnixNano())
	if err := sess.SendKeysAndEnter(earlier); err != nil {
		t.Fatal(err)
	}
	waitForPane(t, sess, func(p string) bool { return strings.Contains(p, "Press up to edit queued messages") })

	msg := fmt.Sprintf("ASK200_LATER_%d deckbox ALERT", time.Now().UnixNano())
	tun := sendTuning(true, false)
	tun.retry.targetBusyByHook = hookKnown(false)
	if res, err := executeSend(sess, "claude", msg, true, tun); err != nil {
		t.Fatalf("delivery=%q err=%v", res.delivery, err)
	}
	if fakeSubmittedContaining(t, transcript, earlier) != 1 || fakeSubmittedContaining(t, transcript, msg) != 1 {
		t.Fatalf("queue not flushed: earlier=%d later=%d", fakeSubmittedContaining(t, transcript, earlier), fakeSubmittedContaining(t, transcript, msg))
	}
}

// The flush is for an idle target only. A target the hook reports mid-turn
// (or a hook that is unknown) keeps the ASK-95 rule: no bare Enter at the
// queued hint, Claude takes the queue up when its turn ends.
func TestASK200NoFlushWhileHookBusyOrUnknown(t *testing.T) {
	for name, probe := range map[string]func() (bool, bool){
		"busy":    hookKnown(true),
		"unknown": func() (bool, bool) { return false, false },
	} {
		t.Run(name, func(t *testing.T) {
			sess, transcript := claudeLikeFakeControl(t, "idle", map[string]any{"stall_queue": true})
			msg := fmt.Sprintf("ASK200_HELD_%s_%d", name, time.Now().UnixNano())
			tun := sendTuning(true, false)
			tun.retry.targetBusyByHook = probe
			_, _ = executeSend(sess, "claude", msg, true, tun)
			if got := fakeSubmittedContaining(t, transcript, msg); got != 0 {
				t.Fatalf("hook %s: Enter pressed at the queued hint, message submitted %d time(s)", name, got)
			}
		})
	}
}

// A stalled queue the flush Enter cannot release is a failed send, not the
// exit-0 "delivered" the conductor's alert job got for five hours.
func TestASK200StalledQueueThatStaysHeldIsNotSuccess(t *testing.T) {
	sess, transcript := claudeLikeFakeControl(t, "idle", map[string]any{"stall_queue": true, "stall_queue_deaf": true})
	msg := fmt.Sprintf("ASK200_DEAF_%d deckbox ALERT", time.Now().UnixNano())
	tun := sendTuning(true, false)
	tun.retry.targetBusyByHook = hookKnown(false)
	res, err := executeSend(sess, "claude", msg, true, tun)
	if fakeQueued(t, transcript, msg) != 1 || fakeSubmittedContaining(t, transcript, msg) != 0 {
		t.Fatalf("fixture: want the message queued and unsubmitted")
	}
	if err == nil || res.delivery != deliveryTypedNotSubmitted {
		t.Fatalf("delivery=%q err=%v, want %q with an error", res.delivery, err, deliveryTypedNotSubmitted)
	}
}
