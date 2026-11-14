package main

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Issue #3131: a `launch` from inside a session that links that session as
// the parent is the parent asking its child. The kickoff carries the same
// envelope `session send` uses, so the child's first turn (often the whole
// job) answers the parent and an urgent result wakes it under the existing
// "it asked, so wake it" rule.
func TestIssue3131_LaunchKickoffTag(t *testing.T) {
	parent := &session.Instance{ID: "coord-1", Tool: "claude"}
	shellParent := &session.Instance{ID: "coord-1", Tool: "shell"}
	const msg = "review the diff in ./pr.patch"
	for _, tc := range []struct {
		name    string
		message string
		in      launchKickoffTagInputs
		want    bool
	}{
		{"caller is the linked claude parent", msg, launchKickoffTagInputs{senderID: "coord-1", parent: parent, childTool: "claude", enabled: true, childReportsTurns: true}, true},
		{"tag_sends off or --no-tag", msg, launchKickoffTagInputs{senderID: "coord-1", parent: parent, childTool: "claude", enabled: false, childReportsTurns: true}, false},
		{"human shell (no caller id)", msg, launchKickoffTagInputs{senderID: "", parent: parent, childTool: "claude", enabled: true, childReportsTurns: true}, false},
		{"no parent linked (--no-parent)", msg, launchKickoffTagInputs{senderID: "coord-1", parent: nil, childTool: "claude", enabled: true, childReportsTurns: true}, false},
		{"--parent names another session", msg, launchKickoffTagInputs{senderID: "someone-else", parent: parent, childTool: "claude", enabled: true, childReportsTurns: true}, false},
		{"parent is not claude-compatible", msg, launchKickoffTagInputs{senderID: "coord-1", parent: shellParent, childTool: "claude", enabled: true, childReportsTurns: true}, false},
		{"child is not claude-compatible", msg, launchKickoffTagInputs{senderID: "coord-1", parent: parent, childTool: "codex", enabled: true, childReportsTurns: true}, false},
		// Review M1: a child whose turns are never reported (--no-transition-notify,
		// or [notifications] transition events off) is told by its identity block
		// to answer an enveloped message with `session send`, which would type
		// its whole answer into the parent. No record is sent, so the tag buys
		// no wake either.
		{"child turns not reported (--no-transition-notify)", msg, launchKickoffTagInputs{senderID: "coord-1", parent: parent, childTool: "claude", enabled: true, childReportsTurns: false}, false},
		{"bare slash command", "/review", launchKickoffTagInputs{senderID: "coord-1", parent: parent, childTool: "claude", enabled: true, childReportsTurns: true}, false},
		{"already enveloped", session.SendEnvelope("x") + "\n" + msg, launchKickoffTagInputs{senderID: "coord-1", parent: parent, childTool: "claude", enabled: true, childReportsTurns: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, tagged := tagLaunchKickoff(tc.message, tc.in)
			if tagged != tc.want {
				t.Fatalf("tagged = %v, want %v (message %q)", tagged, tc.want, got)
			}
			if tagged {
				if want := session.SendEnvelope("coord-1") + "\n" + tc.message; got != want {
					t.Fatalf("message = %q, want %q", got, want)
				}
				if from := session.PromptSender(got); from != "coord-1" {
					t.Fatalf("the child's first turn must read as a send from the parent, got sender %q", from)
				}
			} else if got != tc.message {
				t.Fatalf("an untagged kickoff must be unchanged: %q", got)
			}
		})
	}
	if got, tagged := tagLaunchKickoff("  ", launchKickoffTagInputs{senderID: "coord-1", parent: parent, childTool: "claude", enabled: true, childReportsTurns: true}); tagged || strings.TrimSpace(got) != "" {
		t.Fatal("an empty kickoff is never tagged")
	}
}
