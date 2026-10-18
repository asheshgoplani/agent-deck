package main

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Follow-ups to #2722, end to end with the fake hermes of
// issue2582_hermes_restart_record_test.go.

func issue2582StartedA(t *testing.T, argsFile string) (*session.Instance, string) {
	t.Helper()
	a := session.NewInstanceWithTool("issue2582-A", t.TempDir(), "hermes")
	a.Command = "hermes"
	if err := a.Start(); err != nil {
		t.Fatalf("start A: %v", err)
	}
	t.Cleanup(func() { _ = a.Kill() })
	issue2582Launches(t, argsFile, 1)
	gen := a.HermesHookGeneration
	issue2582Hook(t, a, gen, "pre_llm_call", issue2582OwnA, "cli")
	issue2582Hook(t, a, gen, "post_llm_call", issue2582OwnA, "cli")
	issue2582Hook(t, a, gen, "on_session_end", issue2582OwnA, "cli")
	return a, gen
}

// Starting a stopped entry resumes its own conversation, as a restart does.
func TestIssue2582HermesStartOfStoppedEntryResumesOwnConversation(t *testing.T) {
	argsFile := issue2582FakeHermes(t)
	a, _ := issue2582StartedA(t, argsFile)
	if err := a.Kill(); err != nil {
		t.Fatalf("stop A: %v", err)
	}
	if err := a.Start(); err != nil {
		t.Fatalf("start A again: %v", err)
	}
	lines := issue2582Launches(t, argsFile, 2)
	if got := lines[1]; !strings.Contains(got, "--resume "+issue2582OwnA) {
		t.Fatalf("start of the stopped entry launched %q, want --resume %s", got, issue2582OwnA)
	}
}

// An event without extra.platform cannot rebind the entry to another id.
func TestIssue2582HermesEventWithoutPlatformDoesNotRebind(t *testing.T) {
	argsFile := issue2582FakeHermes(t)
	a, gen := issue2582StartedA(t, argsFile)
	issue2582Hook(t, a, gen, "pre_llm_call", issue2582ChildA, "") // a forwarder that drops extra
	if err := a.Restart(); err != nil {
		t.Fatalf("restart A: %v", err)
	}
	lines := issue2582Launches(t, argsFile, 2)
	if got := lines[1]; !strings.Contains(got, "--resume "+issue2582OwnA) {
		t.Fatalf("restart launched %q, want --resume %s: a platform-less event rebound the entry", got, issue2582OwnA)
	}
}

// RestartFresh starts a new conversation even though one is recorded.
func TestIssue2582HermesRestartFreshStartsNewConversation(t *testing.T) {
	argsFile := issue2582FakeHermes(t)
	a, _ := issue2582StartedA(t, argsFile)
	if err := a.RestartFresh(); err != nil {
		t.Fatalf("restart fresh: %v", err)
	}
	lines := issue2582Launches(t, argsFile, 2)
	if got := lines[1]; strings.Contains(got, "--resume") {
		t.Fatalf("fresh restart launched %q, want a new conversation", got)
	}
}
