package main

import (
	"testing"
)

// TestLaunch_ToolWithFlags_FoldsExtrasIntoWrapper pins the CLI parse boundary
// of the issue-#601 data flow. The reporter's repro:
//
//	agent-deck launch . -c "codex --dangerously-bypass-approvals-and-sandbox --session-id UUID"
//
// must produce a wrapper string shaped exactly "{command} <extras>" with EVERY
// extra flag folded in. The session-layer test
// TestPrepareCommand_Issue601_ReporterRepro then proves that this exact wrapper
// shape results in flags landing INSIDE the bash -c single-quoted payload.
//
// Keeping assertions at both ends of the data-flow trace means neither layer
// can silently drop flags without a test turning red.
func TestLaunch_ToolWithFlags_FoldsExtrasIntoWrapper(t *testing.T) {
	raw := "codex --dangerously-bypass-approvals-and-sandbox --session-id aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

	tool, command, wrapper, note, _, err := resolveSessionCommand(raw, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if tool != "codex" {
		t.Fatalf("tool = %q, want codex", tool)
	}
	if command == "" {
		t.Fatalf("command should not be empty")
	}

	want := "{command} --dangerously-bypass-approvals-and-sandbox --session-id aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	if wrapper != want {
		t.Fatalf("wrapper did not fold extras correctly.\n  got:  %q\n  want: %q", wrapper, want)
	}
	if note == "" {
		t.Fatalf("expected resolveSessionCommand to emit a note explaining the wrapper fold; got empty")
	}
}

// TestLaunch_ToolWithSessionIdFlag_IsPreserved guards the narrower #601 symptom
// the reporter documented: --session-id silently dropped by bash-c-positional-args,
// breaking deterministic session IDs and JSONL file resolution for claude-compatible
// wrappers.
func TestLaunch_ToolWithSessionIdFlag_IsPreserved(t *testing.T) {
	raw := "my-claude-wrapper --session-id abc-123 --dangerously-skip-permissions"

	_, command, wrapper, _, _, err := resolveSessionCommand(raw, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if command != "my-claude-wrapper" {
		t.Fatalf("command = %q, want %q (base tool only)", command, "my-claude-wrapper")
	}
	want := "{command} --session-id abc-123 --dangerously-skip-permissions"
	if wrapper != want {
		t.Fatalf("wrapper shape dropped --session-id or --dang… flag.\n  got:  %q\n  want: %q", wrapper, want)
	}
}
