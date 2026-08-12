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
