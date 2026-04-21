package session

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// writeJSONL writes the given records as JSONL to path, flushing fsync so a
// tailing reader sees the bytes immediately.
func writeJSONL(t *testing.T, path string, records []string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	for _, r := range records {
		if _, err := f.WriteString(r + "\n"); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	if err := f.Sync(); err != nil {
		t.Fatalf("sync %s: %v", path, err)
	}
}

// collectStreamEvents reads all JSONL events that the streamer wrote to buf,
// returning them in order.
func collectStreamEvents(t *testing.T, buf *bytes.Buffer) []StreamEvent {
	t.Helper()
	var events []StreamEvent
	sc := bufio.NewScanner(bytes.NewReader(buf.Bytes()))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev StreamEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			t.Fatalf("parse event %q: %v", string(line), err)
		}
		events = append(events, ev)
	}
	return events
}

// streamInBackground starts StreamTranscript in a goroutine with the given
// config, returning a cancel fn, a buffer, and a done channel that closes
// with the final error.
func streamInBackground(t *testing.T, path, sessionID string, sentAt time.Time, cfg StreamConfig) (context.CancelFunc, *safeBuf, chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	buf := &safeBuf{}
	done := make(chan error, 1)
	go func() {
		done <- StreamTranscript(ctx, path, sessionID, sentAt, buf, cfg)
	}()
	return cancel, buf, done
}

// safeBuf is a goroutine-safe bytes.Buffer wrapper.
type safeBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuf) Snapshot() *bytes.Buffer {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := bytes.NewBuffer(nil)
	out.Write(b.buf.Bytes())
	return out
}

// waitForEvents polls buf until it contains at least n events, or the deadline
// hits. Returns the events collected.
func waitForEvents(t *testing.T, buf *safeBuf, n int, timeout time.Duration) []StreamEvent {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		events := collectStreamEvents(t, buf.Snapshot())
		if len(events) >= n {
			return events
		}
		if time.Now().After(deadline) {
			return events
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestStreamConfig_Defaults verifies the approved defaults: 10s idle, 4000
// chars, 3 tools. These are load-bearing for the #689 streaming contract.
func TestStreamConfig_Defaults(t *testing.T) {
	cfg := StreamConfig{}.WithDefaults()

	if cfg.IdleTimeout != 10*time.Second {
		t.Fatalf("default IdleTimeout = %v, want 10s", cfg.IdleTimeout)
	}
	if cfg.CharBudget != 4000 {
		t.Fatalf("default CharBudget = %d, want 4000", cfg.CharBudget)
	}
	if cfg.ToolBudget != 3 {
		t.Fatalf("default ToolBudget = %d, want 3", cfg.ToolBudget)
	}
}

// TestStreamConfig_Overrides verifies callers can override each knob.
func TestStreamConfig_Overrides(t *testing.T) {
	cfg := StreamConfig{
		IdleTimeout: 2 * time.Second,
		CharBudget:  500,
		ToolBudget:  1,
	}.WithDefaults()

	if cfg.IdleTimeout != 2*time.Second {
		t.Fatalf("IdleTimeout override failed: %v", cfg.IdleTimeout)
	}
	if cfg.CharBudget != 500 {
		t.Fatalf("CharBudget override failed: %d", cfg.CharBudget)
	}
	if cfg.ToolBudget != 1 {
		t.Fatalf("ToolBudget override failed: %d", cfg.ToolBudget)
	}
}

// TestStreamTranscript_EmitsStartWithSchemaVersion verifies the contract that
// the first event is `start` carrying `schema_version`, so downstream
// consumers can branch on schema evolution.
func TestStreamTranscript_EmitsStartWithSchemaVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	writeJSONL(t, path, []string{}) // create empty file

	cancel, buf, done := streamInBackground(t, path, "session-abc", time.Now(), StreamConfig{
		PollInterval: 10 * time.Millisecond,
		IdleTimeout:  200 * time.Millisecond,
	})
	defer cancel()

	// Wait for start event
	events := waitForEvents(t, buf, 1, 2*time.Second)
	cancel()
	<-done

	if len(events) == 0 {
		t.Fatalf("no events emitted")
	}
	start := events[0]
	if start.Type != "start" {
		t.Fatalf("first event type = %q, want start", start.Type)
	}
	if start.SchemaVersion != StreamSchemaVersion {
		t.Fatalf("start.schema_version = %q, want %q", start.SchemaVersion, StreamSchemaVersion)
	}
	if start.SessionID != "session-abc" {
		t.Fatalf("start.session_id = %q, want session-abc", start.SessionID)
	}
}

// TestStreamTranscript_EmitsTextDelta verifies that assistant text blocks
// are emitted as text events carrying message_id + delta.
func TestStreamTranscript_EmitsTextDelta(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")

	// Pre-existing record BEFORE sentAt — must NOT be re-emitted.
	oldTS := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339Nano)
	oldRec := fmt.Sprintf(`{"type":"assistant","timestamp":%q,"uuid":"u-old","message":{"id":"msg-old","role":"assistant","content":[{"type":"text","text":"old chatter"}],"stop_reason":null}}`, oldTS)
	writeJSONL(t, path, []string{oldRec})

	sentAt := time.Now()
	cancel, buf, done := streamInBackground(t, path, "session-abc", sentAt, StreamConfig{
		PollInterval: 10 * time.Millisecond,
		IdleTimeout:  500 * time.Millisecond,
		CharBudget:   1, // flush immediately
	})
	defer cancel()

	// Append a new assistant text block
	newTS := time.Now().UTC().Format(time.RFC3339Nano)
	newRec := fmt.Sprintf(`{"type":"assistant","timestamp":%q,"uuid":"u-1","message":{"id":"msg-1","role":"assistant","content":[{"type":"text","text":"Hello, world!"}],"stop_reason":"end_turn"}}`, newTS)
	writeJSONL(t, path, []string{newRec})

	// Expect start + text + stop
	events := waitForEvents(t, buf, 3, 2*time.Second)
	cancel()
	<-done

	var gotText bool
	for _, e := range events {
		if e.Type == "text" && strings.Contains(e.Delta, "Hello, world!") {
			gotText = true
			if e.MessageID != "msg-1" {
				t.Errorf("text.message_id = %q, want msg-1", e.MessageID)
			}
		}
		if e.Type == "text" && strings.Contains(e.Delta, "old chatter") {
			t.Errorf("streamer replayed record from before sentAt: %+v", e)
		}
	}
	if !gotText {
		t.Fatalf("no text event with expected delta; events=%+v", events)
	}
}

// TestStreamTranscript_EmitsToolUseAndResult verifies tool_use + tool_result
// events are emitted when assistant invokes a tool and the user record
// follows with the result.
func TestStreamTranscript_EmitsToolUseAndResult(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	writeJSONL(t, path, []string{})

	sentAt := time.Now()
	cancel, buf, done := streamInBackground(t, path, "sid", sentAt, StreamConfig{
		PollInterval: 10 * time.Millisecond,
		IdleTimeout:  500 * time.Millisecond,
		ToolBudget:   1, // flush any buffered text on first tool
	})
	defer cancel()

	ts := time.Now().UTC().Format(time.RFC3339Nano)
	toolUse := fmt.Sprintf(`{"type":"assistant","timestamp":%q,"uuid":"u-a","message":{"id":"msg-a","role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls"}}],"stop_reason":"tool_use"}}`, ts)
	toolRes := fmt.Sprintf(`{"type":"user","timestamp":%q,"uuid":"u-b","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"file1\nfile2"}]}}`, ts)
	finalText := fmt.Sprintf(`{"type":"assistant","timestamp":%q,"uuid":"u-c","message":{"id":"msg-c","role":"assistant","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn"}}`, ts)
	writeJSONL(t, path, []string{toolUse, toolRes, finalText})

	events := waitForEvents(t, buf, 4, 2*time.Second)
	cancel()
	<-done

	var gotToolUse, gotToolResult, gotStop bool
	for _, e := range events {
		switch e.Type {
		case "tool_use":
			gotToolUse = true
			if e.ToolUseID != "toolu_1" {
				t.Errorf("tool_use.tool_use_id = %q, want toolu_1", e.ToolUseID)
			}
			if e.Name != "Bash" {
				t.Errorf("tool_use.name = %q, want Bash", e.Name)
			}
		case "tool_result":
			gotToolResult = true
			if e.ToolUseID != "toolu_1" {
				t.Errorf("tool_result.tool_use_id = %q, want toolu_1", e.ToolUseID)
			}
		case "stop":
			gotStop = true
			if e.Reason != "end_turn" {
				t.Errorf("stop.reason = %q, want end_turn", e.Reason)
			}
		}
	}
	if !gotToolUse {
		t.Errorf("no tool_use event; got %+v", events)
	}
	if !gotToolResult {
		t.Errorf("no tool_result event; got %+v", events)
	}
	if !gotStop {
		t.Errorf("no stop event; got %+v", events)
	}
}

// TestStreamTranscript_ReturnsOnStop verifies the streamer exits cleanly
// after emitting a stop event with reason=end_turn.
func TestStreamTranscript_ReturnsOnStop(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	writeJSONL(t, path, []string{})

	sentAt := time.Now()
	buf := &safeBuf{}
	done := make(chan error, 1)
	go func() {
		done <- StreamTranscript(context.Background(), path, "sid", sentAt, buf, StreamConfig{
			PollInterval: 10 * time.Millisecond,
			IdleTimeout:  200 * time.Millisecond,
			CharBudget:   1,
		})
	}()

	ts := time.Now().UTC().Format(time.RFC3339Nano)
	rec := fmt.Sprintf(`{"type":"assistant","timestamp":%q,"uuid":"u1","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"bye"}],"stop_reason":"end_turn"}}`, ts)
	writeJSONL(t, path, []string{rec})

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("StreamTranscript returned error on natural stop: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("StreamTranscript did not return within 3s of stop event")
	}
}

// TestStreamTranscript_IdleTimeoutEmitsError verifies that if the transcript
// goes silent beyond IdleTimeout, the streamer emits an error event and
// returns.
func TestStreamTranscript_IdleTimeoutEmitsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	writeJSONL(t, path, []string{})

	sentAt := time.Now()
	buf := &safeBuf{}
	done := make(chan error, 1)
	go func() {
		done <- StreamTranscript(context.Background(), path, "sid", sentAt, buf, StreamConfig{
			PollInterval: 10 * time.Millisecond,
			IdleTimeout:  150 * time.Millisecond,
		})
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("StreamTranscript returned nil on idle timeout; want non-nil error")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("StreamTranscript did not return after idle timeout")
	}

	events := collectStreamEvents(t, buf.Snapshot())
	var hasError bool
	for _, e := range events {
		if e.Type == "error" {
			hasError = true
			break
		}
	}
	if !hasError {
		t.Fatalf("no error event emitted on idle timeout; events=%+v", events)
	}
}

// TestStreamTranscript_CharBudgetFlushes verifies that buffered text is
// flushed once the char budget is crossed, even without a stop boundary.
func TestStreamTranscript_CharBudgetFlushes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	writeJSONL(t, path, []string{})

	sentAt := time.Now()
	cancel, buf, done := streamInBackground(t, path, "sid", sentAt, StreamConfig{
		PollInterval: 10 * time.Millisecond,
		IdleTimeout:  5 * time.Second, // large — so only char budget can flush
		CharBudget:   20,
	})
	defer cancel()

	big := strings.Repeat("x", 100)
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	rec := fmt.Sprintf(`{"type":"assistant","timestamp":%q,"uuid":"u-big","message":{"id":"m-big","role":"assistant","content":[{"type":"text","text":%q}],"stop_reason":null}}`, ts, big)
	writeJSONL(t, path, []string{rec})

	// Expect start + text flush (without stop event yet — stop_reason was null)
	events := waitForEvents(t, buf, 2, 2*time.Second)
	cancel()
	<-done

	var gotText bool
	for _, e := range events {
		if e.Type == "text" && len(e.Delta) > 0 {
			gotText = true
		}
	}
	if !gotText {
		t.Fatalf("char-budget flush did not emit text event: %+v", events)
	}
}

// TestStreamTranscript_ContextCancelReturns verifies that the streamer
// returns promptly when the parent context is cancelled.
func TestStreamTranscript_ContextCancelReturns(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	writeJSONL(t, path, []string{})

	ctx, cancel := context.WithCancel(context.Background())
	buf := &safeBuf{}
	done := make(chan error, 1)
	go func() {
		done <- StreamTranscript(ctx, path, "sid", time.Now(), buf, StreamConfig{
			PollInterval: 10 * time.Millisecond,
			IdleTimeout:  30 * time.Second, // would never fire
		})
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// ok
	case <-time.After(1 * time.Second):
		t.Fatalf("StreamTranscript did not return after context cancel")
	}
}
