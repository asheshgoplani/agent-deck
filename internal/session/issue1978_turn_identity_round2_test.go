package session

import (
	"bytes"
	"context"
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
		id, err := AwaitTurnIdentity(TurnQuery{Path: path, Prompt: "queued question", Cursor: cursor, NotBefore: sentAt}, 3*time.Second, time.Millisecond)
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
	id, err := AwaitTurnIdentity(TurnQuery{Path: path, Prompt: "heartbeat", Cursor: 0, NotBefore: base}, time.Second, time.Millisecond)
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

// --- Codex review of PR #2273 (dc54fdef) -----------------------------------

// TestIssue1978_IdentityGuardRejectsRecordInsideOldToleranceWindow: with the
// transcript path resolved only after the send (cursor 0), an identical prompt
// written ONE SECOND before sentAt used to pass a two-second tolerance and be
// adopted, returning an older reply. The guard is now exact: at or after
// NotBefore, or not this turn.
func TestIssue1978_IdentityGuardRejectsRecordInsideOldToleranceWindow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	base := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	ts := func(d time.Duration) string { return base.Add(d).Format(time.RFC3339Nano) }
	appendTranscript(t, path,
		userLine("one-second-old", ts(-time.Second), "heartbeat"),
		assistantLine("old-reply", ts(-500*time.Millisecond), "OLD", "end_turn"),
		userLine("ours", ts(300*time.Millisecond), "heartbeat"),
	)
	id, err := AwaitTurnIdentity(TurnQuery{Path: path, Prompt: "heartbeat", NotBefore: base}, time.Second, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if id.UUID != "ours" {
		t.Fatalf("bound to %q, want the record at or after sentAt", id.UUID)
	}
}

// TestIssue1978_IdentityGuardRejectsMissingOrMalformedTimestamp: under the
// NotBefore guard a record with no usable timestamp is not "not old", it is
// unverifiable, and must not be adopted from offset 0.
func TestIssue1978_IdentityGuardRejectsMissingOrMalformedTimestamp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	base := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	appendTranscript(t, path,
		userLine("no-ts", "", "heartbeat"),
		userLine("bad-ts", "yesterday", "heartbeat"),
	)
	if id, err := AwaitTurnIdentity(TurnQuery{Path: path, Prompt: "heartbeat", NotBefore: base}, 50*time.Millisecond, time.Millisecond); err == nil {
		t.Fatalf("adopted %q despite no verifiable timestamp", id.UUID)
	}
	// Without the guard (cursor known) timestamps are not required.
	if _, err := AwaitTurnIdentity(TurnQuery{Path: path, Prompt: "heartbeat"}, time.Second, time.Millisecond); err != nil {
		t.Fatalf("cursor-guarded query must accept an untimestamped record: %v", err)
	}
}

// TestIssue1978_ScanDoesNotConsumePartialTrailingRecord proves the partial
// record handling deterministically: one scan over a file whose last line has
// no newline must report not-found and hand back a cursor BEFORE that line,
// so the next scan re-reads it once it is complete. No sleeps, no goroutine.
func TestIssue1978_ScanDoesNotConsumePartialTrailingRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	complete := userLine("other", "", "something else") + "\n"
	partial := `{"type":"user","uuid":"mine","message":{"role":"user","content":"mine"}`
	if err := os.WriteFile(path, []byte(complete+partial), 0o600); err != nil {
		t.Fatal(err)
	}
	q := TurnQuery{Path: path, Prompt: "mine"}
	_, next, found, err := scanTurnIdentity(q, 0)
	if err != nil || found {
		t.Fatalf("found=%v err=%v on a partial record", found, err)
	}
	if next != int64(len(complete)) {
		t.Fatalf("cursor advanced to %d, want %d (before the partial line)", next, len(complete))
	}
	appendTranscript(t, path, "}")
	id, _, found, err := scanTurnIdentity(q, next)
	if err != nil || !found || id.UUID != "mine" {
		t.Fatalf("after completion: found=%v id=%+v err=%v", found, id, err)
	}
	if id.StartOffset != int64(len(complete)+len(partial)+2) {
		t.Fatalf("StartOffset=%d, want end of the completed record", id.StartOffset)
	}
}

// TestIssue1978_TurnAdvancedIsASingleScan: the verification loop's
// authoritative submission signal — this send's record exists after the
// pre-send cursor — without waiting.
func TestIssue1978_TurnAdvancedIsASingleScan(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	appendTranscript(t, path, userLine("old", "", "ping"))
	cursor, _ := TranscriptCursor(path)
	q := TurnQuery{Path: path, Prompt: "ping", Cursor: cursor}
	if TurnAdvanced(q) {
		t.Fatal("advanced before the record exists (an older identical prompt was counted)")
	}
	appendTranscript(t, path, userLine("new", "", "ping"))
	if !TurnAdvanced(q) {
		t.Fatal("record after the cursor not detected")
	}
}

// TestIssue1978_TurnScopedStreamEndsAtInterruption: the requested turn is
// interrupted (a later human prompt lands before end_turn). The stream must
// stop with an error event at that boundary, never stream the next turn's
// answer as this one's completion.
func TestIssue1978_TurnScopedStreamEndsAtInterruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	prefix := userLine("mine", "", "mine") + "\n"
	if err := os.WriteFile(path, []byte(prefix), 0o600); err != nil {
		t.Fatal(err)
	}
	appendTranscript(t, path,
		assistantLine("partial", "", "started answering", "tool_use"),
		userLine("next-prompt", "", "never mind, do this instead"),
		assistantLine("next-reply", "", "NEXT TURN ANSWER", "end_turn"),
	)
	id := TurnIdentity{UUID: "mine", Path: path, StartOffset: int64(len(prefix))}
	var out bytes.Buffer
	err := StreamTranscriptForTurn(context.Background(), id, "sid", &out, StreamConfig{
		PollInterval: time.Millisecond, IdleTimeout: time.Second, CharBudget: 1024, ToolBudget: 10,
	})
	if !errors.Is(err, ErrStreamTurnInterrupted) {
		t.Fatalf("err = %v, want ErrStreamTurnInterrupted", err)
	}
	got := out.String()
	if strings.Contains(got, "NEXT TURN ANSWER") {
		t.Fatalf("streamed the next turn's answer:\n%s", got)
	}
	if !strings.Contains(got, `"type":"error"`) || strings.Contains(got, `"type":"stop"`) {
		t.Fatalf("want an error event and no stop event at the boundary:\n%s", got)
	}
}

// TestIssue1978_TruncatedTranscriptRefusesInsteadOfReplaying (CodeRabbit on
// #2273): a transcript that shrank below the pre-send cursor has lost the
// turn boundary. Rescanning from offset 0 would replay an older identical
// prompt as this send's identity, and a turn-scoped stream would replay
// earlier turns. Both refuse with ErrTranscriptTruncated.
func TestIssue1978_TruncatedTranscriptRefusesInsteadOfReplaying(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	appendTranscript(t, path,
		userLine("older-identical", "", "ping"),
		assistantLine("older-reply", "", "OLD ANSWER", "end_turn"),
	)
	cursor, _ := TranscriptCursor(path)
	// Truncate below the cursor, leaving only the older identical prompt.
	if err := os.WriteFile(path, []byte(userLine("older-identical", "", "ping")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := AwaitTurnIdentity(TurnQuery{Path: path, Prompt: "ping", Cursor: cursor}, 50*time.Millisecond, time.Millisecond)
	if !errors.Is(err, ErrTranscriptTruncated) {
		t.Fatalf("identity err = %v, want ErrTranscriptTruncated (bound to the pre-send prompt?)", err)
	}

	id := TurnIdentity{UUID: "mine", Path: path, StartOffset: cursor}
	if _, err := AwaitTurnResponse(id, 50*time.Millisecond, time.Millisecond); !errors.Is(err, ErrTranscriptTruncated) {
		t.Fatalf("response err = %v, want ErrTranscriptTruncated", err)
	}

	var out bytes.Buffer
	err = StreamTranscriptForTurn(context.Background(), id, "sid", &out, StreamConfig{
		PollInterval: time.Millisecond, IdleTimeout: time.Second, CharBudget: 1024, ToolBudget: 10,
	})
	if !errors.Is(err, ErrTranscriptTruncated) {
		t.Fatalf("stream err = %v, want ErrTranscriptTruncated", err)
	}
	if strings.Contains(out.String(), "OLD ANSWER") || !strings.Contains(out.String(), `"type":"error"`) {
		t.Fatalf("stream replayed earlier turns or emitted no error event:\n%s", out.String())
	}
}
