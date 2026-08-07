{{include:reviewer-preamble.md}}

{{SPEC_BLOCK}}

A previous review at commit {{REVIEWED_SHA}} reported:
{{PREVIOUS_FINDINGS}}

Do, in order:
1. Verify each finding above is actually fixed — an unfixed or half-fixed
   finding is a new finding.
2. Closely review the commits made since then: git diff {{REVIEWED_SHA}}...HEAD
3. Quick-scan the rest of the branch diff for anything the fixes broke.
4. Run the test suite. Known pre-existing failures (baseline): {{BASELINE}} —
   only NEW failures are findings.

Run the review layers per {{AGENT_DECK_REPO}}/skills/review/references/ against
`git diff {{REVIEWED_SHA}}...HEAD` — the same layers the round-1 reviewer ran,
scoped to the new commits — so every finding carries a real provenance tag.

Report findings in the merged format from
{{AGENT_DECK_REPO}}/skills/review/SKILL.md: file:line — severity (critical |
major | minor) — [patch | decision-needed | defer] — provenance — one line
each. Then 2-3 "Checked:" evidence lines. A verdict with no evidence is not
acceptable.

Write your full output to {{VERDICT_FILE}}, in this order: every layer's
raw findings first, then a line containing exactly `## Merged findings`, then
the merged list, the "Checked:" lines and the verdict line. That heading is a
parsing anchor — emit it verbatim, exactly once. Then print ONLY the merged
list, the "Checked:" lines and the verdict line as your response.
End with exactly one line, using real counts:
VERDICT: clean
VERDICT: fix-needed patch=<n> decision-needed=<n> defer=<n>
