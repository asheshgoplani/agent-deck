package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestHandleInbox_PrintsAndTruncates is the contract for the
// `agent-deck inbox <session>` CLI surface added for issue #805. The command
// must:
//   - print pending events to stdout in human-readable form
//   - truncate the inbox file so the next invocation is empty
//   - print "no pending events" when the inbox is empty (idempotent)
//
// The conductor's TUI/skill is the primary consumer; printing is also
// useful for ops debugging when we suspect the in-process retry exhausted.
func TestHandleInbox_PrintsAndTruncates(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("AGENT_DECK_PROFILE", "")
	session.ClearUserConfigCache()
	t.Cleanup(func() { session.ClearUserConfigCache() })
	if err := os.MkdirAll(home+"/.agent-deck", 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	parent := "parent-cli-inbox"
	ev := session.TransitionNotificationEvent{
		ChildSessionID:  "child-cli-x",
		ChildTitle:      "worker",
		Profile:         "_test",
		FromStatus:      "running",
		ToStatus:        "waiting",
		Timestamp:       time.Now(),
		TargetSessionID: parent,
		TargetKind:      "parent",
	}
	if err := session.WriteInboxEvent(parent, ev); err != nil {
		t.Fatalf("seed inbox: %v", err)
	}

	var stdout bytes.Buffer
	if err := runInbox(&stdout, []string{parent}); err != nil {
		t.Fatalf("runInbox: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "child-cli-x") {
		t.Fatalf("expected child id in output, got %q", out)
	}
	if !strings.Contains(out, "running") || !strings.Contains(out, "waiting") {
		t.Fatalf("expected transition pair in output, got %q", out)
	}

	// Second call must hit truncated state and print the empty marker.
	stdout.Reset()
	if err := runInbox(&stdout, []string{parent}); err != nil {
		t.Fatalf("runInbox (second): %v", err)
	}
	out = stdout.String()
	if !strings.Contains(strings.ToLower(out), "no pending events") {
		t.Fatalf("expected 'no pending events' on empty inbox, got %q", out)
	}
}
