package main

import (
	"os"
	"strings"
	"testing"
)

// These owners must synchronously confirm teardown before completing their
// durable intent or reporting success. This deliberately rejects Kill(), whose
// asynchronous return reopened the lifecycle-finalization race.
func TestLifecycleConsumersRequireConfirmedKillAndWait(t *testing.T) {
	for file, minimum := range map[string]int{"session_remove_cmd.go": 3, "session_cmd.go": 1, "worktree_cmd.go": 1, "conductor_cmd.go": 1, "main.go": 1} {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Count(string(raw), ".KillAndWait()"); got < minimum {
			t.Fatalf("%s confirmed lifecycle teardowns=%d, want at least %d", file, got, minimum)
		}
	}
}

// session stop is a short-lived CLI command.  It must not schedule the
// process-tree reaper in a goroutine and then exit, because that leaves
// detached descendants (such as an HLS ffmpeg encoder) alive after the
// session is marked stopped.
func TestSessionStopUsesConfirmedKillAndWait(t *testing.T) {
	raw, err := os.ReadFile("session_cmd.go")
	if err != nil {
		t.Fatal(err)
	}
	body := extractFuncBody(string(raw), "handleSessionStop")
	if body == "" {
		t.Fatal("could not extract handleSessionStop body")
	}
	if !strings.Contains(body, "inst.KillAndWait()") {
		t.Fatal("handleSessionStop must wait for process teardown before reporting success")
	}
}
