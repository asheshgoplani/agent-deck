package send

import "testing"

// Regression tests for issue #2079: `launch --message-file` (and any other
// multi-line send) can deliver only the tail of a prompt when a slow-mounting
// composer swallows the leading bytes of a paste — and every downstream
// signal reads identically to a clean delivery, because nothing compares what
// landed against what was sent.
//
// Claude's composer collapses a framed multi-line paste behind
// "[Pasted text #N +M lines]" whether the paste arrived whole or was cut
// short: a truncated paste still produces a well-formed marker, just one
// declaring fewer lines than the message actually has. ExpectedPasteMarkerLines
// and PasteMarkerLineCounts are the two halves of that comparison.

func TestExpectedPasteMarkerLines_SingleLineMessageHasNoMarker(t *testing.T) {
	if got := ExpectedPasteMarkerLines("just one line"); got != 0 {
		t.Fatalf("single-line message: want 0 (never collapses behind a marker), got %d", got)
	}
}

func TestExpectedPasteMarkerLines_CountsPhysicalLines(t *testing.T) {
	// Matches the fixture pairing in issue #1855's own regression test
	// (cmd/agent-deck/issue1855_arrival_paste_marker_test.go): a two
	// physical-line message collapses behind a "+2 lines" marker.
	msg := "/superpowers:writing-skills\n" +
		"Write a skill that writes cupcake flavors for seeded data instead of Lorem Ipsum."
	if got := ExpectedPasteMarkerLines(msg); got != 2 {
		t.Fatalf("want 2 physical lines, got %d", got)
	}
}

func TestExpectedPasteMarkerLines_LongPromptFile(t *testing.T) {
	// The issue's own reproduction: a --message-file prompt of several
	// physical lines.
	msg := "line one\nline two\nline three\nline four"
	if got := ExpectedPasteMarkerLines(msg); got != 4 {
		t.Fatalf("want 4 physical lines, got %d", got)
	}
}

func TestExpectedPasteMarkerLines_NormalizesCRLFAndBareCR(t *testing.T) {
	// The tmux transport normalizes CRLF and bare-CR line breaks to LF before
	// choosing a transport (sendKeysChunkedToTarget) — a Windows-authored
	// --message-file must be measured the same way, or this check would flag
	// every CRLF prompt as truncated.
	crlf := "one\r\ntwo\r\nthree"
	if got := ExpectedPasteMarkerLines(crlf); got != 3 {
		t.Fatalf("CRLF: want 3, got %d", got)
	}
	bareCR := "one\rtwo\rthree"
	if got := ExpectedPasteMarkerLines(bareCR); got != 3 {
		t.Fatalf("bare CR: want 3, got %d", got)
	}
}

func TestPasteMarkerLineCounts_NoMarker(t *testing.T) {
	if got := PasteMarkerLineCounts("assistant response\n❯ \n"); got != nil {
		t.Fatalf("want nil, got %v", got)
	}
}

func TestPasteMarkerLineCounts_SingleMarker(t *testing.T) {
	got := PasteMarkerLineCounts("some prior output\n❯ [Pasted text #1 +4 lines]\n")
	if len(got) != 1 || got[0] != 4 {
		t.Fatalf("want [4], got %v", got)
	}
}

func TestPasteMarkerLineCounts_CaseInsensitiveAndMultiple(t *testing.T) {
	got := PasteMarkerLineCounts("[PASTED TEXT #1 +2 LINES]\nwork\n[Pasted text #2 +7 lines]\n")
	if len(got) != 2 || got[0] != 2 || got[1] != 7 {
		t.Fatalf("want [2 7], got %v", got)
	}
}

// TestPasteMarkerLineCounts_DetectsTruncation is the exact defect shape from
// #2079: the message has 4 physical lines but the composer's marker declares
// only 1 — the composer accepted a fragment, not the whole paste. This is the
// signal SendKeysAndEnterChecked's caller (sendMessageWhenReady) uses to
// withhold Enter instead of submitting the fragment.
func TestPasteMarkerLineCounts_DetectsTruncation(t *testing.T) {
	message := "You are handling ONE task off the list.\n" +
		"Use your own judgment, including when you conclude you cannot help.\n" +
		"Say so and stay put.\n" +
		"Do not guess."
	expected := ExpectedPasteMarkerLines(message)
	if expected != 4 {
		t.Fatalf("expected line count: want 4, got %d", expected)
	}

	// A remounting composer swallowed everything but the last line, which
	// Claude still frames as a well-formed (but short) paste.
	truncatedPane := "some prior output\n❯ [Pasted text #1 +1 lines]\n"
	counts := PasteMarkerLineCounts(truncatedPane)
	if len(counts) != 1 {
		t.Fatalf("want exactly one marker, got %v", counts)
	}
	if declared := counts[0]; declared >= expected {
		t.Fatalf("truncation must be detectable: declared=%d expected=%d", declared, expected)
	}

	// A clean delivery declares the full count and must NOT be flagged.
	cleanPane := "some prior output\n❯ [Pasted text #1 +4 lines]\n"
	counts = PasteMarkerLineCounts(cleanPane)
	if len(counts) != 1 || counts[0] < expected {
		t.Fatalf("clean delivery must declare the full line count: got %v, want >= %d", counts, expected)
	}
}
