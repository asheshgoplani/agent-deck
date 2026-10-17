package session

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Claude writes each content block of a message as its own transcript record,
// and every record carries the message's stop_reason. A final message with
// thinking therefore arrives as a thinking record marked end_turn followed by
// the text record holding the reply. These records copy that shape from a
// real conductor turn whose reply was cut down to a mid-turn sentence.
const (
	splitTurnPrompt     = `{"type":"user","uuid":"prompt","message":{"role":"user","content":"restart sessions"}}`
	splitTurnMidText    = `{"type":"assistant","uuid":"a1","message":{"id":"msg_1","role":"assistant","content":[{"type":"text","text":"Now the PATH change."}],"stop_reason":"tool_use"}}`
	splitTurnToolUse    = `{"type":"assistant","uuid":"a2","message":{"id":"msg_1","role":"assistant","content":[{"type":"tool_use","id":"tu_1","name":"Bash","input":{"command":"ls"}}],"stop_reason":"tool_use"}}`
	splitTurnToolResult = `{"type":"user","uuid":"u1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_1","content":"ok"}]}}`
	splitTurnThinking   = `{"type":"assistant","uuid":"a3","message":{"id":"msg_2","role":"assistant","content":[{"type":"thinking","thinking":"summarize"}],"stop_reason":"end_turn"}}`
	splitTurnReply      = `{"type":"assistant","uuid":"a4","message":{"id":"msg_2","role":"assistant","content":[{"type":"text","text":"I restarted the 16 sessions."}],"stop_reason":"end_turn"}}`
	splitTurnStopHook   = `{"type":"user","uuid":"u2","isMeta":true,"message":{"role":"user","content":"Stop hook feedback: child sessions completed"}}`
	splitTurnDuration   = `{"type":"system","uuid":"s1","subtype":"turn_duration"}`
)

func writeSplitTurn(t *testing.T, records ...string) (string, TurnIdentity) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	prompt := splitTurnPrompt + "\n"
	body := prompt + strings.Join(records, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, TurnIdentity{UUID: "prompt", Path: path, StartOffset: int64(len(prompt))}
}

func appendRecords(t *testing.T, path string, records ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, r := range records {
		fmt.Fprintln(f, r)
	}
}

func TestAwaitTurnResponse_ReplyAfterThinkingStopRecordIsKept(t *testing.T) {
	_, id := writeSplitTurn(t, splitTurnMidText, splitTurnToolUse, splitTurnToolResult,
		splitTurnThinking, splitTurnReply, splitTurnStopHook, splitTurnDuration)

	resp, err := AwaitTurnResponse(id, time.Second, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if want := "Now the PATH change.\n\nI restarted the 16 sessions."; resp.Content != want {
		t.Fatalf("reply = %q, want %q", resp.Content, want)
	}
}

func TestAwaitTurnResponse_WaitsForReplyRecordAfterThinkingStop(t *testing.T) {
	path, id := writeSplitTurn(t, splitTurnMidText, splitTurnToolUse, splitTurnToolResult, splitTurnThinking)

	type result struct {
		resp *ResponseOutput
		err  error
	}
	got := make(chan result, 1)
	go func() {
		resp, err := AwaitTurnResponse(id, 3*time.Second, time.Millisecond)
		got <- result{resp, err}
	}()
	select {
	case r := <-got:
		t.Fatalf("returned before the reply record was written: %+v, %v", r.resp, r.err)
	case <-time.After(50 * time.Millisecond):
	}
	appendRecords(t, path, splitTurnReply)

	r := <-got
	if r.err != nil {
		t.Fatal(r.err)
	}
	if !strings.HasSuffix(r.resp.Content, "I restarted the 16 sessions.") {
		t.Fatalf("reply = %q, want it to end with the final message's text", r.resp.Content)
	}
}

func TestAwaitTurnResponse_ThinkingOnlyStopEndsAtTheNextRecord(t *testing.T) {
	_, id := writeSplitTurn(t, splitTurnMidText, splitTurnToolUse, splitTurnToolResult,
		splitTurnThinking, splitTurnStopHook)

	resp, err := AwaitTurnResponse(id, time.Second, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "Now the PATH change." {
		t.Fatalf("reply = %q", resp.Content)
	}
}

func TestStreamTranscriptForTurn_ReplyAfterThinkingStopRecordIsStreamed(t *testing.T) {
	path, id := writeSplitTurn(t, splitTurnMidText, splitTurnToolUse, splitTurnToolResult, splitTurnThinking)

	var out bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- StreamTranscriptForTurn(context.Background(), id, "sid", &out, StreamConfig{
			PollInterval: time.Millisecond,
			IdleTimeout:  time.Second,
			CharBudget:   1024,
			ToolBudget:   10,
		})
	}()
	select {
	case err := <-done:
		t.Fatalf("stream stopped before the reply record was written: %v\n%s", err, out.String())
	case <-time.After(50 * time.Millisecond):
	}
	appendRecords(t, path, splitTurnReply, splitTurnStopHook)

	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "I restarted the 16 sessions.") || !strings.Contains(got, `"type":"stop"`) {
		t.Fatalf("stream = %s", got)
	}
}
