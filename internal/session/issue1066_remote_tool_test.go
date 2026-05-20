// Issue #1066 — Remote sessions render incorrectly: tool field not propagated
// to consumers (counter, web bridge, renderer).
//
// Reporter: @ddorman-dn — four user-visible symptoms:
//   1) Header session counter shows 0 even with remote sessions running.
//   2) Web UI doesn't show remotes at all.
//   3) Remote claude-session render falls back to generic placeholder text.
//   4) Cost/usage doesn't update from remote sessions.
//
// Hypothesis: remote sessions have a separate code path from local. The
// `Tool` field (probed locally) doesn't propagate over the remote control
// channel, so renderer falls back to generic, counter doesn't group by tool,
// web UI doesn't key panels on tool, cost/usage doesn't fetch.
//
// This file owns the structural invariants enforced by the fix:
//   - RemoteSessionInfo carries Tool through JSON round-trip (the wire
//     contract used by `agent-deck list --json` on the remote → SSHRunner
//     in the local). If this regresses to "" the renderer drops to generic
//     for ALL remote claude sessions.
//   - RemoteSessionInfo.Tool is the only place downstream consumers should
//     read tool from for a remote — there is no second-pass probe.

package session

import (
	"encoding/json"
	"testing"
)

// TestIssue1066_RemoteSessionInfo_ToolField_JSONRoundTrip asserts that the
// `tool` JSON key emitted by remote `agent-deck list --json` decodes back
// into RemoteSessionInfo.Tool. If this regresses, every downstream consumer
// (counter, renderer, web UI, cost/usage) falls back to "generic" for
// remote claude sessions because Tool is the keying field for tool-specific
// behavior.
func TestIssue1066_RemoteSessionInfo_ToolField_JSONRoundTrip(t *testing.T) {
	// Wire format produced by `agent-deck list --json` on the remote
	// (cmd/agent-deck/main.go ~line 1785: `Tool string json:"tool"`).
	wire := []byte(`[
		{
			"id":     "abc123",
			"title":  "demo-session",
			"path":   "/home/user/project",
			"group":  "my-sessions",
			"tool":   "claude",
			"status": "running",
			"created_at": "2026-01-01T00:00:00Z"
		}
	]`)

	var sessions []RemoteSessionInfo
	if err := json.Unmarshal(wire, &sessions); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("len(sessions) = %d, want 1", len(sessions))
	}

	got := sessions[0]
	if got.Tool != "claude" {
		t.Fatalf("Tool = %q, want %q — remote tool field MUST propagate to RemoteSessionInfo.Tool", got.Tool, "claude")
	}
	if got.ID != "abc123" {
		t.Fatalf("ID = %q, want abc123", got.ID)
	}
	if got.Status != "running" {
		t.Fatalf("Status = %q, want running", got.Status)
	}
}

// TestIssue1066_RemoteSessionInfo_AllToolsRoundTrip asserts the field is
// not silently coerced for any common tool name. If a future refactor
// switches to an enum, this test catches the regression.
func TestIssue1066_RemoteSessionInfo_AllToolsRoundTrip(t *testing.T) {
	for _, tool := range []string{"claude", "gemini", "codex", "opencode", "shell", "copilot"} {
		t.Run(tool, func(t *testing.T) {
			payload := `[{"id":"x","title":"t","tool":"` + tool + `","status":"running","created_at":"2026-01-01T00:00:00Z"}]`
			var sessions []RemoteSessionInfo
			if err := json.Unmarshal([]byte(payload), &sessions); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if sessions[0].Tool != tool {
				t.Fatalf("Tool = %q, want %q", sessions[0].Tool, tool)
			}
		})
	}
}
