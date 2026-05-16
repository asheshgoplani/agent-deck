# Synthesizer prompt — merge per-transcript reports into FINDINGS.md

You are synthesizing N per-transcript reports from the agent-deck conductor into ONE findings document. You are a short-lived worker session.

## Your task

1. Read EVERY file in:
   **`{REPORTS_DIR}`**
2. Write the merged findings to:
   **`{OUTPUT_PATH}`**
   using the EXACT schema below.
3. After writing, print `DONE: {OUTPUT_PATH}` to stdout and exit.

## Input format

Each input file follows this schema (you can rely on these section headers):

```
# Report: agent-deck / <sid>
## Summary
## agent-deck capabilities exercised
## Issues encountered
## User corrections / interventions
## Workflow patterns observed
## Capability discovery
## Honest assessment
```

## Hard rules

- Read EVERY report. Do not skip even if a filename looks weird.
- For repeated items across reports, record **frequency** (e.g. "seen in 7/12 reports").
- Cite source reports by FILENAME (e.g. `5ed9163e.md`) AND propagate the uuid-prefix from the source report (e.g. `5ed9163e.md [69f468d4]`) so the chain stays traceable.
- Do NOT invent findings that aren't in source reports.
- Do NOT touch any files except OUTPUT_PATH.
- Do NOT file GitHub issues, do NOT modify CAPABILITIES.md (the human will).
- One pass, no follow-ups. After writing, exit.
- Capabilities are GENERAL nouns ("Manage sessions", "Attach MCPs"), not flags.

## Privacy (CRITICAL — this synthesis may be shared publicly)

Treat the output as if it were already public on GitHub. Inherit the privacy rules from the per-transcript reports, and additionally:

- Do NOT include real people, business, customer, or project names — use placeholders (`<client>`, `<person-A>`)
- Do NOT include paths with usernames, IP addresses, hostnames, internal domains, tokens, or passwords
- Do NOT include specific worktree / branch names or tmux session titles
- DO preserve agent-deck command names, public file names, bug shapes, reproducers, severity
- DO preserve uuid-prefix and report-filename citations — these get post-processed later

When in doubt, generic placeholder.

## Output schema

```markdown
# FINDINGS — agent-deck conductor — run {YYYY-MM-DD}

**Reports analyzed:** N
**Date range covered:** <earliest event ts across reports> → <latest>

## Capabilities exercised across all transcripts

Most-frequent first. Group by general capability name. For each row, include
which reports saw it, total uses observed, and any qualitative notes.

| Capability | Total uses | Reports it appears in (filenames) | Notes |
|---|---|---|---|
| Manage sessions | 47 | 5ed9163e.md, 0a1f3a0b.md, … | Mixed outcomes |
| ... | ... | ... | ... |

## Recurring issues (seen in ≥2 reports)

Group identical or near-identical issues observed in multiple reports.
For each:
- **Issue:** <one-line description>
- **Frequency:** N reports (list the filenames)
- **Citations:** report-filename [uuid-prefix], …
- **Severity guess:** likely-bug | friction | known-limitation | user-error
- **Surface affected:** CLI | TUI | Web UI | n/a

## Single-occurrence issues worth investigating

Issues that appeared in only one report but look serious enough to chase.
Same format as above.

## Recurring workflow patterns

Patterns that appear in ≥2 reports. Generic, reusable patterns only.
- **Pattern:** ...
- **Frequency:** N reports
- **Citations:** ...

## Capabilities discovered (not obviously documented)

Things reports flagged as "Capability discovery". Group identical findings.

## Per-report honest-assessment digest

One line per source report — just the report's own "Honest assessment" verdict.
Helps the human spot signal-rich vs signal-poor transcripts at a glance.

| Report | Assessment summary (one line) |
|---|---|
| 5ed9163e.md | Signal-rich: end-to-end release pipeline + concrete bugs |
| 0a1f3a0b.md | … |
| ... | ... |

## Headline summary (one paragraph for the human)

What's the take-away across all N reports? What's the most surprising finding?
What's the highest-priority item that looks filable as a GitHub issue?
Limit to one paragraph — no bullet lists here.

END.
```
