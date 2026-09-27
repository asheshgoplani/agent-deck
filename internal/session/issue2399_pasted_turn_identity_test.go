package session

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Issue #2399: a Claude follow-up sent with --message-file is multi-line, so
// Claude Code 2.1.277+ stores its user row as a pasted-content block. The
// waiter compared the row's literal body with the sent text, never bound
// the turn, and held --wait for the whole timeout although the answer had
// landed ~25 s after the send. A short first turn is stored plainly, which
// is why fresh sessions were fine.

const issue2399FollowUp = "Follow-up for the second turn:\n1. Read /work/child/notes.md\n2. Summarise it in three bullets\n\nReply with the bullets only."

// issue2399Transcript replays the shared pasted-content fixture as a live
// transcript: everything through the first turn is on disk before the send,
// the rest lands after it. It returns the path and the pre-send cursor.
func issue2399Transcript(t *testing.T) (string, int64, []byte) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "recall", "query", "testdata", "rows", "claude-pasted-content.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	split := bytes.Index(data, []byte(`"uuid":"u-turn2"`))
	if split < 0 {
		t.Fatal("fixture has no second turn")
	}
	split = bytes.LastIndexByte(data[:split], '\n') + 1
	path := filepath.Join(t.TempDir(), "7c1f0a52-3d4e-4b8a-9f61-2a5b8c9d0e13.jsonl")
	if err := os.WriteFile(path, data[:split], 0o600); err != nil {
		t.Fatal(err)
	}
	cursor, err := TranscriptCursor(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, cursor, data[split:]
}

func TestIssue2399_FollowUpBindsPastedContentUserRow(t *testing.T) {
	path, cursor, rest := issue2399Transcript(t)
	q := TurnQuery{Path: path, Prompt: issue2399FollowUp, Cursor: cursor}
	if TurnAdvanced(q) {
		t.Fatal("turn advanced before the follow-up landed")
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return
		}
		defer f.Close()
		_, _ = f.Write(rest)
	}()

	start := time.Now()
	id, err := AwaitTurnIdentity(q, 3*time.Second, 5*time.Millisecond)
	if err != nil {
		t.Fatalf("turn identity not established for a pasted-content follow-up: %v", err)
	}
	if id.UUID != "u-turn2" {
		t.Fatalf("bound %q, want the follow-up's own row u-turn2", id.UUID)
	}
	if waited := time.Since(start); waited > 2*time.Second {
		t.Fatalf("identity took %s; the waiter must bind as soon as the row lands", waited)
	}
	if !TurnAdvanced(q) {
		t.Fatal("verification loop did not see the pasted follow-up as turn advancement")
	}
	resp, err := AwaitTurnResponse(id, 3*time.Second, 5*time.Millisecond)
	if err != nil {
		t.Fatalf("reply: %v", err)
	}
	if resp.Content != "- one\n- two\n- three" {
		t.Fatalf("reply %q is not the follow-up's own answer", resp.Content)
	}
}

// Unwrapping must not loosen identity: a different message, or text that
// only shares the pasted block's first line, never binds.
func TestIssue2399_PastedContentNeedsTheWholeText(t *testing.T) {
	path, cursor, rest := issue2399Transcript(t)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write(rest)
	f.Close()
	for _, prompt := range []string{"Follow-up for the second turn:", strings.Replace(issue2399FollowUp, "three", "four", 1)} {
		if TurnAdvanced(TurnQuery{Path: path, Prompt: prompt, Cursor: cursor}) {
			t.Errorf("prompt %q bound a pasted row holding different text", prompt)
		}
	}
}
