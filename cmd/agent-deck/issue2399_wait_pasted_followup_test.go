package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Issue #2399 at the --wait helper: the follow-up's user row lands as a
// pasted-content block ~after the send, the turn completes natively, and
// awaitClaudeWaitReply must return that turn's answer right away instead of
// holding the whole --timeout and failing with "turn identity not
// established".
func TestIssue2399_WaitReturnsAfterPastedFollowUpCompletes(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "internal", "recall", "query", "testdata", "rows", "claude-pasted-content.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	split := bytes.Index(data, []byte(`"uuid":"u-turn2"`))
	split = bytes.LastIndexByte(data[:split], '\n') + 1
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, data[:split], 0o600); err != nil {
		t.Fatal(err)
	}
	cursor, err := session.TranscriptCursor(path)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return
		}
		defer f.Close()
		_, _ = f.Write(data[split:])
	}()

	const followUp = "Follow-up for the second turn:\n1. Read /work/child/notes.md\n2. Summarise it in three bullets\n\nReply with the bullets only."
	timeout := 5 * time.Second
	start := time.Now()
	resp, status, identityErr, completionErr, responseErr := awaitClaudeWaitReply(
		session.TurnQuery{Path: path, Prompt: followUp, Cursor: cursor},
		time.Now().Add(timeout),
		func(time.Duration) (string, error) { return "waiting", nil },
	)
	if identityErr != nil {
		t.Fatalf("turn identity not established: %v", identityErr)
	}
	if completionErr != nil || responseErr != nil {
		t.Fatalf("completion=%v response=%v", completionErr, responseErr)
	}
	if resp == nil || resp.Content != "- one\n- two\n- three" || status != "waiting" {
		t.Fatalf("got %+v status=%q", resp, status)
	}
	if waited := time.Since(start); waited > timeout/2 {
		t.Fatalf("waiter returned after %s; it must return once the native turn completes", waited)
	}
}
