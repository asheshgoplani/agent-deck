package matrix

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/sixgate/lint"
)

// ResultSchema is the matrix.json schema version.
const ResultSchema = 1

// Artifact filenames inside the G3 gate directory.
const (
	ResultJSONFile     = "matrix.json"
	ResultMarkdownFile = "matrix.md"
)

// Row outcomes. Every row ends on exactly one of these, and none of them means
// "we did not look".
const (
	// OutcomeOK: the row ran and behaved exactly as it declared it would.
	OutcomeOK = "ok"
	// OutcomeDivergence: the journey stopped holding somewhere other than where
	// the row said it would — earlier, later, or when it claimed it would hold
	// throughout.
	OutcomeDivergence = "divergence"
	// OutcomeBlank: the Blank Detector fired. A row can declare that its world
	// looks different; it can never declare that a figure may be missing.
	OutcomeBlank = "blank"
	// OutcomeTimeout: the row exceeded its wall-time cap. It is recorded as a
	// result, never as a skip: a row that ran out of time has not passed.
	OutcomeTimeout = "timeout"
	// OutcomeError: the row could not be run at all.
	OutcomeError = "error"
	// OutcomeNotRun: the run stopped before reaching this row, because an
	// earlier row timed out and did not unwind. Naming it is the point — a
	// matrix that quietly renders 12 of 18 rows reads like a matrix of 12.
	OutcomeNotRun = "not-run"
)

// FixtureProvenance records where a row's world came from and when it was
// recorded, so a matrix that has quietly gone stale says so.
type FixtureProvenance struct {
	// Name is the corpus case, or a description for a synthesized world.
	Name string `json:"name"`
	// Source is the repo-relative directory the case lives in, when it has one.
	Source string `json:"source,omitempty"`
	// RecordedAt is when the fixture last changed in version control. It is the
	// honest answer to "how old is this evidence": a corpus case recorded two
	// years ago against a harness format that has moved on is reassuring and
	// wrong, and only a date makes that visible.
	RecordedAt string `json:"recorded_at,omitempty"`
	// PinnedNow is the timestamp fixture-driven reports are stamped with, which
	// is what makes their numbers reproducible. It is NOT when the fixture was
	// recorded, and the two are kept apart deliberately.
	PinnedNow string `json:"pinned_now,omitempty"`
	// Synthesized marks a world built by the harness rather than recorded from
	// a real session — the cold first run has no transcript to record.
	Synthesized bool `json:"synthesized,omitempty"`
}

// RowResult is one row's line in the matrix.
type RowResult struct {
	ID   string `json:"id"`
	Note string `json:"note"`

	Adapter      string `json:"adapter"`
	SessionState string `json:"session_state"`
	DataSize     string `json:"data_size"`
	Config       string `json:"config"`
	Terminal     string `json:"terminal"`
	Driver       string `json:"driver"`

	Fixture FixtureProvenance `json:"fixture"`

	// Outcome is one of the Outcome* constants.
	Outcome string `json:"outcome"`
	// Artifact is the repo-relative directory holding this row's frames,
	// transcript and run.json.
	Artifact string `json:"artifact,omitempty"`

	// StepsWithFrames and StepsExpected count capture steps: how many produced
	// a frame, out of how many the journey asks for.
	StepsWithFrames int `json:"steps_with_frames"`
	StepsExpected   int `json:"steps_expected"`

	// BlankFindings are Blank Detector hits on ANY frame this row produced,
	// including the first-run modals a scripted step never names.
	BlankFindings []lint.Finding `json:"blank_findings,omitempty"`

	// ExpectedDivergence is the step the row declared, empty for a row that
	// claimed the whole journey holds. ActualDivergence is what happened.
	ExpectedDivergence string `json:"expected_divergence,omitempty"`
	ActualDivergence   string `json:"actual_divergence,omitempty"`
	// Why is the row's written reason for a declared divergence.
	Why string `json:"why,omitempty"`

	DurationMS int `json:"duration_ms"`
	BudgetMS   int `json:"budget_ms"`
	// TimedOut and Unwound describe a row that exceeded its cap: whether it
	// then finished unwinding, which is the difference between "slow" and
	// "there may be a tmux server still alive on this machine".
	TimedOut bool `json:"timed_out,omitempty"`
	Unwound  bool `json:"unwound,omitempty"`

	// TmuxServersSpawned and PTYDelta are copied from the row's own run.json so
	// the matrix can be read for fleet safety without opening 18 files.
	TmuxServersSpawned int  `json:"tmux_servers_spawned"`
	PTYDelta           int  `json:"pty_delta"`
	TeardownVerified   bool `json:"teardown_verified,omitempty"`

	// Failures are the reasons this row is not ok.
	Failures []string `json:"failures,omitempty"`
}

// SeamRow is one A-versus-B comparison.
type SeamRow struct {
	A   string `json:"a"`
	B   string `json:"b"`
	Why string `json:"why"`
	// Report is the repo-relative path of the full comparison.
	Report string `json:"report,omitempty"`
	// FramesCompared is how many frames both rows produced.
	FramesCompared int `json:"frames_compared"`
	// NumericDisagreements counts lines that say the same thing with different
	// figures. Those are the ones that matter: a number that moves when nothing
	// but the driver changed is either a bug or an undocumented dependency.
	NumericDisagreements int `json:"numeric_disagreements"`
	// Findings are those lines, quoted.
	Findings []SeamFinding `json:"findings,omitempty"`
	// Note explains a comparison that could not be made.
	Note string `json:"note,omitempty"`
}

// SeamFinding is one line two drivers rendered with different figures.
type SeamFinding struct {
	Frame string `json:"frame"`
	A     string `json:"a"`
	B     string `json:"b"`
}

// Totals summarises the run.
type Totals struct {
	Rows          int `json:"rows"`
	OK            int `json:"ok"`
	Divergence    int `json:"divergence"`
	Blank         int `json:"blank"`
	Timeout       int `json:"timeout"`
	Errored       int `json:"error"`
	NotRun        int `json:"not_run"`
	BlankFindings int `json:"blank_findings"`
	DriverBRows   int `json:"driver_b_rows"`
}

// Result is G3's artifact.
type Result struct {
	Schema int `json:"schema"`
	// Pass is what SIXGATE's verdict reads.
	Pass        bool   `json:"pass"`
	Slug        string `json:"slug"`
	Sentence    string `json:"sentence,omitempty"`
	GeneratedAt string `json:"generated_at"`
	// Harness is the sixgate build that ran the matrix. Rows are only
	// comparable across runs of the same harness, so it is recorded per file
	// and repeated per row's own run.json.
	Harness  string `json:"harness"`
	GitSHA   string `json:"git_sha,omitempty"`
	GitDirty bool   `json:"git_dirty,omitempty"`

	Coverage   []AxisCoverage `json:"coverage"`
	Exclusions []Exclusion    `json:"exclusions,omitempty"`

	Rows  []RowResult `json:"rows"`
	Seams []SeamRow   `json:"seams,omitempty"`

	Totals Totals `json:"totals"`

	// Aborted, when set, is why the run stopped before every row was reached.
	Aborted string `json:"aborted,omitempty"`
	// LeakRisk is the alarm: a row exceeded its cap and did NOT finish
	// unwinding, so a tmux server may still be alive on this host. It is a
	// top-level field rather than a note inside a row because it is the one
	// fact a reader must not have to go looking for.
	LeakRisk bool `json:"leak_risk,omitempty"`
	// PTYBefore and PTYAfter bracket the whole matrix run.
	PTYBefore int `json:"pty_before"`
	PTYAfter  int `json:"pty_after"`
}

// Finish computes the totals and the pass signal.
//
// Pass is deliberately conjunctive: every row ok, no row unrun, no leak risk. A
// matrix that renders beautifully while six rows never executed is the exact
// shape of evidence this framework exists to refuse.
func (r *Result) Finish() {
	r.Totals = Totals{}
	for _, row := range r.Rows {
		r.Totals.Rows++
		r.Totals.BlankFindings += len(row.BlankFindings)
		if row.Driver == DriverB {
			r.Totals.DriverBRows++
		}
		switch row.Outcome {
		case OutcomeOK:
			r.Totals.OK++
		case OutcomeDivergence:
			r.Totals.Divergence++
		case OutcomeBlank:
			r.Totals.Blank++
		case OutcomeTimeout:
			r.Totals.Timeout++
		case OutcomeError:
			r.Totals.Errored++
		case OutcomeNotRun:
			r.Totals.NotRun++
		}
	}
	r.Pass = r.Totals.Rows > 0 &&
		r.Totals.OK == r.Totals.Rows &&
		!r.LeakRisk &&
		r.Aborted == ""
}

// Write persists matrix.json and matrix.md.
func Write(dir string, r *Result) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(filepath.Join(dir, ResultJSONFile), raw, 0o644); err != nil { //nolint:gosec // committed artifact
		return err
	}
	return os.WriteFile(filepath.Join(dir, ResultMarkdownFile), []byte(r.Markdown()), 0o644) //nolint:gosec // committed artifact
}

// Markdown renders matrix.md: the ten-second read.
//
// The column order is fixed by what a reader needs first — outcome, then which
// world, then where it diverged — and the table is deliberately the FIRST thing
// in the document. Everything explanatory sits below it, because a report whose
// verdict is on page two is a report that gets skimmed to the wrong conclusion.
func (r *Result) Markdown() string {
	var b strings.Builder

	if r.Pass {
		fmt.Fprintf(&b, "# G3 MATRIX — %s — PASS (%d/%d rows)\n\n", r.Slug, r.Totals.OK, r.Totals.Rows)
	} else {
		fmt.Fprintf(&b, "# G3 MATRIX — %s — FAIL (%d/%d rows ok)\n\n", r.Slug, r.Totals.OK, r.Totals.Rows)
	}
	if r.LeakRisk {
		b.WriteString("> **LEAK RISK.** A row exceeded its wall-time cap and did not finish unwinding.\n")
		b.WriteString("> A tmux server may still be alive on this host. Check the row's `run.json` for its\n")
		b.WriteString("> socket path and verify by socket path only — never by process name, which is what\n")
		b.WriteString("> destroyed this machine's session fleet on 2026-07-26.\n\n")
	}
	if r.Aborted != "" {
		fmt.Fprintf(&b, "> **Run stopped early:** %s\n\n", r.Aborted)
	}
	if r.Sentence != "" {
		fmt.Fprintf(&b, "> %s\n\n", r.Sentence)
	}

	b.WriteString("| outcome | row | drv | term | frames | blank-lint | first divergent step | artifact |\n")
	b.WriteString("|---------|-----|-----|------|--------|-----------|----------------------|----------|\n")
	for _, row := range r.Rows {
		fmt.Fprintf(&b, "| %s | `%s` | %s | %s | %d/%d | %s | %s | %s |\n",
			outcomeCell(row.Outcome),
			row.ID,
			row.Driver,
			row.Terminal,
			row.StepsWithFrames, row.StepsExpected,
			blankCell(len(row.BlankFindings)),
			divergenceCell(row),
			artifactCell(row.Artifact),
		)
	}
	b.WriteString("\n")

	fmt.Fprintf(&b, "- **Harness:** %s", r.Harness)
	if r.GitSHA != "" {
		fmt.Fprintf(&b, " at `%s`%s", r.GitSHA, dirtySuffix(r.GitDirty))
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "- **Generated:** %s\n", r.GeneratedAt)
	fmt.Fprintf(&b, "- **Driver B rows:** %d (the shipped binary in a real pane; the rest read the real model in process)\n", r.Totals.DriverBRows)
	fmt.Fprintf(&b, "- **PTYs across the whole run:** %d → %d (%+d). Only growth is this harness's fault.\n",
		r.PTYBefore, r.PTYAfter, r.PTYAfter-r.PTYBefore)
	fmt.Fprintf(&b, "- **Blank Detector findings:** %d — a row may declare that its world looks different; it may never declare that a figure is allowed to be missing.\n\n",
		r.Totals.BlankFindings)

	b.WriteString("## How to read `first divergent step`\n\n")
	b.WriteString("Most rows change the world in a way that legitimately changes the screen: a session\n")
	b.WriteString("with no memory files cannot show a memory-files category, and an 80-column terminal\n")
	b.WriteString("cannot paint a 28-cell gauge. Each row therefore DECLARES, in `matrix.yaml` and in\n")
	b.WriteString("advance, the step where it expects the journey to stop holding, and the runner\n")
	b.WriteString("asserts the divergence happens exactly there. `expected` in the column below means\n")
	b.WriteString("the row diverged where it said it would — which is a stricter claim than passing,\n")
	b.WriteString("because a row that starts failing one step earlier fails the gate.\n\n")

	r.writeRowDetail(&b)
	r.writeSeams(&b)
	r.writeCoverage(&b)
	return b.String()
}

func (r *Result) writeRowDetail(b *strings.Builder) {
	b.WriteString("## Rows\n\n")
	for _, row := range r.Rows {
		fmt.Fprintf(b, "### `%s` — %s\n\n", row.ID, strings.ToUpper(row.Outcome))
		fmt.Fprintf(b, "%s\n\n", row.Note)
		fmt.Fprintf(b, "- **world:** adapter `%s` · state `%s` · data `%s` · config `%s` · terminal `%s` · driver `%s`\n",
			row.Adapter, row.SessionState, row.DataSize, row.Config, row.Terminal, row.Driver)
		fmt.Fprintf(b, "- **fixture:** %s\n", fixtureLine(row.Fixture))
		if row.ExpectedDivergence != "" {
			fmt.Fprintf(b, "- **declared divergence at `%s`:** %s\n", row.ExpectedDivergence, strings.TrimSpace(row.Why))
		} else if row.Outcome != OutcomeNotRun {
			b.WriteString("- **declared:** the whole journey holds in this world\n")
		}
		if row.ActualDivergence != "" {
			fmt.Fprintf(b, "- **actually diverged at:** `%s`\n", row.ActualDivergence)
		}
		if row.Outcome != OutcomeNotRun {
			fmt.Fprintf(b, "- **time:** %d ms of a %d ms budget%s\n", row.DurationMS, row.BudgetMS, timeoutSuffix(row))
			fmt.Fprintf(b, "- **fleet:** %d tmux server(s) spawned, PTY delta %+d%s\n",
				row.TmuxServersSpawned, row.PTYDelta, teardownSuffix(row))
		}
		if row.Artifact != "" {
			fmt.Fprintf(b, "- **artifact:** `%s`\n", row.Artifact)
		}
		b.WriteString("\n")

		if len(row.BlankFindings) > 0 {
			b.WriteString("The Blank Detector fired on this row. Nobody wrote an assertion for these:\n\n")
			b.WriteString("| frame | line | rule | text |\n|-------|------|------|------|\n")
			for _, f := range row.BlankFindings {
				fmt.Fprintf(b, "| `%s` | %d | `%s` | `%s` |\n", f.Frame, f.Line, f.Rule, mdCell(strings.TrimSpace(f.Text)))
			}
			b.WriteString("\n")
		}
		for _, f := range row.Failures {
			fmt.Fprintf(b, "- **fail:** %s\n", f)
		}
		if len(row.Failures) > 0 {
			b.WriteString("\n")
		}
	}
}

func (r *Result) writeSeams(b *strings.Builder) {
	if len(r.Seams) == 0 {
		return
	}
	b.WriteString("## Seam divergence — A versus B\n\n")
	b.WriteString("Where the in-process driver and the shipped binary run the same script against the\n")
	b.WriteString("same recorded case, every line they disagree about is a fact nobody had to think to\n")
	b.WriteString("look for. Only NUMERIC disagreements are counted here — two lines identical except\n")
	b.WriteString("for their digits — because a number that moves when nothing but the driver changed\n")
	b.WriteString("is either a bug or a dependency nobody documented.\n\n")
	b.WriteString("| A | B | frames compared | numeric disagreements | report |\n")
	b.WriteString("|---|---|-----------------|-----------------------|--------|\n")
	for _, s := range r.Seams {
		fmt.Fprintf(b, "| `%s` | `%s` | %d | %d | %s |\n",
			s.A, s.B, s.FramesCompared, s.NumericDisagreements, artifactCell(s.Report))
	}
	b.WriteString("\n")
	for _, s := range r.Seams {
		if s.Note != "" {
			fmt.Fprintf(b, "- `%s` vs `%s`: %s\n", s.A, s.B, s.Note)
		}
		for _, f := range s.Findings {
			fmt.Fprintf(b, "- **`%s`** in `%s`:\n\n```\nA: %s\nB: %s\n```\n\n", f.Frame, s.A, f.A, f.B)
		}
	}
	b.WriteString("\n")
}

func (r *Result) writeCoverage(b *strings.Builder) {
	b.WriteString("## Coverage and declared gaps\n\n")
	b.WriteString("`unaccounted` is the only interesting column: a value that the axes declare, no row\n")
	b.WriteString("uses, and no exclusion explains is a gap nobody decided on.\n\n")
	b.WriteString("| axis | covered | excluded (with a reason) | unaccounted |\n")
	b.WriteString("|------|---------|--------------------------|-------------|\n")
	for _, c := range r.Coverage {
		var ex []string
		for _, e := range c.Excluded {
			ex = append(ex, e.Value)
		}
		fmt.Fprintf(b, "| `%s` | %s | %s | %s |\n",
			c.Axis, joinCode(c.Covered), joinCode(ex), boldIfAny(c.Undeclared))
	}
	b.WriteString("\n")
	if len(r.Exclusions) > 0 {
		b.WriteString("### Why each gap exists\n\n")
		for _, e := range r.Exclusions {
			fmt.Fprintf(b, "- **`%s: %s`** — %s\n", e.Axis, e.Value, strings.TrimSpace(e.Why))
		}
		b.WriteString("\n")
	}
}

func outcomeCell(o string) string {
	switch o {
	case OutcomeOK:
		return "ok"
	case OutcomeNotRun:
		return "**not-run**"
	default:
		return "**" + o + "**"
	}
}

func blankCell(n int) string {
	if n == 0 {
		return "clean"
	}
	return fmt.Sprintf("**%d**", n)
}

func divergenceCell(row RowResult) string {
	switch {
	case row.Outcome == OutcomeNotRun:
		return "—"
	case row.ActualDivergence == "" && row.ExpectedDivergence == "":
		return "none"
	case row.ActualDivergence == row.ExpectedDivergence:
		return "`" + row.ActualDivergence + "` (expected)"
	case row.ActualDivergence == "":
		return "none, but `" + row.ExpectedDivergence + "` was declared"
	default:
		return "**`" + row.ActualDivergence + "`**"
	}
}

func artifactCell(p string) string {
	if p == "" {
		return "—"
	}
	return "[dir](" + relLink(p) + ")"
}

// relLink turns a repo-relative artifact path into a link that resolves from
// matrix.md's own directory, which is where a reader clicks it.
func relLink(p string) string {
	const marker = "G3-matrix/"
	if i := strings.Index(p, marker); i >= 0 {
		return p[i+len(marker):]
	}
	return p
}

func fixtureLine(f FixtureProvenance) string {
	var parts []string
	if f.Name != "" {
		parts = append(parts, "`"+f.Name+"`")
	}
	if f.Synthesized {
		parts = append(parts, "synthesized by the harness (a machine that has never run the software has no transcript to record)")
	}
	if f.Source != "" {
		parts = append(parts, "from `"+f.Source+"`")
	}
	if f.RecordedAt != "" {
		parts = append(parts, "recorded "+f.RecordedAt)
	}
	if f.PinnedNow != "" {
		parts = append(parts, "reports stamped "+f.PinnedNow)
	}
	if len(parts) == 0 {
		return "—"
	}
	return strings.Join(parts, ", ")
}

func timeoutSuffix(row RowResult) string {
	if !row.TimedOut {
		return ""
	}
	if row.Unwound {
		return " — **exceeded the cap**, then unwound cleanly, so later rows were safe to run"
	}
	return " — **exceeded the cap and did NOT unwind**"
}

func teardownSuffix(row RowResult) string {
	if row.Driver != DriverB {
		return ""
	}
	if row.TeardownVerified {
		return ", teardown verified (`tmux -L … ls` failed as required)"
	}
	return ", **teardown NOT verified**"
}

func joinCode(vs []string) string {
	if len(vs) == 0 {
		return "—"
	}
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, "`"+v+"`")
	}
	return strings.Join(out, " ")
}

func boldIfAny(vs []string) string {
	if len(vs) == 0 {
		return "—"
	}
	return "**" + strings.Join(vs, ", ") + "**"
}

func dirtySuffix(dirty bool) string {
	if dirty {
		return " (working tree dirty)"
	}
	return ""
}

func mdCell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 120 {
		s = s[:117] + "…"
	}
	if s == "" {
		return "—"
	}
	return s
}
