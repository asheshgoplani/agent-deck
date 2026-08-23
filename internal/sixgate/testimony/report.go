package testimony

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Write persists testimony.json and testimony.md. The transcript, which the
// driver alone can narrate (it typed the questions and watched the clock), is
// written by the driver into the same directory.
func Write(dir string, r *Report) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(filepath.Join(dir, ReportJSONFile), raw, 0o644); err != nil { //nolint:gosec // committed artifact
		return err
	}
	return os.WriteFile(filepath.Join(dir, ReportMDFile), []byte(r.Markdown()), 0o644) //nolint:gosec // committed artifact
}

// Markdown renders testimony.md. Its first line is the verdict; its header
// states the identity-only scope before a single row can be misread as a
// token check.
func (r *Report) Markdown() string {
	var b strings.Builder
	if r.Pass {
		fmt.Fprintf(&b, "# G4b TESTIMONY — %s — PASS\n\n", r.Slug)
	} else {
		fmt.Fprintf(&b, "# G4b TESTIMONY — %s — FAIL (%d problem(s))\n\n", r.Slug, len(r.Problems))
	}
	fmt.Fprintf(&b, "> **Scope:** %s\n\n", r.Scope)
	b.WriteString("The oracle here is the probe session's own agent: its context is literally in\n")
	b.WriteString("front of it, so it can be asked for CHECKABLE STRINGS — quote the first line of\n")
	b.WriteString("a file, name the files you see, say LISTED or LOADED — and graded against what\n")
	b.WriteString("the inspector claims. Testimony is not measurement and agents can be wrong: a\n")
	b.WriteString("disagreement below is a finding to investigate, never automatically an\n")
	b.WriteString("inspector bug.\n\n")

	agree, disagree, unver := 0, 0, 0
	for _, row := range r.Rows {
		switch row.Verdict {
		case VerdictAgree:
			agree++
		case VerdictDisagree:
			disagree++
		default:
			unver++
		}
	}
	fmt.Fprintf(&b, "- **Probe:** `%s` (harness `%s`), launched in `%s` — disposable, created for this run, torn down at its end\n",
		r.Probe.Title, r.Probe.Harness, r.Probe.Workdir)
	fmt.Fprintf(&b, "- **Lifecycle CLI:** `%s` · **Inspector:** `%s`\n", r.Probe.LifecycleCLI, r.Probe.InspectorCLI)
	fmt.Fprintf(&b, "- **Recipe nonce:** `%s`\n", r.Recipe.Nonce)
	fmt.Fprintf(&b, "- **Claims:** %d — %d agree, %d disagree, %d unverifiable\n", len(r.Rows), agree, disagree, unver)
	fmt.Fprintf(&b, "- **Generated:** %s", r.GeneratedAt)
	if r.Tool != "" {
		fmt.Fprintf(&b, " · **Tool:** %s", r.Tool)
	}
	b.WriteString("\n\n")

	if len(r.Problems) > 0 {
		b.WriteString("## Why this run did not pass\n\n")
		for _, p := range r.Problems {
			fmt.Fprintf(&b, "- %s\n", p)
		}
		b.WriteString("\n")
	}

	b.WriteString("## Agreement table\n\n")
	b.WriteString("| claim | inspector says | testimony says | verdict |\n")
	b.WriteString("|-------|----------------|----------------|---------|\n")
	for _, row := range r.Rows {
		verdict := string(row.Verdict)
		if !row.Pass {
			verdict = "**" + verdict + "**"
		}
		fmt.Fprintf(&b, "| `%s` — %s | %s | %s | %s |\n",
			row.ID, mdCell(row.Claim), mdCell(row.Inspector), mdCell(row.Testimony), verdict)
	}
	b.WriteString("\nNotes:\n\n")
	for _, row := range r.Rows {
		fmt.Fprintf(&b, "- **`%s`** — %s\n", row.ID, row.Note)
	}
	b.WriteString("\n")

	b.WriteString("## Teardown — the lifecycle's mandatory ending\n\n")
	fmt.Fprintf(&b, "- stopped: %s · removed: %s · verified gone in the fleet list: %s\n",
		yesNo(r.Teardown.Stopped), yesNo(r.Teardown.Removed), yesNo(r.Teardown.VerifiedGone))
	if r.Teardown.Detail != "" {
		fmt.Fprintf(&b, "- %s\n", mdCell(r.Teardown.Detail))
	}
	b.WriteString("\nA probe that outlives its run is a leak into a live fleet. The full questions\n")
	b.WriteString("and replies are in `transcript.md`, verbatim.\n")
	return b.String()
}

func yesNo(ok bool) string {
	if ok {
		return "yes"
	}
	return "**NO**"
}

func mdCell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 220 {
		s = s[:217] + "…"
	}
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}
