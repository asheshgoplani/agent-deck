package session

import (
	"strings"
	"testing"
)

// SPEC T5: the comment carries the tagged payload (artifact title + excerpt +
// comment) verbatim.
func TestFormatArtifactCommentCarriesTaggedPayload(t *testing.T) {
	c := ArtifactComment{
		ArtifactID:    "perf-report",
		ArtifactTitle: "ARD Import Perf — Profiling Report",
		Excerpt:       "one SQL round-trip per tag during resolution, producing ~14,000 queries",
		Comment:       "Batch the tag resolution into a single IN(...) lookup and memoize per import run.",
		Profile:       "personal",
	}
	got := FormatArtifactComment(c)

	// The three human-meaningful parts must appear verbatim.
	for _, want := range []string{c.ArtifactTitle, c.Excerpt, c.Comment} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatted payload missing %q\n--- payload ---\n%s", want, got)
		}
	}
	// Tagged so a downstream skill can recognize and act on it.
	if !strings.Contains(got, "[fleet-console]") {
		t.Fatalf("expected a [fleet-console] tag, got:\n%s", got)
	}
}

// SPEC T4: POST comment to a BUSY session routes through the durable inbox (not
// raw send-keys) — the message is retrievable via inbox drain.
func TestDeliverArtifactCommentBusyRoutesToDurableInbox(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("AGENT_DECK_PROFILE", "")
	ClearUserConfigCache()
	t.Cleanup(func() { ClearUserConfigCache() })

	const target = "sess-busy-123"
	c := ArtifactComment{
		ArtifactID:    "perf-report",
		ArtifactTitle: "ARD Import Perf",
		Excerpt:       "~14,000 queries for a 1,000-row batch",
		Comment:       "Batch into a single IN(...) lookup.",
		Profile:       "_test",
	}

	// busy=true MUST NOT shell out to send-keys (which a running pane would
	// swallow); it commits to the target's durable inbox instead.
	if err := DeliverArtifactComment(target, true, c); err != nil {
		t.Fatalf("DeliverArtifactComment busy: %v", err)
	}

	// The comment is retrievable via the same drain a parent/conductor runs.
	events, err := DrainInboxForParent(target)
	if err != nil {
		t.Fatalf("DrainInboxForParent: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected exactly 1 inbox event, got %d: %+v", len(events), events)
	}
	ev := events[0]
	// Verbatim payload survives the round-trip.
	for _, want := range []string{c.ArtifactTitle, c.Excerpt, c.Comment} {
		if !strings.Contains(ev.DoneSummary, want) {
			t.Fatalf("drained event missing %q in DoneSummary=%q", want, ev.DoneSummary)
		}
	}
	if ev.TargetSessionID != target {
		t.Fatalf("expected target %q, got %q", target, ev.TargetSessionID)
	}
}

// Two comments on the same artifact must BOTH survive the drain — the inbox's
// last-wins-per-child collapse must not silently eat the first annotation.
func TestDeliverArtifactCommentBusyKeepsMultiple(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("AGENT_DECK_PROFILE", "")
	ClearUserConfigCache()
	t.Cleanup(func() { ClearUserConfigCache() })

	const target = "sess-busy-multi"
	base := ArtifactComment{ArtifactID: "doc", ArtifactTitle: "Doc", Profile: "_test"}

	first := base
	first.Excerpt, first.Comment = "passage one", "comment one"
	second := base
	second.Excerpt, second.Comment = "passage two", "comment two"

	if err := DeliverArtifactComment(target, true, first); err != nil {
		t.Fatal(err)
	}
	if err := DeliverArtifactComment(target, true, second); err != nil {
		t.Fatal(err)
	}

	events, err := DrainInboxForParent(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 distinct annotations to survive, got %d: %+v", len(events), events)
	}
}
