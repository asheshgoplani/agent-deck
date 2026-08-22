package matrix

import (
	"fmt"
	"sort"
	"strings"
)

// The matrix validator's own evidence, runnable without a test runner because
// SIXGATE's gates run against built artifacts rather than inside `go test`.
//
// The two properties worth proving here are the ones a future maintainer will
// be tempted to weaken on a bad afternoon: that the cold-first-run row and the
// real-binary row cannot be excluded, and that a row cannot expect a divergence
// without naming the step and writing down why.

const goodMatrix = `
version: 1
slug: self-test
sentence: "Every world the journey has to survive, and the ones it deliberately does not."
budget: {row: "2m", pane_row: "6m", unwind_grace: "2m"}
axes:
  adapter: [claude, codex]
  session_state: [running, idle]
  data_size: [typical, empty]
  config: [configured, cold-first-run]
  terminal: ["200x50", "80x24"]
  driver: [A, B]
include:
  - id: claude-typical
    note: "the ordinary world: a running claude session with the data it was recorded with"
    fixture: claude-cold
    adapter: claude
    session_state: running
    data_size: typical
    config: configured
    terminal: "200x50"
    driver: A
    expect: full-journey
  - id: claude-empty
    note: "a session with nothing to inspect, where an empty state has to say so"
    fixture: claude-cold
    adapter: claude
    session_state: idle
    data_size: empty
    config: configured
    terminal: "200x50"
    driver: A
    expect: diverges
    diverges_at: 02-busy
    why: "an idle session cannot satisfy the busy-status assertion the journey opens with"
  - id: cold
    note: "a machine that has never run the software, where the first-run modal lives"
    adapter: claude
    session_state: running
    data_size: empty
    config: cold-first-run
    terminal: "200x50"
    driver: B
    expect: diverges
    diverges_at: 02-busy
    why: "a cold machine owns no session, so there is nothing to press the key on"
    stop_after: 01-home
    stop_reason: "there is no session to inspect, so later steps would record timeouts and call them evidence"
exclude:
  - axis: adapter
    value: codex
    why: "the self-test corpus carries no codex case, and a row driven against an invented transcript is evidence of the fixture"
  - axis: terminal
    value: "80x24"
    why: "kept out of the self-test so the fixture stays small; the real matrix covers it"
seam_pairs: []
`

// badMatrix packs one instance of every failure that matters.
const badMatrix = `
version: 2
slug: ""
sentence: "short"
budget: {row: "", pane_row: "nope", unwind_grace: "-1s"}
axes:
  adapter: [claude, claude]
  session_state: []
  data_size: [typical]
  config: [configured, cold-first-run]
  terminal: ["wide"]
  driver: [A, C]
include:
  - id: Bad_ID
    note: "short"
    fixture: ""
    adapter: gemini
    session_state: running
    data_size: typical
    config: configured
    terminal: "wide"
    driver: A
    expect: maybe
  - id: Bad_ID
    note: "a second row that repeats the first row's identifier entirely"
    fixture: claude-cold
    adapter: claude
    session_state: running
    data_size: typical
    config: configured
    terminal: "wide"
    driver: A
    expect: diverges
    stop_reason: "orphaned"
exclude:
  - axis: config
    value: cold-first-run
    why: "we would rather not run it because it is slow and nobody looks at it anyway"
  - axis: driver
    value: B
    why: "we would rather not spawn tmux at all, which is a safety argument until it is a coverage hole"
  - axis: nonsense
    value: x
    why: "an exclusion naming an axis that does not exist records nothing at all"
seam_pairs:
  - a: Bad_ID
    b: missing
    why: "short"
`

var wantBadPaths = []string{
	"version",
	"slug",
	"sentence",
	"budget.row",
	"budget.pane_row",
	"budget.unwind_grace",
	"axes.adapter[1]",
	"axes.session_state",
	"axes.terminal[0]",
	"axes.driver[1]",
	"include[0].id",
	"include[0].note",
	"include[0].adapter",
	"include[0].fixture",
	"include[0].expect",
	"include[1].id",
	"include[1].diverges_at",
	"include[1].why",
	"include[1].stop_reason",
	"exclude[0].value",
	"exclude[1].value",
	"exclude[2].axis",
	"seam_pairs[0].b",
	"seam_pairs[0].why",
}

// SelfTest proves the matrix validator still rejects what it claims to reject.
func SelfTest() error {
	var errs []string
	known := KnownSteps{"01-home": true, "02-busy": true}

	good, err := Parse(strings.NewReader(goodMatrix))
	if err != nil {
		errs = append(errs, "the known-good matrix failed to parse: "+err.Error())
	} else if ps := good.Validate(known); ps != nil {
		errs = append(errs, "the known-good matrix failed validation:\n    "+strings.ReplaceAll(ps.Error(), "\n", "\n    "))
	}

	bad, err := Parse(strings.NewReader(badMatrix))
	if err != nil {
		errs = append(errs, "the known-bad matrix failed to parse (it must parse, then fail validation): "+err.Error())
	} else {
		got := map[string]bool{}
		for _, p := range bad.Validate(known) {
			got[p.Path] = true
		}
		for _, want := range wantBadPaths {
			if !got[want] {
				errs = append(errs, fmt.Sprintf("validator missed a known defect at %q", want))
			}
		}
	}

	// The two un-excludable rows, checked from the other direction: a matrix
	// that simply omits them, without any exclusion to argue with, must still
	// fail. Refusing the exclusion alone would be trivially routed around by
	// deleting the row and saying nothing.
	errs = append(errs, checkMandatoryOmission(known)...)

	// An unknown key must fail loudly: a typo'd axis in a coverage declaration
	// is a coverage hole that looks like coverage.
	if _, err := Parse(strings.NewReader("version: 1\naxes: {adaptor: [claude]}\n")); err == nil {
		errs = append(errs, "an unknown schema key was accepted; a typo'd axis would silently declare nothing")
	}

	if len(errs) == 0 {
		return nil
	}
	sort.Strings(errs)
	return fmt.Errorf("G3 matrix validator self-test failed:\n  %s", strings.Join(errs, "\n  "))
}

// checkMandatoryOmission proves that dropping the cold row or the Driver B row
// fails validation even when no exclusion mentions them.
func checkMandatoryOmission(known KnownSteps) []string {
	var errs []string
	for _, c := range []struct {
		drop string
		want string
	}{
		{drop: "cold", want: ConfigCold},
		{drop: "driver-b", want: "driver " + DriverB},
	} {
		s, err := Parse(strings.NewReader(goodMatrix))
		if err != nil {
			errs = append(errs, "self-test fixture stopped parsing: "+err.Error())
			continue
		}
		switch c.drop {
		case "cold":
			s.Include = s.Include[:2] // removes the only cold-first-run row, which is also the only driver B row
		case "driver-b":
			// Keep the cold row but move it onto Driver A, so the ONLY thing
			// missing is a real-binary row.
			s.Include[2].Driver = DriverA
			s.Include[2].Config = ConfigCold
		}
		ps := s.Validate(known)
		if ps == nil {
			errs = append(errs, fmt.Sprintf("a matrix missing the %s row validated; the rule is un-excludable and must fail here too", c.want))
			continue
		}
		found := false
		for _, p := range ps {
			if p.Path == "include" {
				found = true
			}
		}
		if !found {
			errs = append(errs, fmt.Sprintf("a matrix missing the %s row failed for the wrong reason: %s", c.want, ps.Error()))
		}
	}
	return errs
}
