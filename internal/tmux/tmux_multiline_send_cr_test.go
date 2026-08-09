// Round-2 regression tests for issue #1855: carriage returns are line breaks
// too, and none of them may survive into the delivered byte stream.
//
// The round-1 fix defined "multi-line" as "contains LF" and leaned on
// `paste-buffer -r`, which only stops tmux's own LF→CR rewrite — it never
// removes CRs the payload already carries. That left two input classes broken:
//
//  1. a CRLF body (any Windows-authored --message-file) delivered literal \r
//     bytes inside the bracketed-paste frame, and a CR is a submit key — the
//     exact byte the round-1 tests declare fatal, just out of their fixtures'
//     reach because those are LF-only; and
//  2. a bare-CR body (no LF at all) failed the `strings.Contains(content,
//     "\n")` gate entirely and went out as one UNFRAMED `send-keys -l`,
//     leaving issue #1855 wholly unfixed for that input.
//
// The repo already defines \r as a line terminator — longestMessageLineBytes
// (cmd/agent-deck/session_cmd.go) splits on both \n and \r because ICRNL (the
// tty default) turns an incoming CR into NL before the line discipline sees
// it. The transport now normalizes CRLF and bare CR to LF before its fork, so
// both classes take the framed paste and the frame is CR-free.
//
// These tests reuse the round-1 harness (recordTransport / deliveredBytes /
// assertMultiLineSurvived) and assert the delivered byte stream, not the
// incantation.
package tmux

import (
	"strings"
	"testing"
)

// TestSendKeysAndEnter_CRLFMultiLine_NormalizedAndFramed: a small CRLF body —
// the shape of every Windows-authored --message-file — must arrive framed,
// LF-separated, with no CR anywhere in the stream.
func TestSendKeysAndEnter_CRLFMultiLine_NormalizedAndFramed(t *testing.T) {
	const crlfMessage = "/superpowers:writing-skills\r\n" +
		"Write a skill that writes cupcake flavors and recipes for seeded data instead of Lorem Ipsum."
	const normalized = "/superpowers:writing-skills\n" +
		"Write a skill that writes cupcake flavors and recipes for seeded data instead of Lorem Ipsum."
	if len(crlfMessage) > canonicalSafeBytes {
		t.Fatalf("fixture no longer exercises the small-payload branch: %d bytes > %d",
			len(crlfMessage), canonicalSafeBytes)
	}

	calls := recordTransport(t)
	s := &Session{Name: "ml-crlf"}
	if err := s.SendKeysAndEnter(crlfMessage); err != nil {
		t.Fatalf("SendKeysAndEnter returned error: %v", err)
	}

	assertMultiLineSurvived(t, deliveredBytes(*calls), normalized)
}

// TestSendKeysAndEnter_BareCRMultiLine_TakesFramedPaste: a CR-delimited body
// with no LF is still a multi-line message — pre-fix it slipped through the
// LF-only gate and went out as one unframed send-keys -l.
func TestSendKeysAndEnter_BareCRMultiLine_TakesFramedPaste(t *testing.T) {
	const crMessage = "line one alpha\rline two bravo\rline three charlie"
	const normalized = "line one alpha\nline two bravo\nline three charlie"
	if len(crMessage) > canonicalSafeBytes || strings.Contains(crMessage, "\n") {
		t.Fatal("fixture must be a small, LF-free, CR-delimited body")
	}

	calls := recordTransport(t)
	s := &Session{Name: "ml-cr"}
	if err := s.SendKeysAndEnter(crMessage); err != nil {
		t.Fatalf("SendKeysAndEnter returned error: %v", err)
	}

	assertMultiLineSurvived(t, deliveredBytes(*calls), normalized)
}

// TestSendKeysAndEnter_LargeCRLFMultiLine_NormalizedAndFramed covers the same
// normalization on the >canonicalSafeBytes branch, where the payload was
// always going through paste-buffer and -r alone would have let the payload's
// own CRs straight into the frame.
func TestSendKeysAndEnter_LargeCRLFMultiLine_NormalizedAndFramed(t *testing.T) {
	var b strings.Builder
	b.WriteString("/superpowers:writing-skills\r\n")
	for b.Len() <= canonicalSafeBytes {
		b.WriteString("Write a skill that writes cupcake flavors for seeded data.\r\n")
	}
	crlfMessage := strings.TrimRight(b.String(), "\r\n")
	if len(crlfMessage) <= canonicalSafeBytes {
		t.Fatalf("fixture does not exercise the large-payload branch: %d bytes", len(crlfMessage))
	}
	normalized := strings.ReplaceAll(crlfMessage, "\r\n", "\n")

	calls := recordTransport(t)
	s := &Session{Name: "ml-crlf-large"}
	if err := s.SendKeysAndEnter(crlfMessage); err != nil {
		t.Fatalf("SendKeysAndEnter returned error: %v", err)
	}

	assertMultiLineSurvived(t, deliveredBytes(*calls), normalized)
}
