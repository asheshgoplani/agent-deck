package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/sendqueue"
)

func TestBusyClaudeQueueMarkerReturnsWithinTwoSeconds(t *testing.T) {
	const msg = "SEND_FAST_UNIQUE_123456789"
	mock := &mockSendRetryTarget{
		statuses: []string{"active"},
		panes:    []string{busyPaneNoBody(), busyPaneWithBody(msg)},
	}
	start := time.Now()
	delivery, err := sendWithRetryTarget(mock, msg, false, sendRetryOptions{
		tool: "claude", maxRetries: 10, checkDelay: 100 * time.Millisecond,
		verifyDelivery:   true,
		targetBusyByHook: func() (bool, bool) { return true, true },
	})
	if err != nil || delivery != deliveryQueued || time.Since(start) >= 2*time.Second {
		t.Fatalf("delivery=%q err=%v elapsed=%v", delivery, err, time.Since(start))
	}
}

func TestBusyClaudeNoVisibleEchoIsUnknownAfterBound(t *testing.T) {
	mock := &mockSendRetryTarget{statuses: []string{"active"}, panes: []string{claudeComposer("")}}
	delivery, err := sendWithRetryTarget(mock, "SEND_FAST_UNKNOWN_123456789", false, sendRetryOptions{
		tool: "claude", maxRetries: 2, checkDelay: 0, verifyDelivery: true,
		targetBusyByHook: func() (bool, bool) { return true, true },
	})
	if err != nil || delivery != deliveryUnverified {
		t.Fatalf("delivery=%q err=%v", delivery, err)
	}
}

func TestQueueChildEchoUpgradesVerdict(t *testing.T) {
	for _, tc := range []struct{ delivery, verdict string }{
		{deliveryQueued, "delivered"},
		{deliveryDelivered, "delivered"},
		{deliveryUnverified, "unknown"},
	} {
		rec := &sendqueue.Record{State: sendqueue.StateTyping, Verdict: "queued"}
		set := func(fn func(*sendqueue.Record)) error { fn(rec); return nil }
		applyChildResult(rec, map[string]interface{}{"success": true, "delivery": tc.delivery}, 0, true, set)
		if rec.Verdict != tc.verdict {
			t.Errorf("%s: verdict %s, want %s", tc.delivery, rec.Verdict, tc.verdict)
		}
	}
}

func TestPendingQueueDoesNotWaitForEarlierTranscript(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	var last string
	for i := 0; i < 13; i++ {
		id, err := sendqueue.NextID(dir, now)
		if err != nil {
			t.Fatal(err)
		}
		last = id
		state := sendqueue.StateTyped
		if i == 12 {
			state = sendqueue.StateQueued
		}
		if err := sendqueue.Save(dir, &sendqueue.Record{SendID: id, State: state, SessionID: "busy-claude"}); err != nil {
			t.Fatal(err)
		}
	}
	if next := nextPending(dir, "busy-claude"); next == nil || next.SendID != last {
		t.Fatalf("next send = %+v, want final queued message", next)
	}
	// The earlier sends remain readable for independent transcript watchers.
	if files, err := filepath.Glob(filepath.Join(dir, "*.json")); err != nil || len(files) != 13 {
		t.Fatalf("durable records: %d, %v", len(files), err)
	}
	if _, err := os.Stat(filepath.Join(dir, last+".json")); err != nil {
		t.Fatal(err)
	}
}

func TestBusyAndIdleHarnessQueueGate(t *testing.T) {
	for _, tc := range []struct {
		tool, status string
		wait         bool
	}{
		{"claude", "running", false}, {"claude", "waiting", false},
		{"codex", "running", true}, {"codex", "waiting", false},
		{"pi", "running", true}, {"pi", "waiting", false},
		{"shell", "running", true}, {"shell", "waiting", false},
		{"unknown", "running", true}, {"unknown", "waiting", false},
		{"claude", "unknown", true}, {"shell", "", true},
	} {
		if got := shouldWaitForIdle(tc.tool, tc.status); got != tc.wait {
			t.Errorf("%s %s: wait=%v, want %v", tc.tool, tc.status, got, tc.wait)
		}
	}
}
