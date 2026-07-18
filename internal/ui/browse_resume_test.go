package ui

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// agent-hopdeck: covers the dedupe lookup used by resumeBrowsedSession to
// decide whether a browsed Claude session is already adopted into the fleet.
func TestFindInstanceByClaudeSessionID(t *testing.T) {
	h := &Home{}
	a := session.NewInstanceWithTool("a", "/a", "claude")
	a.ClaudeSessionID = "sess-a"
	b := session.NewInstanceWithTool("b", "/b", "claude")
	b.ClaudeSessionID = "sess-b"
	h.instances = []*session.Instance{a, b}

	if got := h.findInstanceByClaudeSessionID("sess-b"); got != b {
		t.Fatalf("expected instance b, got %v", got)
	}
	if got := h.findInstanceByClaudeSessionID("nope"); got != nil {
		t.Fatalf("expected nil for unknown id, got %v", got)
	}
	if got := h.findInstanceByClaudeSessionID(""); got != nil {
		t.Fatalf("expected nil for empty id, got %v", got)
	}
}
