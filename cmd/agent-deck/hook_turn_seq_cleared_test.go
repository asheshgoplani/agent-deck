package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Issue #3012 step 2: the TUI removes <id>.json on every attach-return
// (ClearHookStatus). The count must survive that, or a token taken at N reads
// a later N as "no turn" after the count restarted from 0.
func TestIssue3012_TurnSeqSurvivesHookFileRemoval(t *testing.T) {
	hookEventsHome(t)
	const id = "inst-3012-cleared"
	writeHookStatus(id, "waiting", "sess-c", "SessionStart", "")
	writeHookStatus(id, "running", "sess-c", "UserPromptSubmit", "")
	writeHookStatus(id, "waiting", "sess-c", "Stop", "")
	if err := os.Remove(filepath.Join(getHooksDir(), id+".json")); err != nil {
		t.Fatal(err)
	}
	writeHookStatus(id, "waiting", "sess-c", "Notification", "")
	if got := readTurnSeq(t, id); got != 2 {
		t.Fatalf("turn_seq after the hook file was removed = %d, want 2 carried", got)
	}
	if err := os.Remove(filepath.Join(getHooksDir(), id+".json")); err != nil {
		t.Fatal(err)
	}
	writeHookStatus(id, "running", "sess-c", "UserPromptSubmit", "")
	if got := readTurnSeq(t, id); got != 3 {
		t.Fatalf("turn_seq after a removal and a prompt = %d, want 3", got)
	}
}
