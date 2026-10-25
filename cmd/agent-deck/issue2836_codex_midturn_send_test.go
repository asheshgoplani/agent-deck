package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func issue2836Stamp(at time.Time) string {
	return at.UTC().Format("2006-01-02T15:04:05.000Z")
}

func issue2836Append(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range lines {
		if _, err := f.WriteString(line + "\n"); err != nil {
			_ = f.Close()
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// Issue #2836: a send that lands while a Codex turn runs starts no new turn.
// Codex records it as one more user input of the running turn. The marker
// waited for a newer turn that never came, so every later send refused.
func TestIssue2836CodexSendTakenUpMidTurnReconciles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("CODEX_HOME", filepath.Join(home, "codex"))
	path := filepath.Join(home, "codex", "sessions", "2026", "10", "21", "rollout-test-thread-2836.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	// Turn T is running, opened by its own user input.
	turnStart := time.Now().Add(-time.Minute)
	issue2836Append(t, path,
		`{"timestamp":"`+issue2836Stamp(turnStart)+`","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-t"}}`,
		`{"timestamp":"`+issue2836Stamp(turnStart.Add(250*time.Millisecond))+`","type":"event_msg","payload":{"type":"item_completed","turn_id":"turn-t","item":{"type":"UserMessage","id":"open"}}}`,
	)
	inst := &session.Instance{ID: "instance-2836", Tool: "codex", CodexSessionID: "thread-2836"}

	// A send lands mid-turn with no positive evidence: the marker stays.
	sender, err := acquireCodexAcceptanceGuard(inst, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	sentAt := time.Now()
	if err := sender.Prepare(inst.ID, sentAt); err != nil {
		t.Fatal(err)
	}
	if err := sender.RecordTransportOutcome(deliveryNoEvidence, sentAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	sender.Release()

	// Before Codex takes the text up, the next send must still refuse.
	if guard, err := acquireCodexAcceptanceGuard(inst, time.Second); err == nil {
		guard.Release()
		t.Fatal("a later send crossed an attempt nothing has taken up yet")
	} else if !strings.Contains(err.Error(), "manually remove") {
		t.Fatalf("refusal lost its recovery hint: %v", err)
	}

	// Codex takes the text up inside T, then T completes. No new turn.
	issue2836Append(t, path,
		`{"timestamp":"`+issue2836Stamp(sentAt.Add(13*time.Second))+`","type":"event_msg","payload":{"type":"item_completed","turn_id":"turn-t","item":{"type":"UserMessage","id":"mid"}}}`,
		`{"timestamp":"`+issue2836Stamp(sentAt.Add(3*time.Minute))+`","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-t","last_agent_message":"done"}}`,
	)

	next, err := acquireCodexAcceptanceGuard(inst, time.Second)
	if err != nil {
		t.Fatalf("send taken up by the running turn did not reconcile: %v", err)
	}
	defer next.Release()
	if next.fence.priorTurnGeneration != "thread-2836:turn-t" {
		t.Fatalf("next fence = %q, want the unchanged turn", next.fence.priorTurnGeneration)
	}
}
