package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// The legacy `inbox <target>` form used to read the raw argument as a file name,
// so a title, a short id or `self` printed "No pending events." while records
// were pending, and an unknown name looked like an empty inbox.
func TestInboxLegacyForm_ResolvesTargetLikeDrain(t *testing.T) {
	for _, tc := range []struct {
		name string
		arg  func(id string) string
	}{
		{"title", func(string) string { return "inbox-target" }},
		{"short id", func(id string) string { return id[:8] }},
		{"self", func(string) string { return "self" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cliInboxTestHome(t)
			const id = "abcdef12-1794000000"
			registerInboxDrainTarget(t, id)
			t.Setenv("AGENTDECK_INSTANCE_ID", id)
			if err := session.CommitToInbox(id, session.TransitionNotificationEvent{
				ChildSessionID: "child-legacy-x", ChildTitle: "w", Profile: "personal",
				FromStatus: "running", ToStatus: "waiting", Timestamp: time.Now(),
			}); err != nil {
				t.Fatalf("commit: %v", err)
			}
			var buf bytes.Buffer
			if err := runInbox(&buf, []string{tc.arg(id)}); err != nil {
				t.Fatalf("legacy inbox: %v", err)
			}
			if !strings.Contains(buf.String(), "child-legacy-x") {
				t.Fatalf("pending record not shown for %s, got: %q", tc.name, buf.String())
			}
		})
	}
}

func TestInboxLegacyForm_UnknownTargetIsAnErrorNotEmpty(t *testing.T) {
	cliInboxTestHome(t)
	var buf bytes.Buffer
	err := runInbox(&buf, []string{"nosuch-legacy"})
	if err == nil {
		t.Fatalf("unknown target succeeded, output: %q", buf.String())
	}
	if got := inboxExitCode(err); got != 2 {
		t.Fatalf("exit code = %d, want 2", got)
	}
	if strings.Contains(buf.String(), "No pending events") {
		t.Fatalf("unknown target reported as empty inbox: %q", buf.String())
	}
}
