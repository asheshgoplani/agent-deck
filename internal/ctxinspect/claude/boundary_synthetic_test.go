package claude

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/ctxinspect"
)

// The synthetic-placeholder fix landed on the pre-block path and stopped there.
// The boundary path — the one taken once the startup injections are on record —
// still treated the first assistant record after them as final, whatever it
// carried. Claude Code writes <synthetic> zero-usage placeholders on any session
// that was interrupted or hit an auth failure, and they land after the burst as
// readily as before it, so the parse died on the placeholder and never reached
// the real records behind it. Four live sessions holding 215 claude-fable-5
// records each were told no model and no measured total were on file, and were
// handed the remedy that unknown implies.
//
// These tests pin the boundary path to the same rule as the pre-block one: a
// record that cannot supply an accounting is not the boundary.

// TestParseHead_SyntheticAfterStartupBlockDoesNotEndTheParse is the regression
// test for the live failure.
func TestParseHead_SyntheticAfterStartupBlockDoesNotEndTheParse(t *testing.T) {
	head, err := ParseHead(writeTranscript(t,
		recSkillListing,
		recSyntheticZeroUsage,
		recSyntheticZeroUsage,
		recRealTurnAfterSynthetic,
	))
	if err != nil {
		t.Fatalf("ParseHead: %v", err)
	}
	if head.FirstTurn == nil {
		t.Fatal("FirstTurn is nil: a synthetic placeholder behind the startup block ended the parse, so the session is reported as never having completed a model turn")
	}
	if got, want := head.FirstTurn.Model, "claude-fable-5"; got != want {
		t.Errorf("FirstTurn.Model = %q, want %q", got, want)
	}
	if got, want := head.FirstTurn.PromptTokens(), 2000; got != want {
		t.Errorf("FirstTurn.PromptTokens() = %d, want %d", got, want)
	}
	if !head.ReachedAssistant {
		t.Error("ReachedAssistant = false: a usable boundary record was found, so the boundary was reached")
	}
	if got, want := head.PostBlockSkipped, 2; got != want {
		t.Errorf("PostBlockSkipped = %d, want %d: the two placeholders that were stepped over must be counted, not silently dropped", got, want)
	}
	if head.PostBlockUndecodable != 0 {
		t.Errorf("PostBlockUndecodable = %d, want 0: a placeholder decodes perfectly well, it simply reports nothing", head.PostBlockUndecodable)
	}
}

// TestParseHead_ModelSurvivesAnUnreadableAccounting covers the half of the
// failure that a later usable record cannot repair: when nothing behind the
// block ever carries an accounting, the model name is still on record and must
// still be reported. It is not an accounting claim.
func TestParseHead_ModelSurvivesAnUnreadableAccounting(t *testing.T) {
	head, err := ParseHead(writeTranscript(t,
		recSkillListing,
		// The real shape: the placeholder names itself "<synthetic>", and the
		// record behind it names the model while still reporting nothing.
		recSyntheticZeroUsage,
		`{"type":"assistant","sessionId":"s1","message":{"model":"claude-fable-5","usage":{"input_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`,
	))
	if err != nil {
		t.Fatalf("ParseHead: %v", err)
	}
	if head.FirstTurn != nil {
		t.Fatal("FirstTurn is non-nil: a zero-token prompt is not a measurement and must never become the anchor")
	}
	if got, want := head.ModelSeen, "claude-fable-5"; got != want {
		t.Errorf("ModelSeen = %q, want %q: the transcript names the model on a record that carries no usage, and losing it costs the window as well as the label", got, want)
	}
	if got := modelOf(ctxinspect.Request{Tool: "claude", Model: "stale-guess"}, head, nil); got != "claude-fable-5" {
		t.Errorf("modelOf = %q, want %q: what the session recorded beats what the caller believed was configured", got, "claude-fable-5")
	}
}

// TestParseHead_UnreadableBoundaryDoesNotBlameTheSession pins the sentence.
// "The startup injections are recorded after the only model turn" is the
// opposite of what happened here, and a wrong reason attached to an honest
// unknown is the failure this whole area exists to prevent.
func TestParseHead_UnreadableBoundaryDoesNotBlameTheSession(t *testing.T) {
	head, err := ParseHead(writeTranscript(t,
		recSkillListing,
		recSyntheticZeroUsage,
	))
	if err != nil {
		t.Fatalf("ParseHead: %v", err)
	}
	for _, s := range head.ResumeSignals {
		if strings.Contains(s, "after the only model turn") || strings.Contains(s, "not on record before the first model turn") {
			t.Errorf("resume signal %q describes the opposite transcript: the injections came FIRST here", s)
		}
	}
	if !strings.Contains(strings.Join(head.Warnings, "\n"), "carry no usable accounting") {
		t.Errorf("no warning names what actually happened; warnings were:\n%s", strings.Join(head.Warnings, "\n"))
	}
	if got, want := head.PostBlockSkipped, 1; got != want {
		t.Errorf("PostBlockSkipped = %d, want %d", got, want)
	}
}

// TestParseHead_UndecodableRecordBehindTheBlockIsCountedApart is the honesty
// half. A placeholder went into no request, so stepping over it costs nothing.
// A record that would not parse might have been a real model turn, in which case
// the turn that follows it also carried its output — and the anchor then covers
// more than the fixed prefix, which the report must say rather than assume away.
func TestParseHead_UndecodableRecordBehindTheBlockIsCountedApart(t *testing.T) {
	head, err := ParseHead(writeTranscript(t,
		recSkillListing,
		`{"type":"assistant","sessionId":"s1","message":"not-an-object"}`,
		recRealTurnAfterSynthetic,
	))
	if err != nil {
		t.Fatalf("ParseHead: %v", err)
	}
	if head.FirstTurn == nil {
		t.Fatal("FirstTurn is nil: an undecodable record must not end the parse either")
	}
	if got, want := head.PostBlockUndecodable, 1; got != want {
		t.Errorf("PostBlockUndecodable = %d, want %d", got, want)
	}
}
