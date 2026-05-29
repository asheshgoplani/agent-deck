package main

// Audit B7 — `agent-deck inbox drain` must resolve the caller's OWN session id
// robustly in worktree/sandbox/cron contexts, where there is no tmux to query.
// The conductor template runs this as heartbeat step 1, so it must work via the
// AGENTDECK_INSTANCE_ID env var (always injected into managed sessions), with
// tmux only as a fallback. Both the bare form (`drain`) and the explicit `self`
// keyword resolve to the env id.

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestB7_InboxDrainSelf_ResolvesFromInstanceEnv(t *testing.T) {
	cliInboxTestHome(t)
	self := "conductor-self-1777800000"
	t.Setenv("AGENTDECK_INSTANCE_ID", self)

	if err := session.CommitToInbox(self, session.TransitionNotificationEvent{
		ChildSessionID: "child-self", ChildTitle: "worker", Profile: "personal",
		FromStatus: "running", ToStatus: "waiting", Timestamp: time.Now(),
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Explicit "self" keyword.
	var buf bytes.Buffer
	if err := runInbox(&buf, []string{"drain", "self"}); err != nil {
		t.Fatalf("inbox drain self: %v", err)
	}
	if !strings.Contains(buf.String(), "child-self") {
		t.Fatalf("`drain self` did not resolve via AGENTDECK_INSTANCE_ID:\n%s", buf.String())
	}

	// Re-commit and try the BARE form (no argument) — same resolution.
	if err := session.CommitToInbox(self, session.TransitionNotificationEvent{
		ChildSessionID: "child-self-2", ChildTitle: "worker", Profile: "personal",
		FromStatus: "running", ToStatus: "waiting", Timestamp: time.Now(),
	}); err != nil {
		t.Fatalf("commit2: %v", err)
	}
	buf.Reset()
	if err := runInbox(&buf, []string{"drain"}); err != nil {
		t.Fatalf("inbox drain (bare): %v", err)
	}
	if !strings.Contains(buf.String(), "child-self-2") {
		t.Fatalf("bare `drain` did not resolve self via env:\n%s", buf.String())
	}
}

func TestB7_InboxDrainSelf_NoContextIsAClearError(t *testing.T) {
	cliInboxTestHome(t)
	t.Setenv("AGENTDECK_INSTANCE_ID", "")
	t.Setenv("AGENT_DECK_SESSION_ID", "")
	t.Setenv("TMUX", "")

	var buf bytes.Buffer
	err := runInbox(&buf, []string{"drain", "self"})
	if err == nil {
		t.Fatalf("expected a clear error when self cannot be resolved")
	}
	if !strings.Contains(err.Error(), "session") {
		t.Fatalf("error should explain the missing session context, got: %v", err)
	}
}
