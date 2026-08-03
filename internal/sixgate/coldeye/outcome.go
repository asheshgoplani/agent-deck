package coldeye

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// OutcomeSchema is the outcome.json schema version.
const OutcomeSchema = 1

// requiredSections are the headings a report must carry. They are parsed, so a
// reviewer who drops one produces an incomplete report rather than a shorter
// one, and the gate says which section is missing.
var requiredSections = []string{
	"First 3 minutes",
	"What confused me",
	"What looked broken",
	"What I tried that did not work",
	"What I expected to exist and could not find",
	"Verdict",
	"Contamination",
}

// brokenSection is the heading whose items must each be closed.
const brokenSection = "What looked broken"

// contaminationSection is the reviewer's self-report.
const contaminationSection = "Contamination"

var (
	headingRe  = regexp.MustCompile(`(?m)^##\s+(.*?)\s*$`)
	bulletRe   = regexp.MustCompile(`^\s*[-*]\s+(.*\S)\s*$`)
	templateRe = regexp.MustCompile(`^(\.\.\.|none|yes/no — because \.\.\.)$`)
)

// Report is a parsed cold-eye report.
type Report struct {
	Path     string
	Sections map[string]string
	// Broken are the items under "What looked broken", in order.
	Broken []string
	// Contamination is the reviewer's self-report, trimmed.
	Contamination string
}

// ParseReport reads a report and pulls out the parts the gate grades.
func ParseReport(path string) (*Report, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // path derived from the gate tree
	if err != nil {
		return nil, err
	}
	body := strings.ReplaceAll(string(raw), "\r\n", "\n")
	r := &Report{Path: path, Sections: map[string]string{}}

	idx := headingRe.FindAllStringSubmatchIndex(body, -1)
	for i, m := range idx {
		title := strings.TrimSpace(body[m[2]:m[3]])
		start := m[1]
		end := len(body)
		if i+1 < len(idx) {
			end = idx[i+1][0]
		}
		r.Sections[title] = strings.TrimSpace(body[start:end])
	}

	if s, ok := r.section(brokenSection); ok {
		for _, line := range strings.Split(s, "\n") {
			m := bulletRe.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			item := strings.TrimSpace(m[1])
			// The template's own placeholder is not a finding. A reviewer who
			// left it in has said nothing looked broken, which is a legitimate
			// answer and must not be turned into an unclosed item.
			if templateRe.MatchString(item) {
				continue
			}
			r.Broken = append(r.Broken, item)
		}
	}
	if s, ok := r.section(contaminationSection); ok {
		r.Contamination = strings.TrimSpace(s)
	}
	return r, nil
}

// section finds a heading by prefix, so "Verdict: would I trust..." matches
// "Verdict".
func (r *Report) section(prefix string) (string, bool) {
	for title, body := range r.Sections {
		if strings.HasPrefix(title, prefix) {
			return body, true
		}
	}
	return "", false
}

// Resolutions is the author's answer to the reviewer, one entry per item the
// reviewer listed as broken.
type Resolutions struct {
	Version int          `yaml:"version"`
	Items   []Resolution `yaml:"items"`
}

// Resolution closes one finding.
type Resolution struct {
	// Quote is the reviewer's item, verbatim. It is matched against the report,
	// so re-ordering or rewording the report invalidates the resolution rather
	// than silently re-pointing it at a different complaint.
	Quote string `yaml:"quote"`
	// Status is "fixed" or "accepted".
	Status string `yaml:"status"`
	// Reason is why. Required for both: "we fixed it" without saying how is not
	// reviewable, and "we accept it" without saying why is not a decision.
	Reason string `yaml:"reason"`
	// Evidence optionally points at the commit, artifact or frame that shows it.
	Evidence string `yaml:"evidence,omitempty"`
}

// Recognised resolution statuses.
const (
	StatusFixed    = "fixed"
	StatusAccepted = "accepted"
)

// LoadResolutions reads the author's answers. An absent file is not an error:
// a report with no findings needs none.
func LoadResolutions(path string) (*Resolutions, error) {
	f, err := os.Open(path) //nolint:gosec // path derived from the gate tree
	if err != nil {
		if os.IsNotExist(err) {
			return &Resolutions{Version: 1}, nil
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()
	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	var out Resolutions
	if err := dec.Decode(&out); err != nil {
		if errors.Is(err, io.EOF) {
			return &Resolutions{Version: 1}, nil
		}
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &out, nil
}

// Item is one graded finding in the outcome.
type Item struct {
	Quote    string `json:"quote"`
	Status   string `json:"status"`
	Reason   string `json:"reason,omitempty"`
	Evidence string `json:"evidence,omitempty"`
	Closed   bool   `json:"closed"`
	Detail   string `json:"detail,omitempty"`
}

// Outcome is G5's pass signal.
type Outcome struct {
	Schema      int    `json:"schema"`
	Pass        bool   `json:"pass"`
	Slug        string `json:"slug"`
	GeneratedAt string `json:"generated_at"`
	Tool        string `json:"tool,omitempty"`
	// ReportPresent is the first thing that has to be true. No report, no gate.
	ReportPresent bool `json:"report_present"`
	// Contamination is the reviewer's own answer, reproduced.
	Contamination string `json:"contamination"`
	// Contaminated is true when the reviewer said the brief was spoiled. A
	// contaminated review is not a weaker review, it is a different one, and
	// grading it would be grading a colleague being polite.
	Contaminated bool     `json:"contaminated"`
	Sections     []string `json:"sections_found"`
	Missing      []string `json:"sections_missing,omitempty"`
	Items        []Item   `json:"items"`
	Problems     []string `json:"problems,omitempty"`
}

// cleanContamination are the answers that mean "nothing leaked".
var cleanContamination = map[string]bool{"none": true, "n/a": true, "-": true, "nothing": true, "": true}

// Grade turns a report plus the author's resolutions into the gate's outcome.
func Grade(slug, tool string, r *Report, res *Resolutions, now time.Time) *Outcome {
	out := &Outcome{
		Schema:      OutcomeSchema,
		Slug:        slug,
		Tool:        tool,
		GeneratedAt: now.UTC().Format(time.RFC3339),
		Pass:        true,
	}
	if r == nil {
		out.Pass = false
		out.Problems = append(out.Problems,
			"no "+ReportFile+": G5 has not been run. A gate whose reviewer never reported is not a gate that passed — no transcript, not done")
		return out
	}
	out.ReportPresent = true

	for title := range r.Sections {
		out.Sections = append(out.Sections, title)
	}
	sortStrings(out.Sections)
	for _, want := range requiredSections {
		if _, ok := r.section(want); !ok {
			out.Missing = append(out.Missing, want)
		}
	}
	if len(out.Missing) > 0 {
		out.Pass = false
		out.Problems = append(out.Problems,
			"the report is missing required section(s): "+strings.Join(out.Missing, ", ")+
				" — each one is a question the reviewer was asked, and an unanswered question is not a clean answer")
	}

	out.Contamination = r.Contamination
	if !cleanContamination[strings.ToLower(strings.TrimSpace(r.Contamination))] {
		out.Contaminated = true
		out.Pass = false
		out.Problems = append(out.Problems,
			"the reviewer reported contamination: "+oneLine(r.Contamination)+
				" — the review must be redone with a clean brief, because a spoiled cold eye looks exactly like a real one")
	}

	byQuote := map[string]Resolution{}
	for _, item := range res.Items {
		byQuote[normalize(item.Quote)] = item
	}
	used := map[string]bool{}

	for _, quote := range r.Broken {
		it := Item{Quote: quote}
		resolution, ok := byQuote[normalize(quote)]
		switch {
		case !ok:
			it.Detail = "no resolution: the reviewer said this looked broken and nobody has said whether it was fixed or knowingly accepted"
		case resolution.Status != StatusFixed && resolution.Status != StatusAccepted:
			it.Status = resolution.Status
			it.Detail = fmt.Sprintf("status %q is not fixed|accepted", resolution.Status)
		case len(strings.TrimSpace(resolution.Reason)) < 20:
			it.Status, it.Reason = resolution.Status, resolution.Reason
			it.Detail = "the reason is not a real sentence; \"fixed\" without saying how is not reviewable and \"accepted\" without saying why is not a decision"
		default:
			it.Status, it.Reason, it.Evidence, it.Closed = resolution.Status, resolution.Reason, resolution.Evidence, true
		}
		used[normalize(quote)] = true
		if !it.Closed {
			out.Pass = false
			out.Problems = append(out.Problems, "unclosed finding: "+oneLine(quote)+" — "+it.Detail)
		}
		out.Items = append(out.Items, it)
	}

	for _, item := range res.Items {
		if used[normalize(item.Quote)] {
			continue
		}
		out.Pass = false
		out.Problems = append(out.Problems,
			"a resolution quotes something the report does not say: "+oneLine(item.Quote)+
				" — a resolution matched by verbatim quote cannot survive the report being reworded, which is the point")
	}
	return out
}

// Write persists outcome.json and outcome.md.
func Write(dir string, o *Outcome) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(filepath.Join(dir, OutcomeJSONFile), raw, 0o644); err != nil { //nolint:gosec // committed artifact
		return err
	}
	return os.WriteFile(filepath.Join(dir, OutcomeMDFile), []byte(o.Markdown()), 0o644) //nolint:gosec // committed artifact
}

// Markdown renders outcome.md.
func (o *Outcome) Markdown() string {
	var b strings.Builder
	if o.Pass {
		fmt.Fprintf(&b, "# G5 COLD-EYE — %s — PASS\n\n", o.Slug)
	} else {
		fmt.Fprintf(&b, "# G5 COLD-EYE — %s — FAIL (%d problem(s))\n\n", o.Slug, len(o.Problems))
	}
	b.WriteString("A reviewer was given the built binary and one sentence. This file grades what\n")
	b.WriteString("came back: the report exists, it answers every question it was asked, the\n")
	b.WriteString("reviewer says the brief was not spoiled, and every item they called broken is\n")
	b.WriteString("either fixed or knowingly accepted in writing.\n\n")
	fmt.Fprintf(&b, "- **Report present:** %v\n", o.ReportPresent)
	fmt.Fprintf(&b, "- **Contamination self-report:** %s\n", orDash(oneLine(o.Contamination)))
	fmt.Fprintf(&b, "- **Findings:** %d\n", len(o.Items))
	fmt.Fprintf(&b, "- **Generated:** %s\n\n", o.GeneratedAt)

	if len(o.Problems) > 0 {
		b.WriteString("## Why this gate did not pass\n\n")
		for _, p := range o.Problems {
			fmt.Fprintf(&b, "- %s\n", p)
		}
		b.WriteString("\n")
	}

	if len(o.Items) == 0 {
		b.WriteString("## Findings\n\nThe reviewer listed nothing under \"what looked broken\".\n")
		return b.String()
	}
	b.WriteString("## Findings, and what was done about each\n\n")
	b.WriteString("| the reviewer said | status | why | evidence |\n")
	b.WriteString("|-------------------|--------|-----|----------|\n")
	for _, it := range o.Items {
		status := it.Status
		if !it.Closed {
			status = "**UNCLOSED**"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n",
			cell(it.Quote), orDash(status), cell(firstNonEmpty(it.Reason, it.Detail)), cell(it.Evidence))
	}
	b.WriteString("\n")
	return b.String()
}

func normalize(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

func cell(s string) string {
	s = strings.ReplaceAll(oneLine(s), "|", "\\|")
	if len(s) > 180 {
		s = s[:177] + "…"
	}
	if s == "" {
		return "—"
	}
	return s
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
