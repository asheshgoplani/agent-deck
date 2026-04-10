package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// mockStatusChecker implements statusChecker for testing waitForCompletion.
type mockStatusChecker struct {
	statuses []string // statuses returned in order
	errors   []error  // errors returned in order (nil = no error)
	idx      atomic.Int32
}

func (m *mockStatusChecker) GetStatus() (string, error) {
	i := int(m.idx.Add(1) - 1)
	if i >= len(m.statuses) {
		// Stay on last status if we exceed the list
		i = len(m.statuses) - 1
	}
	var err error
	if i < len(m.errors) {
		err = m.errors[i]
	}
	return m.statuses[i], err
}

func TestWaitForCompletion_ImmediateWaiting(t *testing.T) {
	mock := &mockStatusChecker{
		statuses: []string{"waiting"},
	}
	status, err := waitForCompletion(mock, 5*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "waiting" {
		t.Errorf("expected status 'waiting', got %q", status)
	}
}

func TestWaitForCompletion_ActiveThenWaiting(t *testing.T) {
	mock := &mockStatusChecker{
		statuses: []string{"active", "active", "waiting"},
	}
	status, err := waitForCompletion(mock, 30*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "waiting" {
		t.Errorf("expected status 'waiting', got %q", status)
	}
}

func TestWaitForCompletion_ActiveThenIdle(t *testing.T) {
	mock := &mockStatusChecker{
		statuses: []string{"active", "idle"},
	}
	status, err := waitForCompletion(mock, 30*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "idle" {
		t.Errorf("expected status 'idle', got %q", status)
	}
}

func TestWaitForCompletion_ActiveThenInactive(t *testing.T) {
	mock := &mockStatusChecker{
		statuses: []string{"active", "inactive"},
	}
	status, err := waitForCompletion(mock, 30*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "inactive" {
		t.Errorf("expected status 'inactive', got %q", status)
	}
}

func TestWaitForCompletion_TransientErrors(t *testing.T) {
	mock := &mockStatusChecker{
		statuses: []string{"", "", "waiting"},
		errors:   []error{fmt.Errorf("tmux error"), fmt.Errorf("tmux error"), nil},
	}
	status, err := waitForCompletion(mock, 30*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "waiting" {
		t.Errorf("expected status 'waiting', got %q", status)
	}
}

func TestWaitForCompletion_SessionDeath(t *testing.T) {
	// When GetStatus returns 5+ consecutive errors, the session is dead.
	// waitForCompletion should return ("error", nil) instead of hanging.
	mock := &mockStatusChecker{
		statuses: []string{"", "", "", "", "", "", ""},
		errors: []error{
			fmt.Errorf("tmux session not found"),
			fmt.Errorf("tmux session not found"),
			fmt.Errorf("tmux session not found"),
			fmt.Errorf("tmux session not found"),
			fmt.Errorf("tmux session not found"),
			fmt.Errorf("tmux session not found"),
			fmt.Errorf("tmux session not found"),
		},
	}
	status, err := waitForCompletion(mock, 10*time.Second)
	if err != nil {
		t.Fatalf("expected nil error (session death detection), got: %v", err)
	}
	if status != "error" {
		t.Errorf("expected status 'error' for session death, got %q", status)
	}
}

func TestWaitForCompletion_TransientRecovery(t *testing.T) {
	// Fewer than 5 consecutive errors should recover when a valid status follows.
	mock := &mockStatusChecker{
		statuses: []string{"", "", "", "waiting"},
		errors:   []error{fmt.Errorf("tmux error"), fmt.Errorf("tmux error"), fmt.Errorf("tmux error"), nil},
	}
	status, err := waitForCompletion(mock, 30*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "waiting" {
		t.Errorf("expected status 'waiting' after transient recovery, got %q", status)
	}
}

func TestWaitForCompletion_Timeout(t *testing.T) {
	mock := &mockStatusChecker{
		statuses: []string{"active"}, // Stays active forever
	}
	// Use a very short timeout so the test doesn't block
	_, err := waitForCompletion(mock, 2*time.Second)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

type mockSendRetryTarget struct {
	sendKeysErr error
	statuses    []string
	statusErrs  []error
	panes       []string
	paneErrs    []error

	statusIdx atomic.Int32
	paneIdx   atomic.Int32

	sendKeysCalls  int32
	sendEnterCalls int32
	sendCtrlCCalls int32
}

func (m *mockSendRetryTarget) SendKeysAndEnter(_ string) error {
	atomic.AddInt32(&m.sendKeysCalls, 1)
	return m.sendKeysErr
}

func (m *mockSendRetryTarget) GetStatus() (string, error) {
	i := int(m.statusIdx.Add(1) - 1)
	if len(m.statuses) == 0 {
		return "", nil
	}
	if i >= len(m.statuses) {
		i = len(m.statuses) - 1
	}
	var err error
	if i < len(m.statusErrs) {
		err = m.statusErrs[i]
	}
	return m.statuses[i], err
}

func (m *mockSendRetryTarget) SendEnter() error {
	atomic.AddInt32(&m.sendEnterCalls, 1)
	return nil
}

func (m *mockSendRetryTarget) SendCtrlC() error {
	atomic.AddInt32(&m.sendCtrlCCalls, 1)
	return nil
}

func (m *mockSendRetryTarget) CapturePaneFresh() (string, error) {
	i := int(m.paneIdx.Add(1) - 1)
	if len(m.panes) == 0 {
		return "", nil
	}
	if i >= len(m.panes) {
		i = len(m.panes) - 1
	}
	var err error
	if i < len(m.paneErrs) {
		err = m.paneErrs[i]
	}
	return m.panes[i], err
}

func TestSendWithRetryTarget_SkipVerify(t *testing.T) {
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{""},
	}
	err := sendWithRetryTarget(mock, "hello", true, sendRetryOptions{maxRetries: 4, checkDelay: 0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if atomic.LoadInt32(&mock.sendEnterCalls) != 0 {
		t.Fatalf("expected 0 SendEnter calls, got %d", mock.sendEnterCalls)
	}
}

func TestSendWithRetryTarget_StopsWhenActive(t *testing.T) {
	mock := &mockSendRetryTarget{
		statuses: []string{"active"},
		panes:    []string{""},
	}
	err := sendWithRetryTarget(mock, "hello", false, sendRetryOptions{maxRetries: 4, checkDelay: 0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if atomic.LoadInt32(&mock.sendEnterCalls) != 0 {
		t.Fatalf("expected 0 SendEnter calls, got %d", mock.sendEnterCalls)
	}
}

func TestSendWithRetryTarget_WaitingWithoutPasteMarkerReturnsSuccess(t *testing.T) {
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting", "waiting", "waiting", "waiting"},
		panes:    []string{"", "", "", ""},
	}
	err := sendWithRetryTarget(mock, "hello", false, sendRetryOptions{maxRetries: 4, checkDelay: 0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// With aggressive early retry (retry < 5), all 4 iterations nudge Enter.
	if got := atomic.LoadInt32(&mock.sendEnterCalls); got != 4 {
		t.Fatalf("expected 4 aggressive early SendEnter calls for waiting-without-active state, got %d", got)
	}
}

func TestSendWithRetryTarget_RetriesOnUnsentPasteMarker(t *testing.T) {
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting", "waiting", "waiting", "waiting", "waiting"},
		panes: []string{
			"[Pasted text #1 +89 lines]",
			"[Pasted text #1 +89 lines]",
			"[Pasted text #1 +89 lines]",
			"[Pasted text #1 +89 lines]",
			"[Pasted text #1 +89 lines]",
		},
	}
	err := sendWithRetryTarget(mock, "hello", false, sendRetryOptions{maxRetries: 5, checkDelay: 0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := atomic.LoadInt32(&mock.sendEnterCalls); got != 5 {
		t.Fatalf("expected 5 SendEnter calls when unsent marker persists, got %d", got)
	}
}

func TestSendWithRetryTarget_DetectsPasteMarkerAfterInitialWaiting(t *testing.T) {
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting", "waiting", "active"},
		panes: []string{
			"",
			"[Pasted text #1 +18 lines]",
			"",
		},
	}
	err := sendWithRetryTarget(mock, "hello", false, sendRetryOptions{maxRetries: 5, checkDelay: 0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 2 calls: retry 0 fires early aggressive nudge (waiting, no active seen),
	// retry 1 fires from paste marker detection.
	if got := atomic.LoadInt32(&mock.sendEnterCalls); got != 2 {
		t.Fatalf("expected 2 SendEnter calls (1 early nudge + 1 paste marker), got %d", got)
	}
}

func TestSendWithRetryTarget_RetriesWhenComposerPromptStillHasMessage(t *testing.T) {
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting", "active"},
		panes: []string{
			"❯ Write one line: LAUNCH_OK",
			"",
		},
	}
	err := sendWithRetryTarget(mock, "Write one line: LAUNCH_OK", false, sendRetryOptions{maxRetries: 4, checkDelay: 0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := atomic.LoadInt32(&mock.sendEnterCalls); got != 1 {
		t.Fatalf("expected 1 SendEnter call when composer still has unsent message, got %d", got)
	}
}

func TestSendWithRetryTarget_RetriesWhenWrappedComposerPromptStillHasMessage(t *testing.T) {
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting", "active"},
		panes: []string{
			"────────────────\n❯ Read these 3 files and produce a summary for DIAGTOKEN_123. Keep\n  under 80 lines.\n────────────────",
			"",
		},
	}
	message := "Read these 3 files and produce a summary for DIAGTOKEN_123. Keep under 80 lines."
	err := sendWithRetryTarget(mock, message, false, sendRetryOptions{maxRetries: 4, checkDelay: 0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := atomic.LoadInt32(&mock.sendEnterCalls); got != 1 {
		t.Fatalf("expected 1 SendEnter call when wrapped composer prompt still has unsent message, got %d", got)
	}
}

func TestSendWithRetryTarget_AmbiguousStateUsesLimitedFallbackRetries(t *testing.T) {
	mock := &mockSendRetryTarget{
		statuses: []string{"error", "error", "error", "error"},
		panes:    []string{"", "", "", ""},
	}
	err := sendWithRetryTarget(mock, "hello", false, sendRetryOptions{maxRetries: 4, checkDelay: 0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Ambiguous-state Enter budget increased from 2 to 4; all 4 retries send Enter.
	if got := atomic.LoadInt32(&mock.sendEnterCalls); got != 4 {
		t.Fatalf("expected 4 fallback SendEnter calls (increased budget), got %d", got)
	}
}

func TestSendWithRetryTarget_ReturnsErrorWhenInitialSendFails(t *testing.T) {
	mock := &mockSendRetryTarget{
		sendKeysErr: fmt.Errorf("tmux send failed"),
	}
	err := sendWithRetryTarget(mock, "hello", false, sendRetryOptions{maxRetries: 3, checkDelay: 0})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "failed to send message") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestSendWithRetryTarget_AggressiveEarlyEnterNudge(t *testing.T) {
	// Verify that SendEnter is called on every iteration for the first 5
	// retries when in waiting-without-active state, then every 2nd iteration.
	mock := &mockSendRetryTarget{
		statuses: []string{
			"waiting", "waiting", "waiting", "waiting", "waiting", // retries 0-4: all nudge
			"waiting", "waiting", "waiting", "waiting", "waiting", // retries 5-9: even nudge
		},
		panes: []string{"", "", "", "", "", "", "", "", "", ""},
	}
	err := sendWithRetryTarget(mock, "hello", false, sendRetryOptions{maxRetries: 10, checkDelay: 0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// First 5 retries (0-4): all nudge = 5 calls
	// Retries 5-9: retry%2==0 means retries 6, 8 nudge = 2 calls
	// Total: 5 + 2 = 7
	// But wait: retry 5 is not < 5 and 5%2 != 0, so no nudge.
	// retry 6: 6%2 == 0, nudge. retry 7: no. retry 8: nudge. retry 9: no.
	// Total: 5 (early) + 2 (even from 5-9) = 7
	if got := atomic.LoadInt32(&mock.sendEnterCalls); got != 7 {
		t.Fatalf("expected 7 SendEnter calls (5 early + 2 even), got %d", got)
	}
}

func TestSendWithRetryTarget_IncreasedAmbiguousBudget(t *testing.T) {
	// Verify that ambiguous-state Enter budget is 4 (up from 2).
	mock := &mockSendRetryTarget{
		statuses: []string{"error", "error", "error", "error", "error"},
		panes:    []string{"", "", "", "", ""},
	}
	err := sendWithRetryTarget(mock, "hello", false, sendRetryOptions{maxRetries: 5, checkDelay: 0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Retries 0, 1, 2, 3 are < 4 so SendEnter is called 4 times; retry 4 is not.
	if got := atomic.LoadInt32(&mock.sendEnterCalls); got != 4 {
		t.Fatalf("expected 4 SendEnter calls for increased ambiguous budget, got %d", got)
	}
}

func TestSendWithRetryTarget_FullResendAfterMessageLost(t *testing.T) {
	// Simulate the TUI init race: agent reports "waiting" but never transitions
	// to "active" because the message was lost during init. After
	// fullResendThreshold (8) consecutive waiting checks with no activity,
	// sendWithRetryTarget should Ctrl+C and re-send the full message.
	// After re-send, the agent transitions to "active".
	statuses := make([]string, 12)
	panes := make([]string, 12)
	for i := range statuses {
		statuses[i] = "waiting"
		panes[i] = ""
	}
	// After the full resend (at check ~9), agent becomes active
	statuses[10] = "active"
	statuses[11] = "active"

	mock := &mockSendRetryTarget{
		statuses: statuses,
		panes:    panes,
	}
	err := sendWithRetryTarget(mock, "hello", false, sendRetryOptions{maxRetries: 12, checkDelay: 0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := atomic.LoadInt32(&mock.sendCtrlCCalls); got != 1 {
		t.Fatalf("expected 1 SendCtrlC call for full resend, got %d", got)
	}
	// sendKeysCalls: 1 initial + 1 resend = 2
	if got := atomic.LoadInt32(&mock.sendKeysCalls); got != 2 {
		t.Fatalf("expected 2 SendKeysAndEnter calls (initial + resend), got %d", got)
	}
}

func TestSendWithRetryTarget_FullResendMaxLimit(t *testing.T) {
	// Verify that full resends are capped at maxFullResends (3).
	// With fullResendThreshold=8, we need at least 8*4=32 retries
	// to trigger all 3 resends plus some trailing checks.
	n := 40
	statuses := make([]string, n)
	panes := make([]string, n)
	for i := range statuses {
		statuses[i] = "waiting"
		panes[i] = ""
	}
	mock := &mockSendRetryTarget{
		statuses: statuses,
		panes:    panes,
	}
	err := sendWithRetryTarget(mock, "hello", false, sendRetryOptions{maxRetries: n, checkDelay: 0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should have exactly 3 full resends (the cap)
	if got := atomic.LoadInt32(&mock.sendCtrlCCalls); got != 3 {
		t.Fatalf("expected 3 SendCtrlC calls (max resends), got %d", got)
	}
	// 1 initial + 3 resends = 4
	if got := atomic.LoadInt32(&mock.sendKeysCalls); got != 4 {
		t.Fatalf("expected 4 SendKeysAndEnter calls (initial + 3 resends), got %d", got)
	}
}

func TestSendWithRetryTarget_NoWaitDoesNotResend(t *testing.T) {
	// Regression test for issue #479: --no-wait sends message twice.
	// When maxFullResends is negative (disabled), the verification loop
	// must never Ctrl+C + re-send even if the session stays in "waiting"
	// past the fullResendThreshold window.
	n := 12
	statuses := make([]string, n)
	panes := make([]string, n)
	for i := range statuses {
		statuses[i] = "waiting"
		panes[i] = ""
	}
	mock := &mockSendRetryTarget{
		statuses: statuses,
		panes:    panes,
	}
	err := sendWithRetryTarget(mock, "hello", false, sendRetryOptions{
		maxRetries:     n,
		checkDelay:     0,
		maxFullResends: -1, // disabled, as used by --no-wait
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := atomic.LoadInt32(&mock.sendCtrlCCalls); got != 0 {
		t.Fatalf("expected 0 SendCtrlC calls (resend disabled), got %d", got)
	}
	// Only the initial send, no resends
	if got := atomic.LoadInt32(&mock.sendKeysCalls); got != 1 {
		t.Fatalf("expected 1 SendKeysAndEnter call (initial only), got %d", got)
	}
}

// skipIfNoTmuxServer skips the test if tmux is not available or not running.
func skipIfNoTmuxServer(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	if err := exec.Command("tmux", "list-sessions").Run(); err != nil {
		t.Skip("tmux server not running")
	}
}

// TestSendWithRetry_DelayedInputHandler_Integration reproduces the bug where
// session send reports success but the message is silently dropped.
//
// The bug scenario: Claude Code renders the ❯ prompt (causing GetStatus to
// report "waiting") before its Ink-based TUI input handler is ready to accept
// keystrokes. waitForAgentReady returns, sendWithRetry sends keys, but the TUI
// discards them because it hasn't finished initializing.
//
// This test simulates that race by running a script that:
// 1. Immediately prints a ❯ prompt (so status detection sees "waiting")
// 2. Sleeps before starting to read input (simulating TUI init delay)
// 3. After the delay, reads a line and echoes it with a marker
func TestSendWithRetry_DelayedInputHandler_Integration(t *testing.T) {
	skipIfNoTmuxServer(t)
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if os.Getenv("AGENT_DECK_INTEGRATION_TESTS") == "" {
		t.Skip("skipping flaky tmux integration test (set AGENT_DECK_INTEGRATION_TESTS=1 to enable)")
	}

	sess := tmux.NewSession("send-test-delayed", "/tmp")

	// Script that simulates Claude's startup race condition.
	// Traps SIGINT so Ctrl+C doesn't kill it (like real Claude TUI).
	// The inner loop discards empty lines (simulating how Claude's Ink TUI
	// ignores empty Enter presses) and only accepts non-empty input.
	script := `bash -c '
		trap "" INT   # Ignore Ctrl+C (like Claude Ink TUI)

		# Phase 1: Show prompt before input handler is ready
		printf "❯ "

		# Phase 2: TUI init delay — drain all input that arrives
		sleep 2
		while read -t 0.1 -r _discard 2>/dev/null; do :; done

		# Phase 3: TUI ready — show fresh prompt, accept non-empty input only
		# (Claude ignores empty Enter presses at the prompt)
		while true; do
			printf "\n❯ "
			read -r line
			if [ -n "$line" ]; then
				echo "GOT: $line"
				break
			fi
		done
		sleep 2
	'`

	if err := sess.Start(script); err != nil {
		t.Fatalf("Failed to start session: %v", err)
	}
	defer func() { _ = sess.Kill() }()

	// Wait for the ❯ prompt to appear (simulates what waitForAgentReady sees)
	time.Sleep(500 * time.Millisecond)

	message := "DELAYED_HANDLER_TEST_MSG"
	err := sendWithRetry(sess, message, false)
	if err != nil {
		t.Fatalf("sendWithRetry failed: %v", err)
	}

	// Wait for the script to process the re-sent message
	time.Sleep(3 * time.Second)

	content, err := sess.CapturePane()
	if err != nil {
		t.Fatalf("CapturePane failed: %v", err)
	}

	t.Logf("Pane content after send:\n%s", content)

	if !strings.Contains(content, "GOT: "+message) {
		t.Errorf("Message was sent but never delivered to the input handler.\n"+
			"sendWithRetry reported success but the message was lost during the TUI init window.\n"+
			"Pane content:\n%s", content)
	}
}

// Integration test coverage for Codex readiness: waitForAgentReady uses a
// concrete *tmux.Session so it cannot be unit tested with mocks here.
// See TestSend_CodexReadiness in internal/integration/send_reliability_test.go
// (Plan 02) for integration test coverage of Codex prompt gating.

// TestWaitOutputRetrieval_StaleSessionID verifies that --wait correctly
// retrieves output even when the initially-loaded ClaudeSessionID is stale.
// This simulates the bug where inst.GetLastResponse() fails because the
// session ID stored in the DB doesn't match the actual JSONL file on disk.
func TestWaitOutputRetrieval_StaleSessionID(t *testing.T) {
	// Set up a temp Claude config dir with a JSONL file
	tmpDir := t.TempDir()
	projectPath := "/test/wait-project"
	encodedPath := session.ConvertToClaudeDirName(projectPath)

	projectsDir := filepath.Join(tmpDir, "projects", encodedPath)
	if err := os.MkdirAll(projectsDir, 0755); err != nil {
		t.Fatalf("failed to create projects dir: %v", err)
	}

	// Override config dir for test
	origConfigDir := os.Getenv("CLAUDE_CONFIG_DIR")
	os.Setenv("CLAUDE_CONFIG_DIR", tmpDir)
	defer os.Setenv("CLAUDE_CONFIG_DIR", origConfigDir)
	session.ClearUserConfigCache()
	defer session.ClearUserConfigCache()

	// Create the "real" session JSONL file (what Claude actually wrote to)
	realSessionID := "real-session-id-after-start"
	realJSONL := filepath.Join(projectsDir, realSessionID+".jsonl")
	jsonlContent := `{"type":"summary","sessionId":"` + realSessionID + `"}
{"message":{"role":"user","content":"hello"},"sessionId":"` + realSessionID + `","type":"user","timestamp":"2026-01-01T00:00:00Z"}
{"message":{"role":"assistant","content":[{"type":"text","text":"Hello! How can I help?"}]},"sessionId":"` + realSessionID + `","type":"assistant","timestamp":"2026-01-01T00:00:01Z"}`
	if err := os.WriteFile(realJSONL, []byte(jsonlContent), 0644); err != nil {
		t.Fatalf("failed to write JSONL: %v", err)
	}

	t.Run("stale session ID fails to find file", func(t *testing.T) {
		// Instance with stale session ID (doesn't match any JSONL file)
		inst := session.NewInstance("wait-test", projectPath)
		inst.Tool = "claude"
		inst.ClaudeSessionID = "stale-old-session-id"

		_, err := inst.GetLastResponse()
		if err == nil {
			t.Fatal("expected error with stale session ID, got nil")
		}
	})

	t.Run("correct session ID finds file", func(t *testing.T) {
		// Instance with correct session ID
		inst := session.NewInstance("wait-test", projectPath)
		inst.Tool = "claude"
		inst.ClaudeSessionID = realSessionID

		resp, err := inst.GetLastResponse()
		if err != nil {
			t.Fatalf("unexpected error with correct session ID: %v", err)
		}
		if resp.Content != "Hello! How can I help?" {
			t.Errorf("expected 'Hello! How can I help?', got %q", resp.Content)
		}
	})

	t.Run("refreshing session ID fixes retrieval", func(t *testing.T) {
		// Simulates the --wait fix: start with stale ID, then refresh
		inst := session.NewInstance("wait-test", projectPath)
		inst.Tool = "claude"
		inst.ClaudeSessionID = "stale-old-session-id"

		// First attempt fails (stale ID)
		_, err := inst.GetLastResponse()
		if err == nil {
			t.Fatal("expected error with stale session ID")
		}

		// Simulate refreshing session ID (as the fix does from tmux env)
		inst.ClaudeSessionID = realSessionID
		inst.ClaudeDetectedAt = time.Now()

		// Second attempt succeeds with refreshed ID
		resp, err := inst.GetLastResponse()
		if err != nil {
			t.Fatalf("unexpected error after refresh: %v", err)
		}
		if resp.Content != "Hello! How can I help?" {
			t.Errorf("expected 'Hello! How can I help?', got %q", resp.Content)
		}
	})
}

// writeClaudeJSONL creates a JSONL file with a user message and an assistant response
// at the given timestamp. Returns the file path.
func writeClaudeJSONL(t *testing.T, projectsDir, sessionID, userMsg, assistantMsg, timestamp string) string {
	t.Helper()
	file := filepath.Join(projectsDir, sessionID+".jsonl")

	type message struct {
		Role    string      `json:"role"`
		Content interface{} `json:"content"`
	}
	type record struct {
		SessionID string   `json:"sessionId"`
		Type      string   `json:"type"`
		Message   *message `json:"message,omitempty"`
		Timestamp string   `json:"timestamp,omitempty"`
	}

	var lines []string

	// Summary line
	summaryBytes, _ := json.Marshal(record{SessionID: sessionID, Type: "summary"})
	lines = append(lines, string(summaryBytes))

	// User message
	userRec, _ := json.Marshal(record{
		SessionID: sessionID,
		Type:      "user",
		Message:   &message{Role: "user", Content: userMsg},
		Timestamp: timestamp,
	})
	lines = append(lines, string(userRec))

	// Assistant message (content as array of blocks, matching real Claude format)
	blocks := []map[string]string{{"type": "text", "text": assistantMsg}}
	assistantRec, _ := json.Marshal(record{
		SessionID: sessionID,
		Type:      "assistant",
		Message:   &message{Role: "assistant", Content: blocks},
		Timestamp: timestamp,
	})
	lines = append(lines, string(assistantRec))

	if err := os.WriteFile(file, []byte(strings.Join(lines, "\n")), 0644); err != nil {
		t.Fatalf("failed to write JSONL: %v", err)
	}
	return file
}

// setFastFreshOutputConfig overrides waitForFreshOutput timing for fast tests.
func setFastFreshOutputConfig(t *testing.T, timeout time.Duration) {
	t.Helper()
	freshOutputTestConfig = &freshOutputConfig{
		pollInterval: 50 * time.Millisecond,
		timeout:      timeout,
	}
	t.Cleanup(func() { freshOutputTestConfig = nil })
}

// TestWaitForFreshOutput_ReturnsNewResponse verifies that waitForFreshOutput
// polls until a response newer than sentAt appears in the JSONL file.
func TestWaitForFreshOutput_ReturnsNewResponse(t *testing.T) {
	tmpDir := t.TempDir()
	projectPath := "/test/fresh-output"
	encodedPath := session.ConvertToClaudeDirName(projectPath)
	projectsDir := filepath.Join(tmpDir, "projects", encodedPath)
	if err := os.MkdirAll(projectsDir, 0755); err != nil {
		t.Fatalf("failed to create projects dir: %v", err)
	}

	origConfigDir := os.Getenv("CLAUDE_CONFIG_DIR")
	os.Setenv("CLAUDE_CONFIG_DIR", tmpDir)
	t.Cleanup(func() {
		os.Setenv("CLAUDE_CONFIG_DIR", origConfigDir)
		session.ClearUserConfigCache()
	})
	session.ClearUserConfigCache()

	sessionID := "fresh-output-session-id"

	t.Run("stale response is skipped until fresh one appears", func(t *testing.T) {
		setFastFreshOutputConfig(t, 2*time.Second)

		// Write an OLD response (before sentAt)
		oldTimestamp := "2026-01-01T00:00:00Z"
		writeClaudeJSONL(t, projectsDir, sessionID, "old question", "old answer", oldTimestamp)

		inst := session.NewInstance("fresh-test", projectPath)
		inst.Tool = "claude"
		inst.ClaudeSessionID = sessionID

		sentAt := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

		// In a goroutine, simulate Claude flushing a new response after a short delay
		go func() {
			time.Sleep(200 * time.Millisecond)
			newTimestamp := "2026-03-01T00:00:05Z"
			writeClaudeJSONL(t, projectsDir, sessionID, "new question", "new answer", newTimestamp)
		}()

		resp, err := waitForFreshOutput(inst, sentAt)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.Content != "new answer" {
			t.Errorf("expected 'new answer', got %q", resp.Content)
		}
	})

	t.Run("returns stale response on timeout rather than failing", func(t *testing.T) {
		setFastFreshOutputConfig(t, 300*time.Millisecond)

		// Write a response that will always be older than sentAt
		oldTimestamp := "2026-01-01T00:00:00Z"
		writeClaudeJSONL(t, projectsDir, sessionID, "only question", "only answer", oldTimestamp)

		inst := session.NewInstance("timeout-test", projectPath)
		inst.Tool = "claude"
		inst.ClaudeSessionID = sessionID

		// sentAt is well after the only response — freshness poll will time out
		sentAt := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

		resp, err := waitForFreshOutput(inst, sentAt)
		if err != nil {
			t.Fatalf("should not error even on timeout, got: %v", err)
		}
		// Should still return the stale response rather than nil
		if resp.Content != "only answer" {
			t.Errorf("expected fallback to 'only answer', got %q", resp.Content)
		}
	})

	t.Run("immediately returns if response is already fresh", func(t *testing.T) {
		setFastFreshOutputConfig(t, 2*time.Second)

		freshTimestamp := "2026-06-01T12:00:00Z"
		writeClaudeJSONL(t, projectsDir, sessionID, "question", "instant answer", freshTimestamp)

		inst := session.NewInstance("instant-test", projectPath)
		inst.Tool = "claude"
		inst.ClaudeSessionID = sessionID

		sentAt := time.Date(2026, 6, 1, 11, 0, 0, 0, time.UTC) // 1 hour before response

		start := time.Now()
		resp, err := waitForFreshOutput(inst, sentAt)
		elapsed := time.Since(start)

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.Content != "instant answer" {
			t.Errorf("expected 'instant answer', got %q", resp.Content)
		}
		if elapsed > 500*time.Millisecond {
			t.Errorf("expected fast return for already-fresh response, took %v", elapsed)
		}
	})

	t.Run("same-second timestamp accepted via skew tolerance", func(t *testing.T) {
		setFastFreshOutputConfig(t, 2*time.Second)

		// Timestamp is exactly the same second as sentAt (second precision)
		writeClaudeJSONL(t, projectsDir, sessionID, "q", "same-second answer", "2026-04-01T10:00:00Z")

		inst := session.NewInstance("skew-test", projectPath)
		inst.Tool = "claude"
		inst.ClaudeSessionID = sessionID

		// sentAt at the exact same second — the 250ms tolerance should accept it
		// because the timestamp (whole-second) is only 0ms "before" sentAt.
		sentAt := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)

		resp, err := waitForFreshOutput(inst, sentAt)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.Content != "same-second answer" {
			t.Errorf("expected 'same-second answer', got %q", resp.Content)
		}
	})

	t.Run("response 1s before sentAt rejected with tighter tolerance", func(t *testing.T) {
		setFastFreshOutputConfig(t, 300*time.Millisecond)

		// Response timestamp is 1 second BEFORE sentAt — outside the 250ms tolerance
		writeClaudeJSONL(t, projectsDir, sessionID, "q", "old-ish answer", "2026-04-01T09:59:59Z")

		inst := session.NewInstance("tight-skew-test", projectPath)
		inst.Tool = "claude"
		inst.ClaudeSessionID = sessionID

		sentAt := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)

		resp, err := waitForFreshOutput(inst, sentAt)
		if err != nil {
			t.Fatalf("should not error even on timeout, got: %v", err)
		}
		// Falls through to timeout since 1s > 250ms tolerance, returns stale
		if resp.Content != "old-ish answer" {
			t.Errorf("expected fallback to 'old-ish answer', got %q", resp.Content)
		}
	})

	t.Run("non-claude tool skips freshness polling", func(t *testing.T) {
		setFastFreshOutputConfig(t, 2*time.Second)

		inst := session.NewInstance("codex-test", projectPath)
		inst.Tool = "codex"

		start := time.Now()
		resp, err := waitForFreshOutput(inst, time.Now())
		elapsed := time.Since(start)

		// Codex path goes straight to GetLastResponseBestEffort, no polling
		if elapsed > 500*time.Millisecond {
			t.Errorf("non-claude tool should skip polling, took %v", elapsed)
		}
		// Just verify no crash; response content depends on codex session state
		_ = resp
		_ = err
	})

	t.Run("unparseable timestamp falls through to timeout", func(t *testing.T) {
		setFastFreshOutputConfig(t, 300*time.Millisecond)

		// Write JSONL with a non-RFC3339 timestamp
		writeClaudeJSONL(t, projectsDir, sessionID, "q", "bad-ts answer", "not-a-timestamp")

		inst := session.NewInstance("bad-ts-test", projectPath)
		inst.Tool = "claude"
		inst.ClaudeSessionID = sessionID

		sentAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

		resp, err := waitForFreshOutput(inst, sentAt)
		if err != nil {
			t.Fatalf("should not error, got: %v", err)
		}
		// Falls through to timeout, returns last response
		if resp.Content != "bad-ts answer" {
			t.Errorf("expected 'bad-ts answer', got %q", resp.Content)
		}
	})
}

// TestSessionOutput_RefreshesSessionID verifies that the session ID refresh
// logic would correctly update a stale ClaudeSessionID before reading output.
func TestSessionOutput_RefreshesSessionID(t *testing.T) {
	tmpDir := t.TempDir()
	projectPath := "/test/output-refresh"
	encodedPath := session.ConvertToClaudeDirName(projectPath)
	projectsDir := filepath.Join(tmpDir, "projects", encodedPath)
	if err := os.MkdirAll(projectsDir, 0755); err != nil {
		t.Fatalf("failed to create projects dir: %v", err)
	}

	origConfigDir := os.Getenv("CLAUDE_CONFIG_DIR")
	os.Setenv("CLAUDE_CONFIG_DIR", tmpDir)
	t.Cleanup(func() {
		os.Setenv("CLAUDE_CONFIG_DIR", origConfigDir)
		session.ClearUserConfigCache()
	})
	session.ClearUserConfigCache()

	// Create the "real" current session JSONL
	realSessionID := "current-active-session"
	writeClaudeJSONL(t, projectsDir, realSessionID, "hello", "Hi there!", "2026-03-01T00:00:01Z")

	t.Run("stale ID fails then refreshed ID succeeds", func(t *testing.T) {
		inst := session.NewInstance("output-refresh-test", projectPath)
		inst.Tool = "claude"
		inst.ClaudeSessionID = "stale-nonexistent-id"

		// Direct read with stale ID fails
		_, err := inst.GetLastResponse()
		if err == nil {
			t.Fatal("expected error with stale session ID, got nil")
		}

		// Simulate the refresh that handleSessionOutput now does
		inst.ClaudeSessionID = realSessionID
		inst.ClaudeDetectedAt = time.Now()

		resp, err := inst.GetLastResponseBestEffort()
		if err != nil {
			t.Fatalf("unexpected error after refresh: %v", err)
		}
		if resp.Content != "Hi there!" {
			t.Errorf("expected 'Hi there!', got %q", resp.Content)
		}
	})

	t.Run("best-effort returns graceful empty when disk scan cannot recover", func(t *testing.T) {
		inst := session.NewInstance("output-disk-fallback", projectPath)
		inst.Tool = "claude"
		inst.ClaudeSessionID = "totally-bogus-id"

		resp, err := inst.GetLastResponseBestEffort()
		if err != nil {
			t.Fatalf("best-effort should not error for Claude, got: %v", err)
		}
		if resp.Content != "" {
			t.Errorf("expected empty graceful response, got %q", resp.Content)
		}
	})
}
