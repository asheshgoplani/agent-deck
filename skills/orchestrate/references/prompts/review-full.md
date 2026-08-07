{{include:reviewer-preamble.md}}

{{SPEC_BLOCK}}

Review the full branch diff: git diff $(git merge-base {{BASE_BRANCH}} HEAD)...HEAD

Execute the review layers per {{AGENT_DECK_REPO}}/skills/review/references/ —
run `adversarial.md`, `edge-cases.md` and `verification-gap.md`, plus
`deletion-check.md` if the diff removes meaningful code — then merge, dedup,
grade severity and triage exactly as {{AGENT_DECK_REPO}}/skills/review/SKILL.md
describes. Add spec compliance against the task file above as an explicit
concern threaded into `edge-cases`, `verification-gap` and `deletion-check`:
anything missing, extra, or misunderstood is a finding. The adversarial layer
stays spec-blind by design — it receives the diff only, so run it FIRST,
before you read the task file or any repo file. Not knowing the author's
intent is exactly what makes that layer catch what the others rationalise;
handing it the spec restores the anchoring bias it exists to remove. Also run
the test suite and judge whether the tests actually cover the change.

Known pre-existing test failures (the implementer's recorded baseline):
{{BASELINE}}. These are NOT findings — only failures new against this
baseline are.

Write your full output to {{VERDICT_FILE}}, in this order: every layer's
raw findings first, then a line containing exactly `## Merged findings`, then
the merged list, the "Checked:" lines and the verdict line. That heading is a
parsing anchor — emit it verbatim, exactly once. Then print ONLY the merged
findings list, the "Checked:" lines and the verdict line as your response. A
verdict with no evidence is not acceptable.
End with exactly one line, using real counts:
VERDICT: clean
VERDICT: fix-needed patch=<n> decision-needed=<n> defer=<n>
