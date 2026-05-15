package main

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// freshComposerChecker simulates a freshly restarted Claude session that has
// reached "waiting" with a visible composer prompt — but whose slash-command
// parser is still registering. Real symptom (#966): a bare `/help` sent in
// this window is silently dropped, while a conversational wrap is honored.
type freshComposerChecker struct {
	captures    atomic.Int64
	statusCalls atomic.Int64
}

func (m *freshComposerChecker) GetStatus() (string, error) {
	m.statusCalls.Add(1)
	return "waiting", nil
}

func (m *freshComposerChecker) CapturePaneFresh() (string, error) {
	m.captures.Add(1)
	// Composer prompt is visible (Claude's interactive input line).
	return "❯ \n", nil
}

// TestSessionSend_WaitsForSlashCommandReady_RegressionFor966 is the regression
// test for issue #966.
//
// After `agent-deck session restart`, Claude reaches "waiting" with the
// composer prompt visible well before its slash-command router finishes
// registering. The first bare slash command sent in that window is silently
// dropped. The send path must apply additional hold-back when the payload
// is a bare slash command targeted at a Claude session.
//
// This test asserts the existence and shape of `waitForSlashCommandReady`:
// it must not return immediately even when the pane already looks "ready",
// because that early-ready state is exactly the race we're guarding.
func TestSessionSend_WaitsForSlashCommandReady_RegressionFor966(t *testing.T) {
	mock := &freshComposerChecker{}

	// Give the gate plenty of headroom to settle.
	requested := 3 * time.Second
	start := time.Now()
	err := waitForSlashCommandReady(mock, "claude", requested)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("expected success once composer is stable, got %v (elapsed=%v)", err, elapsed)
	}

	// Must NOT return instantly. The entire point is to insert a settle
	// window long enough for Claude's slash registration to complete after
	// the composer prompt first appears. The legacy code path returns the
	// moment `agent-ready` is satisfied, which is the bug.
	minHoldback := 500 * time.Millisecond
	if elapsed < minHoldback {
		t.Fatalf("waitForSlashCommandReady returned in %v — no hold-back applied; #966 slash-registration race is not gated (need at least %v)", elapsed, minHoldback)
	}

	// Must actually probe the pane (not just blind-sleep).
	if mock.captures.Load() == 0 {
		t.Errorf("expected pane captures to verify composer stability, got 0")
	}

	// Must respect caller timeout: never run longer than requested.
	if elapsed > requested+500*time.Millisecond {
		t.Errorf("waitForSlashCommandReady overran timeout: elapsed=%v, requested=%v", elapsed, requested)
	}
}

// TestSessionSend_SlashGate_OnlyForClaudeSlashMessages ensures the gate fires
// only for the bug's actual trigger condition (Claude tool + bare slash
// message). Conversational text and non-Claude tools must NOT pay the extra
// latency.
func TestSessionSend_SlashGate_OnlyForClaudeSlashMessages(t *testing.T) {
	cases := []struct {
		name    string
		tool    string
		message string
		want    bool
	}{
		{"claude+slash", "claude", "/help", true},
		{"claude+slash-with-args", "claude", "/foo:bar phase-1", true},
		{"claude+slash-with-leading-space", "claude", "  /help", true},
		{"claude+conversational-wrap", "claude", "Please run /foo:bar phase-1", false},
		{"claude+plain", "claude", "hello", false},
		{"claude+empty", "claude", "", false},
		{"codex+slash", "codex", "/help", false},
		{"gemini+slash", "gemini", "/help", false},
		{"shell+slash", "shell", "/help", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldGateSlashRegistration(tc.tool, tc.message)
			if got != tc.want {
				t.Errorf("shouldGateSlashRegistration(%q, %q) = %v, want %v", tc.tool, tc.message, got, tc.want)
			}
		})
	}
}

// TestSessionSend_SlashGate_TimeoutFloor asserts the gate honors a tight
// caller timeout: it must not block longer than requested even if the
// composer never appears stable. This protects #957's --timeout contract.
func TestSessionSend_SlashGate_TimeoutFloor(t *testing.T) {
	// Mock whose pane never shows a composer prompt — stability check
	// can never succeed, so the gate must hit timeout.
	mock := &noComposerChecker{}

	requested := 400 * time.Millisecond
	start := time.Now()
	err := waitForSlashCommandReady(mock, "claude", requested)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected timeout error when composer never appears, got nil (elapsed=%v)", elapsed)
	}
	if !strings.Contains(err.Error(), "slash") {
		t.Errorf("expected error to mention slash readiness, got: %v", err)
	}
	upper := requested + 800*time.Millisecond
	if elapsed > upper {
		t.Fatalf("slash gate ignored caller timeout: elapsed=%v, requested=%v, upper=%v", elapsed, requested, upper)
	}
}

type noComposerChecker struct{}

func (m *noComposerChecker) GetStatus() (string, error)        { return "waiting", nil }
func (m *noComposerChecker) CapturePaneFresh() (string, error) { return "", nil }
