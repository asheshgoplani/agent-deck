package session

import (
	"strings"
	"testing"
)

// Issue #1568: after a Claude Code /compact, `session output -q` returned the
// last PRE-compact assistant reply forever. Root cause: the extractor scanned
// the transcript forward with a bufio.Scanner capped at a 1MB line; /compact
// inserts multi-megabyte single-line records (file-history-snapshot, compact
// summaries) mid-file, the oversized line hit bufio.ErrTooLong, Scan() stopped
// silently at the compact boundary, and every post-compact record was never
// seen. The fix anchors at EOF and walks backward with no line-length ceiling.

func issue1568Record(parts ...string) string {
	return "{" + strings.Join(parts, ",") + "}"
}

func issue1568AssistantLine(text, ts, sid string) string {
	return issue1568Record(
		`"type":"assistant"`,
		`"sessionId":"`+sid+`"`,
		`"timestamp":"`+ts+`"`,
		`"message":{"role":"assistant","content":[{"type":"text","text":"`+text+`"}]}`,
	)
}

// TestIssue1568_CompactOversizedMidFileLine reproduces the reported bug: a
// >1MB single-line record between the pre-compact and post-compact assistant
// replies must not truncate extraction at the compact boundary.
func TestIssue1568_CompactOversizedMidFileLine(t *testing.T) {
	huge := strings.Repeat("x", 2*1024*1024) // 2MB single-line snapshot payload
	lines := []string{
		issue1568AssistantLine("stale pre-compact reply", "2026-07-17T10:00:00Z", "sess-1568"),
		issue1568Record(`"type":"file-history-snapshot"`, `"snapshot":"`+huge+`"`),
		issue1568Record(`"type":"user"`, `"isCompactSummary":true`, `"message":{"role":"user","content":"compact summary"}`),
		issue1568Record(`"type":"system"`, `"subtype":"compact_boundary"`),
		issue1568AssistantLine("fresh post-compact reply", "2026-07-17T14:00:00Z", "sess-1568"),
		// Noise after the real reply: sidechain assistant + queue-operation.
		issue1568Record(`"type":"assistant"`, `"isSidechain":true`, `"message":{"role":"assistant","content":[{"type":"text","text":"subagent noise"}]}`),
		issue1568Record(`"type":"queue-operation"`, `"operation":"dequeue"`),
	}
	data := []byte(strings.Join(lines, "\n") + "\n")

	resp, err := parseClaudeLastAssistantMessage(data, "sess-1568.jsonl")
	if err != nil {
		t.Fatalf("parseClaudeLastAssistantMessage: %v", err)
	}
	if resp.Content != "fresh post-compact reply" {
		t.Fatalf("got stale content %q, want %q", resp.Content, "fresh post-compact reply")
	}
	if resp.Timestamp != "2026-07-17T14:00:00Z" {
		t.Fatalf("got timestamp %q, want post-compact timestamp", resp.Timestamp)
	}
	if resp.SessionID != "sess-1568" {
		t.Fatalf("got sessionID %q, want %q", resp.SessionID, "sess-1568")
	}
}

// TestIssue1568_OversizedFinalLine: an oversized record as the very last line
// must not hide the assistant reply just before it.
func TestIssue1568_OversizedFinalLine(t *testing.T) {
	huge := strings.Repeat("y", 2*1024*1024)
	lines := []string{
		issue1568AssistantLine("the real reply", "2026-07-17T15:00:00Z", "sess-1568"),
		issue1568Record(`"type":"file-history-snapshot"`, `"snapshot":"`+huge+`"`),
	}
	data := []byte(strings.Join(lines, "\n") + "\n")

	resp, err := parseClaudeLastAssistantMessage(data, "sess-1568.jsonl")
	if err != nil {
		t.Fatalf("parseClaudeLastAssistantMessage: %v", err)
	}
	if resp.Content != "the real reply" {
		t.Fatalf("got %q, want %q", resp.Content, "the real reply")
	}
}

// TestIssue1568_NoAssistantText keeps the error contract: a transcript with no
// non-sidechain assistant text still errors.
func TestIssue1568_NoAssistantText(t *testing.T) {
	lines := []string{
		issue1568Record(`"type":"user"`, `"message":{"role":"user","content":"hello"}`),
		issue1568Record(`"type":"assistant"`, `"isSidechain":true`, `"message":{"role":"assistant","content":[{"type":"text","text":"sidechain only"}]}`),
		issue1568Record(`"type":"assistant"`, `"message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash"}]}`),
	}
	data := []byte(strings.Join(lines, "\n") + "\n")

	if _, err := parseClaudeLastAssistantMessage(data, "sess-1568.jsonl"); err == nil {
		t.Fatal("expected error for transcript without assistant text, got nil")
	}
}

// TestIssue1568_NoTrailingNewline: the last line must be parsed even without a
// trailing newline (file mid-write).
func TestIssue1568_NoTrailingNewline(t *testing.T) {
	lines := []string{
		issue1568AssistantLine("older", "2026-07-17T09:00:00Z", "sess-1568"),
		issue1568AssistantLine("newest", "2026-07-17T16:00:00Z", "sess-1568"),
	}
	data := []byte(strings.Join(lines, "\n")) // no trailing \n

	resp, err := parseClaudeLastAssistantMessage(data, "sess-1568.jsonl")
	if err != nil {
		t.Fatalf("parseClaudeLastAssistantMessage: %v", err)
	}
	if resp.Content != "newest" {
		t.Fatalf("got %q, want %q", resp.Content, "newest")
	}
}
