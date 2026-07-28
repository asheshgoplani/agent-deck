---
name: review
description: Multi-layer code review — an adversarial pass, a mechanical edge-case path trace, and a verification-gap check, merged into one deduplicated, severity-graded, triaged findings list with a machine-readable verdict. Use when the user asks to "review this", "review my changes", "review this diff/PR/branch", wants a second opinion on a change before committing or merging, or when an orchestrated reviewer child is told to run the shared review layers.
metadata:
  compatibility: "claude, opencode"
---

## What this is

Three orthogonal reviewers — `adversarial`, `edge-cases`, `verification-gap`
(plus `deletion-check` when applicable) — run independently and merge into
one findings list. Layers beat one big prompt because each has a different
blind spot; the adversarial layer is deliberately starved of context so it
can't anchor on the author's stated intent.

## 1. Resolve the target

- No argument: the uncommitted working-tree changes — `git diff HEAD`, plus any
  untracked files the user names.
- Diff range: `git diff <range>`. Branch: `git diff $(git merge-base <base>
  <branch>)...<branch>`. PR number: `gh pr diff <n>`. File list:
  `git diff -- <files>`.
- Optional trailing `also consider <...>` argument: capture it verbatim and
  append it to **every** layer's prompt as an extra concern. It never replaces
  a layer's own checklist.

State the target you resolved, in one line, before dispatching.

## 2. Scope the layers

- Diff contains code → `adversarial` + `edge-cases` + `verification-gap`.
- Diff removes meaningful code (deleted functions, branches, guards,
  validation, cleanup, tests — not renames/moves/formatting) → also add
  `deletion-check`.
- Docs/config only → `adversarial` alone, and print one line saying why the
  other layers were skipped (no code paths to trace, no behavior to protect).

## 3. Dispatch

When an Agent/subagent tool is available, dispatch one subagent per layer, all
in one message so they run in parallel. Each subagent's prompt: read its layer
file under this skill's own directory (`${CLAUDE_PLUGIN_ROOT}/skills/review/`
when installed as a plugin — never a path relative to the user's repo, which
is the cwd and does not contain these files) — `references/adversarial.md`,
`references/edge-cases.md`, `references/verification-gap.md`, or
`references/deletion-check.md` — and execute it against the resolved target,
plus its inputs, plus any `also consider` text. Otherwise run the layers
sequentially in-context: adversarial → edge-cases → verification-gap →
deletion-check.

The information asymmetry below is a rule, not a suggestion: denying the
adversarial layer the author's intent kills anchoring bias; denying the
tracing layers repo access would manufacture false positives.

| Layer | Gets |
| --- | --- |
| `adversarial` | the diff **only** — no spec, no conversation, no repo access |
| `edge-cases` | diff + full post-change content of touched files + repo read access |
| `verification-gap` | diff + full post-change content of touched files + repo read access |
| `deletion-check` | diff + full post-change content of touched files + repo read access |

Shared vocabulary: the adversarial layer checks the diff against
`${CLAUDE_PLUGIN_ROOT}/skills/review/references/principles.md` (DRY / KISS /
YAGNI / SOLID and their violation smells), so a subagent dispatched for that
layer gets that file too.

## 4. Merge & dedup

Two findings merge when they are at the same location *and* describe the same
underlying issue. Keep the more detailed description, and union the
provenance tags: `[Adversarial]`, `[Edge]`, `[V-Gap]`, `[Deletion]`.

Assign severity here and only here — `critical` / `major` / `minor`. The
layers were forbidden to grade because each had partial information by design.

- `critical` — data loss, corruption, security exposure, or a break in
  behavior that shipped and is in use.
- `major` — wrong behavior on a reachable path, or a missing guard a real input hits.
- `minor` — everything else worth saying.

Sort by severity first; within a severity, findings flagged by more layers
sort first — multi-layer agreement is a free confidence signal.

## 5. Tone transform

The adversarial layer's output is hostile on purpose. Rewrite each finding as
observation + concrete fix: what is true about the code, and the specific
change that resolves it. Drop adjectives about the author entirely. Before:
"This is a lazy, broken null check that will blow up in prod." After:
"`user.email` is read without a null check; a signed-out user hits a
nil-pointer panic. Add `if user == nil { return }` before the read."

## 6. Triage

Every merged finding gets exactly one bucket:

- `patch` — mechanical and in scope; fix it now.
- `decision-needed` — needs a human or the conductor: a scope question, a
  product decision, a trade-off the spec does not settle.
- `defer` — pre-existing or out of scope; append to the run's deferred-work
  file if one was named, otherwise list under a `Deferred` heading. A `defer`
  item never extends a fix loop.

## Output format

```text
N. <file>:<line> — <critical|major|minor> — [<patch|decision-needed|defer>] — <provenance> — <observation + concrete fix>
```

Then 2–3 lines starting with `Checked:` summarising what was actually
verified (which layers ran, what the suite did if it was run, what was
skipped and why). Then exactly one verdict line, last:

```text
VERDICT: clean
VERDICT: fix-needed patch=<n> decision-needed=<n> defer=<n>
```

- `VERDICT: clean` is emitted iff `patch == 0 && decision-needed == 0`. Defer
  items may exist and are still listed above it.
- The clean verdict is a mandated exact line — never an empty response,
  never "looks good to me", never silence.
- Counts in `fix-needed` are real counts, not estimates.
