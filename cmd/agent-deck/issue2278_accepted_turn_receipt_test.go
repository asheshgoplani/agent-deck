package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestCodexCompletionTimeoutRetainsAcceptedTurn(t *testing.T) {
	acceptedAt := time.Date(2026, 9, 14, 15, 0, 0, 123, time.UTC)
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	path := filepath.Join(home, "sessions", "2026", "09", "14", "rollout-test-thread-1.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"type":"event_msg","payload":{"type":"task_started","turn_id":"turn-previous"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inst := &session.Instance{ID: "instance-1", Tool: "codex", CodexSessionID: "thread-1"}
	fence := captureCodexAcceptanceFence(inst)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := f.WriteString(`{"type":"event_msg","payload":{"type":"task_started","turn_id":"turn-new"}}` + "\n")
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatalf("append turn: write=%v close=%v", writeErr, closeErr)
	}
	receipt := waitForAcceptedCodexTurn(inst, deliverySubmitted, acceptedAt, fence)
	if receipt == nil || receipt.CodexSessionID != "thread-1" ||
		receipt.TurnGeneration != "thread-1:turn-new" || receipt.ReceiptID == "" {
		t.Fatalf("unexpected receipt: %#v", receipt)
	}
	if got := waitForAcceptedCodexTurn(inst, deliveryUnverified, acceptedAt, fence); got != nil {
		t.Fatalf("unverified send acquired ownership: %#v", got)
	}
	payload := completionTimeoutPayload(map[string]interface{}{
		"delivery": deliverySubmitted, "submitted": true, "accepted_turn_kind": "codex_rollout",
		"accepted_turn": receipt,
	})
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	accepted, ok := got["accepted_turn"].(map[string]interface{})
	if got["completion"] != "timeout" || !ok || accepted["receipt_id"] != receipt.ReceiptID {
		t.Fatalf("timeout lost accepted-turn receipt: %s", raw)
	}
}

func TestStructuredCodexWaitRequiresExactAcceptedTurn(t *testing.T) {
	codex := &session.Instance{Tool: "codex"}
	if err := requireStructuredCodexAcceptedTurn(codex, true, true, nil); err == nil {
		t.Fatal("structured Codex wait accepted a receipt-less generation")
	}
	if err := requireStructuredCodexAcceptedTurn(codex, false, true, nil); err != nil {
		t.Fatalf("human Codex wait must retain legacy output behavior: %v", err)
	}
	if err := requireStructuredCodexAcceptedTurn(&session.Instance{Tool: "claude"}, true, true, nil); err != nil {
		t.Fatalf("non-Codex structured wait must retain legacy fallback: %v", err)
	}
}

func TestStructuredCodexWaitRetriesReceiptAtCompletionBoundary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	path := filepath.Join(home, "sessions", "2026", "09", "14", "rollout-test-thread-retry.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"type":"event_msg","payload":{"type":"task_started","turn_id":"turn-old"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inst := &session.Instance{ID: "instance-1", Tool: "codex", CodexSessionID: "thread-retry"}
	fence := captureCodexAcceptanceFence(inst)
	oldTimeout, oldInterval := codexAcceptedTurnPollTimeout, codexAcceptedTurnPollInterval
	codexAcceptedTurnPollTimeout = time.Millisecond
	codexAcceptedTurnPollInterval = time.Millisecond
	defer func() {
		codexAcceptedTurnPollTimeout = oldTimeout
		codexAcceptedTurnPollInterval = oldInterval
	}()

	initial := waitForAcceptedCodexTurn(inst, deliverySubmitted, time.Now(), fence)
	if initial != nil {
		t.Fatalf("initial poll unexpectedly found a receipt: %#v", initial)
	}
	if err := appendCodexTurnStart(path, "turn-late"); err != nil {
		t.Fatal(err)
	}
	receipt, err := retryAndRequireStructuredCodexAcceptedTurn(
		inst, true, true, initial, deliverySubmitted, time.Now(), fence,
	)
	if err != nil {
		t.Fatalf("completion-boundary retry was refused: %v", err)
	}
	if receipt == nil || receipt.TurnGeneration != "thread-retry:turn-late" {
		t.Fatalf("completion-boundary retry returned %#v", receipt)
	}
}

func TestCodexExactOutputDelayRetainsReceiptForBridge(t *testing.T) {
	receipt := &codexAcceptedTurnReceipt{
		ReceiptID: "receipt-1", InstanceID: "instance-1", CodexSessionID: "thread-1",
		TurnGeneration: "thread-1:turn-new", AcceptedAt: "2026-09-14T15:00:00Z",
	}
	payload := responseReadFailureData(map[string]interface{}{
		"delivery": deliverySubmitted, "submitted": true, "accepted_turn_kind": "codex_rollout",
		"accepted_turn": receipt,
	})
	if payload == nil {
		t.Fatal("structured Codex response delay lost its error data")
	}
	payload["success"] = false
	payload["error"] = "failed to get response: exact Codex turn output not available"
	payload["code"] = ErrCodeInvalidOperation
	got, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("..", "..", "conductor", "tests", "fixtures", "issue2278_codex_output_pending.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got)+"\n" != string(want) {
		t.Fatalf("Go output-pending schema drifted from bridge fixture:\ngot  %s\nwant %s", got, want)
	}
	legacy := responseReadFailureData(map[string]interface{}{
		"delivery": deliverySubmitted, "submitted": true,
	})
	if legacy["completion"] != "timeout" || legacy["delivery"] != deliverySubmitted || legacy["submitted"] != true {
		t.Fatalf("non-Codex response failure lost legacy async ownership: %#v", legacy)
	}
}

func TestSessionSendHelpDocumentsStructuredCodexContract(t *testing.T) {
	const helper = "AGENT_DECK_SESSION_SEND_HELP_TEST"
	if os.Getenv(helper) == "1" {
		handleSessionSend("default", []string{"--help"})
		os.Exit(2)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestSessionSendHelpDocumentsStructuredCodexContract$")
	cmd.Env = append(os.Environ(), helper+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("session send --help failed: %v\n%s", err, out)
	}
	for _, want := range []string{
		"Codex --json --wait:",
		"one structured result correlated to the accepted Codex turn",
		"locally readable exact accepted-turn receipt",
		"remote or sandboxed targets are refused",
		"direct pane or keyboard input is outside this guarantee",
	} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("session send --help missing %q:\n%s", want, out)
		}
	}
}

func appendCodexTurnStart(path, turnID string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_, writeErr := f.WriteString(`{"type":"event_msg","payload":{"type":"task_started","turn_id":"` + turnID + `"}}` + "\n")
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func TestCodexAcceptanceGuardSerializesFenceAssignment(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("CODEX_HOME", filepath.Join(home, "codex"))
	path := filepath.Join(home, "codex", "sessions", "2026", "09", "14", "rollout-test-thread-locked.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"type":"event_msg","payload":{"type":"task_started","turn_id":"turn-old"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inst := &session.Instance{ID: "instance-1", Tool: "codex", CodexSessionID: "thread-locked"}
	first, err := acquireCodexAcceptanceGuard(inst, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	if err := first.Prepare(inst.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := first.RecordTransportOutcome(deliverySubmitted, time.Now()); err != nil {
		t.Fatal(err)
	}

	secondResult := make(chan *codexAcceptanceGuard, 1)
	secondErr := make(chan error, 1)
	go func() {
		guard, acquireErr := acquireCodexAcceptanceGuard(inst, time.Second)
		secondResult <- guard
		secondErr <- acquireErr
	}()
	select {
	case guard := <-secondResult:
		if guard != nil {
			guard.Release()
		}
		t.Fatal("second sender crossed the first sender's acceptance window")
	case <-time.After(20 * time.Millisecond):
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := f.WriteString(`{"type":"event_msg","payload":{"type":"task_started","turn_id":"turn-new"}}` + "\n")
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatalf("append accepted generation: write=%v close=%v", writeErr, closeErr)
	}
	if err := validateCodexAcceptanceFence(inst, first.fence); err == nil {
		t.Fatal("an intervening turn did not invalidate the pre-submit fence")
	}
	receipt := waitForAcceptedCodexTurn(inst, deliverySubmitted, time.Now(), first.fence)
	if receipt == nil || receipt.TurnGeneration != "thread-locked:turn-new" {
		t.Fatalf("first sender did not own new generation: %#v", receipt)
	}
	if err := first.ResolveAccepted(); err != nil {
		t.Fatal(err)
	}
	first.Release()

	second := <-secondResult
	if err := <-secondErr; err != nil {
		t.Fatal(err)
	}
	defer second.Release()
	if second.fence.priorTurnGeneration != receipt.TurnGeneration {
		t.Fatalf("second fence=%q, want first generation %q", second.fence.priorTurnGeneration, receipt.TurnGeneration)
	}
	if got := newCodexAcceptedTurnReceipt(
		inst, deliverySubmitted, time.Now(), second.fence, receipt.TurnGeneration,
	); got != nil {
		t.Fatalf("second sender claimed first sender's generation: %#v", got)
	}
}

func TestCodexAcceptanceGuardReconcilesOrdinarySendBeforeStructuredRetry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("CODEX_HOME", filepath.Join(home, "codex"))
	path := filepath.Join(home, "codex", "sessions", "2026", "09", "14", "rollout-test-thread-reverse.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"type":"event_msg","payload":{"type":"task_started","turn_id":"turn-old"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inst := &session.Instance{ID: "instance-reverse", Tool: "codex", CodexSessionID: "thread-reverse"}

	// An ordinary no-wait sender exits after positive transport evidence but
	// before task_started is durable. Its marker outlives the process lock.
	ordinary, err := acquireCodexAcceptanceGuard(inst, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := ordinary.Prepare(inst.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := ordinary.RecordTransportOutcome(deliverySubmitted, time.Now()); err != nil {
		t.Fatal(err)
	}
	ordinary.Release()

	// A later structured sender cannot capture the ordinary sender's fence or
	// submit while that prior generation is unresolved.
	if guard, err := acquireCodexAcceptanceGuard(inst, time.Second); err == nil {
		guard.Release()
		t.Fatal("structured sender crossed an unresolved ordinary submission")
	} else if !strings.Contains(err.Error(), "manually remove") {
		t.Fatalf("unresolved refusal omitted recovery path: %v", err)
	}

	if err := appendCodexTurnStart(path, "turn-ordinary"); err != nil {
		t.Fatal(err)
	}
	structured, err := acquireCodexAcceptanceGuard(inst, time.Second)
	if err != nil {
		t.Fatalf("durable ordinary generation did not reconcile: %v", err)
	}
	defer structured.Release()
	if structured.fence.priorTurnGeneration != "thread-reverse:turn-ordinary" {
		t.Fatalf("new structured fence = %q", structured.fence.priorTurnGeneration)
	}
}

func TestEveryLocalCodexSendUsesAcceptanceGuard(t *testing.T) {
	local := &session.Instance{Tool: "codex"}
	for _, tc := range []struct {
		name       string
		json, wait bool
	}{
		{name: "ordinary"},
		{name: "no-wait JSON", json: true},
		{name: "human wait", wait: true},
		{name: "structured wait", json: true, wait: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !shouldAcquireCodexAcceptanceGuard(local, tc.json, tc.wait, false) {
				t.Fatal("local Codex send bypassed the acceptance guard")
			}
		})
	}
	if shouldAcquireCodexAcceptanceGuard(local, false, false, true) {
		t.Fatal("draft-only input must not create a submitted-turn marker")
	}
	if shouldAcquireCodexAcceptanceGuard(&session.Instance{Tool: "claude"}, true, true, false) {
		t.Fatal("non-Codex send entered Codex marker protocol")
	}
	for _, inst := range []*session.Instance{
		{Tool: "codex", SSHHost: "remote"},
		{Tool: "codex", Sandbox: &session.SandboxConfig{Enabled: true}},
	} {
		if shouldAcquireCodexAcceptanceGuard(inst, false, false, false) {
			t.Fatal("ordinary remote/sandbox send attempted a host marker read")
		}
		if !shouldAcquireCodexAcceptanceGuard(inst, true, true, false) {
			t.Fatal("structured remote/sandbox send bypassed fail-closed guard")
		}
	}
}

func TestDelayedCodexAcceptanceRetainsGuardThroughCompletionRetry(t *testing.T) {
	if !retainCodexAcceptanceGuardForCompletion(true, nil) {
		t.Fatal("wait with delayed task_started would release before the completion-boundary retry")
	}
	if retainCodexAcceptanceGuardForCompletion(false, nil) {
		t.Fatal("non-wait send cannot retain a process lock after it returns")
	}
	if retainCodexAcceptanceGuardForCompletion(true, &codexAcceptedTurnReceipt{}) {
		t.Fatal("an exact accepted generation no longer needs the process lock")
	}
}

func TestCodexAcceptanceGuardClearsOnlyDefinitiveNonDelivery(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("CODEX_HOME", filepath.Join(home, "codex"))

	tests := []struct {
		name     string
		delivery string
		retained bool
	}{
		{name: "line too long", delivery: deliveryLineTooLong, retained: false},
		{name: "composer blocked", delivery: deliveryComposerBlocked, retained: false},
		{name: "submitted", delivery: deliverySubmitted, retained: true},
		{name: "typed", delivery: deliveryTyped, retained: true},
		{name: "typed not submitted", delivery: deliveryTypedNotSubmitted, retained: true},
		{name: "no evidence", delivery: deliveryNoEvidence, retained: true},
		{name: "send failed", delivery: deliverySendFailed, retained: true},
		{name: "unverified", delivery: deliveryUnverified, retained: true},
	}

	for n, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sessionID := fmt.Sprintf("thread-outcome-%d", n)
			path := filepath.Join(home, "codex", "sessions", "2026", "09", "14", "rollout-test-"+sessionID+".jsonl")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			inst := &session.Instance{ID: "instance-" + sessionID, Tool: "codex", CodexSessionID: sessionID}
			guard, err := acquireCodexAcceptanceGuard(inst, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if err := guard.Prepare(inst.ID, time.Now()); err != nil {
				guard.Release()
				t.Fatal(err)
			}
			if err := guard.RecordTransportOutcome(tt.delivery, time.Now()); err != nil {
				guard.Release()
				t.Fatal(err)
			}
			guard.Release()

			_, reconcileErr := session.ReconcileCodexSubmissionMarker(inst.ID, sessionID, "")
			if tt.retained && reconcileErr == nil {
				t.Fatal("ambiguous/submitted outcome did not retain its marker")
			}
			if !tt.retained && reconcileErr != nil {
				t.Fatalf("definitive non-delivery retained a marker: %v", reconcileErr)
			}
		})
	}
}

func TestCodexAcceptanceGuardRejectsNonLocalStructuredWait(t *testing.T) {
	tests := []struct {
		name string
		inst *session.Instance
	}{
		{
			name: "SSH",
			inst: &session.Instance{
				ID: "remote-instance", Tool: "codex", CodexSessionID: "thread-remote",
				SSHHost: "remote",
			},
		},
		{
			name: "sandbox",
			inst: &session.Instance{
				ID: "sandbox-instance", Tool: "codex", CodexSessionID: "thread-sandbox",
				Sandbox: &session.SandboxConfig{Enabled: true},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			guard, err := acquireCodexAcceptanceGuard(tt.inst, 10*time.Millisecond)
			if guard != nil || err == nil || !strings.Contains(err.Error(), "unavailable for remote or sandboxed") {
				t.Fatalf("guard=(%#v, %v), want explicit pre-send refusal", guard, err)
			}
		})
	}
}

func TestCodexTimeoutFixtureMatchesGoSchema(t *testing.T) {
	receipt := &codexAcceptedTurnReceipt{
		ReceiptID: "receipt-1", InstanceID: "instance-1", CodexSessionID: "thread-1",
		TurnGeneration: "thread-1:turn-new", AcceptedAt: "2026-09-14T15:00:00Z",
	}
	payload := completionTimeoutPayload(map[string]interface{}{
		"delivery": deliverySubmitted, "submitted": true, "accepted_turn_kind": "codex_rollout",
		"accepted_turn": receipt,
	})
	payload["success"] = false
	payload["error"] = "timeout waiting for completion: agent still running"
	payload["code"] = ErrCodeInvalidOperation
	got, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("..", "..", "conductor", "tests", "fixtures", "issue2278_codex_timeout.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got)+"\n" != string(want) {
		t.Fatalf("Go timeout schema drifted from bridge fixture:\ngot  %s\nwant %s", got, want)
	}
}

func TestWaitForCodexTurnOutputWaitsForExactRepeatedReply(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	path := filepath.Join(home, "sessions", "2026", "09", "14", "rollout-test-thread-1.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	old := `{"timestamp":"2026-09-14T15:00:00Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-previous","last_agent_message":"OK"}}` + "\n"
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	freshOutputTestConfig = &freshOutputConfig{pollInterval: time.Millisecond, timeout: time.Second}
	defer func() { freshOutputTestConfig = nil }()
	writeDone := make(chan error, 1)
	go func() {
		time.Sleep(5 * time.Millisecond)
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err == nil {
			_, err = f.WriteString(`{"timestamp":"2026-09-14T15:01:00Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-new","last_agent_message":"OK"}}` + "\n")
			if closeErr := f.Close(); err == nil {
				err = closeErr
			}
		}
		writeDone <- err
	}()
	inst := &session.Instance{ID: "instance-1", Tool: "codex", CodexSessionID: "thread-1"}
	response, err := waitForCodexTurnOutput(inst, "thread-1:turn-new")
	if writeErr := <-writeDone; writeErr != nil {
		t.Fatal(writeErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	if response.Content != "OK" || response.CodexTurnGeneration != "thread-1:turn-new" {
		t.Fatalf("returned stale identical response: %#v", response)
	}
}
