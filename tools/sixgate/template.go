package main

import (
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/sixgate/script"
)

// g0Template is what `sixgate scaffold` writes. It is deliberately NOT valid:
// every field an author must think about carries the placeholder token, so the
// scaffold cannot unlock G1..G5 until a human has actually written the journey.
const g0Template = `# G0 — SCRIPT
#
# The user journey, written as literal keystrokes, BEFORE the feature is built.
# If nobody can write this down, the feature is not defined yet and there is
# nothing to build. This file is the acceptance test: G1 drives it, G2 asserts
# on the frames it captured, G3 runs it across the matrix.
#
# Write it in the words of whoever asked for the feature, not in the words of
# whoever is implementing it.
#
#   validate:  sixgate validate <SLUG>
#   unlock:    sixgate scaffold <SLUG>     (creates G1..G5 once this validates)

version: 1
slug: <SLUG>

# One line of English: the journey, as the person who asked for it described it.
sentence: "<PLACEHOLDER> open the app, press the key, see the thing, leave."

# The terminal geometry these assertions are written against.
term: {width: 200, height: 50}

# The source files this feature owns. VERDICT.md hashes them, so a transcript
# recorded three commits ago stops counting as evidence.
#   internal/pkg/file.go   a path or glob
#   internal/pkg/...       a Go-style recursive directory
owns:
  - <PLACEHOLDER>/path/to/the/file/this/feature/owns.go

# Blank Detector suppressions. Every entry needs a real justification; an
# unexplained suppression is exactly how a blank percentage ships.
banned_screen_patterns_allow: []

steps:
  - id: 01-open
    note: "<PLACEHOLDER> what the user is doing and why"
    do: {wait_for: "<PLACEHOLDER> a substring that proves the first screen rendered"}
    capture: home
    expect:
      - screen_contains: "<PLACEHOLDER> something a human would look for"
        why: "<PLACEHOLDER> which regression this assertion stands guard against"

  - id: 02-act
    do: {key: "<PLACEHOLDER>"}
    capture: after-key
    expect:
      # Assert a real figure OR the honest sentence that explains its absence.
      # Never assert only "it did not crash".
      - screen_matches: '<PLACEHOLDER>'
        why: "<PLACEHOLDER>"

  - id: 03-exit
    do: {key: "q"}
`

// renderTemplate fills in the slug and, when the caller supplied one, the
// journey sentence. Everything else stays a placeholder on purpose.
func renderTemplate(slug, sentence string) string {
	out := strings.ReplaceAll(g0Template, "<SLUG>", slug)
	if strings.TrimSpace(sentence) != "" {
		out = strings.Replace(out,
			`sentence: "<PLACEHOLDER> open the app, press the key, see the thing, leave."`,
			"sentence: "+yamlQuote(sentence), 1)
	}
	return strings.ReplaceAll(out, "<PLACEHOLDER>", script.PlaceholderToken)
}

func yamlQuote(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}
