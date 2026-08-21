package lint

import (
	"fmt"
	"sort"
	"strings"
)

// This file is the Blank Detector's own acceptance evidence, and it exists in
// the product tree rather than in a _test.go file on purpose: SIXGATE's whole
// claim is that a gate runs against the artifact a user gets, on any machine,
// without a test runner. `sixgate selfcheck` executes it.

// positives maps a rule ID to a frame that rule must flag. Other rules are
// free to fire on the same frame; only the named rule is required.
var positives = map[string]string{
	"blank-percent":    "context used: ( %)  of the window",
	"orphan-percent":   "context used:  % of the window",
	"empty-parens":     "memory files ()",
	"empty-brackets":   "occupancy []  12.3k",
	"nan":              "total: NaN tokens",
	"infinity":         "ratio: +Inf",
	"go-nil":           "session: <nil>",
	"go-format-bug":    "tokens: %!d(MISSING)",
	"format-verb-leak": "total: %d tokens",
	"undefined":        "window size: undefined",
	"null":             "provider: null",
	"js-object":        "items: [object Object]",
	"template-leak":    "title: {{ .Name }}",
	"bare-dash-figure": "total tokens: --",
	"zero-with-items":  "memory files (8 items) 0 tokens",
	"empty-label":      "Total tokens:",
}

// negative is a frame in the shape the context inspector actually renders. No
// rule may fire on it. When a rule turns out to be too blunt for real terminal
// output, this corpus is where that shows up, not in a user's terminal.
const negative = "" +
	"agent-deck · context inspector · claude-session\n" +
	"[████████░░░░░░░░░░░░░░░░░░░░]  12.3k / 200k  (6.2%)  fixed startup overhead\n" +
	"system prompt (1 item, 0 actionable)\n" +
	"memory files (8 items, 3 actionable)\n" +
	"mcp tools (12 items)\n" +
	"skills (4 items)\n" +
	"history: 45.2k tokens · anchor 12.1k measured\n" +
	"coverage: 98.4% of the measured total was attributed to a named item\n" +
	"~ = estimated (character heuristic), error within +2.1% of the measured total\n" +
	"12.3k  fixed startup overhead (context window size unknown, so no percentage is shown)\n" +
	"Detail:\n" +
	"    CLAUDE.md                  4.1k  ~\n" +
	"    memory/MEMORY.md          12.9k  ~\n" +
	"context used: ( %)\n" +
	"press esc to go back, q to quit\n"

// SelfTest proves the catalogue still does what it claims: every rule fires on
// its known positive, no rule fires on a realistic frame, and every rule is
// covered by a positive. It returns nil when the detector is trustworthy.
func SelfTest() error {
	var errs []string

	covered := map[string]bool{}
	for _, r := range rules {
		frame, ok := positives[r.ID]
		if !ok {
			errs = append(errs, fmt.Sprintf("rule %q has no positive sample: an unproven rule is not a gate", r.ID))
			continue
		}
		covered[r.ID] = true
		if !hasRule(Scan("positive/"+r.ID, frame, nil), r.ID) {
			errs = append(errs, fmt.Sprintf("rule %q did not fire on its own positive sample %q", r.ID, frame))
		}
	}
	known := KnownRuleIDs()
	for id := range positives {
		if !known[id] {
			errs = append(errs, fmt.Sprintf("positive sample names unknown rule %q", id))
		}
	}

	for _, f := range Scan("negative-corpus", negative, nil) {
		errs = append(errs, fmt.Sprintf("false positive: %s — %s", f, f.Why))
	}

	// Allowlisting must actually suppress, or a justified exception would be
	// no exception at all.
	if got := Scan("positive/blank-percent", positives["blank-percent"], map[string]string{"blank-percent": "x", "orphan-percent": "x"}); hasRule(got, "blank-percent") {
		errs = append(errs, "allowlist did not suppress rule \"blank-percent\"")
	}

	if len(errs) == 0 {
		return nil
	}
	sort.Strings(errs)
	return fmt.Errorf("blank detector self-test failed:\n  %s", strings.Join(errs, "\n  "))
}

func hasRule(fs []Finding, id string) bool {
	for _, f := range fs {
		if f.Rule == id {
			return true
		}
	}
	return false
}
