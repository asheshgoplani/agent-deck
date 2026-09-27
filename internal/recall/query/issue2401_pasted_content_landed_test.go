package query

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Claude Code 2.1.277+ stores a long or multi-line paste in the user row
// wrapped as a pasted-content block, not as the literal text:
//
//	"\n\n<pasted_content id=\"4f2a\">\n<text>\n</pasted_content id=\"4f2a\">\n"
//
// The fixture is a 2.1.280 transcript with that shape: a short first turn
// stored plainly, then a multi-line follow-up and a long single-line brief,
// both wrapped, each followed by its answer and system/turn_duration.
const pastedFixture = "claude-pasted-content.jsonl"

const pastedFollowUp = "Follow-up for the second turn:\n1. Read /work/child/notes.md\n2. Summarise it in three bullets\n\nReply with the bullets only."

func pastedLongBrief() string {
	items := make([]string, 0, 8)
	for i := 1; i <= 8; i++ {
		items = append(items, fmt.Sprintf("item-%02d keeps the assignment on one line so Claude collapses it into a pasted block", i))
	}
	return "Long single-line brief: " + strings.Join(items, " ")
}

// TestIssue2401_FindLandedMatchesPastedContentRow: send-status confirmation
// must find the user row Claude wrote for a long send even though the row
// holds the pasted-content wrapper around the text, not the text itself.
func TestIssue2401_FindLandedMatchesPastedContentRow(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join("testdata", "rows", pastedFixture)
	sentAt := time.Date(2026, 9, 26, 20, 1, 30, 0, time.UTC)
	if id, ts, ok := FindLanded(ctx, "claude", path, 0, pastedFollowUp, sentAt); !ok || id != "u-turn2" || ts != "2026-09-26T20:01:34.000Z" {
		t.Fatalf("multi-line pasted follow-up not confirmed: id=%q ts=%q ok=%v", id, ts, ok)
	}
	if id, _, ok := FindLanded(ctx, "claude", path, 0, pastedLongBrief(), sentAt); !ok || id != "u-turn3" {
		t.Fatalf("long single-line pasted send not confirmed: id=%q ok=%v", id, ok)
	}
	// Plain rows keep matching, and unwrapping never widens a match: a
	// different text, or only a part of the pasted block, is not this send.
	if id, _, ok := FindLanded(ctx, "claude", path, 0, "Reply with OK.", time.Time{}); !ok || id != "u-turn1" {
		t.Fatalf("plain row: id=%q ok=%v", id, ok)
	}
	if id, _, ok := FindLanded(ctx, "claude", path, 0, "Follow-up for the second turn:", time.Time{}); ok {
		t.Fatalf("a prefix of the pasted block counted as the send: %s", id)
	}
	// The send-time guard still applies to a pasted row.
	if id, _, ok := FindLanded(ctx, "claude", path, 0, pastedFollowUp, sentAt.Add(10*time.Minute)); ok {
		t.Fatalf("a pasted row stamped before the send counted: %s", id)
	}
}

func TestUnwrapPastedContent(t *testing.T) {
	cases := []struct {
		name, in, want string
		ok             bool
	}{
		{"plain", "just typed", "just typed", false},
		{"whole message pasted", "\n\n<pasted_content id=\"d03a\">\nline one\nline two\n</pasted_content id=\"d03a\">\n", "\n\nline one\nline two\n", true},
		{"typed prefix", "look at this \n\n<pasted_content id=\"96da\">\nbody\n</pasted_content id=\"96da\">\n", "look at this \n\nbody\n", true},
		{"two blocks", "<pasted_content id=\"a\">\nx\n</pasted_content id=\"a\"> and <pasted_content id=\"b\">\ny\n</pasted_content id=\"b\">", "x and y", true},
		{"unclosed", "<pasted_content id=\"a\">\nx", "<pasted_content id=\"a\">\nx", false},
		{"mismatched id", "<pasted_content id=\"a\">\nx\n</pasted_content id=\"b\">", "<pasted_content id=\"a\">\nx\n</pasted_content id=\"b\">", false},
	}
	for _, c := range cases {
		got, ok := UnwrapPastedContent(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: UnwrapPastedContent(%q) = %q, %v; want %q, %v", c.name, c.in, got, ok, c.want, c.ok)
		}
	}
}
