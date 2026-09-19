package ctxtext

import (
	"strings"
	"testing"
)

// The payoff line is the one sentence on the screen that tells somebody to do
// something. That makes it the sentence most likely to overclaim, so the four
// shapes it can take are pinned here.

func TestActionableSentenceNamesTheSavingWhenThereIsOne(t *testing.T) {
	got := ActionableSentence(3, 5200, true, "5.2k")
	if !strings.Contains(got, "3 items") || !strings.Contains(got, "5.2k") {
		t.Fatalf("got %q, want the count and the figure", got)
	}
	if strings.Contains(got, "remove") {
		t.Fatalf("got %q: most levers are an edit, so the summary must not promise removal", got)
	}
}

func TestActionableSentenceRefusesToQuoteASavingItCannotEstablish(t *testing.T) {
	got := ActionableSentence(2, 0, false, "—")
	if !strings.Contains(got, "could not be established") {
		t.Fatalf("got %q: an unmeasurable cost must say so, not read as zero", got)
	}
	if strings.Contains(got, "saves nothing today") {
		t.Fatalf("got %q: \"saves nothing\" is a measurement, and nothing was measured", got)
	}
}

func TestActionableSentenceDistinguishesZeroFromUnknown(t *testing.T) {
	got := ActionableSentence(2, 0, true, "0")
	if !strings.Contains(got, "saves nothing today") {
		t.Fatalf("got %q: an established zero is a real answer and should be given plainly", got)
	}
}

func TestActionableSentenceSaysSoWhenThereIsNothingToDo(t *testing.T) {
	got := ActionableSentence(0, 0, true, "0")
	if !strings.Contains(got, "nothing in this report is under your control") {
		t.Fatalf("got %q: an empty result must be stated, not left as an absent line", got)
	}
	if strings.Contains(got, "0 items") {
		t.Fatalf("got %q: \"0 items you can act on\" is a worse sentence than the one that explains why", got)
	}
}

func TestActionableSentenceIsSingularForOne(t *testing.T) {
	got := ActionableSentence(1, 40, true, "40")
	if !strings.Contains(got, "1 item,") {
		t.Fatalf("got %q, want a singular noun", got)
	}
}
