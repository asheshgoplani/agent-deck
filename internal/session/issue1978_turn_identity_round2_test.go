package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Round-2 review of PR #2043 (issues #1978 / #2033): `--wait` and `--stream`
// must bind to the turn THIS send produced, never to the turn already in
// flight when the message was queued, and never to an older identical prompt.

func appendTranscript(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, line := range lines {
		fmt.Fprintln(f, line)
	}
}

func userLine(uuid, ts, text string) string {
	return fmt.Sprintf(`{"type":"user","uuid":%q,"timestamp":%q,"message":{"role":"user","content":%q}}`, uuid, ts, text)
}

func assistantLine(uuid, ts, text, stop string) string {
	return fmt.Sprintf(`{"type":"assistant","uuid":%q,"timestamp":%q,"message":{"role":"assistant","content":[{"type":"text","text":%q}],"stop_reason":%q}}`, uuid, ts, text, stop)
}

// TestIssue1978_QueuedSendBindsToItsOwnTurnNotTheInFlightOne is the red-path
// test the maintainer asked for: a send lands while a turn is in progress and
// is queued. The in-flight turn finishes first. The reply returned for the
// queued send must be the queued turn's, and the in-flight tail must never be
// returned as its answer.
func TestIssue1978_QueuedSendBindsToItsOwnTurnNotTheInFlightOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	base := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	ts := func(d time.Duration) string { return base.Add(d).Format(time.RFC3339Nano) }

	// The turn in flight when the send happens.
	appendTranscript(t, path, userLine("inflight-user", ts(-10*time.Second), "long task"))
	cursor, err := TranscriptCursor(path)
	if err != nil {
		t.Fatal(err)
	}
	sentAt := base

	done := make(chan struct {
		resp *ResponseOutput
		err  error
	}, 1)
	go func() {
		id, err := AwaitTurnIdentity(TurnQuery{Path: path, Prompt: "queued question", Cursor: cursor, NotBefore: sentAt.Add(-2 * time.Second)}, 3*time.Second, time.Millisecond)
		if err != nil {
			done <- struct {
				resp *ResponseOutput
				err  error
			}{nil, err}
			return
		}
		resp, err := AwaitTurnResponse(id, 3*time.Second, time.Millisecond)
		done <- struct {
			resp *ResponseOutput
			err  error
		}{resp, err}
	}()

	// The in-flight turn's reply arrives AFTER sentAt: a timestamp-only
	// freshness check accepts it as the queued send's answer.
	time.Sleep(20 * time.Millisecond)
	appendTranscript(t, path, assistantLine("inflight-reply", ts(2*time.Second), "IN-FLIGHT TAIL", "end_turn"))
	time.Sleep(20 * time.Millisecond)
	appendTranscript(t, path,
		userLine("queued-user", ts(3*time.Second), "queued question"),
		assistantLine("queued-reply", ts(4*time.Second), "QUEUED REPLY", "end_turn"),
	)

	got := <-done
	if got.err != nil {
		t.Fatalf("queued send: %v", got.err)
	}
	if got.resp.Content != "QUEUED REPLY" {
		t.Fatalf("queued send returned %q, want the queued turn's reply", got.resp.Content)
	}
}

// TestIssue1978_IdentityRejectsOlderIdenticalPromptBeforeSentAt covers the
// fallback where the transcript path only becomes known after the send, so
// the search starts at offset 0. Heartbeats and nudges resend identical text;
// a record from before this send must never be adopted as its identity.
func TestIssue1978_IdentityRejectsOlderIdenticalPromptBeforeSentAt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	base := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	ts := func(d time.Duration) string { return base.Add(d).Format(time.RFC3339Nano) }
	appendTranscript(t, path,
		userLine("old-heartbeat", ts(-time.Minute), "heartbeat"),
		assistantLine("old-reply", ts(-50*time.Second), "OLD", "end_turn"),
		userLine("new-heartbeat", ts(time.Second), "heartbeat"),
	)
	id, err := AwaitTurnIdentity(TurnQuery{Path: path, Prompt: "heartbeat", Cursor: 0, NotBefore: base.Add(-2 * time.Second)}, time.Second, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if id.UUID != "new-heartbeat" {
		t.Fatalf("bound to %q, want the record written after the send", id.UUID)
	}
}

// TestIssue1978_IdentityMatchesTrimmedPrompt: Claude stores the composer text
// as submitted, so surrounding whitespace and CRLF from the transport must not
// prevent a send from finding its own record.
func TestIssue1978_IdentityMatchesTrimmedPrompt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	appendTranscript(t, path, userLine("mine", "", "line one\nline two "))
	id, err := AwaitTurnIdentity(TurnQuery{Path: path, Prompt: "line one\r\nline two\n", Cursor: 0}, time.Second, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if id.UUID != "mine" {
		t.Fatalf("bound to %q", id.UUID)
	}
}

// TestIssue1978_ResponseIsBoundedByDeadlineAndReportsIncomplete: a turn that
// has produced text but no end_turn by the deadline is returned as incomplete,
// never blocked past the caller's budget and never dressed up as complete.
func TestIssue1978_ResponseIsBoundedByDeadlineAndReportsIncomplete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	prefix := userLine("mine", "", "q") + "\n"
	if err := os.WriteFile(path, []byte(prefix), 0o600); err != nil {
		t.Fatal(err)
	}
	appendTranscript(t, path, assistantLine("partial", "", "so far", "tool_use"))
	id := TurnIdentity{UUID: "mine", Path: path, StartOffset: int64(len(prefix))}
	start := time.Now()
	resp, err := AwaitTurnResponse(id, 50*time.Millisecond, time.Millisecond)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("AwaitTurnResponse ran %s past a 50ms budget", elapsed)
	}
	if !errors.Is(err, ErrTurnResponseIncomplete) {
		t.Fatalf("err = %v, want ErrTurnResponseIncomplete", err)
	}
	if resp == nil || resp.Content != "so far" {
		t.Fatalf("resp = %+v, want the partial text so the caller can surface it", resp)
	}
}

// TestIssue1978_ResponseAcceptsStopSequenceAsTurnEnd mirrors the streamer:
// Claude ends turns with end_turn, stop_sequence or max_tokens.
func TestIssue1978_ResponseAcceptsStopSequenceAsTurnEnd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	prefix := userLine("mine", "", "q") + "\n"
	if err := os.WriteFile(path, []byte(prefix), 0o600); err != nil {
		t.Fatal(err)
	}
	appendTranscript(t, path, assistantLine("r", "", "done", "stop_sequence"))
	id := TurnIdentity{UUID: "mine", Path: path, StartOffset: int64(len(prefix))}
	resp, err := AwaitTurnResponse(id, time.Second, time.Millisecond)
	if err != nil || resp.Content != "done" {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
}

// TestIssue1978_ResponseSkipsToolResultUserRecords: tool_result user records
// belong to this turn and must not be mistaken for the next human prompt.
func TestIssue1978_ResponseSkipsToolResultUserRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	prefix := userLine("mine", "", "q") + "\n"
	if err := os.WriteFile(path, []byte(prefix), 0o600); err != nil {
		t.Fatal(err)
	}
	appendTranscript(t, path,
		`{"type":"assistant","uuid":"a1","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{}}],"stop_reason":"tool_use"}}`,
		`{"type":"user","uuid":"u-tool","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}`,
		assistantLine("a2", "", "final", "end_turn"),
	)
	id := TurnIdentity{UUID: "mine", Path: path, StartOffset: int64(len(prefix))}
	resp, err := AwaitTurnResponse(id, time.Second, time.Millisecond)
	if err != nil || !strings.Contains(resp.Content, "final") {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
}
