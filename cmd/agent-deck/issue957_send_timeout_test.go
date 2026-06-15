package main

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/send"
)

// neverReadyChecker simulates a busy agent that never reaches "waiting"/"idle".
// GetStatus always returns "active" (the loading state), so WaitForAgentReady
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
func TestSessionSend_RespectsTimeoutFlag_RegressionFor957(t *testing.T) {
	mock := &neverReadyChecker{}

	requested := 1 * time.Second
	start := time.Now()
	err := send.WaitForAgentReady(mock, "shell", requested, send.PromptGates{})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected timeout error for never-ready agent, got nil (elapsed=%v)", elapsed)
	}

	upper := 3 * requested
	if elapsed > upper {
		t.Fatalf("WaitForAgentReady ignored --timeout: elapsed=%v, requested=%v, upper bound=%v", elapsed, requested, upper)
	}

	lower := requested / 2
	if elapsed < lower {
		t.Fatalf("WaitForAgentReady returned too quickly: elapsed=%v, requested=%v, lower bound=%v", elapsed, requested, lower)
	}

	if !strings.Contains(err.Error(), "not ready") {
		t.Errorf("expected error to mention readiness, got: %v", err)
	}

	if mock.calls.Load() == 0 {
		t.Errorf("expected GetStatus to be polled at least once, got 0 calls")
	}
}

// TestWaitForAgentReady_ShorterTimeout_ReturnsFaster asserts the wait actually
// scales with --timeout (not just "anything <80s passes").
func TestWaitForAgentReady_ShorterTimeout_ReturnsFaster(t *testing.T) {
	mock := &neverReadyChecker{}

	requested := 500 * time.Millisecond
	start := time.Now()
	err := send.WaitForAgentReady(mock, "shell", requested, send.PromptGates{})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected timeout error, got nil")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("500ms timeout took %v — wait loop not honoring caller timeout", elapsed)
	}
}
