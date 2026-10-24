package main

import (
	"strings"
	"testing"
)

// The first thing a cold run meets must not be a hex id the user never chose.
//
// With no argument the command adopts the session it is running inside
// ($AGENTDECK_INSTANCE_ID, else the owner of the tmux session). When the old
// id-from-tmux-name lookup failed it reported
// `session "72aad8b2" not found` — an identifier the reader never typed, cannot
// look up and did not know existed, which reads as a broken tool rather than an
// unaimed one.
func TestContextResolutionHintExplainsWhatItTried(t *testing.T) {
	// Outside tmux there is nothing to auto-detect, and the message has to say
	// that rather than blame a session.
	t.Setenv("TMUX", "")
	t.Setenv("AGENTDECK_INSTANCE_ID", "")
	got := contextResolutionHint("session '72aad8b2' not found", ErrCodeNotFound, "", "personal")
	if strings.Contains(got, "72aad8b2") {
		t.Fatalf("a message the user cannot act on must not lead with an id they never chose: %q", got)
	}
	for _, want := range []string{"no session was named", "not inside an agent-deck session", "agent-deck list", "agent-deck session context <title>"} {
		if !strings.Contains(got, want) {
			t.Fatalf("hint is missing %q:\n%s", want, got)
		}
	}
}

// A reference the user DID type is theirs, and the message keeps it — replacing
// it would hide the typo they need to see. It still gains the next command, and
// it names the profile searched, because the commonest cause of a miss is
// having looked in the wrong one.
func TestContextResolutionHintKeepsAnExplicitReference(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("AGENTDECK_INSTANCE_ID", "")
	got := contextResolutionHint("session 'my-projekt' not found", ErrCodeNotFound, "my-projekt", "personal")
	if !strings.Contains(got, "my-projekt") {
		t.Fatalf("the reference the user typed must survive: %q", got)
	}
	if !strings.Contains(got, "agent-deck list") {
		t.Fatalf("an unresolved reference must still say what to run next: %q", got)
	}
}

// Inside a session whose id this profile does not know, the hint says it looked
// for the calling session and keeps the resolver's reason, so the id it used is
// the real one ($AGENTDECK_INSTANCE_ID), not a tmux name suffix.
func TestContextResolutionHintInsideAnUnknownSession(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("AGENTDECK_INSTANCE_ID", "deadbeef-1700000000")
	got := contextResolutionHint("session 'deadbeef-1700000000' not found", ErrCodeNotFound, "", "personal")
	for _, want := range []string{"running inside", `profile "personal"`, "deadbeef-1700000000", "agent-deck list"} {
		if !strings.Contains(got, want) {
			t.Fatalf("hint is missing %q:\n%s", want, got)
		}
	}
}

// Only a not-found gets the hint. An ambiguous reference already tells the
// reader exactly what to do, and appending a second instruction to it would
// bury the list of candidates it just printed.
func TestContextResolutionHintLeavesOtherErrorsAlone(t *testing.T) {
	msg := "path '/repo' has multiple sessions:\n  - a (aaaa)\n  - b (bbbb)\nUse title or ID to specify."
	if got := contextResolutionHint(msg, ErrCodeAmbiguous, "/repo", "personal"); got != msg {
		t.Fatalf("an ambiguous reference must be reported verbatim, got %q", got)
	}
}
