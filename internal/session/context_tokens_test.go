package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCtxTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func assistantLine(input, cacheRead, cacheCreate, output int) string {
	return fmt.Sprintf(`{"type":"assistant","message":{"model":"claude-opus-4-8","usage":{"input_tokens":%d,"output_tokens":%d,"cache_creation_input_tokens":%d,"cache_read_input_tokens":%d}}}`,
		input, output, cacheCreate, cacheRead)
}

func TestCurrentContextTokensFromTail_LastAssistantWins(t *testing.T) {
	path := writeCtxTranscript(t,
		assistantLine(1000, 0, 0, 50),
		`{"type":"user","message":{"content":"hi"}}`,
		assistantLine(2000, 140000, 3000, 75),
	)
	tokens, ok := currentContextTokensFromTail(path)
	if !ok {
		t.Fatal("expected ok")
	}
	if want := 2000 + 140000 + 3000; tokens != want {
		t.Fatalf("tokens = %d, want %d", tokens, want)
	}
}

func TestCurrentContextTokensFromTail_SkipsZeroUsageAndMalformed(t *testing.T) {
	path := writeCtxTranscript(t,
		assistantLine(500, 200, 0, 10),
		`{"type":"assistant","message":{"usage":{"input_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}`,
		`not json at all`,
	)
	tokens, ok := currentContextTokensFromTail(path)
	if !ok {
		t.Fatal("expected ok")
	}
	if want := 700; tokens != want {
		t.Fatalf("tokens = %d, want %d", tokens, want)
	}
}

func TestCurrentContextTokensFromTail_NoAssistant(t *testing.T) {
	path := writeCtxTranscript(t, `{"type":"user","message":{"content":"hi"}}`)
	if _, ok := currentContextTokensFromTail(path); ok {
		t.Fatal("expected !ok for transcript with no assistant usage")
	}
}

func TestCurrentContextTokensFromTail_MissingFile(t *testing.T) {
	if _, ok := currentContextTokensFromTail(filepath.Join(t.TempDir(), "nope.jsonl")); ok {
		t.Fatal("expected !ok for missing file")
	}
}

// The tail window must grow past a giant trailing record (e.g. a multi-MB tool
// result) that pushes the newest assistant line out of the initial window.
func TestCurrentContextTokensFromTail_GrowsPastGiantTrailingLine(t *testing.T) {
	giant := fmt.Sprintf(`{"type":"user","message":{"content":"%s"}}`,
		strings.Repeat("x", contextTailReadInitial+1024))
	path := writeCtxTranscript(t,
		assistantLine(1234, 100, 0, 5),
		giant,
	)
	tokens, ok := currentContextTokensFromTail(path)
	if !ok {
		t.Fatal("expected ok after growing the tail window")
	}
	if want := 1334; tokens != want {
		t.Fatalf("tokens = %d, want %d", tokens, want)
	}
}

func TestCurrentContextTokensForInstance_NoTranscript(t *testing.T) {
	if _, ok := CurrentContextTokensForInstance(nil); ok {
		t.Fatal("nil instance must not resolve")
	}
	if _, ok := CurrentContextTokensForInstance(&Instance{Tool: "codex"}); ok {
		t.Fatal("instance without ClaudeSessionID must not resolve")
	}
}
