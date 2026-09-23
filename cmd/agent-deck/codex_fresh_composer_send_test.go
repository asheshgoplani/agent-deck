package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// A fresh Codex composer (0.155+) owns its thread before any rollout exists:
// the only trace is the thread writer lock the live process holds open. The
// send guard must accept that thread with an empty prior generation, and the
// first rollout turn must then produce the exact accepted-turn receipt.
func TestCodexAcceptanceGuardAcceptsFreshComposerThread(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "codex")
	t.Setenv("CODEX_HOME", home)
	project := filepath.Join(root, "project")
	for _, dir := range []string{project, filepath.Join(home, "thread-writer-locks"), filepath.Join(root, "bin")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	const thread = "01a0cc56-3416-7531-9a25-c53c0d193753"
	lock := filepath.Join(home, "thread-writer-locks", thread+".lock")
	fakeCodex := filepath.Join(root, "bin", "codex")
	script := "#!/bin/sh\nexec 9>>\"$1\"\nwhile :; do sleep 1; done\n"
	if err := os.WriteFile(fakeCodex, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	inst := session.NewInstanceWithTool("fresh-codex-composer", project, "codex")
	inst.Status = session.StatusWaiting
	tmuxSess := inst.GetTmuxSession()
	if tmuxSess == nil {
		t.Fatal("missing tmux session")
	}
	if err := tmuxSess.Start(fakeCodex + " " + lock); err != nil {
		t.Fatalf("start fake codex pane: %v", err)
	}
	t.Cleanup(func() { _ = tmuxSess.Kill() })
	deadline := time.Now().Add(10 * time.Second)
	for inst.LiveCodexThreadID() != thread {
		if time.Now().After(deadline) {
			t.Fatalf("pane process never held the writer lock (live thread %q)", inst.LiveCodexThreadID())
		}
		time.Sleep(100 * time.Millisecond)
	}

	storage, err := session.NewStorageWithProfile("fresh_codex_composer")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	if err := storage.SaveWithGroups([]*session.Instance{inst}, nil); err != nil {
		t.Fatal(err)
	}

	if err := hydrateLegacyCodexIdentity(inst, []*session.Instance{inst}, storage); err != nil {
		t.Fatalf("fresh composer identity: %v", err)
	}
	guard, err := acquireCodexAcceptanceGuard(inst, time.Second)
	if err != nil {
		t.Fatalf("fresh composer guard: %v", err)
	}
	defer guard.Release()
	if inst.CodexSessionID != thread || !guard.fence.available || guard.fence.priorTurnGeneration != "" {
		t.Fatalf("unexpected fresh guard: id=%q fence=%#v", inst.CodexSessionID, guard.fence)
	}
	if persisted := persistedCodexIdentity(t, storage, inst.ID); persisted != thread {
		t.Fatalf("persisted identity = %q, want %q", persisted, thread)
	}
	if err := validateCodexAcceptanceFence(inst, guard.fence); err != nil {
		t.Fatalf("fence before the first turn: %v", err)
	}

	// Codex writes the rollout when the submitted turn starts.
	writeLegacyCodexRollout(t, home, thread, "23")
	receipt := waitForAcceptedCodexTurn(inst, deliverySubmitted, time.Now(), guard.fence)
	if receipt == nil || receipt.TurnGeneration != thread+":turn-existing" {
		t.Fatalf("first turn receipt = %#v", receipt)
	}
}

// Codex 0.155's pane gives the send loop no reliable busy edge, so without a
// transcript signal every send ends "delivered, confirmation unknown" and a
// structured --json --wait send can never carry an accepted-turn receipt. A
// new turn in the exact rollout past the acceptance fence is the submission.
func TestCodexTurnAdvancedPastFence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	const thread = "01a0cc41-3ddb-7170-98bc-27b021e69141"
	path := filepath.Join(home, "sessions", "2026", "09", "23", "rollout-2026-09-23T05-13-42-"+thread+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	first := `{"type":"event_msg","payload":{"type":"task_started","turn_id":"01a0cc41-a48e-7cd2-be47-03ad5253712d"}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"task_complete","turn_id":"01a0cc41-a48e-7cd2-be47-03ad5253712d","last_agent_message":"pong"}}` + "\n"
	if err := os.WriteFile(path, []byte(first), 0o600); err != nil {
		t.Fatal(err)
	}
	inst := &session.Instance{ID: "turn-advance", Tool: "codex", CodexSessionID: thread}
	fence := captureCodexAcceptanceFence(inst)
	if !fence.available {
		t.Fatal("fence unavailable")
	}
	if codexTurnAdvancedPastFence(inst, fence) {
		t.Fatal("the fence's own turn is not a new submission")
	}
	if codexTurnAdvancedPastFence(inst, codexAcceptanceFence{}) {
		t.Fatal("an unavailable fence proves nothing")
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, werr := f.WriteString(`{"type":"event_msg","payload":{"type":"task_started","turn_id":"01a0cc45-7448-7191-b5f9-582a363e8392"}}` + "\n")
	if cerr := f.Close(); werr != nil || cerr != nil {
		t.Fatalf("append turn: %v %v", werr, cerr)
	}
	if !codexTurnAdvancedPastFence(inst, fence) {
		t.Fatal("a new rollout turn past the fence must confirm submission")
	}
}

// The Codex arrival loop must take the rollout's turn advance as submission,
// like the Claude loop does with its transcript. Same frames as the #1793
// "delivered, confirmation unknown" case, plus a turn that starts.
func TestCodexArrivalLoopTakesTurnAdvanceAsSubmission(t *testing.T) {
	msg := "Reply with exactly: json-pong3"
	mock := &mockSendRetryTarget{
		statuses: []string{"waiting"},
		panes:    []string{"› \n", "› " + msg + "\n› \n"},
	}
	calls := 0
	delivery, err := sendWithRetryTarget(mock, msg, true, sendRetryOptions{
		maxRetries: 4, checkDelay: 0, tool: "codex",
		turnAdvanced: func() bool { calls++; return calls >= 2 },
	})
	if err != nil || delivery != deliverySubmitted {
		t.Fatalf("delivery = %q, %v; want %q once the rollout turn advanced", delivery, err, deliverySubmitted)
	}
}
