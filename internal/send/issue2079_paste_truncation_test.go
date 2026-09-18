package send

import (
	"strings"
	"testing"
)

// Regression tests for issue #2079: `launch --message-file` (and any other
// multi-line send) can deliver only the tail of a prompt when a slow-mounting
// composer swallows the leading bytes of a paste — and every downstream
// signal reads identically to a clean delivery, because nothing compares what
// landed against what was sent.
//
// Claude's composer collapses a framed multi-line paste behind
// "[Pasted text #N +M lines]" whether the paste arrived whole or was cut
// short: a truncated paste still produces a well-formed marker, just one
// declaring fewer line breaks than the message actually has.
// ExpectedPasteMarkerLineBreaks and PasteMarkerLineCounts are the two halves
// of that comparison; CheckPasteMarker is the comparison itself.
//
// Marker semantics (Claude Code, verified against the shipped bundle): M is
// `(text.match(/\r\n|\r|\n/g) || []).length` — the number of hard line-break
// sequences in the paste, NOT the number of physical lines and NOT the
// number of rows the pane wraps the text to. A paste with no line break
// renders as a bare "[Pasted text #N]" with no count. The rc.3 launch
// regression compared M with a physical line count (M+1) and refused every
// multi-line launch prompt as truncated; these tests pin the real semantics.

func TestExpectedPasteMarkerLineBreaks_SingleLineMessageHasNoCount(t *testing.T) {
	if got := ExpectedPasteMarkerLineBreaks("just one line"); got != 0 {
		t.Fatalf("single-line message: want 0 (no count to compare), got %d", got)
	}
	long := strings.Repeat("x", 400)
	if got := ExpectedPasteMarkerLineBreaks(long); got != 0 {
		t.Fatalf("long single-line message: want 0 (collapses as a bare [Pasted text #N]), got %d", got)
	}
}

func TestExpectedPasteMarkerLineBreaks_CountsHardLineBreaks(t *testing.T) {
	// Two physical lines carry one hard line break: Claude shows "+1 lines".
	msg := "/superpowers:writing-skills\n" +
		"Write a skill that writes cupcake flavors for seeded data instead of Lorem Ipsum."
	if got := ExpectedPasteMarkerLineBreaks(msg); got != 1 {
		t.Fatalf("want 1 hard line break, got %d", got)
	}
}

func TestExpectedPasteMarkerLineBreaks_LongPromptFile(t *testing.T) {
	// The issue's own reproduction: a --message-file prompt of four
	// physical lines collapses behind "+3 lines".
	msg := "line one\nline two\nline three\nline four"
	if got := ExpectedPasteMarkerLineBreaks(msg); got != 3 {
		t.Fatalf("want 3 hard line breaks, got %d", got)
	}
}

func TestExpectedPasteMarkerLineBreaks_NormalizesCRLFAndBareCR(t *testing.T) {
	// The tmux transport normalizes CRLF and bare-CR line breaks to LF before
	// choosing a transport (sendKeysChunkedToTarget) — a Windows-authored
	// --message-file must be measured the same way, or this check would flag
	// every CRLF prompt as truncated. Claude itself counts CRLF as one break.
	crlf := "one\r\ntwo\r\nthree"
	if got := ExpectedPasteMarkerLineBreaks(crlf); got != 2 {
		t.Fatalf("CRLF: want 2, got %d", got)
	}
	bareCR := "one\rtwo\rthree"
	if got := ExpectedPasteMarkerLineBreaks(bareCR); got != 2 {
		t.Fatalf("bare CR: want 2, got %d", got)
	}
}

func TestExpectedPasteMarkerLineBreaks_TrailingNewlineNotCounted(t *testing.T) {
	// A trailing newline may or may not survive into the composer's count;
	// excluding it makes the expectation the floor of what Claude declares.
	if got := ExpectedPasteMarkerLineBreaks("a\nb\nc\n"); got != 2 {
		t.Fatalf("trailing LF: want 2, got %d", got)
	}
	if got := ExpectedPasteMarkerLineBreaks("a\r\nb\r\nc\r\n\r\n"); got != 2 {
		t.Fatalf("trailing CRLFs: want 2, got %d", got)
	}
}

func TestPasteMarkerLineCounts_NoMarker(t *testing.T) {
	if got := PasteMarkerLineCounts("assistant response\n❯ \n"); got != nil {
		t.Fatalf("want nil, got %v", got)
	}
}

func TestPasteMarkerLineCounts_BareMarkerHasNoCount(t *testing.T) {
	// A long single-line paste collapses behind "[Pasted text #N]" with no
	// "+M lines" suffix: nothing to parse, nothing to compare.
	if got := PasteMarkerLineCounts("❯ [Pasted text #1]\n"); got != nil {
		t.Fatalf("want nil for a bare marker, got %v", got)
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

// TestCheckPasteMarker_DetectsTruncation is the exact defect shape from
// #2079: the message has 4 physical lines (3 hard breaks) but the composer's
// marker declares only 1 — the composer accepted a fragment, not the whole
// paste. This is the signal SendKeysAndEnterChecked's caller
// (sendMessageWhenReady) uses to withhold Enter instead of submitting it.
func TestCheckPasteMarker_DetectsTruncation(t *testing.T) {
	message := "You are handling ONE task off the list.\n" +
		"Use your own judgment, including when you conclude you cannot help.\n" +
		"Say so and stay put.\n" +
		"Do not guess."
	expected := ExpectedPasteMarkerLineBreaks(message)
	if expected != 3 {
		t.Fatalf("expected line-break count: want 3, got %d", expected)
	}

	// A remounting composer swallowed everything but the last two lines,
	// which Claude still frames as a well-formed (but short) paste.
	verdict, declared := CheckPasteMarker("some prior output\n❯ [Pasted text #1 +1 lines]\n", expected)
	if verdict != PasteMarkerTruncated || declared != 1 {
		t.Fatalf("truncation must be detectable: verdict=%v declared=%d", verdict, declared)
	}

	// A clean delivery declares the full count and must NOT be flagged.
	verdict, declared = CheckPasteMarker("some prior output\n❯ [Pasted text #1 +3 lines]\n", expected)
	if verdict != PasteMarkerIntact || declared != 3 {
		t.Fatalf("clean delivery must be intact: verdict=%v declared=%d", verdict, declared)
	}

	// No counted marker at all is unknown, never truncated.
	verdict, declared = CheckPasteMarker("some prior output\n❯ \n", expected)
	if verdict != PasteMarkerAbsent || declared != -1 {
		t.Fatalf("no marker must be absent: verdict=%v declared=%d", verdict, declared)
	}
}

// launchLine is the exact one-line launch prompt (139 chars) from the rc.3
// P1 report; launchSentinelBlock is what `agent-deck launch` appends to every
// child prompt (cmd/agent-deck assertDoneInstruction). Together they are a
// 6-line message with 5 hard line breaks, which Claude Code's composer
// renders as "[Pasted text #1 +5 lines]".
const (
	launchLine          = "Read /tmp/exec-carry-2147/PROMPT.md and execute it autonomously end-to-end. Write RESULTS.md and PR-BODY.md and end with the sentinel line."
	launchSentinelBlock = "\n\n## Final step — assert completion\n" +
		"When the task is fully done, print exactly this as the last line of your final message:\n" +
		"  ===AGENTDECK_DONE=== status=ok summary=<what you accomplished, one line>\n" +
		"Use status=fail if you could not complete it; put the blocker in the summary."
	launchMessage          = launchLine + launchSentinelBlock
	exactLaunchPlaceholder = "[Pasted text #1 +5 lines]"
)

// wrapAt soft-wraps text into pane rows of at most width cells (runes here;
// wide runes are counted as two cells) the way a terminal renders a long line,
// joining the rows with LF. The result contains MORE LFs than the message has
// hard breaks — exactly the confusion the guard must not fall for.
func wrapAt(text string, width int) string {
	var rows []string
	for _, line := range strings.Split(text, "\n") {
		var row []rune
		cells := 0
		for _, r := range line {
			w := 1
			if r > 0x2E7F { // CJK and other wide scripts occupy two cells
				w = 2
			}
			if cells+w > width {
				rows = append(rows, string(row))
				row, cells = nil, 0
			}
			row = append(row, r)
			cells += w
		}
		rows = append(rows, string(row))
	}
	return strings.Join(rows, "\n")
}

// markerPane renders a synthetic Claude pane: the (soft-wrapped) transcript echo of
// the paste, the collapsed marker in the composer, and a divider.
func markerPane(wrappedEcho, marker string) string {
	return "some prior output\n" + wrappedEcho + "\n─────────────\n❯ " + marker + "\n─────────────\n"
}

func TestCheckPasteMarker_Table(t *testing.T) {
	longLine400 := strings.Repeat("word ", 80)
	threeLongLines := longLine400 + "\n" + longLine400 + "\n" + longLine400
	cjk := strings.Repeat("漢字かな", 40) // 160 wide runes = 320 cells, one line
	threeCJKLines := cjk + "\n" + cjk + "\n" + cjk

	cases := []struct {
		name     string
		message  string
		pane     string
		wantExp  int
		wantVerd PasteMarkerVerdict
	}{
		// The rc.3 P1: exact launch line + sentinel block, exact placeholder,
		// soft-wrapped at three pane widths. Must submit at every width.
		{"exact launch prompt, pane 80 cols", launchMessage,
			markerPane(wrapAt(launchMessage, 80), exactLaunchPlaceholder), 5, PasteMarkerIntact},
		{"exact launch prompt, pane 120 cols", launchMessage,
			markerPane(wrapAt(launchMessage, 120), exactLaunchPlaceholder), 5, PasteMarkerIntact},
		{"exact launch prompt, pane 200 cols", launchMessage,
			markerPane(wrapAt(launchMessage, 200), exactLaunchPlaceholder), 5, PasteMarkerIntact},
		// One long line (400 chars, no hard break) wrapped at 80/120/200:
		// expectation is 0 (callers skip the check), and even when the check
		// runs the bare marker is "absent", never truncated.
		{"400-char single line, pane 80 cols", longLine400,
			markerPane(wrapAt(longLine400, 80), "[Pasted text #1]"), 0, PasteMarkerAbsent},
		{"400-char single line, pane 120 cols", longLine400,
			markerPane(wrapAt(longLine400, 120), "[Pasted text #1]"), 0, PasteMarkerAbsent},
		{"400-char single line, pane 200 cols", longLine400,
			markerPane(wrapAt(longLine400, 200), "[Pasted text #1]"), 0, PasteMarkerAbsent},
		// A genuine truncation: the composer swallowed the first four lines of
		// the launch prompt and collapsed the remaining two as "+1 lines".
		{"real truncation of launch prompt", launchMessage,
			markerPane(wrapAt(launchMessage, 80), "[Pasted text #1 +1 lines]"), 5, PasteMarkerTruncated},
		{"real truncation, one break short", launchMessage,
			markerPane("", "[Pasted text #1 +4 lines]"), 5, PasteMarkerTruncated},
		// Multi-line message whose lines each wrap in the pane.
		{"3 long lines wrapped at 80", threeLongLines,
			markerPane(wrapAt(threeLongLines, 80), "[Pasted text #1 +2 lines]"), 2, PasteMarkerIntact},
		{"3 long lines wrapped at 80, truncated", threeLongLines,
			markerPane(wrapAt(threeLongLines, 80), "[Pasted text #1 +1 lines]"), 2, PasteMarkerTruncated},
		// CRLF (Windows-authored --message-file): one break per CRLF.
		{"CRLF message", "one\r\ntwo\r\nthree\r\nfour",
			markerPane("", "[Pasted text #1 +3 lines]"), 3, PasteMarkerIntact},
		{"CRLF message truncated", "one\r\ntwo\r\nthree\r\nfour",
			markerPane("", "[Pasted text #1 +2 lines]"), 3, PasteMarkerTruncated},
		// Trailing newline: accepted whether or not the composer counts it.
		{"trailing newline counted by composer", "a\nb\nc\n",
			markerPane("", "[Pasted text #1 +3 lines]"), 2, PasteMarkerIntact},
		{"trailing newline dropped by composer", "a\nb\nc\n",
			markerPane("", "[Pasted text #1 +2 lines]"), 2, PasteMarkerIntact},
		// Unicode wide characters: 320 cells per line wrap to many rows.
		{"CJK wide chars, 3 lines, pane 80", threeCJKLines,
			markerPane(wrapAt(threeCJKLines, 80), "[Pasted text #1 +2 lines]"), 2, PasteMarkerIntact},
		{"CJK wide chars, truncated", threeCJKLines,
			markerPane(wrapAt(threeCJKLines, 80), "[Pasted text #1 +1 lines]"), 2, PasteMarkerTruncated},
		// Earlier pastes leave their markers in the transcript (#1855): only
		// the newest marker describes this send.
		{"older marker in transcript, newest intact", "a\nb\nc",
			"❯ [Pasted text #1 +9 lines]\nassistant reply\n❯ [Pasted text #2 +2 lines]\n", 2, PasteMarkerIntact},
		{"older marker in transcript, newest truncated", "a\nb\nc",
			"❯ [Pasted text #1 +9 lines]\nassistant reply\n❯ [Pasted text #2 +1 lines]\n", 2, PasteMarkerTruncated},
		// The marker itself soft-wrapped across two pane rows still parses.
		{"marker wrapped across rows", launchMessage,
			markerPane("", "[Pasted text #1 +5\nlines]"), 5, PasteMarkerIntact},
		// No marker rendered yet: unknown, not unsafe.
		{"no marker yet", "a\nb\nc", markerPane("", ""), 2, PasteMarkerAbsent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exp := ExpectedPasteMarkerLineBreaks(tc.message)
			if exp != tc.wantExp {
				t.Fatalf("ExpectedPasteMarkerLineBreaks: want %d, got %d", tc.wantExp, exp)
			}
			verdict, declared := CheckPasteMarker(tc.pane, exp)
			if verdict != tc.wantVerd {
				t.Fatalf("CheckPasteMarker: want %v, got %v (declared=%d expected=%d)\npane:\n%s",
					tc.wantVerd, verdict, declared, exp, tc.pane)
			}
		})
	}
}

// TestCheckPasteMarker_DisplayWidthNeverMatters pins the invariant directly:
// for the same message and the same marker, the verdict is identical at every
// pane width, however many rows the text wraps to.
func TestCheckPasteMarker_DisplayWidthNeverMatters(t *testing.T) {
	message := launchMessage
	exp := ExpectedPasteMarkerLineBreaks(message)
	for _, width := range []int{20, 40, 80, 120, 200, 400} {
		echo := wrapAt(message, width)
		if rows := strings.Count(echo, "\n") + 1; width < 120 && rows <= 6 {
			t.Fatalf("width %d: fixture did not wrap (%d rows)", width, rows)
		}
		verdict, _ := CheckPasteMarker(markerPane(echo, exactLaunchPlaceholder), exp)
		if verdict != PasteMarkerIntact {
			t.Fatalf("width %d: want intact, got %v", width, verdict)
		}
	}
}
