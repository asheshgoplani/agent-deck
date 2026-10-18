package ui

import (
	"fmt"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestIssue2537_HealthAlertWaitsForASendToTheConductor: routed events reach
// the conductor through the send queue's worker, which types under the
// conductor's send lock. A health alert typed from this process takes the
// same lock, so it waits for that send instead of pasting next to it, and is
// typed once the send is done.
func TestIssue2537_HealthAlertWaitsForASendToTheConductor(t *testing.T) {
	env := newIssue2524Env(t, "hook-2537-lock", map[string]string{"bind": "127.0.0.1", "port": issue2524FreePort(t)}, "", "")
	held, err := session.AcquireSendLock(env.conductor.ID, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	released := false
	release := func() {
		if !released {
			released = true
			held.Release()
		}
	}
	t.Cleanup(release)

	text := fmt.Sprintf("issue-2537-alert-%d", time.Now().UnixNano())
	env.home.deliveryQueue().enqueue(conductorDelivery{Conductor: issue2524Conductor, Text: text, Alert: "hook-2537-lock", QueuedAt: time.Now()})
	if env.waitForPane(text, time.Second) {
		t.Fatal("the alert was typed while another send held the conductor's send lock")
	}
	release()
	if !env.waitForPane(text, 10*time.Second) {
		t.Fatal("the alert was not typed once the send lock was free")
	}
	time.Sleep(500 * time.Millisecond)
	if n := env.paneCount(text); n != 1 {
		t.Fatalf("alert typed %d times, want once", n)
	}
}

// TestIssue2537_HealthAlertTypesNothingWhileTheSendLockStaysBusy: a send
// lock held past the wait (a stuck sender) gets nothing typed next to it.
func TestIssue2537_HealthAlertTypesNothingWhileTheSendLockStaysBusy(t *testing.T) {
	env := newIssue2524Env(t, "hook-2537-busy", map[string]string{"bind": "127.0.0.1", "port": issue2524FreePort(t)}, "", "")
	prev := conductorSendLockWait
	conductorSendLockWait = 200 * time.Millisecond
	t.Cleanup(func() { conductorSendLockWait = prev })
	held, err := session.AcquireSendLock(env.conductor.ID, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(held.Release)

	text := fmt.Sprintf("issue-2537-busy-%d", time.Now().UnixNano())
	env.home.deliveryQueue().enqueue(conductorDelivery{Conductor: issue2524Conductor, Text: text, Alert: "hook-2537-busy", QueuedAt: time.Now()})
	queueIdle(t, env.home.deliveryQueue())
	if env.waitForPane(text, 500*time.Millisecond) {
		t.Fatal("the alert was typed although the send lock stayed busy")
	}
}
