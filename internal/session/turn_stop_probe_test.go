package session

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// Maintainer regression probes for #2543 (split final message across records).

const probeNextPrompt = `{"type":"user","uuid":"prompt2","message":{"role":"user","content":"next question"}}`

// A thinking-only final message followed directly by the next human prompt
// must end the turn cleanly, not error as "no end_turn before next prompt".
func TestProbe_ThinkingOnlyStopThenHumanPrompt(t *testing.T) {
	_, id := writeSplitTurn(t, splitTurnMidText, splitTurnToolUse, splitTurnToolResult,
		splitTurnThinking, probeNextPrompt)
	resp, err := AwaitTurnResponse(id, time.Second, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "Now the PATH change." {
		t.Fatalf("reply = %q", resp.Content)
	}
}

// Reply record then the next human prompt: reply kept, no error.
func TestProbe_ReplyThenHumanPrompt(t *testing.T) {
	_, id := writeSplitTurn(t, splitTurnThinking, splitTurnReply, probeNextPrompt)
	resp, err := AwaitTurnResponse(id, time.Second, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "I restarted the 16 sessions." {
		t.Fatalf("reply = %q", resp.Content)
	}
}

// Single-record final message (no thinking) ends at EOF immediately.
func TestProbe_PlainReplyEndsAtEOF(t *testing.T) {
	_, id := writeSplitTurn(t, splitTurnReply)
	start := time.Now()
	resp, err := AwaitTurnResponse(id, 2*time.Second, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "I restarted the 16 sessions." || time.Since(start) > 500*time.Millisecond {
		t.Fatalf("reply = %q after %s", resp.Content, time.Since(start))
	}
}

// Thinking-only final message with nothing after it: documents that the
// reader keeps waiting and returns the partial text as incomplete.
func TestProbe_ThinkingOnlyStopAtEOFWaits(t *testing.T) {
	_, id := writeSplitTurn(t, splitTurnMidText, splitTurnToolUse, splitTurnToolResult, splitTurnThinking)
	resp, err := AwaitTurnResponse(id, 200*time.Millisecond, time.Millisecond)
	t.Logf("resp=%+v err=%v", resp, err)
	if !errors.Is(err, ErrTurnResponseIncomplete) {
		t.Fatalf("err = %v, want ErrTurnResponseIncomplete", err)
	}
}

// Stream: thinking-only stop then a human prompt is a normal stop, not an
// interruption.
func TestProbe_StreamThinkingOnlyStopThenHumanPrompt(t *testing.T) {
	_, id := writeSplitTurn(t, splitTurnMidText, splitTurnToolUse, splitTurnToolResult,
		splitTurnThinking, probeNextPrompt)
	var out bytes.Buffer
	err := StreamTranscriptForTurn(context.Background(), id, "sid", &out, StreamConfig{
		PollInterval: time.Millisecond, IdleTimeout: time.Second, CharBudget: 1024, ToolBudget: 10,
	})
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), `"type":"stop"`) || !strings.Contains(out.String(), `"reason":"end_turn"`) {
		t.Fatalf("stream = %s", out.String())
	}
}
