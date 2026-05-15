package main

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// neverReadyChecker simulates a busy agent that never reaches "waiting"/"idle".
// GetStatus always returns "active" (the loading state), so waitForAgentReady
// can never satisfy its readiness predicate and must hit the timeout.
type neverReadyChecker struct {
	calls atomic.Int64
}

func (m *neverReadyChecker) GetStatus() (string, error) {
	m.calls.Add(1)
	return "active", nil
}

func (m *neverReadyChecker) CapturePaneFresh() (string, error) {
	return "", nil
}

// TestSessionSend_RespectsTimeoutFlag_RegressionFor957 is the regression test
// for issue #957: `session send --timeout <duration>` must bound the
// agent-ready wait, not just the post-ready completion wait.
//
// Before the fix, waitForAgentReady ignored its caller's timeout and used a
// hardcoded 80s (400 attempts × 200ms). With a 1s caller-supplied timeout
// against a never-ready mock, the call took ~80s instead of ~1s.
//
// After the fix, the function returns within a small multiple of the
// requested timeout.
func TestSessionSend_RespectsTimeoutFlag_RegressionFor957(t *testing.T) {
	mock := &neverReadyChecker{}

	requested := 1 * time.Second
	start := time.Now()
	err := waitForAgentReady(mock, "shell", requested)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected timeout error for never-ready agent, got nil (elapsed=%v)", elapsed)
	}

	// Must not have run anywhere near the legacy hardcoded 80s gate.
	// Allow generous headroom for CI scheduling jitter (3× the requested).
	upper := 3 * requested
	if elapsed > upper {
		t.Fatalf("waitForAgentReady ignored --timeout: elapsed=%v, requested=%v, upper bound=%v (legacy hardcoded gate was ~80s — looks unfixed)", elapsed, requested, upper)
	}

	// Must not have returned instantly (would mean the wait loop is broken).
	lower := requested / 2
	if elapsed < lower {
		t.Fatalf("waitForAgentReady returned too quickly: elapsed=%v, requested=%v, lower bound=%v", elapsed, requested, lower)
	}

	// Error message should reference the timeout so operators can diagnose.
	if !strings.Contains(err.Error(), "not ready") {
		t.Errorf("expected error to mention readiness, got: %v", err)
	}

	if mock.calls.Load() == 0 {
		t.Errorf("expected GetStatus to be polled at least once, got 0 calls")
	}
}

// TestWaitForAgentReady_ShorterTimeout_ReturnsFaster asserts the wait actually
// scales with --timeout (not just "anything <80s passes"). Catches regressions
// where someone might cap the timeout silently.
func TestWaitForAgentReady_ShorterTimeout_ReturnsFaster(t *testing.T) {
	mock := &neverReadyChecker{}

	requested := 500 * time.Millisecond
	start := time.Now()
	err := waitForAgentReady(mock, "shell", requested)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected timeout error, got nil")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("500ms timeout took %v — wait loop not honoring caller timeout", elapsed)
	}
}
