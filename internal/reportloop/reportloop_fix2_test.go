package reportloop

import (
	"testing"
)

// Re-review N3: a "no" answer raises the lower bound for FixedIn; a newer
// release citing the report after that "no" clears the answer so What's
// new asks again; a stored FixedIn never stands unbounded.

const noAnswerChangelog = "## [1.0.3] - 2026-11-20\n\n### Fixes\n\n- Second try (#70).\n\n" +
	"## [1.0.2] - 2026-11-10\n\n### Fixes\n\n- Other (#71).\n\n" +
	"## [1.0.1] - 2026-11-05\n\n### Fixes\n\n- First try (#70).\n"

func TestReportLoopNoAnswerRaisesLowerBound(t *testing.T) {
	r := Report{Number: 70, FiledVersion: "1.0.0", FixedIn: "1.0.1", Feedback: &Feedback{Answer: "no", Version: "1.0.2"}}
	if got := ReportFixedIn(noAnswerChangelog, r, "1.0.2"); got != "" {
		t.Fatalf("after a \"no\" on v1.0.2, the v1.0.1 fix no longer counts; got %q", got)
	}
	if got := ReportFixedIn(noAnswerChangelog, r, "1.0.3"); got != "1.0.3" {
		t.Fatalf("a citation newer than the \"no\" is the new fix; got %q", got)
	}
	// A "yes" does not move the bound.
	r.Feedback = &Feedback{Answer: "yes", Version: "1.0.2"}
	if got := ReportFixedIn(noAnswerChangelog, r, "1.0.3"); got != "1.0.1" {
		t.Fatalf("a yes keeps the oldest fix after the filed version; got %q", got)
	}
}

func TestReportLoopWhatsNewAsksAgainAfterNoWhenNewerFixCites(t *testing.T) {
	s := tempStore(t)
	if err := s.Record(Report{Number: 70, Title: "[bug] x", FiledVersion: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WhatsNew(noAnswerChangelog, "1.0.2", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordFeedback(70, "no", "1.0.2"); err != nil {
		t.Fatal(err)
	}
	wn, err := s.WhatsNew(noAnswerChangelog, "1.0.3", "1.0.2")
	if err != nil {
		t.Fatal(err)
	}
	if len(wn.Fixed) != 1 || wn.Fixed[0].FixedIn != "1.0.3" || len(wn.NeedsFeedback()) != 1 {
		t.Fatalf("v1.0.3 cites #70 after the \"no\": want it fixed in 1.0.3 and asked again: %+v", wn.Fixed)
	}
	st, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if r := st.Find(70); r.Feedback != nil || r.FixedIn != "1.0.3" {
		t.Fatalf("the stale \"no\" must be cleared and FixedIn moved: %+v feedback=%+v", r, r.Feedback)
	}
}

func TestReportLoopNoAnswerWithoutNewerFixStaysAnswered(t *testing.T) {
	s := tempStore(t)
	_ = s.Record(Report{Number: 70, Title: "[bug] x", FiledVersion: "1.0.0"})
	_ = s.RecordFeedback(70, "no", "1.0.1")
	wn, err := s.WhatsNew(noAnswerChangelog, "1.0.2", "1.0.1")
	if err != nil {
		t.Fatal(err)
	}
	st, _ := s.Load()
	if len(wn.Fixed) != 0 || st.Find(70).Feedback == nil {
		t.Fatalf("no newer citation: nothing to ask, the answer stays: fixed=%+v feedback=%+v", wn.Fixed, st.Find(70).Feedback)
	}
}

func TestReportLoopStoredFixedInIsBounded(t *testing.T) {
	// Stored before the filed version (a stale or hand-edited value): never shown.
	r := Report{Number: 80, FiledVersion: "1.0.2", FixedIn: "1.0.1"}
	if got := ReportFixedIn("", r, "1.0.3"); got != "" {
		t.Fatalf("a stored FixedIn not after the filed version must not stand; got %q", got)
	}
	// The changelog is known and does not cite #80: the stored value does not stand.
	r = Report{Number: 80, FiledVersion: "1.0.0", FixedIn: "1.0.2"}
	if got := ReportFixedIn(noAnswerChangelog, r, "1.0.3"); got != "" {
		t.Fatalf("with a changelog that does not cite #80 the stored value must not stand; got %q", got)
	}
	// Without a changelog a bounded stored value stands.
	if got := ReportFixedIn("", r, "1.0.3"); got != "1.0.2" {
		t.Fatalf("no changelog: a bounded stored value stands; got %q", got)
	}
	// ... unless a "no" came on or after it.
	r.Feedback = &Feedback{Answer: "no", Version: "1.0.2"}
	if got := ReportFixedIn("", r, "1.0.3"); got != "" {
		t.Fatalf("no changelog: a stored value at or before a \"no\" must not stand; got %q", got)
	}
}

// After a "no" is cleared by a newer fix, a later refresh must not fall back
// to the version the person already said was still broken (sim step 11).
func TestReportLoopClearedNoKeepsItsFloor(t *testing.T) {
	s := tempStore(t)
	if err := s.Record(Report{Number: 70, Title: "[bug] x", FiledVersion: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WhatsNew(noAnswerChangelog, "1.0.2", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordFeedback(70, "no", "1.0.2"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WhatsNew(noAnswerChangelog, "1.0.3", "1.0.2"); err != nil {
		t.Fatal(err)
	}
	wn, err := s.WhatsNew(noAnswerChangelog, "1.0.3", "1.0.2")
	if err != nil {
		t.Fatal(err)
	}
	if len(wn.Fixed) != 1 || wn.Fixed[0].FixedIn != "1.0.3" {
		t.Fatalf("second refresh fell back below the cleared \"no\": %+v", wn.Fixed)
	}
	st, _ := s.Load()
	if r := st.Find(70); r.FixedIn != "1.0.3" || r.NotFixedIn != "1.0.2" {
		t.Fatalf("want fixed_in 1.0.3 and not_fixed_in 1.0.2, got %+v", r)
	}
}
