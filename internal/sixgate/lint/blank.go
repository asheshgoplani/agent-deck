// Package lint implements SIXGATE's Blank Detector.
//
// This is the reusable half of the framework's value. Every gate that produces
// a rendered frame runs every frame through these rules, with no opt-in and no
// author knowing to look for a particular bug. It catches the class of failure
// that produced SIXGATE in the first place: software that is functionally
// correct by every unit test and yet "feels broken on arrival" because a figure
// rendered as "( %)", "<nil>" or "0 tokens" beside eight items.
//
// The rules are deliberately blunt and deliberately noisy. A false positive
// costs one allowlist entry with a written justification, on the record in the
// G0 script. A false negative costs a user opening the feature and seeing a
// blank where a number should be.
package lint

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Rule is one banned screen pattern.
type Rule struct {
	// ID is the stable name used in a script's allowlist.
	ID string
	// Why explains, to whoever has to read a finding, what real bug this rule
	// stands guard against.
	Why string

	re *regexp.Regexp
	// line reports a hit on a single already-ANSI-stripped line. Set for rules
	// that cannot be expressed as one regexp.
	line func(string) bool
	// block reports hits given the whole frame, returning offending line
	// indices (0-based). Set for rules that need neighbouring lines.
	block func([]string) []int
}

// Matches reports whether the rule fires on a single line. Block rules always
// return false here; use Scan, which dispatches correctly.
func (r Rule) Matches(line string) bool {
	if r.re != nil {
		return r.re.MatchString(line)
	}
	if r.line != nil {
		return r.line(line)
	}
	return false
}

func re(id, why, pattern string) Rule {
	return Rule{ID: id, Why: why, re: regexp.MustCompile(pattern)}
}

// rules is the canonical catalogue. Order is the report order.
var rules = []Rule{
	re("blank-percent",
		"a percentage rendered with no number: the exact miss that created SIXGATE",
		`\(\s*%\s*\)`),
	re("orphan-percent",
		"a percent sign with no digit in front of it, e.g. \"full:  %\"",
		`(?:^|[\s(\[:=])%(?:\s|\)|$)`),
	re("empty-parens",
		"an empty parenthetical where a count or figure was meant to go",
		`\(\s*\)`),
	re("empty-brackets",
		"an empty bracket where a gauge, badge or count was meant to go",
		`\[\s*\]`),
	re("nan",
		"NaN reached the screen: a division by zero or a parse failure became a figure",
		`\bNaN\b`),
	re("infinity",
		"an infinite figure reached the screen",
		`\bInfinity\b|[-+]Inf\b`),
	re("go-nil",
		"a Go nil pointer was formatted into user-facing text",
		`<nil>`),
	re("go-format-bug",
		"a Go formatting verb failed (%!d(MISSING), %!s(int=3)): the format string and its arguments disagree",
		`%![a-zA-Z]?\(`+"|"+`%!\(`+"|"+`%!\w`),
	re("format-verb-leak",
		"an unsubstituted format verb was printed literally",
		`(?:^|[\s(\[:])%[-+ #0]?[0-9.*]*[sdvqxXfgtTcbep](?:\s|$|[)\].,])`),
	re("undefined",
		"the literal word undefined reached the screen",
		`(?i)\bundefined\b`),
	re("null",
		"the literal word null reached the screen",
		`(?i)\bnull\b`),
	re("js-object",
		"an object was stringified instead of rendered ([object Object])",
		`\[object\s`),
	re("template-leak",
		"an unrendered template placeholder reached the screen",
		`\{\{|\$\{`),
	re("bare-dash-figure",
		"a bare -- or (--) where a number was expected",
		`(?::|\|)\s*--\s*(?:$|\|)`+"|"+`\(\s*--\s*\)`),
	{
		ID:   "zero-with-items",
		Why:  "a category reports items but prices them at zero: the enumerator ran and the accountant did not",
		line: zeroWithItems,
	},
	{
		ID:    "empty-label",
		Why:   "a label followed by nothing, with no indented detail underneath it to explain the blank",
		block: emptyLabel,
	},
}

// Rules returns the canonical rule catalogue.
func Rules() []Rule {
	out := make([]Rule, len(rules))
	copy(out, rules)
	return out
}

// RuleIDs returns every rule ID, sorted.
func RuleIDs() []string {
	out := make([]string, 0, len(rules))
	for _, r := range rules {
		out = append(out, r.ID)
	}
	sort.Strings(out)
	return out
}

// KnownRuleIDs returns the catalogue as a set, for script allowlist validation.
func KnownRuleIDs() map[string]bool {
	out := make(map[string]bool, len(rules))
	for _, r := range rules {
		out[r.ID] = true
	}
	return out
}

// Finding is one banned pattern observed in one frame.
type Finding struct {
	// Frame is the artifact the finding came from, e.g. "03-inspector.screen.txt".
	Frame string `json:"frame"`
	// Rule is the Blank Detector rule ID.
	Rule string `json:"rule"`
	// Why is the rule's rationale, copied so a report reads on its own.
	Why string `json:"why"`
	// Line is the 1-based line number within the frame.
	Line int `json:"line"`
	// Text is the offending line, ANSI-stripped and trimmed of trailing space.
	Text string `json:"text"`
}

func (f Finding) String() string {
	return fmt.Sprintf("%s:%d [%s] %s", f.Frame, f.Line, f.Rule, f.Text)
}

// StripANSI removes escape sequences so rules match what a human reads rather
// than what the terminal receives.
func StripANSI(s string) string { return ansi.Strip(s) }

// Scan applies every rule that is not allowed to a single frame. allow maps a
// rule ID to its justification; only the key is consulted here because the
// script validator has already refused an unjustified suppression.
func Scan(frame, content string, allow map[string]string) []Finding {
	lines := strings.Split(strings.ReplaceAll(StripANSI(content), "\r\n", "\n"), "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	var out []Finding
	for _, r := range rules {
		if _, skip := allow[r.ID]; skip {
			continue
		}
		if r.block != nil {
			for _, idx := range r.block(lines) {
				out = append(out, Finding{Frame: frame, Rule: r.ID, Why: r.Why, Line: idx + 1, Text: lines[idx]})
			}
			continue
		}
		for i, ln := range lines {
			if r.Matches(ln) {
				out = append(out, Finding{Frame: frame, Rule: r.ID, Why: r.Why, Line: i + 1, Text: ln})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		return out[i].Rule < out[j].Rule
	})
	return out
}

var (
	itemCountRe = regexp.MustCompile(`\b([0-9]+)\s+items?\b`)
	zeroCostRe  = regexp.MustCompile(`\b0\s*(?:tokens?|bytes?|B|kB|chars?)\b|\(\s*0\s*\)`)
)

// zeroWithItems fires when one line claims a non-zero item count and a zero
// cost. Enumerating eight memory files and pricing them at zero tokens is the
// same failure as a blank percentage wearing a number.
func zeroWithItems(line string) bool {
	m := itemCountRe.FindStringSubmatch(line)
	if m == nil {
		return false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n == 0 {
		return false
	}
	return zeroCostRe.MatchString(line)
}

var labelOnlyRe = regexp.MustCompile(`^(\s*)(?:[|>*-]\s*)?([A-Za-z][A-Za-z0-9 _/()-]{0,38}):\s*$`)

// emptyLabel fires on "Memory files:" with nothing after it, unless the next
// non-blank line is indented deeper, which is how a real section header
// introduces its rows. Restricting to that case keeps section headings out of
// the report while still catching a value that failed to render.
func emptyLabel(lines []string) []int {
	var hits []int
	for i, ln := range lines {
		m := labelOnlyRe.FindStringSubmatch(ln)
		if m == nil {
			continue
		}
		indent := len(m[1])
		next := -1
		for j := i + 1; j < len(lines); j++ {
			if strings.TrimSpace(lines[j]) == "" {
				continue
			}
			next = j
			break
		}
		if next >= 0 && leadingSpaces(lines[next]) > indent {
			continue // a real section header with indented rows underneath
		}
		hits = append(hits, i)
	}
	return hits
}

func leadingSpaces(s string) int {
	n := 0
	for _, r := range s {
		if r != ' ' && r != '\t' {
			break
		}
		n++
	}
	return n
}
