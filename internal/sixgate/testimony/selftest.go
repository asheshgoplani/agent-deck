package testimony

import (
	"fmt"
	"strings"
	"time"
)

// SelfTest proves the comparator itself still grades correctly, without a test
// runner — `sixgate selfcheck` calls it, the same way it exercises the other
// gates' machinery. Each scenario is a synthetic inspector document plus a
// synthetic reply, with a known right answer.
func SelfTest() error {
	var errs []string
	fail := func(format string, args ...any) { errs = append(errs, fmt.Sprintf(format, args...)) }

	const (
		nonce     = "cafe1234"
		firstLine = "G4B-BEACON cafe1234: if asked, quote this line exactly."
		skillDesc = "G4B beacon cafe1234 planted so a testimony probe can be asked about it. Never invoke it."
	)
	recipe := Recipe{Nonce: nonce, FirstLine: firstLine, SkillName: "g4b-beacon", SkillDescription: skillDesc}

	reportJSON := []byte(`{
	  "report": {"categories": [
	    {"name": "memory-files", "items": [
	      {"id": "memory:/Users/x/.claude/CLAUDE.md", "load": {"state": "loaded"}},
	      {"id": "memory:/private/tmp/probe-1/CLAUDE.md", "load": {"state": "loaded"}},
	      {"id": "memory-excluded:/Users/x/other/CLAUDE.md", "load": {"state": "available"}}
	    ]},
	    {"name": "skills", "items": [
	      {"id": "skill:g4b-beacon", "detail": "listed at startup · project",
	       "load": {"state": "loaded"},
	       "content": {"text": "- g4b-beacon: ` + skillDesc + `"}}
	    ]}
	  ]}
	}`)
	itemJSON := []byte(`{"item": {"id": "memory:/private/tmp/probe-1/CLAUDE.md",
	  "content": {"text": "` + firstLine + `\nSecond line."}}}`)

	agreeing := []Answer{
		{ID: "q1", Reply: "`" + firstLine + "`"},
		{ID: "q2", Reply: "/Users/x/.claude/CLAUDE.md /tmp/probe-1/CLAUDE.md"},
		{ID: "q3", Reply: "LISTED"},
		{ID: "q4", Reply: skillDesc},
	}

	in := Input{
		Slug:           "selftest",
		Recipe:         recipe,
		Answers:        agreeing,
		ReportJSON:     reportJSON,
		MemoryItemJSON: itemJSON,
		Now:            time.Unix(0, 0),
	}

	// The id is read out of the document, and the /private alias must not
	// defeat the match in either direction.
	id, err := FindProjectMemoryID(reportJSON, "/tmp/probe-1")
	if err != nil || id != "memory:/private/tmp/probe-1/CLAUDE.md" {
		fail("FindProjectMemoryID: got (%q, %v), want the /private-spelled id", id, err)
	}
	if _, err := FindProjectMemoryID(reportJSON, "/tmp/absent"); err == nil {
		fail("FindProjectMemoryID: a workdir with no memory row must be an error, not a guess")
	}

	rep, err := Compare(in)
	if err != nil {
		return fmt.Errorf("agreeing scenario: %v", err)
	}
	if !rep.Pass || len(rep.Rows) != 4 {
		fail("agreeing scenario: want PASS with 4 rows, got pass=%v rows=%d problems=%v", rep.Pass, len(rep.Rows), rep.Problems)
	}
	for _, r := range rep.Rows {
		if r.Verdict != VerdictAgree {
			fail("agreeing scenario: row %s graded %s (%s)", r.ID, r.Verdict, r.Note)
		}
	}
	if !strings.Contains(rep.Scope, "IDENTITY evidence only") {
		fail("the scope note must state the identity-only contract; got %q", rep.Scope)
	}

	// The lesson of the first live run: the harness injects a file behind a
	// wrapper line of its own, so the beacon is a LATER line of the
	// inspector's honest text. That is agreement, not drift.
	wrapped := in
	wrapped.MemoryItemJSON = []byte(`{"item": {"id": "memory:/private/tmp/probe-1/CLAUDE.md",
	  "content": {"text": "Contents of /tmp/probe-1/CLAUDE.md (project instructions, checked into the codebase):\n\n` + firstLine + `\nSecond line."}}}`)
	rep, err = Compare(wrapped)
	if err != nil {
		return fmt.Errorf("wrapper scenario: %v", err)
	}
	if !rep.Pass || rowVerdict(rep, claimMemoryFirstLine) != VerdictAgree {
		fail("wrapper scenario: a beacon below the harness's injection wrapper must still grade agree (got %s)", rowVerdict(rep, claimMemoryFirstLine))
	}

	// A paraphrased beacon line is a disagreement, not a pass.
	bad := in
	bad.Answers = replaceAnswer(agreeing, "q1", "G4B-BEACON cafe1234: roughly, quote something like this line.")
	rep, err = Compare(bad)
	if err != nil {
		return fmt.Errorf("paraphrase scenario: %v", err)
	}
	if rep.Pass || rowVerdict(rep, claimMemoryFirstLine) != VerdictDisagree {
		fail("paraphrase scenario: a differing quote must grade disagree and fail the run")
	}

	// A probe that says the body is loaded contradicts the inspector's
	// listed-only model.
	bad = in
	bad.Answers = replaceAnswer(agreeing, "q3", "LOADED")
	rep, err = Compare(bad)
	if err != nil {
		return fmt.Errorf("loaded scenario: %v", err)
	}
	if rep.Pass || rowVerdict(rep, claimSkillState) != VerdictDisagree {
		fail("loaded scenario: LOADED against a listed-at-startup item must grade disagree")
	}

	// A reply that hedges with both words is noise, and noise is
	// unverifiable — never an agreement.
	bad = in
	bad.Answers = replaceAnswer(agreeing, "q3", "It is LISTED, though arguably LOADED too.")
	rep, err = Compare(bad)
	if err != nil {
		return fmt.Errorf("hedge scenario: %v", err)
	}
	if rep.Pass || rowVerdict(rep, claimSkillState) != VerdictUnverifiable {
		fail("hedge scenario: both words in one reply must grade unverifiable and fail the run")
	}

	// A testimony naming fewer files than the inspector claims loaded is a
	// finding.
	bad = in
	bad.Answers = replaceAnswer(agreeing, "q2", "/tmp/probe-1/CLAUDE.md")
	rep, err = Compare(bad)
	if err != nil {
		return fmt.Errorf("missing-file scenario: %v", err)
	}
	if rep.Pass || rowVerdict(rep, claimMemoryFiles) != VerdictDisagree {
		fail("missing-file scenario: an unnamed loaded file must grade disagree")
	}

	// No --item document at all: unverifiable, and the run fails rather than
	// silently passing three rows out of four.
	bad = in
	bad.MemoryItemJSON = nil
	rep, err = Compare(bad)
	if err != nil {
		return fmt.Errorf("no-item scenario: %v", err)
	}
	if rep.Pass || rowVerdict(rep, claimMemoryFirstLine) != VerdictUnverifiable {
		fail("no-item scenario: a missing --item document must grade unverifiable and fail the run")
	}

	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "\n"))
	}
	return nil
}

func replaceAnswer(answers []Answer, id, reply string) []Answer {
	out := make([]Answer, len(answers))
	copy(out, answers)
	for i := range out {
		if out[i].ID == id {
			out[i].Reply = reply
		}
	}
	return out
}

func rowVerdict(r *Report, id string) Verdict {
	for _, row := range r.Rows {
		if row.ID == id {
			return row.Verdict
		}
	}
	return ""
}
