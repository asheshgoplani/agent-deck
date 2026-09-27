package main

import (
	"encoding/json"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/sendqueue"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

const issue2397ClaudeID = "7c1f0a52-3d4e-4b8a-9f61-2a5b8c9d0e13"

// Issue #2397: a Claude --json send receipt names the native conversation
// it went to, as show/output already do. Additive only: Codex and other
// tools' receipts are unchanged.
func TestIssue2397_SyncReceiptCarriesClaudeSessionID(t *testing.T) {
	res := sendDeliveryResult{delivery: deliverySubmitted, transport: "tmux"}
	claude := &session.Instance{ID: "child-1", Title: "child", Tool: "claude", ClaudeSessionID: issue2397ClaudeID}
	if got := sendSuccessData(claude, "hi", res, false)["claude_session_id"]; got != issue2397ClaudeID {
		t.Fatalf("claude receipt claude_session_id = %v, want %s", got, issue2397ClaudeID)
	}
	for _, inst := range []*session.Instance{
		{ID: "child-2", Title: "fresh", Tool: "claude"}, // not known yet: omitted, never ""
		{ID: "child-3", Title: "codex", Tool: "codex", ClaudeSessionID: issue2397ClaudeID},
		{ID: "child-4", Title: "shell", Tool: "shell"},
	} {
		if got, ok := sendSuccessData(inst, "hi", res, false)["claude_session_id"]; ok {
			t.Errorf("%s receipt carries claude_session_id=%v, want absent", inst.Title, got)
		}
	}
}

// The default --json send is queued: its receipt and send-status carry the
// conversation too, refined by the delivering child and by the transcript
// the row actually landed in.
func TestIssue2397_QueuedReceiptCarriesClaudeSessionID(t *testing.T) {
	rec := &sendqueue.Record{SendID: "01A", State: sendqueue.StateQueued, Verdict: "queued", Tool: "claude", ClaudeSessionID: issue2397ClaudeID}
	if got := queuedSendFields(rec)["claude_session_id"]; got != issue2397ClaudeID {
		t.Fatalf("queued receipt claude_session_id = %v", got)
	}
	b, _ := json.Marshal(&sendqueue.Record{SendID: "01B", Tool: "codex"})
	var m map[string]interface{}
	_ = json.Unmarshal(b, &m)
	if _, ok := m["claude_session_id"]; ok {
		t.Fatalf("codex send-status record grew claude_session_id: %s", b)
	}

	got := &sendqueue.Record{SendID: "01C", State: sendqueue.StateTyping, Tool: "claude", ClaudeSessionID: "stale-id"}
	set := func(f func(*sendqueue.Record)) error { f(got); return nil }
	applyChildResult(got, map[string]interface{}{"success": true, "delivery": deliverySubmitted, "submitted": true, "claude_session_id": issue2397ClaudeID}, 0, true, set)
	if got.ClaudeSessionID != issue2397ClaudeID {
		t.Fatalf("child result's claude_session_id not recorded: %q", got.ClaudeSessionID)
	}

	if id := claudeSessionIDFromTranscript("claude", "/home/u/.claude/projects/-work-child/"+issue2397ClaudeID+".jsonl"); id != issue2397ClaudeID {
		t.Fatalf("landed transcript session id = %q", id)
	}
	for _, c := range [][2]string{{"codex", "/x/rollout-2026.jsonl"}, {"claude", ""}, {"claude", "/x/notes.txt"}} {
		if id := claudeSessionIDFromTranscript(c[0], c[1]); id != "" {
			t.Errorf("claudeSessionIDFromTranscript(%q, %q) = %q, want empty", c[0], c[1], id)
		}
	}
}
