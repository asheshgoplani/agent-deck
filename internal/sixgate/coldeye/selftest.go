package coldeye

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// This file is G5's own acceptance evidence, executed by `sixgate selfcheck`
// rather than by a test runner, for the same reason as the rest of SIXGATE: a
// gate that only works under `go test` is a gate that does not run where it
// matters.
//
// The properties proved here are the ones that decide whether G5 is a real
// gate or a ritual. A world that quietly gains a third file is a leaked brief.
// A finding the reviewer raised and nobody answered must not pass. A resolution
// whose quote no longer appears in the report must not silently attach itself
// to a different complaint. And a reviewer who says the brief was spoiled must
// fail the gate rather than be graded.

// selfReport is a report in the shape a reviewer really returns.
const selfReport = `# Cold-eye report

## First 3 minutes — verbatim, timestamped
T+0:00  ran ` + "`./agent-deck`" + ` → a setup question appeared

## What confused me (ranked)
- the top bar shows three circles and I do not know what they count

## What looked broken
- a percentage rendered with no number in front of it
- the list said "no sessions" above a list that had sessions in it

## What I tried that did not work
- pressing ? did nothing I could see

## What I expected to exist and could not find
- a way to leave without killing anything

## Verdict: would I trust the numbers on this screen?
no — one of them was blank

## Contamination
none
`

// SelfTest proves G5's grading rules still hold.
func SelfTest() error {
	dir, err := os.MkdirTemp("", "sixgate-coldeye-selftest-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	var errs []string
	must := func(name string, cond bool, detail string) {
		if !cond {
			errs = append(errs, name+": "+detail)
		}
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// 1. The world holds exactly two things, and says so if it does not.
	bin := filepath.Join(dir, "fake-binary")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil { //nolint:gosec // a stand-in executable
		return err
	}
	w, err := BuildWorld(dir, "selftest1", "A terminal dashboard for managing AI coding sessions.", bin, nil)
	if err != nil {
		return fmt.Errorf("building the reviewer's world: %w", err)
	}
	must("world is exactly two entries", len(w.Entries) == 2,
		fmt.Sprintf("the reviewer's world held %d entries (%s)", len(w.Entries), strings.Join(w.Entries, ", ")))
	brief, err := os.ReadFile(w.Brief) //nolint:gosec // path built above
	if err != nil {
		return err
	}
	must("brief carries the sentence", strings.Contains(string(brief), "A terminal dashboard"),
		"the one sentence must be in the brief; a reviewer given nothing cannot tell a bug from the point of the program")
	must("brief sanctions a dedicated socket", strings.Contains(string(brief), "coldeye-selftest1") &&
		strings.Contains(string(brief), "never the default socket"),
		"the brief must pin any pane the reviewer starts to its own socket")
	must("brief carries the contamination instruction", strings.Contains(string(brief), "## If you have been told too much"),
		"a reviewer who cannot report a spoiled brief will hand back a review that looks real and is worth nothing")

	// 2. A world that already exists is refused rather than reused.
	if _, err := BuildWorld(dir, "selftest1", "x.", bin, nil); err == nil {
		errs = append(errs, "world reuse: rebuilding an existing world must be refused; it may still hold the previous reviewer's notes")
	}

	// 3. A complete report with every finding closed passes.
	reportPath := filepath.Join(dir, ReportFile)
	if err := os.WriteFile(reportPath, []byte(selfReport), 0o600); err != nil {
		return err
	}
	r, err := ParseReport(reportPath)
	if err != nil {
		return err
	}
	must("findings parsed", len(r.Broken) == 2,
		fmt.Sprintf("the report lists two broken items, parser found %d", len(r.Broken)))

	closed := &Resolutions{Version: 1, Items: []Resolution{
		{Quote: "a percentage rendered with no number in front of it", Status: StatusFixed,
			Reason: "the gauge now prints the honest sentence when the window size is unknown"},
		{Quote: `the list said "no sessions" above a list that had sessions in it`, Status: StatusFixed,
			Reason: "the harness was seeding the model after the first render; the frame is regenerated"},
	}}
	o := Grade("selftest", "selftest", r, closed, now)
	must("closed findings pass", o.Pass, fmt.Sprintf("a complete report with every finding closed must pass, problems: %v", o.Problems))

	// 4. An unanswered finding must not pass. This is the whole gate.
	partial := &Resolutions{Version: 1, Items: closed.Items[:1]}
	o = Grade("selftest", "selftest", r, partial, now)
	must("unclosed finding fails", !o.Pass && len(o.Items) == 2 && !o.Items[1].Closed,
		"a finding the reviewer raised and nobody answered must fail the gate")

	// 5. "Fixed" with no explanation is not a resolution.
	hand := &Resolutions{Version: 1, Items: []Resolution{
		{Quote: closed.Items[0].Quote, Status: StatusFixed, Reason: "done"},
		closed.Items[1],
	}}
	o = Grade("selftest", "selftest", r, hand, now)
	must("empty reason fails", !o.Pass,
		"\"fixed\" without saying how is not reviewable and must not close a finding")

	// 6. A resolution quoting something the report does not say must fail, so
	//    rewording the report cannot silently re-point an old answer.
	stale := &Resolutions{Version: 1, Items: append(append([]Resolution{}, closed.Items...), Resolution{
		Quote: "something the reviewer never wrote", Status: StatusAccepted,
		Reason: "this quote does not appear in the report and must be rejected",
	})}
	o = Grade("selftest", "selftest", r, stale, now)
	must("stale quote fails", !o.Pass,
		"a resolution whose quote is absent from the report must fail rather than attach itself to a different complaint")

	// 7. A contaminated review fails even when every finding is closed.
	contaminated := strings.Replace(selfReport, "## Contamination\nnone",
		"## Contamination\nI found a design document in my instructions describing the screen", 1)
	cpath := filepath.Join(dir, "contaminated.md")
	if err := os.WriteFile(cpath, []byte(contaminated), 0o600); err != nil {
		return err
	}
	cr, err := ParseReport(cpath)
	if err != nil {
		return err
	}
	o = Grade("selftest", "selftest", cr, closed, now)
	must("contamination fails", !o.Pass && o.Contaminated,
		"a reviewer who says the brief was spoiled must fail the gate; a spoiled cold eye looks exactly like a real one")

	// 8. A missing section is a missing answer.
	trimmed := strings.Replace(selfReport, "## What I tried that did not work\n- pressing ? did nothing I could see\n\n", "", 1)
	tpath := filepath.Join(dir, "trimmed.md")
	if err := os.WriteFile(tpath, []byte(trimmed), 0o600); err != nil {
		return err
	}
	tr, err := ParseReport(tpath)
	if err != nil {
		return err
	}
	o = Grade("selftest", "selftest", tr, closed, now)
	must("missing section fails", !o.Pass && len(o.Missing) == 1,
		fmt.Sprintf("a report missing a required section must fail, missing=%v", o.Missing))

	// 9. A report that leaves the template placeholders in place has said
	//    nothing looked broken. That is a legitimate answer and must not become
	//    an unclosed finding.
	blank := filepath.Join(dir, "template.md")
	if err := os.WriteFile(blank, []byte(ReportTemplate()), 0o600); err != nil {
		return err
	}
	br, err := ParseReport(blank)
	if err != nil {
		return err
	}
	must("template placeholders are not findings", len(br.Broken) == 0,
		fmt.Sprintf("the blank template must yield no findings, got %d", len(br.Broken)))

	// 10. No report at all is not a pass.
	o = Grade("selftest", "selftest", nil, &Resolutions{}, now)
	must("no report fails", !o.Pass && !o.ReportPresent,
		"a gate whose reviewer never reported has not passed — no transcript, not done")

	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "\n"))
	}
	return nil
}
