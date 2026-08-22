// Package assert is SIXGATE's G2: it reads the frames G1 recorded and decides
// whether the journey did what the script said it would.
//
// THE STRUCTURAL RULE. This package may import the standard library, the G0
// schema (internal/sixgate/script) and the Blank Detector (internal/sixgate/lint)
// — and nothing else. It cannot import internal/ui, internal/session,
// internal/ctxinspect or internal/tmux, and `sixgate selfcheck` fails the build
// if it ever does. That is not tidiness. The failure this whole framework exists
// to prevent was a suite of green unit tests sitting beside a blank on-screen
// percentage: assertions that can reach into the product drift, one convenience
// at a time, back into testing internals. An assert runner that cannot see the
// product has no choice but to read what the user reads.
//
// So the only input here is text: a directory of .screen.txt files and the
// script that says what should be in them. Swap the driver for Playwright and
// this package does not change a line.
package assert

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/sixgate/lint"
	"github.com/asheshgoplani/agent-deck/internal/sixgate/script"
)

// ResultsSchema is the results.json schema version.
const ResultsSchema = 1

// ScreenSuffix is the frame extension G1 writes and G2 reads.
const ScreenSuffix = ".screen.txt"

// KindMustLabel marks an assertion that came from G4's contract rather than
// from the G0 script. It is reported as its own kind so a reader of results.md
// can see at a glance which lines are the journey and which are the price of an
// unoracled number.
const KindMustLabel = "must_label_on_screen"

// Assertion is one script expectation, evaluated against one frame.
type Assertion struct {
	Fixture string `json:"fixture"`
	Step    string `json:"step"`
	Capture string `json:"capture,omitempty"`
	Frame   string `json:"frame"`
	Kind    string `json:"kind"`
	Pattern string `json:"pattern"`
	// Figure names the G4 figure a must-label assertion is standing in for.
	Figure string `json:"figure,omitempty"`
	Why    string `json:"why,omitempty"`
	Pass   bool   `json:"pass"`
	// Evidence is the line that satisfied the assertion, or the line that
	// violated it. A pass with no evidence is a claim; a pass quoting the line
	// it matched is a fact.
	Evidence string `json:"evidence,omitempty"`
	// Line is the 1-based line the evidence came from, 0 when none.
	Line int `json:"line,omitempty"`
	// Error explains an assertion that could not be evaluated at all.
	Error string `json:"error,omitempty"`
}

// FixtureResult is one drive directory's outcome.
type FixtureResult struct {
	Fixture string `json:"fixture"`
	Dir     string `json:"dir"`
	// Frames lists the frames that were read, in step order.
	Frames []string `json:"frames"`
	// MissingFrames names capture steps whose frame is absent. A missing frame
	// is a failure, never a skip: an assertion that did not run has not passed.
	MissingFrames []string `json:"missing_frames,omitempty"`
	// EmptyFrames names frames that exist but hold nothing readable.
	EmptyFrames []string    `json:"empty_frames,omitempty"`
	Assertions  []Assertion `json:"assertions"`
	// LabelAssertions is how many of the above came from G4's contract.
	LabelAssertions int            `json:"label_assertions,omitempty"`
	BlankFindings   []lint.Finding `json:"blank_findings"`
	Pass            bool           `json:"pass"`
	// FirstDivergentStep is the first step where anything went wrong. It is the
	// single most useful fact in the whole artifact and it is deliberately the
	// first line of results.md.
	FirstDivergentStep string `json:"first_divergent_step,omitempty"`
}

// Results is G2's artifact.
type Results struct {
	Schema int `json:"schema"`
	// Pass is what SIXGATE's verdict reads.
	Pass        bool   `json:"pass"`
	Slug        string `json:"slug"`
	Sentence    string `json:"sentence,omitempty"`
	GeneratedAt string `json:"generated_at"`
	Tool        string `json:"tool,omitempty"`

	// SuppressedRules records every Blank Detector rule the script switched
	// off, with its scope and justification, so a suppression is visible in the
	// result rather than only in the script.
	SuppressedRules []Suppression `json:"suppressed_rules,omitempty"`
	// ActiveRules is how many detector rules ran on a frame no suppression
	// covers — that is, the rule count the journey is really guarded by.
	ActiveRules int `json:"active_rules"`

	// MustLabel records the G4 contract this run obeyed.
	//
	// G4 decides which figures have no source of truth; the rule is that such a
	// figure may ship only if the screen labels it an estimate. That rule is
	// worth nothing as prose, so G4 writes the list and G2 asserts it — and the
	// digest below is what stops the two from drifting. `sixgate verdict
	// --check` compares it against the file on disk, so regenerating the oracle
	// after the assert run invalidates the assert run instead of quietly
	// leaving a results.json that satisfied an older, weaker contract.
	MustLabel MustLabelObeyed `json:"must_label"`

	Fixtures []FixtureResult `json:"fixtures"`

	Totals Totals `json:"totals"`
	// FirstDivergentStep is the earliest divergence across all fixtures.
	FirstDivergentStep string `json:"first_divergent_step,omitempty"`
}

// Suppression is one Blank Detector rule the script switched off, with the
// frames it covers and the reason. It is reproduced into the result so a reader
// of results.md can see what was NOT checked without opening the script.
type Suppression struct {
	Rule          string   `json:"rule"`
	Scope         string   `json:"scope"`
	Steps         []string `json:"steps,omitempty"`
	Justification string   `json:"justification"`
}

// Totals summarises the run.
type Totals struct {
	Frames        int `json:"frames"`
	Assertions    int `json:"assertions"`
	Failed        int `json:"assertions_failed"`
	BlankFindings int `json:"blank_findings"`
	MissingFrames int `json:"missing_frames"`
}

// MustLabelObeyed records which G4 contract this assert run was bound by.
type MustLabelObeyed struct {
	// Source is the file, relative to the gate tree, the contract came from.
	Source string `json:"source"`
	// Present says whether that file existed. A run with no contract is
	// legitimate — G4 may not have been run yet — but it must be visible, so
	// that "G2 passed" cannot be read as "G2 checked the labels".
	Present bool `json:"present"`
	// SHA256 is the digest of the exact file obeyed. `verdict --check` compares
	// it against the file on disk.
	SHA256 string `json:"sha256,omitempty"`
	// Entries is how many unoracled figures the contract named.
	Entries int `json:"entries"`
	// Assertions is how many on-screen label checks it produced.
	Assertions int `json:"assertions"`
	// Error records a contract that could not be read at all.
	Error string `json:"error,omitempty"`
}

// MustLabelFile is G4's contract, decoded.
//
// It is decoded from JSON here rather than imported from internal/sixgate/oracle
// because G2's allowlist forbids importing anything but the standard library,
// the G0 schema and the Blank Detector — and that restriction is the point. A
// gate-to-gate contract carried as a FILE is a contract either side can be
// swapped out of; one carried as a Go type is a shared implementation, and two
// gates sharing an implementation cannot check each other.
type MustLabelFile struct {
	Schema  int              `json:"schema"`
	Slug    string           `json:"slug"`
	Entries []MustLabelEntry `json:"entries"`
}

// MustLabelEntry is one figure that has no oracle and must therefore be
// labelled an estimate on screen.
type MustLabelEntry struct {
	Case    string           `json:"case"`
	Figure  string           `json:"figure"`
	What    string           `json:"what"`
	Pattern string           `json:"pattern"`
	Why     string           `json:"why"`
	Frames  []MustLabelFrame `json:"frames"`
}

// MustLabelFrame addresses one recorded frame the marker must appear on.
type MustLabelFrame struct {
	Fixture string `json:"fixture"`
	Step    string `json:"step"`
}

// LoadMustLabel reads G4's contract. An absent file is not an error: it means
// G4 has not run, which the result records rather than hides.
func LoadMustLabel(path string) (*MustLabelFile, string, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // path derived from the gate tree
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", nil
		}
		return nil, "", err
	}
	sum := sha256.Sum256(raw)
	var out MustLabelFile
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, hex.EncodeToString(sum[:]), fmt.Errorf("%s is not readable JSON: %w", filepath.Base(path), err)
	}
	return &out, hex.EncodeToString(sum[:]), nil
}

// entriesFor returns the label contracts that apply to one fixture directory.
func (m *MustLabelFile) entriesFor(fixture string) []MustLabelEntry {
	if m == nil {
		return nil
	}
	var out []MustLabelEntry
	for _, e := range m.Entries {
		for _, f := range e.Frames {
			if f.Fixture == fixture {
				out = append(out, e)
				break
			}
		}
	}
	return out
}

// EvaluateDir evaluates one G1 drive directory against the script.
func EvaluateDir(s *script.Script, dir string) (FixtureResult, error) {
	return EvaluateDirWithLabels(s, dir, nil)
}

// EvaluateDirWithLabels evaluates one drive directory against the script and,
// additionally, against G4's must-label contract.
//
// The label assertions are ordinary on-screen assertions — they read the same
// recorded frame with the same matcher — because that is the only way the
// coupling means anything. "This number is labelled an estimate" has to be a
// statement about pixels a user saw, not about a flag in a struct.
func EvaluateDirWithLabels(s *script.Script, dir string, labels *MustLabelFile) (FixtureResult, error) {
	res := FixtureResult{Fixture: filepath.Base(dir), Dir: dir, Pass: true}

	for _, st := range s.Steps {
		if st.Capture == "" {
			continue
		}
		name := st.ID + ScreenSuffix
		path := filepath.Join(dir, name)
		raw, err := os.ReadFile(path) //nolint:gosec // path derived from the gate tree
		if err != nil {
			res.MissingFrames = append(res.MissingFrames, name)
			res.Pass = false
			res.noteDivergence(st.ID)
			for _, e := range st.Expect {
				kind, pattern := expectKindAndPattern(e)
				res.Assertions = append(res.Assertions, Assertion{
					Fixture: res.Fixture, Step: st.ID, Capture: st.Capture, Frame: name,
					Kind: kind, Pattern: pattern, Why: e.Why, Pass: false,
					Error: "no frame was recorded for this step, so nothing was asserted",
				})
			}
			continue
		}
		res.Frames = append(res.Frames, name)
		content := string(raw)
		if strings.TrimSpace(lint.StripANSI(content)) == "" {
			res.EmptyFrames = append(res.EmptyFrames, name)
			res.Pass = false
			res.noteDivergence(st.ID)
		}

		for _, e := range st.Expect {
			a := evaluate(res.Fixture, st, name, content, e)
			res.Assertions = append(res.Assertions, a)
			if !a.Pass {
				res.Pass = false
				res.noteDivergence(st.ID)
			}
		}

		// G4's contract, applied to this exact frame. An unoracled figure is
		// allowed to ship only because the screen admits it is an estimate, and
		// this is where that permission is charged for.
		for _, e := range labels.entriesFor(res.Fixture) {
			if !e.coversStep(st.ID) {
				continue
			}
			a := evaluateLabel(res.Fixture, st, name, content, e)
			res.Assertions = append(res.Assertions, a)
			res.LabelAssertions++
			if !a.Pass {
				res.Pass = false
				res.noteDivergence(st.ID)
			}
		}

		// The suppressions in force on THIS frame, not on the journey. A rule
		// silenced for the session list must still be armed on the pager.
		findings := lint.Scan(name, content, s.AllowedRulesFor(st.ID))
		if len(findings) > 0 {
			res.BlankFindings = append(res.BlankFindings, findings...)
			res.Pass = false
			res.noteDivergence(st.ID)
		}
	}
	return res, nil
}

// coversStep reports whether this contract names the given capture step.
func (e MustLabelEntry) coversStep(step string) bool {
	for _, f := range e.Frames {
		if f.Step == step {
			return true
		}
	}
	return false
}

// evaluateLabel asserts one must-label pattern against one recorded frame.
func evaluateLabel(fixtureName string, st script.Step, frame, content string, e MustLabelEntry) Assertion {
	a := Assertion{
		Fixture: fixtureName, Step: st.ID, Capture: st.Capture, Frame: frame,
		Kind: KindMustLabel, Pattern: e.Pattern, Figure: e.Figure,
		Why: fmt.Sprintf("G4 found no oracle for %s (%s), so the screen must say it is an estimate: %s",
			e.Figure, strings.TrimSpace(e.What), strings.Join(strings.Fields(e.Why), " ")),
	}
	re, err := regexp.Compile(e.Pattern)
	if err != nil {
		a.Error = "pattern does not compile: " + err.Error()
		return a
	}
	if i, m := lineMatching(frameLines(content), re); i >= 0 {
		a.Pass, a.Line, a.Evidence = true, i+1, m
		return a
	}
	a.Error = "no line on this frame carries the estimate marker beside the figure; a number with no oracle and no label is a failure here, not a footnote"
	return a
}

// noteDivergence records the earliest step that went wrong. Steps carry an
// ordered two-digit prefix, so string order is journey order.
func (r *FixtureResult) noteDivergence(step string) {
	if r.FirstDivergentStep == "" || step < r.FirstDivergentStep {
		r.FirstDivergentStep = step
	}
}

// evaluate applies one expectation to one frame.
func evaluate(fixtureName string, st script.Step, frame, content string, e script.Expect) Assertion {
	kind, pattern := expectKindAndPattern(e)
	a := Assertion{
		Fixture: fixtureName, Step: st.ID, Capture: st.Capture, Frame: frame,
		Kind: kind, Pattern: pattern, Why: e.Why,
	}
	lines := frameLines(content)
	joined := strings.Join(lines, "\n")

	switch script.ExpectKind(kind) {
	case script.ExpContains:
		if i := lineContaining(lines, pattern); i >= 0 {
			a.Pass, a.Line, a.Evidence = true, i+1, lines[i]
		} else if strings.Contains(joined, pattern) {
			// The substring spans a line break: real on screen, but worth
			// saying so rather than quoting a line that does not hold it.
			a.Pass, a.Evidence = true, "(matched across a line boundary)"
		}
	case script.ExpNotContains:
		if i := lineContaining(lines, pattern); i >= 0 {
			a.Evidence, a.Line = lines[i], i+1
		} else if strings.Contains(joined, pattern) {
			a.Evidence = "(present across a line boundary)"
		} else {
			a.Pass = true
		}
	case script.ExpMatches:
		re, err := regexp.Compile(pattern)
		if err != nil {
			a.Error = "pattern does not compile: " + err.Error()
			return a
		}
		if i, m := lineMatching(lines, re); i >= 0 {
			a.Pass, a.Line, a.Evidence = true, i+1, m
		} else if m := re.FindString(joined); m != "" {
			a.Pass, a.Evidence = true, "(matched across a line boundary: "+m+")"
		}
	case script.ExpNotMatches:
		re, err := regexp.Compile(pattern)
		if err != nil {
			a.Error = "pattern does not compile: " + err.Error()
			return a
		}
		if i, m := lineMatching(lines, re); i >= 0 {
			a.Line, a.Evidence = i+1, m
		} else {
			a.Pass = true
		}
	default:
		a.Error = "the step carries no assertion; the script should not have validated"
	}
	return a
}

func expectKindAndPattern(e script.Expect) (kind, pattern string) {
	switch {
	case e.Contains != nil:
		return string(script.ExpContains), *e.Contains
	case e.NotContains != nil:
		return string(script.ExpNotContains), *e.NotContains
	case e.Matches != nil:
		return string(script.ExpMatches), *e.Matches
	case e.NotMatches != nil:
		return string(script.ExpNotMatches), *e.NotMatches
	}
	return "none", ""
}

// frameLines strips ANSI and trailing space so an assertion reads what a human
// reads rather than what the terminal received.
func frameLines(content string) []string {
	plain := lint.StripANSI(strings.ReplaceAll(content, "\r\n", "\n"))
	lines := strings.Split(plain, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	return lines
}

func lineContaining(lines []string, substr string) int {
	for i, ln := range lines {
		if strings.Contains(ln, substr) {
			return i
		}
	}
	return -1
}

func lineMatching(lines []string, re *regexp.Regexp) (int, string) {
	for i, ln := range lines {
		if m := re.FindString(ln); m != "" {
			return i, m
		}
		if re.MatchString(ln) {
			// A pattern that matches an empty string still matched this line;
			// quote the line so the evidence is not blank.
			return i, ln
		}
	}
	return -1, ""
}

// Evaluate runs G2 over every drive directory under g1Root.
//
// mustLabelPath addresses G4's contract. It is a path rather than a value
// because G2 must record the digest of the exact bytes it obeyed: an assert run
// that cannot say which contract it satisfied is an assert run that will still
// look green after the contract gets stricter.
func Evaluate(s *script.Script, g1Root, mustLabelPath, tool string, now time.Time) (*Results, error) {
	dirs, err := driveDirs(g1Root)
	if err != nil {
		return nil, err
	}
	labels, digest, labelErr := LoadMustLabel(mustLabelPath)
	obeyed := MustLabelObeyed{Source: filepath.Base(mustLabelPath), Present: labels != nil, SHA256: digest}
	if labelErr != nil {
		obeyed.Error = labelErr.Error()
	}
	if labels != nil {
		obeyed.Entries = len(labels.Entries)
	}
	out := &Results{
		Schema:          ResultsSchema,
		Slug:            s.Slug,
		Sentence:        s.Sentence,
		GeneratedAt:     now.UTC().Format(time.RFC3339),
		Tool:            tool,
		SuppressedRules: suppressions(s),
		// Only a journey-wide suppression reduces the guard a frame gets; a
		// scoped one leaves every other frame fully armed, and reporting it as
		// a global reduction would understate the coverage.
		ActiveRules: len(lint.RuleIDs()) - countGlobalSuppressions(s),
		MustLabel:   obeyed,
		Pass:        true,
	}
	if labelErr != nil {
		// A contract that exists and cannot be read is worse than none: it
		// looks like the labels were checked.
		out.Pass = false
	}
	if len(dirs) == 0 {
		out.Pass = false
		out.FirstDivergentStep = "—"
		return out, nil
	}
	for _, d := range dirs {
		fr, err := EvaluateDirWithLabels(s, d, labels)
		if err != nil {
			return nil, err
		}
		out.MustLabel.Assertions += fr.LabelAssertions
		out.Fixtures = append(out.Fixtures, fr)
		out.Totals.Frames += len(fr.Frames)
		out.Totals.Assertions += len(fr.Assertions)
		out.Totals.BlankFindings += len(fr.BlankFindings)
		out.Totals.MissingFrames += len(fr.MissingFrames)
		for _, a := range fr.Assertions {
			if !a.Pass {
				out.Totals.Failed++
			}
		}
		if !fr.Pass {
			out.Pass = false
		}
		if fr.FirstDivergentStep != "" && (out.FirstDivergentStep == "" || fr.FirstDivergentStep < out.FirstDivergentStep) {
			out.FirstDivergentStep = fr.FirstDivergentStep
		}
	}
	return out, nil
}

func suppressions(s *script.Script) []Suppression {
	out := make([]Suppression, 0, len(s.Allow))
	for _, a := range s.Allow {
		out = append(out, Suppression{
			Rule:          a.Pattern,
			Scope:         a.ScopeLabel(),
			Steps:         a.Steps,
			Justification: strings.Join(strings.Fields(a.Justification), " "),
		})
	}
	return out
}

func countGlobalSuppressions(s *script.Script) int {
	n := 0
	for _, a := range s.Allow {
		if len(a.Steps) == 0 {
			n++
		}
	}
	return n
}

func driveDirs(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		out = append(out, filepath.Join(root, e.Name()))
	}
	sort.Strings(out)
	return out, nil
}

// Write persists results.json and results.md.
func Write(dir string, r *Results) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(filepath.Join(dir, "results.json"), raw, 0o644); err != nil { //nolint:gosec // committed artifact
		return err
	}
	return os.WriteFile(filepath.Join(dir, "results.md"), []byte(r.Markdown()), 0o644) //nolint:gosec // committed artifact
}

// Markdown renders results.md. Its first line is the verdict and the first
// divergent step, because that is the one thing a reader needs before anything
// else.
func (r *Results) Markdown() string {
	var b strings.Builder
	if r.Pass {
		fmt.Fprintf(&b, "# G2 ASSERT — %s — PASS (no divergence)\n\n", r.Slug)
	} else {
		fmt.Fprintf(&b, "# G2 ASSERT — %s — FAIL at step `%s`\n\n", r.Slug, orDash(r.FirstDivergentStep))
	}
	fmt.Fprintf(&b, "> %s\n\n", r.Sentence)
	b.WriteString("Every assertion below was evaluated against a recorded frame — the text a human\n")
	b.WriteString("would have read on screen. This runner cannot import the product it is judging.\n\n")

	fmt.Fprintf(&b, "- **Frames read:** %d\n", r.Totals.Frames)
	fmt.Fprintf(&b, "- **Assertions:** %d (%d failed)\n", r.Totals.Assertions, r.Totals.Failed)
	fmt.Fprintf(&b, "- **Blank Detector:** %d rule(s) active, %d finding(s)\n", r.ActiveRules, r.Totals.BlankFindings)
	b.WriteString(mustLabelLine(r.MustLabel))
	if r.Totals.MissingFrames > 0 {
		fmt.Fprintf(&b, "- **Missing frames:** %d — an assertion that did not run has not passed\n", r.Totals.MissingFrames)
	}
	if len(r.SuppressedRules) > 0 {
		b.WriteString("- **Suppressed rules** (what was deliberately NOT checked, and where):\n")
		for _, s := range r.SuppressedRules {
			fmt.Fprintf(&b, "  - `%s` on %s — %s\n", s.Rule, s.Scope, s.Justification)
		}
	}
	fmt.Fprintf(&b, "- **Generated:** %s\n\n", r.GeneratedAt)

	for _, f := range r.Fixtures {
		fmt.Fprintf(&b, "## fixture `%s` — %s\n\n", f.Fixture, passWord(f.Pass))
		if f.FirstDivergentStep != "" {
			fmt.Fprintf(&b, "First divergent step: `%s`\n\n", f.FirstDivergentStep)
		}
		for _, name := range f.MissingFrames {
			fmt.Fprintf(&b, "- **missing frame** `%s`\n", name)
		}
		for _, name := range f.EmptyFrames {
			fmt.Fprintf(&b, "- **empty frame** `%s` — the screen rendered nothing readable\n", name)
		}
		if len(f.MissingFrames)+len(f.EmptyFrames) > 0 {
			b.WriteString("\n")
		}

		b.WriteString("| step | assertion | pattern | result | evidence |\n")
		b.WriteString("|------|-----------|---------|--------|----------|\n")
		for _, a := range f.Assertions {
			fmt.Fprintf(&b, "| `%s` | %s | `%s` | %s | %s |\n",
				a.Step, a.Kind, mdCell(a.Pattern), tick(a.Pass), mdCell(evidenceCell(a)))
		}
		b.WriteString("\n")

		failed := false
		for _, a := range f.Assertions {
			if a.Pass {
				continue
			}
			if !failed {
				b.WriteString("### What failed, and what it was guarding against\n\n")
				failed = true
			}
			fmt.Fprintf(&b, "- **`%s` %s `%s`**\n", a.Step, a.Kind, a.Pattern)
			if a.Why != "" {
				fmt.Fprintf(&b, "  - guarding: %s\n", strings.TrimSpace(a.Why))
			}
			if a.Error != "" {
				fmt.Fprintf(&b, "  - could not evaluate: %s\n", a.Error)
			}
			if a.Evidence != "" {
				fmt.Fprintf(&b, "  - on screen at line %d: `%s`\n", a.Line, strings.TrimSpace(a.Evidence))
			}
		}
		if failed {
			b.WriteString("\n")
		}

		if len(f.BlankFindings) > 0 {
			b.WriteString("### Blank Detector findings\n\n")
			b.WriteString("These fired without anybody knowing to look for them. That is the point:\n")
			b.WriteString("the class of failure this framework exists to catch is the one nobody wrote\n")
			b.WriteString("an assertion for.\n\n")
			b.WriteString("| frame | line | rule | text | why the rule exists |\n")
			b.WriteString("|-------|------|------|------|---------------------|\n")
			for _, fd := range f.BlankFindings {
				fmt.Fprintf(&b, "| `%s` | %d | `%s` | `%s` | %s |\n",
					fd.Frame, fd.Line, fd.Rule, mdCell(strings.TrimSpace(fd.Text)), mdCell(fd.Why))
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

// mustLabelLine reports which G4 contract this run obeyed, in one line.
//
// It says "no contract" loudly when there is none, because "G2 passed" must
// never be readable as "G2 checked that every unoracled number admits it is an
// estimate" when in fact G4 had not run.
func mustLabelLine(m MustLabelObeyed) string {
	switch {
	case m.Error != "":
		return fmt.Sprintf("- **G4 must-label contract:** `%s` could not be read — %s\n", m.Source, m.Error)
	case !m.Present:
		return fmt.Sprintf("- **G4 must-label contract:** none on disk (`%s` absent) — no on-screen estimate labels were checked\n", m.Source)
	default:
		return fmt.Sprintf("- **G4 must-label contract:** `%s` `%s` — %d unoracled figure(s), %d on-screen label assertion(s)\n",
			m.Source, shortDigest(m.SHA256), m.Entries, m.Assertions)
	}
}

func shortDigest(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func evidenceCell(a Assertion) string {
	switch {
	case a.Error != "":
		return a.Error
	case a.Evidence != "":
		return strings.TrimSpace(a.Evidence)
	case a.Pass:
		return "—"
	default:
		return "not on screen"
	}
}

func tick(ok bool) string {
	if ok {
		return "PASS"
	}
	return "**FAIL**"
}

func passWord(ok bool) string {
	if ok {
		return "PASS"
	}
	return "FAIL"
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func mdCell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 160 {
		s = s[:157] + "…"
	}
	if s == "" {
		return "—"
	}
	return s
}
