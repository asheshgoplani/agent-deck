package main

import (
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/health"
	"github.com/asheshgoplani/agent-deck/internal/sendqueue"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// The queue controls inside the real worker loop, composed with the retry
// backoff (#2481) and the final journal record of a queued send.

func waitQueueControlResult(t *testing.T, dir, id string, within time.Duration) queueControlResult {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sendqueue.ControlResultPath(dir, id)); err == nil {
			return queueControlResultForTest(t, dir, id)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no control result for %s within %v", id, within)
	return queueControlResult{}
}

// A send refused before typing sits in a long backoff. release types it at
// once through the release child and answers delivered on confirmed
// submission, without waiting for the backoff to end.
func TestQueueReleaseCutsRetryBackoff(t *testing.T) {
	t.Setenv("AGENTDECK_SEND_WORKER_POLL", "3s")
	t.Setenv("AGENTDECK_SEND_LAND_WINDOW", "200ms")
	f := newRetryFixture(t, "_test_queue_release_backoff")
	starts := stubChild(t, -1) // the normal path stays refused
	var released atomic.Int32
	previous := sendChildRelease
	t.Cleanup(func() { sendChildRelease = previous })
	sendChildRelease = func(profile, id, message, resultPath string) (int, func() int, error) {
		released.Add(1)
		_ = os.WriteFile(resultPath, []byte(`{"success":true,"submitted":true,"delivery":"submitted","confirmation":"confirmed"}`), 0o600)
		return 4343, func() int { return 0 }, nil
	}
	rec := f.queue(t, time.Hour, "cli")
	id := rec.SendID // the worker owns rec from here on
	done := make(chan struct{})
	go func() { deliverQueuedAsync(f.profile, f.dir, rec); close(done) }()
	deadline := time.Now().Add(20 * time.Second)
	for len(starts()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if len(starts()) == 0 {
		t.Fatal("the worker never attempted the send")
	}
	requested := time.Now()
	if err := requestQueueControl(f.dir, id, "release"); err != nil {
		t.Fatal(err)
	}
	result := waitQueueControlResult(t, f.dir, id, 10*time.Second)
	if waited := time.Since(requested); waited > 2500*time.Millisecond {
		t.Fatalf("release waited %v, the backoff instead of answering at once", waited)
	}
	if result.Outcome != "delivered" || released.Load() != 1 || result.Record == nil || result.Record.DeliveryEvidence["confirmation"] != "confirmed" {
		t.Fatalf("release result %+v after %d release children", result, released.Load())
	}
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("worker kept going after the release delivered")
	}
	got, err := sendqueue.Load(f.dir, id)
	if err != nil || got.State != sendqueue.StateSubmitted || got.Attempts != len(starts())+1 {
		t.Fatalf("record after release: %+v (normal attempts %d) %v", got, len(starts()), err)
	}
}

// A cancel for a later entry is answered while the worker waits on an older
// one in backoff; the cancelled send is final, never typed, and journaled
// as cancelled rather than as a delivery.
func TestQueueCancelBehindBusyEntryIsFinalAndJournaled(t *testing.T) {
	t.Setenv("AGENTDECK_SEND_WORKER_POLL", "3s")
	t.Cleanup(session.SetCommsLedgerForTest(true))
	f := newRetryFixture(t, "_test_queue_cancel_behind")
	starts := stubChild(t, -1)
	first := f.queue(t, time.Hour, "cli")
	firstID := first.SendID // the worker owns first from here on
	second := f.queue(t, time.Hour, f.senderID)
	done := make(chan struct{})
	go func() { deliverQueuedAsync(f.profile, f.dir, first); close(done) }()
	t.Cleanup(func() {
		// Stop the worker before the temp dirs go: cancel the first entry.
		_ = requestQueueControl(f.dir, firstID, "cancel")
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			t.Error("worker did not stop after the first entry was cancelled")
		}
	})
	deadline := time.Now().Add(20 * time.Second)
	for len(starts()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if err := requestQueueControl(f.dir, second.SendID, "cancel"); err != nil {
		t.Fatal(err)
	}
	result := waitQueueControlResult(t, f.dir, second.SendID, 10*time.Second)
	if result.Outcome != "cancelled" {
		t.Fatalf("cancel result %+v", result)
	}
	got, err := sendqueue.Load(f.dir, second.SendID)
	if err != nil || got.State != sendqueue.StateCancelled || got.Attempts != 0 || !got.Final() {
		t.Fatalf("cancelled entry: %+v %v", got, err)
	}
	if again, _ := sendqueue.Load(f.dir, firstID); again == nil || again.State == sendqueue.StateCancelled {
		t.Fatalf("first entry changed by a cancel of the second: %+v", again)
	}
	dir, err := session.HealthLogDir(f.profile)
	if err != nil {
		t.Fatal(err)
	}
	events, _, err := health.ReadEvents(dir, time.Time{}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	finals := 0
	for _, e := range events {
		if e.Kind == health.KindSend && e.Detail["final"] == true && e.Detail["send_id"] == second.SendID {
			finals++
			if e.Detail["outcome"] != queuedOutcomeCancelled {
				t.Fatalf("cancelled send journaled with outcome %v", e.Detail["outcome"])
			}
		}
	}
	if finals != 1 {
		t.Fatalf("cancelled send journaled %d final records, want 1", finals)
	}
	spool, err := session.ReadCommsSpool(second.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range spool {
		if e.Edge == session.CommsEdgeDelivery && e.Ref == second.SendID {
			found = true
			if e.State != "failed" || !strings.Contains(e.Prompt, "cancelled") {
				t.Fatalf("cancelled send in the ledger as %q (%q)", e.State, e.Prompt)
			}
		}
	}
	if !found {
		t.Fatalf("no ledger record for the cancelled send: %+v", spool)
	}
}
