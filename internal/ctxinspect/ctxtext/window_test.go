package ctxtext

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/ctxinspect"
)

// The cardinal rule of this feature, expressed as a test: an unknown may be
// unknown, but it may never be a dead end. Whatever could not be established
// must be named, and the one line that would establish it printed with it.
func TestWindowLineNeverDeadEnds(t *testing.T) {
	for _, w := range []ctxinspect.WindowInfo{
		{},
		{Source: ctxinspect.WindowUnknown},
		{Source: ctxinspect.WindowUnknown, Detail: "no context-window size is known for model \"claude-nope-9\""},
		// Tokens without a source is still not a window.
		{Tokens: 200_000, Source: ctxinspect.WindowUnknown},
	} {
		line := WindowLine(w)
		if line == "unknown" {
			t.Fatalf("%+v rendered as a bare dead end", w)
		}
		if !strings.Contains(line, WindowEnvVar) {
			t.Fatalf("%+v rendered as %q, want the remedy", w, line)
		}
		sentences := WindowGaugeSentences(w)
		if len(sentences) != 2 {
			t.Fatalf("%+v produced %d sentences, want a reason and a remedy", w, len(sentences))
		}
		if !strings.Contains(sentences[1], WindowEnvVar) {
			t.Fatalf("the remedy sentence must name the variable, got %q", sentences[1])
		}
	}
}

// The reason is never empty, even when the adapter supplied none: "unknown"
// with no reason is precisely the screen this package exists to replace.
func TestWindowUnknownReasonIsNeverEmpty(t *testing.T) {
	if got := strings.TrimSpace(WindowUnknownReason(ctxinspect.WindowInfo{})); got == "" {
		t.Fatal("an unknown window with no detail produced no reason")
	}
	detail := "the rollout carries no model_context_window figure"
	if got := WindowUnknownReason(ctxinspect.WindowInfo{Detail: detail}); got != detail {
		t.Fatalf("the adapter's own reason must survive, got %q", got)
	}
}

// A known window says nothing about a remedy and nothing about an assumption:
// the qualifiers must attach only where they are true.
func TestWindowLineOfAKnownWindow(t *testing.T) {
	w := ctxinspect.WindowInfo{Tokens: 1_000_000, Source: ctxinspect.WindowModelDefault, Detail: "model prefix \"claude-opus-5\""}
	line := WindowLine(w)
	for _, unwanted := range []string{WindowEnvVar, "assumed", "unknown"} {
		if strings.Contains(line, unwanted) {
			t.Fatalf("an established window must not hedge, got %q", line)
		}
	}
	if !strings.Contains(line, "1.0M") || !strings.Contains(line, "model-default") {
		t.Fatalf("an established window must state its size and its source, got %q", line)
	}
	if WindowGaugeSentences(w) != nil {
		t.Fatal("an established window must not apologise for itself")
	}
}

// An assumed window is the one case that shows a percentage over a denominator
// nothing measured. Every rendering of it has to say so.
func TestAssumedWindowIsAlwaysQualified(t *testing.T) {
	w := ctxinspect.WindowInfo{Tokens: 1_000_000, Source: ctxinspect.WindowModelFamily, Detail: "claude-opus-4-9 is a release this build has never seen"}
	if !w.Assumed() {
		t.Fatal("a model-family window must report itself as assumed")
	}
	if !strings.Contains(WindowLine(w), "assumed") {
		t.Fatalf("the window line must say it was assumed, got %q", WindowLine(w))
	}
	if got := PercentText(w, 6.2); got != "≈6.2%" {
		t.Fatalf("PercentText = %q, want the approximation marked", got)
	}
	sentences := WindowGaugeSentences(w)
	if len(sentences) != 2 {
		t.Fatalf("an assumed window produced %d sentences, want the qualifier and the remedy", len(sentences))
	}
	if !strings.Contains(sentences[0], "\u2248") {
		t.Fatalf("the qualifier must explain the mark the gauge carries, got %q", sentences[0])
	}
	if !strings.Contains(sentences[1], WindowEnvVar) {
		t.Fatalf("the remedy must name the variable, got %q", sentences[1])
	}
	// The established case must stay unmarked, or the mark means nothing.
	established := ctxinspect.WindowInfo{Tokens: 1_000_000, Source: ctxinspect.WindowHarnessReported}
	if got := PercentText(established, 6.2); got != "6.2%" {
		t.Fatalf("PercentText = %q, want an unqualified figure", got)
	}
}

// An indeterminate gauge must be distinguishable from an empty one. Drawing
// both with the same glyph reports "unmeasurable" as "0%".
func TestIndeterminateBarIsNotAnEmptyBar(t *testing.T) {
	bar := IndeterminateBar(10)
	if bar == "" || strings.Contains(bar, "░") || strings.Contains(bar, "█") {
		t.Fatalf("IndeterminateBar = %q, want a fill no working gauge uses", bar)
	}
	if got := len([]rune(bar)); got != 12 {
		t.Fatalf("IndeterminateBar(10) is %d cells wide, want 12 with its brackets", got)
	}
	if IndeterminateBar(0) != "" {
		t.Fatal("a zero-width bar must render nothing rather than stray brackets")
	}
}

func TestTokenAmount(t *testing.T) {
	cases := map[int]string{
		0: "0", 999: "999", 1000: "1.0k", 61_900: "61.9k", 1_000_000: "1.0M", -1500: "-1.5k",
	}
	for in, want := range cases {
		if got := TokenAmount(in); got != want {
			t.Errorf("TokenAmount(%d) = %q, want %q", in, got, want)
		}
	}
}

// ExactAmount is the formatter behind the one figure the report labels
// MEASURED. Rounding is right for a dense column and wrong for a figure
// offered for verification: "27.0k" matches 26,951 and 27,013 equally well.
func TestExactAmount(t *testing.T) {
	cases := map[int]string{
		0:       "0",
		7:       "7",
		999:     "999",
		1000:    "1,000",
		27013:   "27,013",
		1234567: "1,234,567",
		-27013:  "-27,013",
	}
	for in, want := range cases {
		if got := ExactAmount(in); got != want {
			t.Errorf("ExactAmount(%d) = %q, want %q", in, got, want)
		}
	}
	if TokenAmount(27013) == ExactAmount(27013) {
		t.Error("the exact and the rounded formatter agree on 27013: one of them is not doing its job")
	}
}
