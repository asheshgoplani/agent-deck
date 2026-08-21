package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestIssue1991_InboxDrainUnknownIDIsNotEmpty(t *testing.T) {
	cliInboxTestHome(t)
	const missing = "does-not-exist-1991"

	var stdout bytes.Buffer
	err := runInbox(&stdout, []string{"drain", missing})
	if err == nil {
		t.Fatal("unknown drain target returned success")
	}
	if got := inboxExitCode(err); got != 2 {
		t.Fatalf("exit code = %d, want 2 (not found)", got)
	}
	if !strings.Contains(err.Error(), missing) {
		t.Fatalf("error %q does not name unresolvable id %q", err, missing)
	}
	if strings.Contains(stdout.String(), "No pending events") {
		t.Fatalf("unknown target was reported as an empty inbox: %q", stdout.String())
	}
}
