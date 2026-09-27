package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Issue #2395: `session send --wait --timeout 35m` returned completion=timeout
// and "turn was not flushed" a few minutes in, while the accepted turn was
// still running. The status heuristic can read a long Codex turn as finished
// (long tool calls leave the pane quiet), and the exact-generation read that
// follows had its own fixed 5s limit, so that internal limit surfaced as the
// caller's timeout. The read must use what is left of the caller's budget.
func TestIssue2395_CodexTurnOutputWaitsForCallerBudget(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	path := filepath.Join(home, "sessions", "2026", "09", "27", "rollout-test-thread-1.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	running := `{"timestamp":"2026-09-27T00:00:00Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-long"}}` + "\n"
	if err := os.WriteFile(path, []byte(running), 0o600); err != nil {
		t.Fatal(err)
	}
	// The turn completes after the old fixed 5s read limit, well inside the
	// caller's budget.
	completeAfter := 5500 * time.Millisecond
	writeDone := make(chan error, 1)
	go func() {
		time.Sleep(completeAfter)
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err == nil {
			_, err = f.WriteString(`{"timestamp":"2026-09-27T00:20:00Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-long","last_agent_message":"DONE"}}` + "\n")
			if closeErr := f.Close(); err == nil {
				err = closeErr
			}
		}
		writeDone <- err
	}()

	inst := &session.Instance{ID: "instance-1", Tool: "codex", CodexSessionID: "thread-1"}
	response, err := waitForCodexTurnOutput(inst, "thread-1:turn-long", time.Now().Add(20*time.Second))
	if writeErr := <-writeDone; writeErr != nil {
		t.Fatal(writeErr)
	}
	if err != nil {
		t.Fatalf("accepted turn still running inside the caller budget was reported as a timeout: %v", err)
	}
	if response.Content != "DONE" || response.CodexTurnGeneration != "thread-1:turn-long" {
		t.Fatalf("response = %#v, want the accepted turn's reply", response)
	}
}

// When the caller's budget does run out, the error names that budget rather
// than an unexplained flush limit.
func TestIssue2395_CodexTurnOutputTimeoutNamesCallerBudget(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	path := filepath.Join(home, "sessions", "2026", "09", "27", "rollout-test-thread-1.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	running := `{"timestamp":"2026-09-27T00:00:00Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-long"}}` + "\n"
	if err := os.WriteFile(path, []byte(running), 0o600); err != nil {
		t.Fatal(err)
	}
	freshOutputTestConfig = &freshOutputConfig{pollInterval: time.Millisecond}
	defer func() { freshOutputTestConfig = nil }()

	inst := &session.Instance{ID: "instance-1", Tool: "codex", CodexSessionID: "thread-1"}
	start := time.Now()
	_, err := waitForCodexTurnOutput(inst, "thread-1:turn-long", start.Add(300*time.Millisecond))
	if err == nil {
		t.Fatal("unfinished turn returned no error")
	}
	if elapsed := time.Since(start); elapsed < 250*time.Millisecond {
		t.Fatalf("read gave up after %s, before the caller budget", elapsed)
	}
	if !strings.Contains(err.Error(), "--timeout") || strings.Contains(err.Error(), "flushed") {
		t.Fatalf("error %q should name the caller's --timeout budget", err)
	}
}
