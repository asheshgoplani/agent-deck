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

// TestNoWaitCodexSendSkipsAcceptedTurnPoll is the timing regression for the
// #2279 review finding: `session send --no-wait` (and any other non-wait
// send) to a local Codex session must not pay the exact-generation poll,
// which can run for the real codexAcceptedTurnPollTimeout default (2s) when
// the rollout file's turn never advances past the fence. It deliberately
// does NOT shorten codexAcceptedTurnPollTimeout/Interval — a shortened poll
// would hide exactly the regression this test exists to catch (the PR under
// review shortened the poll to 1ms in its own tests, so CI never saw the
// 2s block).
func TestNoWaitCodexSendSkipsAcceptedTurnPoll(t *testing.T) {
	if codexAcceptedTurnPollTimeout < time.Second {
		t.Fatalf("codexAcceptedTurnPollTimeout = %v; test requires the real (unshortened) default to be meaningful", codexAcceptedTurnPollTimeout)
	}
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	path := filepath.Join(home, "sessions", "2026", "09", "17", "rollout-test-thread-nowait.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	// The fence's generation is the ONLY generation ever on disk, so if the
	// poll runs it can never observe an advance and must burn the full
	// codexAcceptedTurnPollTimeout before giving up.
	if err := os.WriteFile(path, []byte(`{"type":"event_msg","payload":{"type":"task_started","turn_id":"turn-only"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inst := &session.Instance{ID: "instance-nowait", Tool: "codex", CodexSessionID: "thread-nowait"}
	fence := captureCodexAcceptanceFence(inst)
	if !fence.available {
		t.Fatal("fence setup failed; test cannot exercise the poll")
	}

	// Sanity check: prove this fence really would block for the full
	// timeout if observed synchronously (i.e. the old, unconditional call
	// site). This is what --no-wait must never pay.
	blockingStart := time.Now()
	if got := waitForAcceptedCodexTurn(inst, deliverySubmitted, time.Now(), fence); got != nil {
		t.Fatalf("unexpected receipt from a fence with no new generation: %#v", got)
	}
	if elapsed := time.Since(blockingStart); elapsed < codexAcceptedTurnPollTimeout {
		t.Fatalf("test setup does not actually block: waitForAcceptedCodexTurn returned in %v, want >= %v", elapsed, codexAcceptedTurnPollTimeout)
	}

	// The real assertion: routed through the wait-gated observation used by
	// `session send`, a non-wait send must return immediately.
	start := time.Now()
	got := observeAcceptedCodexTurn(false, inst, deliverySubmitted, time.Now(), fence)
	elapsed := time.Since(start)
	if got != nil {
		t.Fatalf("non-wait send must never observe an accepted turn: %#v", got)
	}
	if elapsed >= 500*time.Millisecond {
		t.Fatalf("--no-wait blocked for %v (>= 500ms budget); the accepted-turn poll must be skipped when not waiting", elapsed)
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

func TestCodexAcceptanceGuardHydratesLegacyIdentity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Join(home, "codex"))
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}

	const sessionID = "11111111-2222-4333-8444-555555555555"
	rollout := filepath.Join(home, "codex", "sessions", "2026", "09", "15", "rollout-test-"+sessionID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(rollout), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rollout, []byte(`{"type":"event_msg","payload":{"type":"task_started","turn_id":"turn-existing"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	inst := session.NewInstanceWithTool("legacy-codex-identity", project, "codex")
	tmuxSess := inst.GetTmuxSession()
	if tmuxSess == nil {
		t.Fatal("missing tmux session")
	}
	if err := tmuxSess.Start("sleep 30"); err != nil {
		t.Fatalf("start isolated pane: %v", err)
	}
	t.Cleanup(func() { _ = tmuxSess.Kill() })
	if err := tmuxSess.SetEnvironment("CODEX_SESSION_ID", sessionID); err != nil {
		t.Fatalf("set pane identity: %v", err)
	}

	storage, err := session.NewStorageWithProfile("legacy_codex_hydration")
	if err != nil {
		t.Fatalf("open isolated storage: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	if err := storage.SaveWithGroups([]*session.Instance{inst}, nil); err != nil {
		t.Fatalf("seed legacy instance: %v", err)
	}
	if inst.CodexSessionID != "" {
		t.Fatalf("precondition: persisted identity = %q, want empty", inst.CodexSessionID)
	}

	if err := hydrateLegacyCodexIdentity(inst, []*session.Instance{inst}, storage); err != nil {
		t.Fatalf("legacy identity was not hydrated before guard acquisition: %v", err)
	}
	guard, err := acquireCodexAcceptanceGuard(inst, time.Second)
	if err != nil {
		t.Fatalf("guard acquisition after hydration: %v", err)
	}
	defer guard.Release()
	if inst.CodexSessionID != sessionID || guard.fence.codexSessionID != sessionID ||
		guard.fence.priorTurnGeneration != sessionID+":turn-existing" {
		t.Fatalf("unexpected hydrated guard: id=%q fence=%#v", inst.CodexSessionID, guard.fence)
	}
	if guard.marker != nil {
		t.Fatal("guard hydration created a submission marker before transport")
	}

	if persisted := persistedCodexIdentity(t, storage, inst.ID); persisted != sessionID {
		t.Fatalf("persisted identity = %q, want %q", persisted, sessionID)
	}
}

func persistedCodexIdentity(t *testing.T, storage *session.Storage, instanceID string) string {
	t.Helper()
	rows, err := storage.GetDB().LoadInstances()
	if err != nil {
		t.Fatalf("reload persisted identity: %v", err)
	}
	for _, row := range rows {
		if row.ID != instanceID {
			continue
		}
		var persisted struct {
			CodexSessionID string `json:"codex_session_id"`
		}
		if err := json.Unmarshal(row.ToolData, &persisted); err != nil {
			t.Fatalf("decode persisted identity: %v", err)
		}
		return persisted.CodexSessionID
	}
	t.Fatalf("instance %q was not persisted", instanceID)
	return ""
}

func startLegacyCodexPane(t *testing.T, inst *session.Instance, identity string) {
	t.Helper()
	tmuxSess := inst.GetTmuxSession()
	if tmuxSess == nil {
		t.Fatal("missing tmux session")
	}
	if err := tmuxSess.Start("sleep 30"); err != nil {
		t.Fatalf("start isolated pane: %v", err)
	}
	t.Cleanup(func() { _ = tmuxSess.Kill() })
	if identity != "" {
		if err := tmuxSess.SetEnvironment("CODEX_SESSION_ID", identity); err != nil {
			t.Fatalf("set pane identity: %v", err)
		}
	}
}

func writeLegacyCodexRollout(t *testing.T, home, identity, suffix string) {
	t.Helper()
	path := filepath.Join(home, "sessions", "2026", "09", suffix, "rollout-test-"+identity+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"type":"event_msg","payload":{"type":"task_started","turn_id":"turn-existing"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func countCodexAcceptanceArtifacts(t *testing.T) int {
	t.Helper()
	root, err := session.GetAgentDeckDir()
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	err = filepath.Walk(filepath.Join(root, "locks"), func(path string, info os.FileInfo, walkErr error) error {
		if os.IsNotExist(walkErr) {
			return filepath.SkipDir
		}
		if walkErr != nil {
			return walkErr
		}
		if !info.IsDir() && (strings.Contains(filepath.Base(path), "codex-acceptance-") ||
			strings.Contains(path, string(filepath.Separator)+"codex-submissions"+string(filepath.Separator))) {
			count++
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return count
}

func TestLegacyCodexIdentityHydrationRefusesUntrustedCandidates(t *testing.T) {
	const candidate = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	const mismatch = "11111111-2222-4333-8444-666666666666"
	tests := []struct {
		name       string
		paneID     string
		startPane  bool
		rolloutIDs []string
		peer       bool
		want       string
	}{
		{name: "stale missing target", paneID: candidate, want: "identity is unavailable"},
		{name: "empty identity", startPane: true, want: "identity is unavailable"},
		{name: "malformed identity", startPane: true, paneID: "not-a-uuid", want: "invalid live Codex session identity"},
		{name: "missing rollout", startPane: true, paneID: candidate, want: "no unique current rollout"},
		{name: "mismatched rollout", startPane: true, paneID: candidate, rolloutIDs: []string{mismatch}, want: "no unique current rollout"},
		{name: "ambiguous rollout", startPane: true, paneID: candidate, rolloutIDs: []string{candidate, candidate}, want: "ambiguous exact context artifact"},
		{name: "stale peer binding owns live identity", startPane: true, paneID: candidate, rolloutIDs: []string{candidate}, peer: true, want: "already owned"},
	}

	for n, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := filepath.Join(t.TempDir(), "codex")
			t.Setenv("CODEX_HOME", home)
			project := filepath.Join(t.TempDir(), "project")
			if err := os.MkdirAll(project, 0o700); err != nil {
				t.Fatal(err)
			}
			inst := session.NewInstanceWithTool(fmt.Sprintf("legacy-refusal-%d", n), project, "codex")
			inst.Status = session.StatusWaiting
			if tt.startPane {
				startLegacyCodexPane(t, inst, tt.paneID)
			}
			for i, identity := range tt.rolloutIDs {
				writeLegacyCodexRollout(t, home, identity, fmt.Sprintf("%02d", i+15))
			}

			peers := []*session.Instance{inst}
			if tt.peer {
				peer := session.NewInstanceWithTool(fmt.Sprintf("legacy-peer-%d", n), project, "codex")
				peer.Status = session.StatusWaiting
				peer.CodexSessionID = mismatch
				startLegacyCodexPane(t, peer, candidate)
				peers = append(peers, peer)
			}
			storage, err := session.NewStorageWithProfile(fmt.Sprintf("legacy_refusal_%d", n))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = storage.Close() })
			if err := storage.SaveWithGroups(peers, nil); err != nil {
				t.Fatal(err)
			}

			before := countCodexAcceptanceArtifacts(t)
			err = hydrateLegacyCodexIdentity(inst, peers, storage)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("hydration error = %v, want %q", err, tt.want)
			}
			if inst.CodexSessionID != "" || persistedCodexIdentity(t, storage, inst.ID) != "" {
				t.Fatal("refused identity was retained in memory or storage")
			}
			if after := countCodexAcceptanceArtifacts(t); after != before {
				t.Fatalf("refused hydration created lock/marker artifacts: before=%d after=%d", before, after)
			}
		})
	}
}

func TestLegacyCodexIdentityHydrationRequiresCurrentGeneration(t *testing.T) {
	home := filepath.Join(t.TempDir(), "codex")
	t.Setenv("CODEX_HOME", home)
	project := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}

	const identity = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	rollout := filepath.Join(home, "sessions", "2026", "09", "15", "rollout-test-"+identity+".jsonl")
	if err := os.MkdirAll(filepath.Dir(rollout), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rollout, []byte(`{"type":"event_msg","payload":{"type":"agent_message"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	inst := session.NewInstanceWithTool("legacy-empty-generation", project, "codex")
	startLegacyCodexPane(t, inst, identity)
	storage, err := session.NewStorageWithProfile("legacy_empty_generation")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	if err := storage.SaveWithGroups([]*session.Instance{inst}, nil); err != nil {
		t.Fatal(err)
	}

	before := countCodexAcceptanceArtifacts(t)
	err = hydrateLegacyCodexIdentity(inst, []*session.Instance{inst}, storage)
	if err == nil || !strings.Contains(err.Error(), "current turn generation is unavailable") {
		t.Fatalf("empty-generation hydration error = %v, want pre-acceptance refusal", err)
	}
	if inst.CodexSessionID != "" || persistedCodexIdentity(t, storage, inst.ID) != "" {
		t.Fatal("empty-generation refusal retained identity in memory or storage")
	}
	if after := countCodexAcceptanceArtifacts(t); after != before {
		t.Fatalf("empty-generation refusal created lock/marker artifacts: before=%d after=%d", before, after)
	}
}

func TestFailedLegacyCodexHydrationPreservesOtherIdentities(t *testing.T) {
	inst := session.NewInstanceWithTool("legacy-unrelated-identities", t.TempDir(), "codex")
	inst.ClaudeSessionID = "claude-before"
	inst.GeminiSessionID = "gemini-before"
	inst.OpenCodeSessionID = "opencode-before"
	inst.CopilotSessionID = "copilot-before"
	inst.GeminiDetectedAt = time.Unix(11, 12).UTC()
	inst.OpenCodeDetectedAt = time.Unix(13, 14).UTC()
	startLegacyCodexPane(t, inst, "not-a-uuid")
	for name, value := range map[string]string{
		"CLAUDE_SESSION_ID":   "claude-from-pane",
		"GEMINI_SESSION_ID":   "gemini-from-pane",
		"OPENCODE_SESSION_ID": "opencode-from-pane",
		"COPILOT_SESSION_ID":  "copilot-from-pane",
	} {
		if err := inst.GetTmuxSession().SetEnvironment(name, value); err != nil {
			t.Fatalf("set %s: %v", name, err)
		}
	}

	type unrelatedIdentityState struct {
		claudeID   string
		claudeAt   time.Time
		geminiID   string
		geminiAt   time.Time
		openCodeID string
		openCodeAt time.Time
		copilotID  string
		copilotAt  time.Time
		genericID  string
		genericAt  time.Time
	}
	snapshot := func() unrelatedIdentityState {
		return unrelatedIdentityState{
			claudeID: inst.ClaudeSessionID, claudeAt: inst.ClaudeDetectedAt,
			geminiID: inst.GeminiSessionID, geminiAt: inst.GeminiDetectedAt,
			openCodeID: inst.OpenCodeSessionID, openCodeAt: inst.OpenCodeDetectedAt,
			copilotID: inst.CopilotSessionID, copilotAt: inst.CopilotDetectedAt,
			genericID: inst.GenericSessionID, genericAt: inst.GenericDetectedAt,
		}
	}
	beforeState := snapshot()
	beforeArtifacts := countCodexAcceptanceArtifacts(t)

	err := hydrateLegacyCodexIdentity(inst, []*session.Instance{inst}, nil)
	if err == nil || !strings.Contains(err.Error(), "invalid live Codex session identity") {
		t.Fatalf("malformed Codex identity error = %v, want refusal", err)
	}
	if after := snapshot(); after != beforeState {
		t.Fatalf("failed Codex hydration mutated unrelated identities: before=%#v after=%#v", beforeState, after)
	}
	if inst.CodexSessionID != "" || !inst.CodexDetectedAt.IsZero() {
		t.Fatalf("failed hydration retained Codex identity: id=%q detected=%v", inst.CodexSessionID, inst.CodexDetectedAt)
	}
	if after := countCodexAcceptanceArtifacts(t); after != beforeArtifacts {
		t.Fatalf("failed hydration created lock/marker artifacts: before=%d after=%d", beforeArtifacts, after)
	}
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
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 64; n++ {
		if _, err := fmt.Fprintf(f, `{"type":"event_msg","payload":{"type":"agent_message","message":"noise-%d"}}`+"\n", n); err != nil {
			_ = f.Close()
			t.Fatal(err)
		}
	}
	if _, err := f.WriteString(
		`{"type":"response_item","payload":{"type":"task_started","turn_id":"turn-unrelated"}}` + "\n" +
			`{"type":"event_msg","payload":{"type":"task_started"`,
	); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
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

func TestCodexAcceptanceLockWaitNormalizesNonPositiveTimeouts(t *testing.T) {
	tests := []struct {
		name    string
		timeout time.Duration
		want    time.Duration
	}{
		{name: "zero uses default then cap", timeout: 0, want: codexAcceptanceLockTimeout},
		{name: "negative uses default then cap", timeout: -time.Second, want: codexAcceptanceLockTimeout},
		{name: "positive below cap", timeout: 2 * time.Second, want: 2 * time.Second},
		{name: "positive equal to cap", timeout: codexAcceptanceLockTimeout, want: codexAcceptanceLockTimeout},
		{name: "positive above cap", timeout: 10 * time.Second, want: codexAcceptanceLockTimeout},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := codexAcceptanceLockWait(tt.timeout); got != tt.want || got <= 0 {
				t.Fatalf("codexAcceptanceLockWait(%v) = %v, want positive %v", tt.timeout, got, tt.want)
			}
		})
	}

	local := &session.Instance{Tool: "codex"}
	for _, tc := range []struct {
		name string
		wait bool
	}{
		{name: "ordinary no-wait"},
		{name: "wait", wait: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !shouldAcquireCodexAcceptanceGuard(local, false, tc.wait, false) {
				t.Fatal("local Codex send bypassed the acceptance guard")
			}
			for _, timeout := range []time.Duration{0, -time.Second} {
				if lockWait := codexAcceptanceLockWait(timeout); lockWait <= 0 {
					t.Fatalf("lock wait = %v for timeout %v, want positive", lockWait, timeout)
				}
			}
		})
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
		{name: "delivered", delivery: deliveryDelivered, retained: true},
		{name: "menu open", delivery: deliveryMenuOpen, retained: true},
		{name: "pane gone", delivery: deliveryPaneGone, retained: true},
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
		want string
	}{
		{
			name: "SSH",
			inst: &session.Instance{
				ID: "remote-instance", Tool: "codex", SSHHost: "remote",
			},
			want: "unavailable for remote or sandboxed",
		},
		{
			name: "sandbox",
			inst: &session.Instance{
				ID: "sandbox-instance", Tool: "codex", Sandbox: &session.SandboxConfig{Enabled: true},
			},
			want: "unavailable for remote or sandboxed",
		},
		{
			name: "non-Codex",
			inst: &session.Instance{ID: "claude-instance", Tool: "claude"},
			want: "not Codex-compatible",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := hydrateLegacyCodexIdentity(tt.inst, []*session.Instance{tt.inst}, nil); err != nil {
				t.Fatalf("out-of-scope target entered legacy hydration: %v", err)
			}
			guard, err := acquireCodexAcceptanceGuard(tt.inst, 10*time.Millisecond)
			if guard != nil || err == nil || !strings.Contains(err.Error(), tt.want) {
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
