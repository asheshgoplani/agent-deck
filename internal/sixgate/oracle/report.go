package oracle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Write persists parity.json, parity.md and must-label.json.
func Write(dir string, p *Parity, tool string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(dir, ParityJSONFile), p); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(dir, MustLabelJSONFile), p.Labels(tool)); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, ParityMDFile), []byte(p.Markdown()), 0o644) //nolint:gosec // committed artifact
}

func writeJSON(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(path, raw, 0o644) //nolint:gosec // committed artifact
}

// Digest returns the SHA-256 of a file, or "" when it is absent.
//
// It is what couples G4 to G2: G2 records the digest of the must-label list it
// obeyed, and `verdict --check` compares that against the file on disk. Without
// it, regenerating the oracle after the assert run would leave a results.json
// that satisfied an older, weaker contract and nobody would notice.
func Digest(path string) (string, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // path derived from the gate tree
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// Markdown renders parity.md. Its first line is the verdict, and the table's
// last column is always the evidence the number was read from, so a reviewer
// can check the arithmetic against the recorded frame without running anything.
func (p *Parity) Markdown() string {
	var b strings.Builder
	if p.Pass {
		fmt.Fprintf(&b, "# G4 ORACLE — %s — PASS\n\n", p.Slug)
	} else {
		fmt.Fprintf(&b, "# G4 ORACLE — %s — FAIL (%d problem(s))\n\n", p.Slug, len(p.Problems))
	}
	fmt.Fprintf(&b, "> %s\n\n", p.Sentence)
	b.WriteString("Every figure below ends in exactly one of three places: it agrees with a number\n")
	b.WriteString("somebody else produced, it disagrees and the disagreement is written down, or it\n")
	b.WriteString("has no oracle at all — in which case the screen must say so, and G2 asserts that\n")
	b.WriteString("it does. A number with no oracle and no on-screen label is a failure here, not a\n")
	b.WriteString("footnote.\n\n")

	fmt.Fprintf(&b, "- **Figures:** %d — %d agree, %d drift, %d unoracled, %d missing from our own screen, %d unmatched in the oracle, %d errors\n",
		p.Totals.Figures, p.Totals.Agree, p.Totals.Drift, p.Totals.NoOracle,
		p.Totals.OursMissing, p.Totals.TheirsAbsent, p.Totals.Errors)
	fmt.Fprintf(&b, "- **Actually compared:** %d (the declaration requires at least %d)\n", p.Compared, p.MinCompared)
	fmt.Fprintf(&b, "- **Generated:** %s\n", p.GeneratedAt)
	if p.Tool != "" {
		fmt.Fprintf(&b, "- **Tool:** %s\n", p.Tool)
	}
	b.WriteString("\n")

	if len(p.Problems) > 0 {
		b.WriteString("## Why this gate did not pass\n\n")
		for _, pr := range p.Problems {
			fmt.Fprintf(&b, "- %s\n", pr)
		}
		b.WriteString("\n")
	}

	for _, c := range p.Cases {
		fmt.Fprintf(&b, "## case `%s` — %s\n\n", c.ID, passWord(c.Pass))
		fmt.Fprintf(&b, "- **Oracle:** %s\n", c.Oracle.Name)
		fmt.Fprintf(&b, "- **Strength:** `%s` — %s\n", c.Oracle.Strength, StrengthNote(c.Oracle.Strength))
		fmt.Fprintf(&b, "- **Consent to collect:** %s · **mode:** `%s`\n", c.Oracle.Consent, c.Oracle.Collect)
		if len(c.Oracle.Command) > 0 {
			fmt.Fprintf(&b, "- **Collection argv (human-invoked only, no shell):** `%s`\n", strings.Join(c.Oracle.Command, " "))
		}
		fmt.Fprintf(&b, "- **Oracle file:** `%s` — %s\n", c.Oracle.Path, presentWord(c.OraclePresent))
		fmt.Fprintf(&b, "- **Our numbers read from:** `%s`\n", filepath.Base(c.OursPath))
		fmt.Fprintf(&b, "- **Limits, in the author's words:** %s\n\n", strings.Join(strings.Fields(c.Oracle.Note), " "))

		b.WriteString("| figure | what | ours | theirs | Δ | Δ% | allowed | verdict | read from |\n")
		b.WriteString("|--------|------|------|--------|---|----|---------|---------|-----------|\n")
		for _, r := range c.Rows {
			fmt.Fprintf(&b, "| `%s` | %s | %s | %s | %s | %s | %s | %s | `%s` |\n",
				r.Figure, cell(r.What), num(r.Ours), num(r.Theirs),
				deltaCell(r), pctCell(r), allowedCell(r), verdictCell(r), cell(evidence(r)))
		}
		b.WriteString("\n")

		explained := false
		for _, r := range c.Rows {
			if r.Detail == "" {
				continue
			}
			if !explained {
				b.WriteString("Notes:\n\n")
				explained = true
			}
			fmt.Fprintf(&b, "- **`%s`** — %s\n", r.Figure, r.Detail)
		}
		if explained {
			b.WriteString("\n")
		}
		for _, r := range c.Rows {
			if len(r.Theirs.Parts) > 1 {
				fmt.Fprintf(&b, "- `%s` oracle side is a sum of the terms `%s`, added by this comparator rather than by the software under test.\n",
					r.Figure, strings.Join(r.Theirs.Parts, "` + `"))
			}
		}
		b.WriteString("\n")
	}

	b.WriteString("## Must be labelled an estimate on screen\n\n")
	if len(p.MustLabel) == 0 {
		b.WriteString("None: every declared figure had an oracle.\n\n")
		return b.String()
	}
	b.WriteString("These figures have no source of truth. That is allowed, and it is allowed only\n")
	b.WriteString("because the user interface admits it. `must-label.json` carries this list to G2,\n")
	b.WriteString("which asserts each pattern against the named recorded frames; `sixgate verdict\n")
	b.WriteString("--check` compares the digest G2 obeyed against the file written here, so the two\n")
	b.WriteString("gates cannot drift apart.\n\n")
	b.WriteString("| figure | must appear on screen | on frames | why |\n")
	b.WriteString("|--------|-----------------------|-----------|-----|\n")
	for _, m := range p.MustLabel {
		frames := make([]string, 0, len(m.Frames))
		for _, f := range m.Frames {
			frames = append(frames, f.Fixture+"/"+f.Step)
		}
		fmt.Fprintf(&b, "| `%s` | `%s` | %s | %s |\n",
			m.Figure, cell(m.Pattern), cell(strings.Join(frames, ", ")), cell(strings.Join(strings.Fields(m.Why), " ")))
	}
	b.WriteString("\n")
	return b.String()
}

func evidence(r Row) string {
	if r.Ours.Evidence != "" {
		return r.Ours.Evidence
	}
	return "—"
}

func num(r Reading) string {
	if !r.Found {
		if r.Error != "" {
			return "error"
		}
		return "—"
	}
	if r.Raw != "" {
		return fmt.Sprintf("%.0f (`%s`)", r.Value, r.Raw)
	}
	return fmt.Sprintf("%.0f", r.Value)
}

func deltaCell(r Row) string {
	if r.Verdict != VerdictAgree && r.Verdict != VerdictDrift {
		return "—"
	}
	return fmt.Sprintf("%+.0f", r.Delta)
}

func pctCell(r Row) string {
	if r.Verdict != VerdictAgree && r.Verdict != VerdictDrift {
		return "—"
	}
	return fmt.Sprintf("%+.2f%%", r.DeltaPct)
}

func allowedCell(r Row) string {
	if r.Verdict != VerdictAgree && r.Verdict != VerdictDrift {
		return "—"
	}
	return fmt.Sprintf("±%.0f", r.Allowed)
}

func verdictCell(r Row) string {
	if r.Pass {
		return string(r.Verdict)
	}
	return "**" + string(r.Verdict) + "**"
}

func presentWord(ok bool) string {
	if ok {
		return "present"
	}
	return "**not collected on this host**"
}

func passWord(ok bool) string {
	if ok {
		return "PASS"
	}
	return "FAIL"
}

func cell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 150 {
		s = s[:147] + "…"
	}
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}
