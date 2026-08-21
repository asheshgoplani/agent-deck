package script

import (
	"fmt"
	"sort"
	"strings"
)

// The validator's own evidence, runnable without a test runner because SIXGATE
// gates run against built artifacts rather than inside `go test`.

const goodScript = `
version: 1
slug: self-test
sentence: "Open the thing, press a key, read a number, leave."
term: {width: 120, height: 40}
owns:
  - go.mod
banned_screen_patterns_allow:
  - pattern: "null"
    justification: "the fixture session is literally named null-check, so the word is expected on screen"
steps:
  - id: 01-open
    do: {wait_for: "sessions"}
    capture: home
    expect:
      - screen_contains: "agent-deck"
        why: "the app rendered at all"
  - id: 02-key
    do: {key: "C"}
    capture: panel
    expect:
      - screen_not_matches: '\(\s*%\s*\)'
        why: "a percentage with no number is the miss this framework exists for"
`

// badScript packs one instance of every validation failure that matters, each
// keyed by the schema path the validator must report.
const badScript = `
version: 2
slug: Self_Test
sentence: "   "
term: {width: 0, height: -1}
owns:
  - "../escape.go"
  - "/absolute/path.go"
banned_screen_patterns_allow:
  - pattern: ""
    justification: "short"
steps:
  - id: opener
    do: {key: "C", wait: "1s"}
    capture: "Home Screen"
    expect: []
  - id: opener
    do: {}
    expect:
      - screen_contains: "x"
        screen_matches: "y"
  - id: 03-regex
    do: {wait: "9m"}
    capture: frame
    expect:
      - screen_matches: "([unclosed"
`

var wantBadPaths = []string{
	"version",
	"slug",
	"sentence",
	"term.width",
	"term.height",
	"owns[0]",
	"banned_screen_patterns_allow[0].pattern",
	"banned_screen_patterns_allow[0].justification",
	"steps[0].id",
	"steps[0].do",
	"steps[0].capture",
	"steps[0].expect",
	"steps[1].id",
	"steps[1].do",
	"steps[1].expect",
	"steps[1].expect[0]",
	"steps[2].do.wait",
	"steps[2].expect[0].screen_matches",
}

// SelfTest proves the validator still rejects what it claims to reject and
// accepts a well-formed script. It returns nil when the schema gate is
// trustworthy.
func SelfTest(known KnownRuleIDs) error {
	var errs []string

	good, err := Parse(strings.NewReader(goodScript))
	if err != nil {
		errs = append(errs, "the known-good script failed to parse: "+err.Error())
	} else if ps := good.Validate(known); ps != nil {
		errs = append(errs, "the known-good script failed validation:\n    "+strings.ReplaceAll(ps.Error(), "\n", "\n    "))
	}

	bad, err := Parse(strings.NewReader(badScript))
	if err != nil {
		errs = append(errs, "the known-bad script failed to parse (it must parse, then fail validation): "+err.Error())
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

	// An unknown key must fail loudly: a typo'd assertion key that decodes to
	// nothing is an assertion that silently never runs.
	if _, err := Parse(strings.NewReader("version: 1\nscreen_contians: x\n")); err == nil {
		errs = append(errs, "an unknown schema key was accepted; a typo'd assertion would silently never run")
	}

	if ps := CheckPlaceholders([]byte("sentence: \"" + PlaceholderToken + "\"\n")); ps == nil {
		errs = append(errs, "placeholder check did not flag a scaffolded "+PlaceholderToken)
	}
	if ps := CheckPlaceholders([]byte(goodScript)); ps != nil {
		errs = append(errs, "placeholder check flagged a clean script")
	}

	if len(errs) == 0 {
		return nil
	}
	sort.Strings(errs)
	return fmt.Errorf("G0 script validator self-test failed:\n  %s", strings.Join(errs, "\n  "))
}
