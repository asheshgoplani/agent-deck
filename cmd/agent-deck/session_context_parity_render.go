package main

import (
	"fmt"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/ctxinspect/verify"
)

// renderContextParity renders the side-by-side comparison of an agent-deck
// report against the harness's own accounting.
//
// The table answers one question per row: do these two accountings of the same
// context agree, and if not by how much. Every column that could be mistaken
// for a claim it is not — a group that was not graded, a lower-bound side, a
// row the harness printed that nothing claimed — is stated rather than omitted.
func renderContextParity(v contextView, spec verify.Spec, p *verify.Parity) string {
	var b strings.Builder

	writeContextHeader(&b, v)
	b.WriteString(fmt.Sprintf("\nlive parity — this report against %s, read back from the session's own pane\n\n", spec.Command))

	rows := [][]string{{"GROUP", "HARNESS", "AGENT-DECK", "DELTA", "TOLERANCE", "VERDICT"}}
	for _, r := range p.Rows {
		rows = append(rows, []string{
			r.Group,
			parityFigure(r.Harness, r.HarnessKnown, true),
			parityFigure(r.Ours, r.OursKnown, r.OursComplete),
			parityDelta(r),
			parityTolerance(r),
			r.Verdict.String(),
		})
	}
	writeContextTable(&b, rows, "  ")

	b.WriteString("\n  verdict: " + parityVerdictLine(p) + "\n")
	b.WriteString("  window:  " + p.WindowNote + "\n")

	b.WriteString("\n  what each group compares:\n")
	for _, r := range p.Rows {
		if strings.TrimSpace(r.Note) == "" {
			continue
		}
		b.WriteString("    " + r.Group + ": " + r.Note + "\n")
	}

	if len(p.Unmapped) > 0 {
		b.WriteString("\n  rows the panel printed that no group claims — a new or renamed row shows up here first:\n")
		for _, u := range p.Unmapped {
			b.WriteString("    " + u + "\n")
		}
	}
	if h := p.Harness; h != nil && len(h.Unrecognized) > 0 {
		b.WriteString("\n  lines that looked like a figure and did not parse — a format change shows up here:\n")
		for _, u := range h.Unrecognized {
			b.WriteString("    " + u + "\n")
		}
	}

	b.WriteString("\n  " + parityToleranceFooter(p.Tolerance) + "\n")
	b.WriteString("  " + spec.Note + "\n")
	return b.String()
}

// parityFigure renders one side's number, marking a lower bound as one and an
// absent figure as absent rather than as zero.
func parityFigure(tokens int, known, complete bool) string {
	if !known {
		return "—"
	}
	if !complete {
		return "≥" + formatTokenAmount(tokens)
	}
	return formatTokenAmount(tokens)
}

// parityDelta renders the signed difference, or a dash when the row was not
// graded.
func parityDelta(r verify.Row) string {
	if !r.HarnessKnown || !r.OursKnown {
		return "—"
	}
	sign := "+"
	if r.Delta < 0 {
		sign = "−"
	}
	d := r.Delta
	if d < 0 {
		d = -d
	}
	if r.Harness == 0 {
		return sign + formatTokenAmount(d)
	}
	return fmt.Sprintf("%s%s (%+.1f%%)", sign, formatTokenAmount(d), r.DeltaPct)
}

// parityTolerance renders the band applied to a row.
func parityTolerance(r verify.Row) string {
	if r.Verdict == verify.VerdictInformational || !r.HarnessKnown || !r.OursKnown {
		return "—"
	}
	return "±" + formatTokenAmount(r.Allowed)
}

// parityVerdictLine states the overall outcome in words, including the case
// where nothing could be graded — which must not read like agreement.
func parityVerdictLine(p *verify.Parity) string {
	switch p.Status {
	case verify.StatusMatch:
		return "MATCH — every comparable group agrees with the harness's own accounting within tolerance"
	case verify.StatusDrift:
		names := make([]string, 0, len(p.Rows))
		for _, r := range p.Drifted() {
			names = append(names, r.Group)
		}
		return "DRIFT — these groups disagree beyond tolerance: " + strings.Join(names, ", ")
	default:
		return "INDETERMINATE — the panel published nothing this report could be compared against; no agreement is claimed"
	}
}

// parityToleranceFooter states the band that was applied and why it exists.
func parityToleranceFooter(t verify.Tolerance) string {
	return fmt.Sprintf("tolerance: ±%s or ±%.0f%%, whichever is larger, plus the harness's own printed rounding; "+
		"per-item figures on the agent-deck side are character-count estimates, not tokenizer output",
		formatTokenAmount(t.AbsTokens), t.Pct)
}
