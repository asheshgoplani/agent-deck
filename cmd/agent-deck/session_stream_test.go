package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestStreamPreconditionError_ClaudeCompatibleIsAllowed verifies every
// Claude-family tool is accepted by the Phase 1 gate.
func TestStreamPreconditionError_ClaudeCompatibleIsAllowed(t *testing.T) {
	for _, tool := range []string{"claude", "claude-code", "claude+"} {
		if session.IsClaudeCompatible(tool) {
			if msg := streamPreconditionError(tool); msg != "" {
				t.Errorf("streamPreconditionError(%q) = %q, want empty", tool, msg)
			}
		}
	}
}

// TestStreamPreconditionError_NonClaudeErrors verifies the CLI refuses
// --stream for non-Claude tools with a stable, informative message.
// Covers the acceptance criterion: "non-Claude tool errors cleanly".
func TestStreamPreconditionError_NonClaudeErrors(t *testing.T) {
	for _, tool := range []string{"codex", "gemini", "aider", "shell"} {
		msg := streamPreconditionError(tool)
		if msg == "" {
			t.Errorf("streamPreconditionError(%q) = empty, want non-empty error", tool)
			continue
		}
		if !strings.Contains(msg, "--stream") || !strings.Contains(msg, tool) {
			t.Errorf("streamPreconditionError(%q) = %q; want message naming the flag and tool", tool, msg)
		}
	}
}

// TestStreamSessionSend_EndToEnd_EmitsEventsOnJSONLTail is the integration
// check for the CLI-side streamer wrapper: given a Claude session whose
// JSONL we control, --stream must emit start + text + stop events and
// return cleanly.
//
// This test bypasses loadSessionData by calling session.StreamTranscript
// directly (streamSessionSend's payload). The full CLI flag wiring above
// is exercised via streamPreconditionError tests + the streamer tests in
// internal/session/transcript_streamer_test.go.
func TestStreamSessionSend_EndToEnd_EmitsEventsOnJSONLTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stream.jsonl")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	sentAt := time.Now()
	var buf bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Write records in a goroutine so the streamer is already tailing.
	go func() {
		time.Sleep(50 * time.Millisecond)
		ts := time.Now().UTC().Format(time.RFC3339Nano)
		recs := []string{
			fmt.Sprintf(`{"type":"assistant","timestamp":%q,"uuid":"u-1","message":{"id":"msg-1","role":"assistant","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn"}}`, ts),
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return
		}
		defer f.Close()
		for _, r := range recs {
			_, _ = f.WriteString(r + "\n")
		}
		_ = f.Sync()
	}()

	err := session.StreamTranscript(ctx, path, "test-session", sentAt, &buf, session.StreamConfig{
		PollInterval: 10 * time.Millisecond,
		IdleTimeout:  2 * time.Second,
		CharBudget:   1,
	})
	if err != nil {
		t.Fatalf("StreamTranscript returned error: %v", err)
	}

	var events []session.StreamEvent
	for _, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		if line == "" {
			continue
		}
		var ev session.StreamEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("parse event %q: %v", line, err)
		}
		events = append(events, ev)
	}

	if len(events) < 3 {
		t.Fatalf("want at least 3 events (start, text, stop); got %d: %+v", len(events), events)
	}
	if events[0].Type != "start" || events[0].SchemaVersion != session.StreamSchemaVersion {
		t.Fatalf("first event not a schema-versioned start: %+v", events[0])
	}
	var gotText, gotStop bool
	for _, e := range events {
		if e.Type == "text" && e.Delta == "hi" {
			gotText = true
		}
		if e.Type == "stop" && e.Reason == "end_turn" {
			gotStop = true
		}
	}
	if !gotText {
		t.Errorf("no text event with expected delta; events=%+v", events)
	}
	if !gotStop {
		t.Errorf("no stop event with reason=end_turn; events=%+v", events)
	}
}
