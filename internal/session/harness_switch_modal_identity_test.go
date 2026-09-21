package session

import (
	"testing"
	"time"
)

// The execution-time modal guard must survive a status poll tick for the same
// reason the UI snapshot must: Status is liveness, not identity, and a tick in
// the window between confirmation and execution used to fail the switch with
// "session changed while switch was pending".
func TestSwitchModalIdentityIgnoresStatusChange(t *testing.T) {
	started := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	inst := &Instance{
		ID: "source", Tool: "claude", Account: "personal", ProjectPath: "/workspace/repo", Title: "source",
		GroupPath: "group", Command: "claude", Status: StatusRunning, ClaudeSessionID: "claude-id", LastStartedAt: started,
	}
	captured := CaptureSwitchModalIdentity(inst)

	inst.Status = StatusWaiting
	if !captured.Matches(inst) {
		t.Fatal("status poll tick invalidated the pending switch")
	}

	inst.ClaudeSessionID = "other-claude-id"
	if captured.Matches(inst) {
		t.Fatal("pending switch accepted a source whose conversation id changed")
	}
}
