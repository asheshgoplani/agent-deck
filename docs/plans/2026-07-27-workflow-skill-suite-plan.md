# Workflow Skill Suite — Implementation Plan

**Date:** 2026-07-27
**Design:** `docs/plans/2026-07-27-workflow-skill-suite-design.md` (approved)
**Branch:** `feature/workflow-skill-suite`
**Repo root for every command below:** `/Users/doozyx/DoozyX/agent-deck/.worktrees/feature-workflow-skill-suite`

> **Base note (already done, do not redo):** this branch was originally cut from
> `origin/main`, which has neither `skills/orchestrate/` nor the design doc. It
> has been reset onto local `main` (`e0d15edc`) so both exist. Every task below
> assumes that base.

---

## Architecture summary

Pure additions plus four small edits:

```
skills/design/SKILL.md                          NEW  (T6)
skills/review/SKILL.md                          NEW  (T5)
skills/review/references/principles.md          NEW  (T1)
skills/review/references/deletion-check.md      NEW  (T1)
skills/review/references/adversarial.md         NEW  (T2)
skills/review/references/edge-cases.md          NEW  (T3)
skills/review/references/verification-gap.md    NEW  (T4)
skills/tdd/SKILL.md                             NEW  (T7)
skills/debug/SKILL.md                           NEW  (T8)
skills/verify/SKILL.md                          NEW  (T9)
internal/tmux/tmux.go                           EDIT (T10)
internal/session/instance.go                    EDIT (T10)
internal/session/child_role_env_test.go         NEW  (T10)
hooks/hooks.json                                NEW  (T11)
hooks/session-start                             NEW  (T11)
hooks/preamble-child.md                         NEW  (T11)
hooks/preamble-interactive.md                   NEW  (T11)
.claude-plugin/marketplace.json                 EDIT (T12)
skills/orchestrate/SKILL.md                     EDIT (T13)
CHANGELOG.md                                    EDIT (T14)
```

### Dependency / parallelism map

| Wave | Tasks | Notes |
| --- | --- | --- |
| 1 | **T1, T2, T3, T4, T5, T6, T7, T8, T9, T10, T11** | All touch disjoint files — **safe to run fully in parallel**. |
| 2 | **T12, T13** | T12 verifies the skill dirs from wave 1 exist; T13 greps for `skills/review/references/*.md` from T1–T4. Disjoint from each other — **T12 ‖ T13**. |
| 3 | **T14** | Touches `CHANGELOG.md` only; run last so it can describe everything. |

### Interfaces the later tasks rely on (frozen here — do not renegotiate)

These strings are a contract across T1–T5, T13 and T14. Any executor that
needs one gets it verbatim inside its own task; this table is the index.

**Env markers (T10 produces, T11 consumes):**

| Variable | Value | When |
| --- | --- | --- |
| `AGENTDECK_ROLE` | `child` | session has a parent |
| `AGENTDECK_PARENT_ID` | parent instance id | session has a parent |
| both | *unset* | session has no parent |

**Review finding line (T5 emits; T1–T4 feed it; T13 consumes):**

```text
N. <file>:<line> — <critical|major|minor> — [<patch|decision-needed|defer>] — <provenance> — <observation + concrete fix>
```

`<provenance>` is one or more of `[Adversarial]` `[Edge]` `[V-Gap]` `[Deletion]`.

**Review verdict lines (exactly one, last line of a review):**

```text
VERDICT: clean
VERDICT: fix-needed patch=<n> decision-needed=<n> defer=<n>
```

`VERDICT: clean` is emitted **iff** `patch == 0 && decision-needed == 0`
(defer items may exist and are still listed above the verdict).

**Severity scale (everywhere):** `critical` / `major` / `minor`. Assigned
**only at merge** in `skills/review/SKILL.md` — never by a layer file.

**Layer file paths (referenced by T5 and T13):**

```text
skills/review/references/adversarial.md
skills/review/references/edge-cases.md
skills/review/references/verification-gap.md
skills/review/references/deletion-check.md
skills/review/references/principles.md
```

### Conventions every markdown task must follow

- Frontmatter is exactly this shape (copy `skills/fleet/SKILL.md`'s style):

  ```yaml
  ---
  name: <skill-dir-name>
  description: <one paragraph, third person, includes the trigger phrases a user would say>
  metadata:
    compatibility: "claude, opencode"
  ---
  ```

- **Generic / upstream-able.** No `DoozyX`, no `/Users/...`, no personal repo
  names, no host names anywhere in a shipped file. Repo-relative paths only.
- Wrap prose at ~78 columns, matching the existing skills.
- No placeholders. Every rule states what to do, not "handle errors".

---

## T1 — `principles.md` + `deletion-check.md`

**tier: mid** · parallel-safe (wave 1)

### Files

- Create `skills/review/references/principles.md` (~30 lines)
- Create `skills/review/references/deletion-check.md` (~50 lines)

Create the directory first: `mkdir -p skills/review/references`.

### Design extract (verbatim, from design §3)

> - **`deletion-check.md`** — did removed code carry behavior nothing
>   re-established or intentionally retired? Only layer with a self-rated
>   `confidence` field (these are inferences).
> - **`principles.md`** (~30 lines) — DRY, KISS, YAGNI, SOLID: one line each on
>   meaning plus 2–3 concrete violation smells (needless abstraction layer,
>   speculative generality, copy-pasted logic drifting apart, god-object/SRP
>   breaks, boolean-flag parameters). Consumed by `design` (architecture pass),
>   `tdd` (refactor step), and the adversarial layer.

And from design §3 item 4, which binds both files:

> **Severity is banned at the leaf and assigned only at merge** (leaves have
> by-design information asymmetry). Canonical scale everywhere: **critical /
> major / minor**.

### `principles.md` — required content

No frontmatter (it is a reference file, not a skill). Structure:

1. A one-line header explaining the file is the shared vocabulary for the
   `design` architecture pass, the `tdd` refactor step, and the adversarial
   review layer.
2. Four sections — **DRY**, **KISS**, **YAGNI**, **SOLID** — each with:
   - one line on what the principle actually means (not the acronym expansion
     alone);
   - 2–3 **violation smells**, phrased as things you can literally see in a
     diff.
3. Distribute at minimum these five named smells across the sections
   (they are named in the design and must appear):
   - needless abstraction layer
   - speculative generality
   - copy-pasted logic drifting apart
   - god-object / SRP breaks
   - boolean-flag parameters
4. Close with a two-line caution: a principle is a lens, not a rule to enforce
   against the user's stated requirements — an abstraction the spec asked for
   is not YAGNI, and inlining twice is not always a DRY violation.

### `deletion-check.md` — required content

No frontmatter. This file must be **self-contained**: a fresh subagent that
reads only this file can execute the layer. Structure:

1. **Role line** — you are checking one thing: did removed code carry behavior
   that nothing re-established and that was not intentionally retired?
2. **When this layer runs** — only when the diff removes meaningful code
   (deleted functions, branches, guards, validation, cleanup, tests). Pure
   renames, moves, and formatting are not deletions for this purpose; say so.
3. **Inputs you get** — the diff, the full post-change content of every file
   the diff touches, and read access to the repo.
4. **Procedure**, numbered:
   1. List every removed behavior (not every removed line): a guard, a
      validation, a fallback, a cleanup, a log/metric, a test case.
   2. For each, search the post-change tree for a replacement (`grep` the
      symbol, the error string, the call site).
   3. Classify: **re-established elsewhere** (drop it) / **intentionally
      retired** — the diff or a commit message says the behavior is gone on
      purpose (drop it, but say so in one line) / **silently lost** (a finding).
   4. Removed tests are findings unless the code they covered is also gone.
5. **Output format** — a numbered list, and this is the only layer that
   self-rates:

   ```text
   N. <file>:<line-in-old-file> — confidence: high|medium|low — what was
      removed, what searching for a replacement turned up, and what breaks
      if nothing re-established it.
   ```

   `confidence: high` = you found the consumer that still needs it;
   `medium` = you found no replacement but no live consumer either;
   `low` = inference from shape alone. State plainly that these are
   inferences, which is why this layer rates itself.
6. **Severity ban** — "Do not assign severity. Severity is decided at merge."
7. **Empty result** — an empty numbered list is a valid, expected outcome; say
   `No silently-lost behavior found.` rather than inventing a finding.

### Verification

```bash
cd /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-workflow-skill-suite
ls skills/review/references/principles.md skills/review/references/deletion-check.md
wc -l skills/review/references/principles.md skills/review/references/deletion-check.md
grep -c -e 'DRY' -e 'KISS' -e 'YAGNI' -e 'SOLID' skills/review/references/principles.md
grep -i -e 'needless abstraction' -e 'speculative generality' -e 'copy-pasted' \
        -e 'SRP' -e 'boolean-flag' skills/review/references/principles.md | wc -l
grep -c 'confidence' skills/review/references/deletion-check.md
grep -i 'severity' skills/review/references/deletion-check.md
grep -riE 'doozyx|/Users/' skills/review/references/ | wc -l
```

Expected: both files listed; `principles.md` 25–40 lines and
`deletion-check.md` 40–65 lines; the acronym grep ≥ 4; the smell grep ≥ 5;
`confidence` appears ≥ 2 times; the severity grep prints the "do not assign
severity" line; the DoozyX/`/Users/` grep prints `0`.

### Commit

```bash
cd /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-workflow-skill-suite
git branch --show-current   # must print: feature/workflow-skill-suite
git add skills/review/references/principles.md skills/review/references/deletion-check.md
git commit -m "feat(skills): add review principles and deletion-check layers"
```

---

## T2 — `adversarial.md`

**tier: mid** · parallel-safe (wave 1)

### File

- Create `skills/review/references/adversarial.md` (~70–90 lines)

`mkdir -p skills/review/references` first if it does not exist.

### Design extract (verbatim, from design §3)

> - **`adversarial.md`** — hostile persona; "find at least ten issues"; zero
>   findings = HALT and re-analyze; checklist explicitly includes principles
>   violations per `principles.md` (over-engineering, duplication, SRP breaks);
>   descriptions only, no severity.

And the information-asymmetry rule this layer exists to enforce (design §3
item 3):

> Information asymmetry is deliberate and stated: the adversarial reviewer gets
> the diff **only** (no spec, no conversation, no repo access — kills anchoring
> bias); edge-cases and verification-gap get the diff plus full post-change file
> content and repo read access (tracing consumers without it manufactures false
> positives).

Plus (design §3 item 4):

> **Severity is banned at the leaf and assigned only at merge** (leaves have
> by-design information asymmetry).

And (design §3 item 5):

> **Tone transform.** Strip the adversarial persona's hostility; reframe as
> observation + concrete fix.

— note in the file that the hostility is *deliberately* left in the layer's own
output because the dispatcher strips it later; the reviewer must not soften
itself.

### Required content

No frontmatter. Self-contained — a fresh subagent that reads only this file can
execute the layer. Structure:

1. **Persona.** A hostile, sceptical reviewer who assumes the diff is wrong
   until proven otherwise. Its job is to *find* problems, not to be fair.
2. **Inputs — and the hard limit.** State explicitly: you receive the **diff
   only**. No spec, no conversation history, no repo access. This is
   deliberate: not knowing the author's intent is what keeps you from
   rationalising the code the way the author did. Do not ask for more context;
   do not speculate about a spec you cannot see. If the diff is unreadable
   without context, that itself is a finding ("this change is not
   self-explanatory at the call site").
3. **The quota.** Find **at least ten issues**. Then:
   > If you found zero issues: **HALT.** Do not emit a verdict. Re-read the
   > diff line by line and analyse again — a zero-finding adversarial pass
   > means the analysis failed, not that the code is perfect.
   Say plainly that the quota is a search-depth forcing function, not a licence
   to pad: a weak finding stated honestly ("minor, may be intentional") is
   allowed; a fabricated one is not.
4. **Checklist** — at least these buckets, one to three lines each:
   - correctness: off-by-one, nil/empty, wrong operator, inverted condition
   - error handling: swallowed errors, unchecked returns, error text that loses
     the cause
   - concurrency: shared state, missing lock, goroutine/task leak, ordering
     assumptions
   - resource lifecycle: unclosed handles, unbounded growth, missing cleanup on
     the failure path
   - security & input trust: unvalidated input, injection, secrets in logs
   - naming & readability: a name that lies about what the thing does
   - **principles violations per `principles.md`** — over-engineering,
     duplication, SRP breaks. Name the file explicitly so the reader can open
     it, and list those three by name inline so the layer still works if the
     file is unavailable.
   - tests: a change with no test touched at all is a finding here too
5. **Output format.** A numbered list, description only:

   ```text
   N. <file>:<line> — what is wrong, and why it matters.
   ```

   Then, immediately: **"Do not assign severity, do not assign a triage
   bucket, do not rank. Severity is decided at merge, where the reviewer has
   the context you were deliberately denied."**
6. **Anti-patterns**, as a short table of what not to do: asking for the spec;
   softening the tone; grading findings; stopping at three because the diff
   "looks fine"; repeating one issue at ten locations to reach the quota
   (that is one finding with a location list).

### Verification

```bash
cd /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-workflow-skill-suite
ls skills/review/references/adversarial.md
wc -l skills/review/references/adversarial.md
grep -i -e 'at least ten' -e 'HALT' skills/review/references/adversarial.md
grep -i 'diff only\|diff **only**\|no repo access' skills/review/references/adversarial.md
grep -c 'principles.md' skills/review/references/adversarial.md
grep -i 'do not assign severity' skills/review/references/adversarial.md
grep -riE 'doozyx|/Users/' skills/review/references/adversarial.md | wc -l
```

Expected: file exists, 60–100 lines; the quota grep prints both the
"at least ten" line and the HALT line; the asymmetry grep prints a line;
`principles.md` referenced ≥ 1 time; the severity grep prints the ban line;
the DoozyX grep prints `0`.

### Commit

```bash
cd /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-workflow-skill-suite
git branch --show-current   # must print: feature/workflow-skill-suite
git add skills/review/references/adversarial.md
git commit -m "feat(skills): add adversarial review layer"
```

---

## T3 — `edge-cases.md`

**tier: mid** · parallel-safe (wave 1)

### File

- Create `skills/review/references/edge-cases.md` (~60–90 lines)

### Design extract (verbatim, from design §3)

> - **`edge-cases.md`** — pure path tracer, never opines on quality; enumerates
>   control-flow boundaries and **implicit branches** (untouched members of
>   enums/status sets/sentinels the diff special-cases); strict JSON output:
>   `location`, `trigger_condition` (≤15 words), `guard_snippet`,
>   `potential_consequence` (≤15 words); `[]` is valid.

And the input contract (design §3 item 3):

> edge-cases and verification-gap get the diff plus full post-change file
> content and repo read access (tracing consumers without it manufactures false
> positives).

### Required content

No frontmatter. Self-contained. Structure:

1. **Role.** A *pure path tracer*. Two sentences making the boundary explicit:
   you enumerate reachable states the change does not visibly handle; you
   **never** opine on code quality, naming, architecture, or style. Those
   belong to other layers, and mixing them in poisons the JSON.
2. **Inputs.** The diff, the **full post-change content** of every file the
   diff touches, and read access to the repo. State why: tracing a consumer
   without being able to read it manufactures false positives.
3. **What to enumerate**, two categories, both required:
   - **Explicit control-flow boundaries** in the changed code: empty / one /
     many; zero / negative / max; nil / missing / absent; first / last
     iteration; concurrent entry; the failure path of every call the diff adds;
     timeout and cancellation.
   - **Implicit branches** — define the term precisely, because it is the
     layer's real value: *members of an enum, status set, error class, or
     sentinel value that the diff special-cases some of, and silently falls
     through for the rest*. Procedure: for every value the diff compares
     against, find the full set of possible values (the enum declaration, the
     constant block, the status list), and list every member the diff does not
     name.
4. **Procedure**, numbered: read the diff → for each changed function, list its
   inputs and their domains → walk each domain boundary → collect the value
   sets the diff branches on and diff them against their declarations → for
   each unhandled case, locate the guard (or its absence) in the post-change
   file → write the record.
5. **Output format — strict JSON, nothing else.** No prose before or after, no
   markdown fence, no commentary. Exactly:

   ```json
   [
     {
       "location": "path/to/file.go:123",
       "trigger_condition": "at most 15 words",
       "guard_snippet": "the line(s) that do or do not guard this case",
       "potential_consequence": "at most 15 words"
     }
   ]
   ```

   State each field's rule: `location` is `file:line` in the post-change file;
   `trigger_condition` and `potential_consequence` are each **≤ 15 words**;
   `guard_snippet` is copied verbatim from the file, or the literal string
   `"(no guard)"` when nothing guards the case.
6. **`[]` is valid.** Say it explicitly, with the reason: a change to a total
   function over a two-value domain genuinely has no unhandled paths, and an
   invented record costs more than a missed one here.
7. **Severity ban.** No severity field, no ranking, no "this is critical" in
   any string. Severity is decided at merge.

### Verification

```bash
cd /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-workflow-skill-suite
ls skills/review/references/edge-cases.md
wc -l skills/review/references/edge-cases.md
grep -c -e 'location' -e 'trigger_condition' -e 'guard_snippet' \
        -e 'potential_consequence' skills/review/references/edge-cases.md
grep -i -e 'implicit branch' -e '15 words' -e 'no guard' skills/review/references/edge-cases.md
grep -F '[]' skills/review/references/edge-cases.md
grep -i 'severity' skills/review/references/edge-cases.md
grep -riE 'doozyx|/Users/' skills/review/references/edge-cases.md | wc -l
```

Expected: file exists, 50–100 lines; the four JSON field names each appear
(grep count ≥ 4); the implicit-branch / 15-words / no-guard greps each print a
line; `[]` appears; the severity grep prints the ban; DoozyX grep prints `0`.

Also sanity-check the embedded JSON example parses:

```bash
cd /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-workflow-skill-suite
python3 - <<'PY'
import json, re, pathlib
src = pathlib.Path("skills/review/references/edge-cases.md").read_text()
block = re.search(r"```json\n(.*?)```", src, re.S).group(1)
json.loads(block)
print("json example OK")
PY
```

Expected output: `json example OK`.

### Commit

```bash
cd /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-workflow-skill-suite
git branch --show-current   # must print: feature/workflow-skill-suite
git add skills/review/references/edge-cases.md
git commit -m "feat(skills): add edge-case path-tracing review layer"
```

---

## T4 — `verification-gap.md`

**tier: mid** · parallel-safe (wave 1)

### File

- Create `skills/review/references/verification-gap.md` (~70–90 lines)

### Design extract (verbatim, from design §3)

> - **`verification-gap.md`** — single question: "if this changed behavior
>   stopped holding where it's used, would any test fail?" Cheap-triage-first
>   whitelist so neutral changes exit in one step; bounded consumer tracing
>   (1–3 hops, named stop conditions); the **Demonstration gate** — name the one
>   concrete mutation the consumer would observe, or drop the finding;
>   anti-fabrication clause — never assert a test exists/passes unless found and
>   read.

And the input contract (design §3 item 3):

> edge-cases and verification-gap get the diff plus full post-change file
> content and repo read access (tracing consumers without it manufactures false
> positives).

### Required content

No frontmatter. Self-contained. Structure:

1. **The single question**, stated once at the top and never widened:
   > If this changed behavior stopped holding where it is used, would any test
   > fail?
   Add one line: this layer does not review quality, does not review edge
   cases, and does not propose designs. It measures whether the change is
   *protected*.
2. **Inputs.** Diff + full post-change file content + repo read access.
3. **Step 1 — cheap triage first.** A whitelist of change classes that exit in
   **one step** with no findings, so the expensive tracing never runs on them.
   List at least: comment/doc-only changes; pure formatting or import
   reordering; renames with no behavior change; additive logging that nothing
   asserts on; new dead code not yet wired to a caller; test-only changes;
   generated files. State the exit action explicitly: emit the empty result and
   stop.
4. **Step 2 — bounded consumer tracing.** For each behavior the diff changes,
   walk outward from the changed symbol to its callers. **1–3 hops maximum.**
   Name the stop conditions explicitly — stop at any of:
   - a hop that reaches a test file (record it: that is the protection);
   - a hop that reaches a public API / entry point / handler boundary;
   - a hop that crosses into a third-party or vendored package;
   - hop 3, whatever it reached;
   - a fan-out wider than ~10 callers (record "wide fan-out, untraced" and stop
     — do not enumerate).
5. **Step 3 — the Demonstration gate.** The hard filter, stated as a gate:
   > Name the **one concrete mutation** you could make to the changed code that
   > a consumer would observe and that no test would catch — a specific wrong
   > constant, a specific flipped condition, a specific removed call. If you
   > cannot name it concretely, **drop the finding.** A gap you cannot
   > demonstrate is a guess.
   Give one worked example of a passing gate and one of a failing gate.
6. **Anti-fabrication clause**, verbatim in spirit:
   > Never assert that a test exists, covers something, or passes unless you
   > have found the file and read the assertion. "There is probably a test for
   > this" is not a finding, and "this is covered" without a `file:line` is a
   > fabrication. Cite `file:line` for every test you claim exists. You may not
   > run the suite as a substitute for reading the assertion — a green suite
   > says nothing about whether *this* behavior is asserted.
7. **Output format.** Numbered list:

   ```text
   N. <file>:<line> — the behavior that changed, where it is consumed
      (file:line, or "untraced: <stop condition>"), and the demonstration
      mutation that nothing would catch.
   ```

   Empty result is valid and expected for whitelisted changes: say
   `No verification gaps found.`
8. **Severity ban.** No severity, no ranking. Decided at merge.

### Verification

```bash
cd /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-workflow-skill-suite
ls skills/review/references/verification-gap.md
wc -l skills/review/references/verification-gap.md
grep -i 'would any test fail' skills/review/references/verification-gap.md
grep -i -e 'triage' -e '1–3 hops\|1-3 hops\|three hops' skills/review/references/verification-gap.md
grep -i 'demonstration' skills/review/references/verification-gap.md
grep -i 'never assert' skills/review/references/verification-gap.md
grep -i 'severity' skills/review/references/verification-gap.md
grep -riE 'doozyx|/Users/' skills/review/references/verification-gap.md | wc -l
```

Expected: file exists, 60–100 lines; every grep above except the last prints at
least one line; the last prints `0`.

### Commit

```bash
cd /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-workflow-skill-suite
git branch --show-current   # must print: feature/workflow-skill-suite
git add skills/review/references/verification-gap.md
git commit -m "feat(skills): add verification-gap review layer"
```

---

## T5 — `skills/review/SKILL.md` (dispatcher)

**tier: mid** · parallel-safe (wave 1)

### File

- Create `skills/review/SKILL.md` (~80–110 lines)

You do **not** need to read the layer files; they are written by T1–T4 and this
task only references them by path. Assume they exist at:

```text
skills/review/references/adversarial.md
skills/review/references/edge-cases.md
skills/review/references/verification-gap.md
skills/review/references/deletion-check.md
skills/review/references/principles.md
```

### Design extract (verbatim, design §3, the whole dispatcher spec)

> `SKILL.md` is a thin dispatcher (~80 lines):
>
> 1. **Resolve target.** Default: uncommitted changes. Args accept a diff range,
>    PR number, branch, or file list. Optional `also consider ...` arg threads
>    task-specific concerns into every layer.
> 2. **Scope.** Code present → all three layers (+ `deletion-check` when the
>    diff removes meaningful code). Docs/config only → adversarial only, and say
>    why the others were skipped.
> 3. **Dispatch layers in parallel** as subagents when the Agent tool is
>    available; sequential in-context otherwise. Information asymmetry is
>    deliberate and stated: the adversarial reviewer gets the diff **only** (no
>    spec, no conversation, no repo access — kills anchoring bias); edge-cases
>    and verification-gap get the diff plus full post-change file content and
>    repo read access (tracing consumers without it manufactures false
>    positives).
> 4. **Merge & dedup.** Same location + same underlying issue = one finding;
>    keep the more detailed description; union provenance tags (`[Adversarial]`,
>    `[Edge]`, `[V-Gap]`). **Severity is banned at the leaf and assigned only at
>    merge** (leaves have by-design information asymmetry). Canonical scale
>    everywhere: **critical / major / minor**. Within a severity, findings
>    flagged by more layers sort first (free confidence signal).
> 5. **Tone transform.** Strip the adversarial persona's hostility; reframe as
>    observation + concrete fix.
> 6. **Triage buckets:** `patch` (mechanical, fix now) / `decision-needed`
>    (escalate to human or conductor) / `defer` (pre-existing, appended to a
>    deferred-work file, out of scope, never extends a loop). "Clean" means *no
>    patch or decision-needed items*, and the clean verdict is a mandated exact
>    line, never an empty response.

### Required content

Frontmatter:

```yaml
---
name: review
description: Multi-layer code review — an adversarial pass, a mechanical edge-case path trace, and a verification-gap check, merged into one deduplicated, severity-graded, triaged findings list with a machine-readable verdict. Use when the user asks to "review this", "review my changes", "review this diff/PR/branch", wants a second opinion on a change before committing or merging, or when an orchestrated reviewer child is told to run the shared review layers.
metadata:
  compatibility: "claude, opencode"
---
```

Body sections, in this order:

1. **What this is** — three orthogonal reviewers plus a merge step. One line on
   why layers beat one big prompt: each layer has a different blind spot, and
   the adversarial layer is deliberately starved of context.

2. **1. Resolve the target.**
   - Default with no argument: the uncommitted working-tree changes
     (`git diff HEAD`, plus untracked files the user names).
   - Accept, and give the resolving command for each: a **diff range**
     (`git diff <range>`), a **branch** (`git diff $(git merge-base <base>
     <branch>)...<branch>`), a **PR number** (`gh pr diff <n>`), an explicit
     **file list** (`git diff -- <files>`).
   - The optional trailing `also consider <...>` argument: capture it verbatim
     and append it to **every** layer's prompt as an extra concern. It never
     replaces a layer's own checklist.
   - State the target you resolved, in one line, before dispatching.

3. **2. Scope the layers.**
   - Diff contains code → `adversarial` + `edge-cases` + `verification-gap`.
   - The diff removes meaningful code (deleted functions, branches, guards,
     validation, cleanup, tests — not renames/moves/formatting) → **add**
     `deletion-check`.
   - Docs/config only → `adversarial` alone, **and print one line saying the
     other layers were skipped and why** (there are no code paths to trace and
     no behavior to protect).

4. **3. Dispatch.**
   - When an Agent/subagent tool is available: one subagent per layer, **all in
     one message so they run in parallel**. Each subagent's prompt is: read
     `skills/review/references/<layer>.md` and execute it against the target,
     plus its inputs, plus any `also consider` text.
   - Otherwise: run the layers sequentially in-context, in the order
     adversarial → edge-cases → verification-gap → deletion-check.
   - **State the information asymmetry as a rule, not a suggestion**, and give
     the exact inputs per layer:

     | Layer | Gets |
     | --- | --- |
     | `adversarial` | the diff **only** — no spec, no conversation, no repo access |
     | `edge-cases` | diff + full post-change content of touched files + repo read access |
     | `verification-gap` | diff + full post-change content of touched files + repo read access |
     | `deletion-check` | diff + full post-change content of touched files + repo read access |

     One line on why: denying the adversarial layer the author's intent is what
     kills anchoring bias; denying the tracing layers repo access would
     manufacture false positives.

5. **4. Merge & dedup.**
   - Two findings merge when they are at the **same location** *and* describe
     the **same underlying issue**. Keep the more detailed description.
   - **Union the provenance tags**: `[Adversarial]`, `[Edge]`, `[V-Gap]`,
     `[Deletion]`.
   - **Assign severity here and only here** — `critical` / `major` / `minor`.
     Restate the ban: the layers were forbidden to grade because they each had
     partial information by design.
   - Give the grading rule in three lines: `critical` = data loss, corruption,
     security exposure, or a break in behavior that shipped and is in use;
     `major` = wrong behavior on a reachable path, or a missing guard a real
     input hits; `minor` = everything else worth saying.
   - **Sort:** severity first; **within a severity, findings flagged by more
     layers sort first** — multi-layer agreement is a free confidence signal.

6. **5. Tone transform.** The adversarial layer's output is hostile on purpose.
   Rewrite each finding as **observation + concrete fix**: what is true about
   the code, and the specific change that resolves it. Drop adjectives about
   the author entirely. One before/after example.

7. **6. Triage.** Every merged finding gets exactly one bucket:
   - `patch` — mechanical and in scope; fix it now.
   - `decision-needed` — needs a human or the conductor: a scope question, a
     product decision, a trade-off the spec does not settle.
   - `defer` — pre-existing or out of scope. Append it to the run's
     deferred-work file if one was named, otherwise list it under a `Deferred`
     heading. **A `defer` item never extends a fix loop.**

8. **Output format** — the frozen contract, reproduced exactly:

   ```text
   N. <file>:<line> — <critical|major|minor> — [<patch|decision-needed|defer>] — <provenance> — <observation + concrete fix>
   ```

   Then 2–3 lines starting with `Checked:` summarising what was actually
   verified (which layers ran, what the suite did if it was run, what was
   skipped and why). Then **exactly one** verdict line, last:

   ```text
   VERDICT: clean
   VERDICT: fix-needed patch=<n> decision-needed=<n> defer=<n>
   ```

   Rules, stated explicitly:
   - `VERDICT: clean` is emitted **iff** `patch == 0 && decision-needed == 0`.
     Defer items may exist and are still listed above it.
   - The clean verdict is a **mandated exact line** — never an empty response,
     never "looks good to me", never silence.
   - Counts in `fix-needed` are real counts, not estimates.

### Verification

```bash
cd /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-workflow-skill-suite
head -6 skills/review/SKILL.md
wc -l skills/review/SKILL.md
for f in adversarial edge-cases verification-gap deletion-check principles; do
  grep -q "references/$f.md" skills/review/SKILL.md && echo "ref ok: $f" || echo "REF MISSING: $f"
done
grep -F 'VERDICT: clean' skills/review/SKILL.md
grep -F 'VERDICT: fix-needed patch=' skills/review/SKILL.md
grep -c -e 'patch' -e 'decision-needed' -e 'defer' skills/review/SKILL.md
grep -c -e 'critical' -e 'major' -e 'minor' skills/review/SKILL.md
grep -i 'anchoring' skills/review/SKILL.md
grep -riE 'doozyx|/Users/' skills/review/SKILL.md | wc -l
```

Expected: frontmatter shows `name: review`; 70–120 lines; five `ref ok:` lines
(`principles` may be referenced indirectly — if it is not referenced, add one
sentence pointing the adversarial layer at it and re-run); both `VERDICT:`
greps print; bucket and severity greps ≥ 3 each; the anchoring line prints; the
DoozyX grep prints `0`.

### Commit

```bash
cd /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-workflow-skill-suite
git branch --show-current   # must print: feature/workflow-skill-suite
git add skills/review/SKILL.md
git commit -m "feat(skills): add review dispatcher skill"
```

---

## T6 — `skills/design/SKILL.md`

**tier: mid** · parallel-safe (wave 1)

### File

- Create `skills/design/SKILL.md` (~110–140 lines)

### Design extract (verbatim, design §2, complete)

> Kept from superpowers brainstorming:
>
> - Explore project context before anything else.
> - Clarifying questions **one per message**.
> - 2–3 approaches with trade-offs and a recommendation; YAGNI ruthlessly.
> - Design presented in sections, approval after each.
> - HARD-GATE: no implementation (and no implementation skill) until the design
>   is presented and approved — regardless of perceived simplicity.
> - Spec self-review (placeholders, contradictions, scope, ambiguity), then a
>   user review gate on the written file.
>
> Changed:
>
> - **Spec location:** honor the repo's visible convention (a `docs/plans/`,
>   `docs/specs/`, or similar dir containing prior design docs); default
>   `docs/plans/YYYY-MM-DD-<topic>-design.md`. Always committed — and verified
>   committed (named check for the gitignore trap: `git status` must show the
>   file staged/committed, not ignored).
> - **Principles pass:** before presenting the chosen architecture, check it
>   against `review/references/principles.md` — does any component exist for a
>   requirement nobody stated?
> - **Tiered exit** (replaces "invoke writing-plans"): after approval, size the
>   work. Multi-task / multi-file / PR-worthy → hand the design doc to
>   `orchestrate` (its planner child writes the plan). Genuinely tiny (single
>   file, one sitting) → implement in-session under `tdd` + `verify`.
>   Borderline → one final question with a recommendation.
> - **Child guard:** first block of the skill — a session dispatched as an
>   executor (per the hook) does not brainstorm; it follows its task prompt.
>
> Dropped: visual companion, writing-plans/executing-plans, `docs/superpowers/`
> paths.

### Required content

Frontmatter:

```yaml
---
name: design
description: Collaborative design and brainstorming before any code is written — explores project context, asks one clarifying question at a time, offers 2–3 approaches with trade-offs, and writes an approved, committed design document. Use before building a feature, adding functionality, or changing behavior, and whenever the user says "let's build", "I want to add", "how should we do X", or asks for a design or spec. Hard-gates implementation until the design is approved.
metadata:
  compatibility: "claude, opencode"
---
```

Body sections, in this order. **The child guard is the first block after the
H1** — before any other instruction:

1. **Child guard** (first block):
   > **If this session was dispatched as an executor, stop reading here.** A
   > session whose environment carries `AGENTDECK_ROLE=child`, or whose prompt
   > says the work is already designed and approved, does not brainstorm: its
   > task prompt is the contract. Follow the task prompt. Do not write a spec,
   > do not propose alternatives, do not wait for an approval that no one in
   > this session can give. If you believe the task is genuinely wrong, say so
   > in one line and stop.

2. **The hard gate**, stated once and never qualified:
   > No implementation — and no implementation skill — until the design has
   > been presented and the user has approved it. This holds regardless of how
   > simple the change looks. "It's a one-liner" is the most common way this
   > gate gets skipped, and a one-liner with the wrong requirement is still the
   > wrong one-liner.

3. **1. Explore project context first.** Before the first question: read the
   repo's `README`, `CLAUDE.md`/`AGENTS.md`, and `CONTRIBUTING.md`; find the
   existing design docs directory; look at how the nearest analogous feature is
   built. State what you found in a few lines. Rationale in one line: questions
   asked without context waste the user's turns on things the repo already
   answers.

4. **2. Clarifying questions — one per message.** Hard rule with the reason: a
   batch of five questions gets one merged answer that addresses two of them.
   Ask the highest-leverage unknown, wait, then ask the next. Stop when the
   remaining unknowns no longer change the shape of the design. Include a short
   list of what is usually worth asking (who uses it, what breaks today, what
   must not change, how it is expected to be verified).

5. **3. Approaches — 2–3, with trade-offs and a recommendation.** Each approach
   gets: what it does, what it costs, what it forecloses. Then a named
   recommendation with a one-line reason. **YAGNI ruthlessly**: cut anything
   that serves a requirement the user did not state, and say what you cut.

6. **4. Principles pass** — before presenting the chosen architecture, check it
   against `skills/review/references/principles.md`. The question to answer out
   loud: *does any component here exist for a requirement nobody stated?* Cut
   or justify each one.

7. **5. Present in sections, approve after each.** Motivation → decisions →
   architecture → interfaces → out-of-scope. Stop after each section for
   approval rather than delivering the whole thing at once; a wrong premise
   caught in section one saves rewriting sections two through five.

8. **6. Write the spec.**
   - **Location:** honor the repo's visible convention — look for a directory
     that already contains design docs (`docs/plans/`, `docs/specs/`,
     `docs/design/`, `docs/rfcs/`) and use it. Only when none exists, default to
     `docs/plans/YYYY-MM-DD-<topic>-design.md`.
   - **Always committed, and verified committed.** Name the gitignore trap
     explicitly: a spec written into an ignored directory looks committed and
     is invisible to every downstream session. The check, verbatim:

     ```bash
     git check-ignore -v <spec-path>          # must find nothing (exit 1)
     git add <spec-path> && git commit -m "docs(plans): <topic> design"
     git log -1 --oneline -- <spec-path>      # must print a commit
     ```

     If `check-ignore` matches, move the file to a tracked directory — do not
     force-add it into an ignored tree.

9. **7. Spec self-review, then the user review gate.** Before handing it over,
   read your own document for: placeholders (`TBD`, "etc.", "handle errors"),
   internal contradictions, scope creep past what was approved, and ambiguity a
   fresh reader would resolve differently than you meant. Fix what you find,
   then explicitly ask the user to review the written file — approval of the
   conversation is not approval of the document.

10. **8. Tiered exit.** After approval, size the work and take exactly one exit:
    - **Multi-task / multi-file / PR-worthy →** hand the committed design doc
      path to the `orchestrate` skill. Do not write the plan yourself:
      orchestrate's planner child writes it against the codebase.
    - **Genuinely tiny** — one file, one sitting, no new interfaces →
      implement in-session under `tdd`, then `verify` before claiming done.
    - **Borderline →** ask **one** final question with your recommendation, and
      take the answer.

11. **Red flags** — a short table of the rationalisations that skip the gate:
    "this is obviously what they want"; "I'll design as I code"; "the spec dir
    is gitignored, I'll just keep it in the chat"; "I'll ask all my questions at
    once to save time"; "they said build it, so approval is implied".

### Verification

```bash
cd /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-workflow-skill-suite
head -6 skills/design/SKILL.md
wc -l skills/design/SKILL.md
grep -n 'AGENTDECK_ROLE=child' skills/design/SKILL.md
grep -n 'check-ignore' skills/design/SKILL.md
grep -n 'principles.md' skills/design/SKILL.md
grep -c -e 'orchestrate' -e 'tdd' -e 'verify' skills/design/SKILL.md
grep -i 'one per message\|one question at a time' skills/design/SKILL.md
grep -riE 'doozyx|/Users/|docs/superpowers' skills/design/SKILL.md | wc -l
```

Expected: frontmatter shows `name: design`; 100–150 lines; the child guard grep
prints a line **within the first 30 lines** (verify the line number); the
`check-ignore`, `principles.md`, and one-per-message greps each print; the exit
grep ≥ 3; the last grep prints `0`.

### Commit

```bash
cd /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-workflow-skill-suite
git branch --show-current   # must print: feature/workflow-skill-suite
git add skills/design/SKILL.md
git commit -m "feat(skills): add design skill with hard approval gate"
```

---

## T7 — `skills/tdd/SKILL.md`

**tier: mid** · parallel-safe (wave 1)

### File

- Create `skills/tdd/SKILL.md` (~90 lines, budget 80–110)

### Design extract (verbatim, design §4)

> **`tdd`** (~90 lines). Iron law: *no production code without a failing test
> first*; code written first is deleted, not kept as reference. Red → **verify
> RED** (watch it fail, for the right reason; an immediately-passing test is
> testing existing behavior) → minimal green → **verify GREEN** (clean output,
> no warnings) → refactor (checked against `principles.md`). Two gates: before
> writing a test, name the production change that would make it fail (can't →
> redesign the test); before adding a mock, list the real side effects and never
> assert on the mock itself. **Mutation check:** wrong constant, flipped branch,
> missing side effect, empty return — a mutation nothing catches means the
> behavior is unprotected. Exceptions (prototypes, generated code) require
> asking.

> These three are standalone (any session, any repo) and are what the hook
> nudges orchestrate's implementer children toward.

### Required content

Frontmatter:

```yaml
---
name: tdd
description: Red/green/refactor discipline for implementing any feature or bug fix — write the failing test first, watch it fail for the right reason, write the minimum code to pass, then refactor. Use before writing production code for a new behavior or a fix, and whenever tests are being added after the fact.
metadata:
  compatibility: "claude, opencode"
---
```

Body, in this order:

1. **Iron law**, in a blockquote:
   > No production code without a failing test first. If production code was
   > written before its test, **delete it** — do not keep it open as a
   > reference while writing the test. A test written to match code you are
   > looking at tests what the code does, not what it should do.

2. **The cycle**, five explicit steps, each with what "done" means:
   1. **RED — write the test.** One behavior, named for the behavior.
   2. **VERIFY RED — run it and watch it fail.** Not "assume it fails."
      Two failure modes to distinguish, both required:
      - it must fail, and
      - it must fail **for the right reason** (the assertion, not a typo, a
        missing import, or a compile error).
      > A test that passes the first time you run it is testing behavior that
      > already exists. Either the behavior is already implemented — find out
      > why you thought otherwise — or the test does not exercise what you
      > think it does.
   3. **GREEN — minimum code to pass.** Not the general solution. Not the next
      three cases. The minimum.
   4. **VERIFY GREEN — run it again.** The target test passes, the rest of the
      suite still passes, and the output is **clean**: no new warnings, no new
      deprecation notices, no new log noise. Warnings introduced by your change
      are part of the change.
   5. **REFACTOR — now, while it is green.** Check the result against
      `skills/review/references/principles.md`. Re-run the suite after.

3. **Gate 1 — before writing a test.** Name, out loud, the **production change
   that would make this test fail**. If you cannot name one, the test is not
   testing your change — redesign it before writing it. One worked example of a
   failed gate (a test that asserts on a constant that no code path reads).

4. **Gate 2 — before adding a mock.** List the real side effects the mock
   stands in for. Then the rule:
   > Never assert on the mock itself. `expect(mock.called).toBe(true)` asserts
   > that you called your own test double; it says nothing about behavior.
   > Assert on the observable result the side effect produces.
   Add one line on preferring a real implementation, an in-memory fake, or a
   temp directory over a mock whenever one is available.

5. **Mutation check** — after green, before moving on. Pick one and apply it
   mentally (or actually, then revert):
   - change a constant to a wrong value
   - flip a branch condition
   - delete a side effect (the write, the emit, the close)
   - make a function return empty/zero
   > If a mutation survives — nothing fails — that behavior is unprotected.
   > Write the test that catches it.

6. **Exceptions require asking.** Throwaway prototypes and generated code are
   the only two, and both need the user to say so. State plainly: "this is
   hard to test" is not an exception, it is a design signal.

7. **Red flags** table — "I'll add tests after"; "the test is trivial, I know
   it fails"; "I'll write all the tests, then all the code"; "I'll assert the
   mock was called"; "the warning was already there" (check whether it was).

### Verification

```bash
cd /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-workflow-skill-suite
head -6 skills/tdd/SKILL.md
wc -l skills/tdd/SKILL.md
grep -i 'no production code without a failing test' skills/tdd/SKILL.md
grep -i -e 'verify red' -e 'verify green' skills/tdd/SKILL.md
grep -i 'mutation' skills/tdd/SKILL.md
grep -n 'principles.md' skills/tdd/SKILL.md
grep -i 'never assert on the mock' skills/tdd/SKILL.md
grep -riE 'doozyx|/Users/' skills/tdd/SKILL.md | wc -l
```

Expected: `name: tdd`; 80–115 lines; every grep above except the last prints;
the last prints `0`.

### Commit

```bash
cd /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-workflow-skill-suite
git branch --show-current   # must print: feature/workflow-skill-suite
git add skills/tdd/SKILL.md
git commit -m "feat(skills): add tdd discipline skill"
```

---

## T8 — `skills/debug/SKILL.md`

**tier: mid** · parallel-safe (wave 1)

### File

- Create `skills/debug/SKILL.md` (~100 lines, budget 90–120)

### Design extract (verbatim, design §4)

> **`debug`** (~100 lines). Iron law: *no fixes without root-cause investigation
> first*. Four sequential phases: investigate (read the actual error, reproduce,
> check recent changes, instrument component boundaries) → pattern analysis
> (find a working example, diff against it) → single hypothesis, smallest test,
> one variable → fix with a failing test first. **3-failed-fixes circuit
> breaker:** after the third failed fix, stop, question the architecture, talk
> to the human before attempt #4. Condensed red-flags table. Chains out to
> `tdd` (repro test) and `verify` (before claiming fixed).

### Required content

Frontmatter:

```yaml
---
name: debug
description: Systematic root-cause debugging for any bug, test failure, or unexpected behavior — investigate before fixing, compare against a working example, test one hypothesis at a time, and stop to question the architecture after three failed fixes. Use as soon as something breaks and before proposing or applying any fix.
metadata:
  compatibility: "claude, opencode"
---
```

Body, in this order:

1. **Iron law**, blockquote:
   > No fixes without root-cause investigation first. A fix applied before you
   > can state *why* the bug happens is a guess, and a guess that happens to
   > make the symptom disappear is the expensive kind.

2. **Phase 1 — Investigate.** Four steps, each with what to actually do:
   1. **Read the actual error.** The whole message, the whole stack, the whole
      failing assertion — not the summary of it. Quote the line that failed.
   2. **Reproduce it.** Find the smallest command that shows the failure
      reliably. Note the reproduction rate: an intermittent bug and a
      deterministic one need different hunts.
   3. **Check what changed recently.** `git log`, `git diff`, and the
      dependency lockfile. A bug that appeared today usually has a commit.
   4. **Instrument component boundaries.** Log/inspect the values crossing each
      boundary between the input and the symptom, and find the **first**
      boundary where the value is already wrong. That boundary — not the crash
      site — is where the bug lives.

3. **Phase 2 — Pattern analysis.** Find a case that **works**: a sibling call
   site, an analogous handler, the same function on different input, the same
   code before the breaking commit. Diff the working case against the broken
   one and list every difference. The cause is almost always in that list.

4. **Phase 3 — One hypothesis at a time.** State the hypothesis as a falsifiable
   sentence ("X is nil at Y because Z never runs when W"). Design the
   **smallest** test that distinguishes it from the alternatives. Change **one
   variable** per attempt. Write down the result before the next attempt —
   verbally "I tried a few things" is how the same attempt gets made twice.

5. **Phase 4 — Fix, test-first.** Write the failing test that reproduces the
   bug *before* the fix (chain out to `tdd` for the cycle), apply the fix,
   watch the test go green, then run the full suite. Before claiming it is
   fixed, chain out to `verify`.

6. **3-failed-fixes circuit breaker**, its own section, stated hard:
   > After the **third** failed fix attempt, stop. Do not attempt a fourth.
   > Three failures means the model of the system is wrong, not that the fix
   > was slightly off. Write down: what you believed, what you tried, what
   > actually happened each time. Then question the architecture — is the
   > component in the wrong place, is the invariant unenforceable, is this a
   > symptom of a design problem — and take it to the human before attempt #4.

7. **Red flags** table, condensed — the thought on the left, the reality on the
   right: "let me just try changing this"; "it's probably a race, let me add a
   sleep"; "I'll add a null check and move on"; "it works now, I don't know
   why"; "the test is flaky, let me retry it"; "let me rewrite this function".

8. **Chains out to** — one short section naming `tdd` (for the reproduction
   test) and `verify` (before claiming fixed).

### Verification

```bash
cd /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-workflow-skill-suite
head -6 skills/debug/SKILL.md
wc -l skills/debug/SKILL.md
grep -i 'no fixes without root-cause' skills/debug/SKILL.md
grep -ci -e 'phase 1' -e 'phase 2' -e 'phase 3' -e 'phase 4' skills/debug/SKILL.md
grep -i 'third' skills/debug/SKILL.md
grep -c -e '`tdd`' -e '`verify`' skills/debug/SKILL.md
grep -riE 'doozyx|/Users/' skills/debug/SKILL.md | wc -l
```

Expected: `name: debug`; 90–125 lines; the iron-law grep prints; the phase grep
≥ 4; the circuit-breaker grep prints; the chain grep ≥ 2; the last prints `0`.

### Commit

```bash
cd /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-workflow-skill-suite
git branch --show-current   # must print: feature/workflow-skill-suite
git add skills/debug/SKILL.md
git commit -m "feat(skills): add systematic debugging skill"
```

---

## T9 — `skills/verify/SKILL.md`

**tier: mid** · parallel-safe (wave 1)

### File

- Create `skills/verify/SKILL.md` (~70 lines, budget 60–90)

### Design extract (verbatim, design §4)

> **`verify`** (~70 lines). Iron law: *no completion claims without fresh
> evidence in this message*. Claim→evidence table: "tests pass" → 0-failure run
> now; "build works" → exit 0 (linter passing insufficient); "bug fixed" →
> original symptom re-tested; "regression test added" → verified red-green cycle
> (revert fix → test must fail → restore); **"child/agent completed" → the VCS
> diff, never the agent's success report**. Red flags: "should/probably/seems",
> any "Done!"/"Perfect!" before evidence.

### Required content

Frontmatter:

```yaml
---
name: verify
description: Evidence gate before any completion claim — every "it works", "tests pass", "fixed", or "done" must be backed by a command run in this message with its output shown. Use before committing, opening a PR, reporting a task complete, or telling the user something is working.
metadata:
  compatibility: "claude, opencode"
---
```

Body, in this order:

1. **Iron law**, blockquote:
   > No completion claim without fresh evidence **in this message**. Evidence
   > from three messages ago is a memory, not a verification — the tree has
   > changed since. Run the command now, show the output, then make the claim.

2. **Claim → evidence table** — the core of the skill. Reproduce at least these
   rows, each with the actual command shape and the acceptance condition:

   | Claim | Evidence required |
   | --- | --- |
   | "tests pass" | The suite run **now**, showing **0 failures**. A subset run proves the subset only — say which you ran. |
   | "build works" | The build command exiting **0**. A passing linter is **not** a build; a type-check is not a build. |
   | "the bug is fixed" | The **original symptom** re-tested by the original reproduction, now absent. |
   | "I added a regression test" | A verified red-green cycle: revert the fix → the test **fails** → restore the fix → the test **passes**. Show both runs. |
   | "the child/agent completed the work" | The **VCS diff** (`git log`, `git diff`) — never the agent's own success report. An agent reporting success is a claim, not evidence. |
   | "nothing else broke" | The full suite, compared against a recorded baseline from before the change. |
   | "it's deployed / running" | A request against the running thing, with its response. |

3. **How to show evidence.** Paste the command and the decisive lines of its
   output — the failure count, the exit status, the assertion. Not the whole
   log. If the output is large, show the tail and say what you filtered.

4. **Baselines.** One short section: record what already failed **before** you
   changed anything, and hold yourself accountable only for new failures — but
   only if you actually recorded it. A baseline claimed from memory is not a
   baseline.

5. **Red flags** table:
   - "should work" / "probably passes" / "seems fine" — hedging words are the
     tell that no command was run.
   - "Done!" / "Perfect!" / "All set!" written before any output appears in the
     message.
   - Citing a run from earlier in the conversation as current.
   - Reporting a subagent's or child session's summary as the result.
   - "The test file exists, so it's covered."

6. **When evidence cannot be gathered.** Say so explicitly and name the reason
   and the gap ("no browser available here — the e2e path is unverified"). An
   honest unverified is fine; a claim dressed as verified is not.

### Verification

```bash
cd /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-workflow-skill-suite
head -6 skills/verify/SKILL.md
wc -l skills/verify/SKILL.md
grep -i 'fresh evidence' skills/verify/SKILL.md
grep -c '|' skills/verify/SKILL.md
grep -i 'red-green\|revert the fix' skills/verify/SKILL.md
grep -i 'never the agent' skills/verify/SKILL.md
grep -i 'probably\|seems' skills/verify/SKILL.md
grep -riE 'doozyx|/Users/' skills/verify/SKILL.md | wc -l
```

Expected: `name: verify`; 60–95 lines; the iron-law, red-green, agent-report and
hedging greps each print; the table-pipe count ≥ 20; the last grep prints `0`.

### Commit

```bash
cd /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-workflow-skill-suite
git branch --show-current   # must print: feature/workflow-skill-suite
git add skills/verify/SKILL.md
git commit -m "feat(skills): add verification evidence-gate skill"
```

---

## T10 — Go: export child role markers into the tmux session env

**tier: mid** · parallel-safe (wave 1 — touches only Go files)

### Files

- Modify `internal/tmux/tmux.go` — add `UnsetEnvironment`
- Modify `internal/session/instance.go` — add `ensureRoleEnv`, call it at all 9
  `ensureProfileEnv()` call sites
- Create `internal/session/child_role_env_test.go`

### Why

`hooks/session-start` (task T11) must tell a dispatched child from an
interactive session by reading the tmux session environment alone — no DB
lookup. From the design, §5:

> **Detection requires a small Go change:** at launch, when the new session has
> a parent, agent-deck exports `AGENTDECK_ROLE=child` and `AGENTDECK_PARENT_ID`
> into the spawned tmux session environment. The hook reads only the env
> (`tmux show-environment`): `AGENTDECK_ROLE=child` → executor preamble;
> anything else (including non-agent-deck sessions, or no tmux at all) →
> interactive preamble, degrading silently. No DB lookups.

### Context you need (already verified — do not re-derive)

- The parent is set on the instance **before** it is started:
  `cmd/agent-deck/launch_cmd.go:516` calls
  `newInstance.SetParentWithPath(parentInstance.ID, parentInstance.ProjectPath)`,
  and `Start()` / `StartWithMessage()` run at lines 724/729. So inside those
  methods `i.ParentSessionID` is already populated.
- The field is `Instance.ParentSessionID string`
  (`internal/session/instance.go:128`).
- `ensureProfileEnv()` (`internal/session/instance.go:1117`) is the existing
  sibling helper. It is **called from 9 sites**: the two spawn paths (`Start`,
  `StartWithMessage`), the fallback recreate path in `Restart`, and six
  `respawn-pane` branches (claude, gemini, opencode, codex, cursor, generic)
  that each `return` before reaching the recreate path.
- `tmux.Session` has `SetEnvironment` and `GetEnvironment`
  (`internal/tmux/tmux.go:1837`, `:1880`) but **no** unset.

### Step 1 — `internal/tmux/tmux.go`

Insert immediately **after** the closing brace of `SetEnvironment` (the
function ending just before `func (s *Session) ApplyThemeOptions()`):

```go
// UnsetEnvironment removes an environment variable from this tmux session.
// Removing a variable that is not set is not an error. Mirrors
// SetEnvironment's error handling: CombinedOutput so tmux's stderr ("no server
// running on ...", "can't find session") reaches the caller instead of a bare
// "exit status 1".
func (s *Session) UnsetEnvironment(key string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := s.tmuxCmdContext(ctx, "set-environment", "-t", s.Name, "-u", key)
	out, err := cmd.CombinedOutput()
	if err == nil {
		// Invalidate cache entry so the next GetEnvironment sees the removal.
		s.envCacheMu.Lock()
		if s.envCache != nil {
			delete(s.envCache, key)
		}
		s.envCacheMu.Unlock()
		return nil
	}
	if trimmed := strings.TrimSpace(string(out)); trimmed != "" {
		return fmt.Errorf("%w: %s", err, trimmed)
	}
	return err
}
```

No new imports: `context`, `time`, `strings`, `fmt` are already imported in
that file. `tmuxCmdContext` is the sanctioned wrapper — using it keeps the
raw-tmux-exec lint test (`internal/tmux/tmux_exec_lint_test.go`) green.

### Step 2 — `internal/session/instance.go`, the helper

Insert immediately **after** the closing brace of `ensureProfileEnv` (which
ends at line ~1124, just before the `logClaudeConfigResolution` doc comment):

```go
// ensureRoleEnv publishes this session's orchestration role into the tmux
// session environment so in-session hooks can tell a dispatched child from an
// interactive session by reading the environment alone — no DB lookup, and it
// works from any shell inside the session.
//
// A parented session carries AGENTDECK_ROLE=child and AGENTDECK_PARENT_ID
// (the parent's instance id). An unparented session carries neither: the vars
// are actively removed rather than left alone, so a session that was
// un-parented (ClearParent, or re-homed elsewhere) and then restarted stops
// announcing itself as a child.
//
// Called alongside ensureProfileEnv at every spawn and respawn site, because
// each respawn-pane branch in Restart() returns before the fallback recreate
// path that would otherwise set it.
func (i *Instance) ensureRoleEnv() {
	if i.tmuxSession == nil {
		return
	}
	if i.ParentSessionID == "" {
		if err := i.tmuxSession.UnsetEnvironment("AGENTDECK_ROLE"); err != nil {
			sessionLog.Debug("unset_role_failed", slog.String("error", err.Error()))
		}
		if err := i.tmuxSession.UnsetEnvironment("AGENTDECK_PARENT_ID"); err != nil {
			sessionLog.Debug("unset_parent_id_failed", slog.String("error", err.Error()))
		}
		return
	}
	if err := i.tmuxSession.SetEnvironment("AGENTDECK_ROLE", "child"); err != nil {
		sessionLog.Warn("set_role_failed", slog.String("error", err.Error()))
	}
	if err := i.tmuxSession.SetEnvironment("AGENTDECK_PARENT_ID", i.ParentSessionID); err != nil {
		sessionLog.Warn("set_parent_id_failed", slog.String("error", err.Error()))
	}
}
```

Note the unset failures are `Debug`, not `Warn`: on a session that never had
the vars this is the ordinary path, and tmux may or may not treat it as an
error depending on version — it must not produce log noise on every launch.

### Step 3 — `internal/session/instance.go`, the call sites

Add `i.ensureRoleEnv()` on the line immediately following **every**
`i.ensureProfileEnv()` call. There are exactly 9. Find them with:

```bash
grep -n 'i\.ensureProfileEnv()' internal/session/instance.go
```

(The definition on line ~1117 reads `func (i *Instance) ensureProfileEnv() {`
and does **not** match this pattern, so every hit is a call site.)

Each edit looks like:

```go
	i.ensureProfileEnv()
	i.ensureRoleEnv()
```

Preserve the surrounding indentation exactly (some sites are inside `if`
blocks). Do not touch the comments above the `ensureProfileEnv()` calls.

### Step 4 — `internal/session/child_role_env_test.go`

New file. It follows the exact pattern of
`internal/session/profile_env_injection_test.go`; the helpers
`skipIfNoTmuxBinary`, `isolateUserHomeForShellRestart`, `uniqueShellTestTitle`,
`cleanupShellSessions` and `waitForTmuxSession` already exist in the package
(`testmain_test.go`, `shell_restart_test.go`).

```go
package session

import (
	"testing"
	"time"
)

// Child role markers (AGENTDECK_ROLE / AGENTDECK_PARENT_ID) in the tmux session
// environment.
//
// The plugin's SessionStart hook decides which preamble to inject by reading
// ONLY the tmux session environment — no DB lookup — so a parented launch must
// publish the markers and an unparented launch must publish nothing. Anything
// else silently gives an interactive session the executor preamble (or the
// reverse), which is invisible until a child starts brainstorming its task.

// startShellSession boots a bare shell session and waits for its tmux session
// to appear, failing the test if it never does.
func startShellSession(t *testing.T, tag string) *Instance {
	t.Helper()
	title := uniqueShellTestTitle(tag)
	inst := NewInstance(title, t.TempDir())
	inst.Command = ""
	return inst
}

func TestStart_ParentedSession_ExportsChildRoleEnv(t *testing.T) {
	skipIfNoTmuxBinary(t)
	isolateUserHomeForShellRestart(t)

	inst := startShellSession(t, "ChildRoleEnv")
	const parentID = "parent-instance-id-42"
	inst.SetParentWithPath(parentID, t.TempDir())

	if err := inst.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	t.Cleanup(func() { cleanupShellSessions(inst.Title) })

	if !waitForTmuxSession(inst.tmuxSession.Name, 1*time.Second) {
		t.Fatalf("tmux session %q never appeared after Start", inst.tmuxSession.Name)
	}

	role, err := inst.tmuxSession.GetEnvironment("AGENTDECK_ROLE")
	if err != nil {
		t.Fatalf("GetEnvironment(AGENTDECK_ROLE) failed: %v", err)
	}
	if role != "child" {
		t.Errorf("AGENTDECK_ROLE = %q, want %q", role, "child")
	}

	gotParent, err := inst.tmuxSession.GetEnvironment("AGENTDECK_PARENT_ID")
	if err != nil {
		t.Fatalf("GetEnvironment(AGENTDECK_PARENT_ID) failed: %v", err)
	}
	if gotParent != parentID {
		t.Errorf("AGENTDECK_PARENT_ID = %q, want %q", gotParent, parentID)
	}
}

func TestStart_UnparentedSession_HasNoChildRoleEnv(t *testing.T) {
	skipIfNoTmuxBinary(t)
	isolateUserHomeForShellRestart(t)

	inst := startShellSession(t, "NoRoleEnv")
	if err := inst.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	t.Cleanup(func() { cleanupShellSessions(inst.Title) })

	if !waitForTmuxSession(inst.tmuxSession.Name, 1*time.Second) {
		t.Fatalf("tmux session %q never appeared after Start", inst.tmuxSession.Name)
	}

	if got, err := inst.tmuxSession.GetEnvironment("AGENTDECK_ROLE"); err == nil {
		t.Errorf("unparented session exported AGENTDECK_ROLE=%q, want it unset", got)
	}
	if got, err := inst.tmuxSession.GetEnvironment("AGENTDECK_PARENT_ID"); err == nil {
		t.Errorf("unparented session exported AGENTDECK_PARENT_ID=%q, want it unset", got)
	}
}

// TestEnsureRoleEnv_ClearsStaleMarkersWhenUnparented pins the removal path: a
// session that carried the markers and then lost its parent must stop
// announcing itself as a child on the next spawn/respawn.
func TestEnsureRoleEnv_ClearsStaleMarkersWhenUnparented(t *testing.T) {
	skipIfNoTmuxBinary(t)
	isolateUserHomeForShellRestart(t)

	inst := startShellSession(t, "StaleRoleEnv")
	inst.SetParentWithPath("parent-to-be-removed", t.TempDir())
	if err := inst.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	t.Cleanup(func() { cleanupShellSessions(inst.Title) })

	if !waitForTmuxSession(inst.tmuxSession.Name, 1*time.Second) {
		t.Fatalf("tmux session %q never appeared after Start", inst.tmuxSession.Name)
	}
	if _, err := inst.tmuxSession.GetEnvironment("AGENTDECK_ROLE"); err != nil {
		t.Fatalf("precondition: parented session should carry AGENTDECK_ROLE: %v", err)
	}

	inst.ClearParent()
	inst.ensureRoleEnv()

	if got, err := inst.tmuxSession.GetEnvironment("AGENTDECK_ROLE"); err == nil {
		t.Errorf("after ClearParent, AGENTDECK_ROLE = %q, want it unset", got)
	}
	if got, err := inst.tmuxSession.GetEnvironment("AGENTDECK_PARENT_ID"); err == nil {
		t.Errorf("after ClearParent, AGENTDECK_PARENT_ID = %q, want it unset", got)
	}
}

// TestEnsureRoleEnv_NilTmuxSession_NoPanic pins the nil guard: a respawn branch
// must never panic when the instance has no tmux session.
func TestEnsureRoleEnv_NilTmuxSession_NoPanic(t *testing.T) {
	inst := NewInstanceWithTool("test", "/tmp/test", "claude")
	inst.tmuxSession = nil
	inst.ensureRoleEnv() // must not panic
}
```

All of that is verified against the current tree: `Instance.Title` is the field
(`internal/session/instance.go:121`), `cleanupShellSessions(title string)`
matches on the `ShellRestart-<tag>-<nanos>` prefix
(`internal/session/shell_restart_test.go:131,139`), and the package's
`TestMain` already bootstraps an **isolated tmux socket**, so these tests never
touch the user's real tmux server.

### Verification

```bash
cd /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-workflow-skill-suite

# 1. Exactly 9 role-env calls, one per profile-env call.
grep -c 'i\.ensureProfileEnv()' internal/session/instance.go   # expect: 9
grep -c 'i\.ensureRoleEnv()'    internal/session/instance.go   # expect: 9

# 2. Compiles and vets clean.
go build ./... && echo BUILD_OK
go vet ./internal/session/ ./internal/tmux/ && echo VET_OK

# 3. The new tests.
go test -race -count=1 -run 'RoleEnv' ./internal/session/ -v

# 4. The tmux package still passes its raw-exec lint.
go test -race -count=1 -run 'TestNoRawTmuxExec|ExecLint' ./internal/tmux/ -v

# 5. Nothing regressed in the two touched packages.
go test -race -count=1 ./internal/session/ ./internal/tmux/
```

Expected:
- step 1 prints `9` twice;
- step 2 prints `BUILD_OK` and `VET_OK`;
- step 3: `TestStart_ParentedSession_ExportsChildRoleEnv`,
  `TestStart_UnparentedSession_HasNoChildRoleEnv`,
  `TestEnsureRoleEnv_ClearsStaleMarkersWhenUnparented`,
  `TestEnsureRoleEnv_NilTmuxSession_NoPanic` all `--- PASS` (or `SKIP` for the
  three tmux-dependent ones if no `tmux` binary is present — in that case say
  so explicitly, do not report them as passing);
- step 4 passes;
- step 5 passes, **except** for failures that also reproduce on a clean
  checkout. Confirm any failure is pre-existing before blaming this change:

  ```bash
  git stash && go test -race -count=1 ./internal/session/ ; git stash pop
  ```

  Report the baseline failures explicitly in your summary.

**tmux hygiene:** these tests start real tmux sessions. If a run is interrupted,
sweep leftovers before re-running, or the macOS PTY pool exhausts and later
runs fail with "Device not configured":

`uniqueShellTestTitle` names them `ShellRestart-<tag>-<nanos>`, so:

```bash
tmux ls 2>/dev/null | grep -i 'ShellRestart-.*RoleEnv' || echo "no leftover test sessions"
```

Kill any leftovers with `tmux kill-session -t <name>` one at a time. **Never
`tmux kill-server`** — that socket is shared with the user's real sessions.

### Commit

```bash
cd /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-workflow-skill-suite
git branch --show-current   # must print: feature/workflow-skill-suite
git add internal/tmux/tmux.go internal/session/instance.go internal/session/child_role_env_test.go
git commit -m "feat(session): export AGENTDECK_ROLE/PARENT_ID for parented sessions"
```

### Interface this task produces (T11 consumes it)

A tmux session spawned by agent-deck for a **parented** instance carries
`AGENTDECK_ROLE=child` and `AGENTDECK_PARENT_ID=<parent id>` in its session
environment, readable from any pane with `tmux show-environment AGENTDECK_ROLE`.
An unparented session carries neither, and `tmux show-environment
AGENTDECK_ROLE` exits non-zero or prints `-AGENTDECK_ROLE`.

---

## T11 — SessionStart hook

**tier: mid** · parallel-safe (wave 1 — touches only `hooks/`)

### Files

- Create `hooks/hooks.json`
- Create `hooks/session-start` (mode `755`)
- Create `hooks/preamble-child.md`
- Create `hooks/preamble-interactive.md`

### Design extract (verbatim, design §5)

> `hooks/hooks.json` registers one SessionStart hook (matcher
> `startup|clear|compact` — compaction re-injection keeps discipline alive in
> long sessions). The `session-start` script injects one of two small preambles:
>
> - **Child sessions** (~15 lines): "You are a dispatched executor. Your task
>   prompt is the contract — do not brainstorm, do not re-open the design, do
>   not spawn your own review loop. Disciplines: `tdd` while implementing,
>   `debug` on failures, `verify` before reporting done. Report via the done
>   sentinel."
> - **Interactive sessions** (~15 lines): lean pipeline nudge — feature/change →
>   `design`; bug → `debug`; before claiming done → `verify`; `review` on
>   demand. No "1% chance → MUST" absolutism.
>
> **Detection requires a small Go change:** at launch, when the new session has
> a parent, agent-deck exports `AGENTDECK_ROLE=child` and `AGENTDECK_PARENT_ID`
> into the spawned tmux session environment. The hook reads only the env
> (`tmux show-environment`): `AGENTDECK_ROLE=child` → executor preamble;
> anything else (including non-agent-deck sessions, or no tmux at all) →
> interactive preamble, degrading silently. No DB lookups.

Binding constraint from the plan brief: **the hook script must degrade silently
outside tmux / outside agent-deck sessions, and `hooks.json` + the hook script
must follow Claude Code plugin hook conventions.**

### `hooks/hooks.json`

```json
{
  "hooks": {
    "SessionStart": [
      {
        "matcher": "startup|clear|compact",
        "hooks": [
          {
            "type": "command",
            "command": "\"${CLAUDE_PLUGIN_ROOT}/hooks/session-start\"",
            "shell": "bash",
            "async": false
          }
        ]
      }
    ]
  }
}
```

### `hooks/session-start`

Write exactly this, then `chmod +x hooks/session-start`.

```bash
#!/usr/bin/env bash
# SessionStart hook for the agent-deck plugin.
#
# Injects one of two short preambles depending on whether this session was
# dispatched as a child (an executor working a task prompt) or is an
# interactive session with a human at the keyboard.
#
# Detection reads ONLY the tmux session environment, which agent-deck populates
# at launch for parented sessions. Every probe is allowed to fail: no tmux, no
# tmux server, not an agent-deck session, an old agent-deck without the markers
# — all of them fall through to the interactive preamble. The hook must never
# fail a session start, so it exits 0 unconditionally.

set -uo pipefail   # deliberately NOT -e: probes below are expected to fail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# agentdeck_role prints the session's AGENTDECK_ROLE, or nothing.
agentdeck_role() {
  command -v tmux >/dev/null 2>&1 || return 0
  [ -n "${TMUX:-}" ] || return 0
  local line
  line="$(tmux show-environment AGENTDECK_ROLE 2>/dev/null)" || return 0
  # tmux prints "AGENTDECK_ROLE=<value>" when set, and "-AGENTDECK_ROLE"
  # (leading dash = removed) or nothing when it is not.
  case "$line" in
    AGENTDECK_ROLE=*) printf '%s' "${line#AGENTDECK_ROLE=}" ;;
    *)                return 0 ;;
  esac
}

if [ "$(agentdeck_role)" = "child" ]; then
  preamble_file="${SCRIPT_DIR}/preamble-child.md"
else
  preamble_file="${SCRIPT_DIR}/preamble-interactive.md"
fi

preamble="$(cat "$preamble_file" 2>/dev/null)" || preamble=""
[ -n "$preamble" ] || exit 0   # nothing to inject; never fail the session

# JSON-escape via bash parameter substitution (one C-level pass each).
escape_for_json() {
  local s="$1"
  s="${s//\\/\\\\}"
  s="${s//\"/\\\"}"
  s="${s//$'\n'/\\n}"
  s="${s//$'\r'/\\r}"
  s="${s//$'\t'/\\t}"
  printf '%s' "$s"
}

context="$(escape_for_json "$preamble")"

# Claude Code reads hookSpecificOutput.additionalContext; other SDK-standard
# harnesses read a top-level additionalContext. Emit exactly one so nothing is
# injected twice. printf, not a heredoc — bash 5.3+ can hang on heredocs here.
if [ -n "${CLAUDE_PLUGIN_ROOT:-}" ]; then
  printf '{\n  "hookSpecificOutput": {\n    "hookEventName": "SessionStart",\n    "additionalContext": "%s"\n  }\n}\n' "$context"
else
  printf '{\n  "additionalContext": "%s"\n}\n' "$context"
fi

exit 0
```

### `hooks/preamble-child.md`

Write this content (it is the injected text, so keep it tight — ~15 lines):

```markdown
You are a **dispatched executor**. A supervising session launched you with a
task prompt, and that prompt is your contract.

- Do **not** brainstorm, and do not invoke a design/brainstorming skill. The
  design is approved and upstream of you.
- Do **not** re-open or revise the design or the plan. If you believe the task
  is genuinely wrong, say so in one line and stop — do not redesign around it.
- Do **not** spawn your own review loop. Your supervisor runs one after you.
- Do **not** wait for approval. There is no user in this session to give it.

Disciplines to use as you work:

- `tdd` while implementing — failing test first.
- `debug` when something breaks — root cause before fix.
- `verify` before you report done — fresh evidence, in the message.

Report completion by printing the done sentinel as your last line:
`===AGENTDECK_DONE=== status=<ok|fail> summary=<one line>`
```

### `hooks/preamble-interactive.md`

```markdown
Workflow skills are available in this session. Reach for them when they fit —
this is a pipeline nudge, not a mandate:

- Building a feature, adding functionality, or changing behavior → start with
  `design`: it explores context, asks one question at a time, and produces an
  approved, committed design document before any code is written.
- Something is broken — a bug, a failing test, unexpected behavior → `debug`:
  root-cause investigation before any fix, with a circuit breaker after three
  failed attempts.
- Writing the code → `tdd`: failing test first, watch it fail for the right
  reason, minimum code to pass, then refactor.
- About to say it works, it's fixed, or it's done → `verify`: every completion
  claim needs fresh evidence in the same message.
- Want a second pair of eyes on a diff → `review`: adversarial, edge-case, and
  verification-gap layers merged into one triaged findings list.

Use judgment about which apply. Skip the ones that don't.
```

### Verification

```bash
cd /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-workflow-skill-suite

# 1. Files exist, script is executable.
ls -l hooks/hooks.json hooks/session-start hooks/preamble-child.md hooks/preamble-interactive.md
test -x hooks/session-start && echo EXECUTABLE_OK

# 2. hooks.json is valid JSON with the right matcher.
python3 -c "import json;d=json.load(open('hooks/hooks.json'));print(d['hooks']['SessionStart'][0]['matcher'])"
# expect: startup|clear|compact

# 3. Script is syntactically valid bash.
bash -n hooks/session-start && echo SYNTAX_OK

# 4. Degrades silently with no tmux at all -> interactive preamble, exit 0.
env -u TMUX -u CLAUDE_PLUGIN_ROOT bash hooks/session-start > /tmp/hook-nontmux.json; echo "exit=$?"
python3 -c "import json;d=json.load(open('/tmp/hook-nontmux.json'));c=d['additionalContext'];assert 'dispatched executor' not in c, 'leaked child preamble';assert 'design' in c;print('INTERACTIVE_OK')"

# 5. Claude Code output shape.
env -u TMUX CLAUDE_PLUGIN_ROOT=. bash hooks/session-start > /tmp/hook-claude.json; echo "exit=$?"
python3 -c "import json;d=json.load(open('/tmp/hook-claude.json'));h=d['hookSpecificOutput'];assert h['hookEventName']=='SessionStart';assert h['additionalContext'];print('CLAUDE_SHAPE_OK')"

# 6. Child detection, against a real tmux session carrying the marker.
#    -L adhooktest puts this on a DEDICATED socket, so nothing here can touch
#    the user's real tmux server. Keep the -L on every tmux call below.
tmux -L adhooktest new-session -d -s hooktest 'sleep 120'
tmux -L adhooktest set-environment -t hooktest AGENTDECK_ROLE child
tmux -L adhooktest run-shell -t hooktest "CLAUDE_PLUGIN_ROOT=$(pwd) $(pwd)/hooks/session-start > /tmp/hook-child.json 2>/tmp/hook-child.err"
sleep 1
python3 -c "import json;d=json.load(open('/tmp/hook-child.json'));c=d['hookSpecificOutput']['additionalContext'];assert 'dispatched executor' in c, c[:200];assert 'AGENTDECK_DONE' in c;print('CHILD_OK')"

# 7. Same session without the marker -> interactive.
tmux -L adhooktest set-environment -t hooktest -u AGENTDECK_ROLE
tmux -L adhooktest run-shell -t hooktest "CLAUDE_PLUGIN_ROOT=$(pwd) $(pwd)/hooks/session-start > /tmp/hook-nochild.json 2>&1"
sleep 1
python3 -c "import json;d=json.load(open('/tmp/hook-nochild.json'));c=d['hookSpecificOutput']['additionalContext'];assert 'dispatched executor' not in c;print('NO_MARKER_OK')"

# 8. Tear down the dedicated socket — always. Safe because of the -L.
tmux -L adhooktest kill-server 2>/dev/null
tmux -L adhooktest ls 2>/dev/null && echo "LEFTOVER — retry the kill" || echo "CLEANUP_OK"

# 9. No environment-specific paths in the shipped files.
grep -riE 'doozyx|/Users/' hooks/ | wc -l   # expect: 0
```

Expected: `EXECUTABLE_OK`, matcher line, `SYNTAX_OK`, `exit=0` on both plain
runs, `INTERACTIVE_OK`, `CLAUDE_SHAPE_OK`, `CHILD_OK`, `NO_MARKER_OK`,
`CLEANUP_OK`, and `0` from the last grep.

If step 6/7's `tmux run-shell` proves awkward in your environment, substitute a
direct run inside the session's own pane — the requirement is only that the
script runs with `$TMUX` pointing at a session whose environment carries (then
lacks) `AGENTDECK_ROLE=child`. **Do not** substitute a plain
`AGENTDECK_ROLE=child bash hooks/session-start`: the script deliberately reads
tmux, not the process environment, and that would not test the real path.

**Never run a bare `tmux kill-server`** while verifying — without the
`-L adhooktest` it targets the user's real server and kills every live session
on it. Every tmux call in steps 6–8 must carry the `-L`.

### Commit

```bash
cd /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-workflow-skill-suite
git branch --show-current   # must print: feature/workflow-skill-suite
git add hooks/
git update-index --chmod=+x hooks/session-start
git commit -m "feat(hooks): child-aware SessionStart preamble injection"
git show --stat HEAD | grep session-start   # confirm mode 100755
```

---

## T12 — Register the new skills (and the hooks) in the marketplace

**tier: cheap** · wave 2 · parallel-safe with T13

### File

- Modify `.claude-plugin/marketplace.json`

### Current content

```json
{
  "name": "agent-deck",
  "owner": {
    "name": "Ashesh Goplani",
    "email": "ashesh.goplani96@gmail.com"
  },
  "metadata": {
    "description": "Skills for managing AI coding agent sessions with agent-deck CLI",
    "version": "1.1.0"
  },
  "plugins": [
    {
      "name": "agent-deck",
      "description": "Complete guide for managing AI coding agent sessions via agent-deck CLI - session lifecycle, MCP management, groups, profiles, and automation",
      "source": "./",
      "strict": false,
      "skills": [
        "./skills/agent-deck",
        "./skills/session-share",
        "./skills/fleet",
        "./skills/orchestrate"
      ]
    }
  ]
}
```

### Replace with

```json
{
  "name": "agent-deck",
  "owner": {
    "name": "Ashesh Goplani",
    "email": "ashesh.goplani96@gmail.com"
  },
  "metadata": {
    "description": "Skills for managing AI coding agent sessions with agent-deck CLI",
    "version": "1.2.0"
  },
  "plugins": [
    {
      "name": "agent-deck",
      "description": "Complete guide for managing AI coding agent sessions via agent-deck CLI - session lifecycle, MCP management, groups, profiles, automation, and the workflow discipline suite (design, review, tdd, debug, verify)",
      "source": "./",
      "strict": false,
      "skills": [
        "./skills/agent-deck",
        "./skills/session-share",
        "./skills/fleet",
        "./skills/orchestrate",
        "./skills/design",
        "./skills/review",
        "./skills/tdd",
        "./skills/debug",
        "./skills/verify"
      ],
      "hooks": "./hooks/hooks.json"
    }
  ]
}
```

Three changes: bump `metadata.version` to `1.2.0`, extend `description` and the
`skills` array with the five new skill directories, and add the explicit
`hooks` path (Claude Code also auto-discovers `hooks/hooks.json` at the plugin
root; the explicit entry makes it unambiguous alongside the explicit `skills`
list).

### Verification

```bash
cd /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-workflow-skill-suite

python3 - <<'PY'
import json, os, sys
d = json.load(open(".claude-plugin/marketplace.json"))
p = d["plugins"][0]
missing = [s for s in p["skills"] if not os.path.isfile(os.path.join(s, "SKILL.md"))]
assert not missing, f"skills listed with no SKILL.md: {missing}"
for s in ["./skills/design", "./skills/review", "./skills/tdd", "./skills/debug", "./skills/verify"]:
    assert s in p["skills"], f"not registered: {s}"
assert p["hooks"] == "./hooks/hooks.json"
assert os.path.isfile("hooks/hooks.json"), "hooks.json missing"
assert d["metadata"]["version"] == "1.2.0"
print(f"OK — {len(p['skills'])} skills registered, all present")
PY
```

Expected: `OK — 9 skills registered, all present`.

Also confirm every skill dir on disk is registered (catches a skill written but
forgotten):

```bash
cd /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-workflow-skill-suite
for d in skills/*/; do
  name="./${d%/}"
  grep -q "\"$name\"" .claude-plugin/marketplace.json && echo "registered: $name" || echo "MISSING FROM MARKETPLACE: $name"
done
```

Expected: nine `registered:` lines, no `MISSING` line.

### Commit

```bash
cd /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-workflow-skill-suite
git branch --show-current   # must print: feature/workflow-skill-suite
git add .claude-plugin/marketplace.json
git commit -m "feat(plugin): register workflow skill suite and SessionStart hook"
```

---

## T13 — Orchestrate upgrades (three targeted edits)

**tier: mid** · wave 2 · parallel-safe with T12

### File

- Modify `skills/orchestrate/SKILL.md` — **three targeted changes, not a
  rewrite.** Everything not named below stays byte-for-byte identical.

### Design extract (verbatim, design §6)

> Three targeted changes, no restructuring:
>
> 1. **Story-file tasks.** The planner child emits
>    `docs/plans/<date>-<slug>-tasks/task-NN-<name>.md`, each self-contained:
>    relevant design-doc extracts **embedded** (not linked), acceptance
>    criteria, exact file paths, and an Interfaces consumes/produces block so a
>    child that sees only its own task knows its neighbors' names. Implementer
>    children read **only their task file** — a child that never reads the full
>    design can't drift from it. Each task file has a small append-only record
>    section the child writes (commits, files touched, concerns): audit trail
>    without conductor context cost.
> 2. **Reviewer children run the shared layers.** The fresh-reviewer prompt
>    shrinks to: task file + diff range + "execute the layers per
>    `skills/review/references/`, write the verdict file." The conductor
>    branches on the machine-readable verdict (`clean` / `fix-needed` +
>    bucketed findings); `decision-needed` escalates to the user;
>    `defer` items append to the run's deferred-work file and never extend the
>    loop.
> 3. **Discipline preambles shrink.** Hand-written "you must TDD/verify" child
>    boilerplate becomes one line pointing at the leaf skills — the hook already
>    injects the executor preamble. Existing anti-superpowers defenses stay
>    (upstream users may still run superpowers).

### Change 1 — story-file tasks (planner prompt + decomposition paragraph)

**1a.** In the `## Planning stage` section, the planner prompt template
currently reads (find it by the line `Read the approved design at
<spec-path>`). Replace the two lines:

```text
Write an implementation plan to docs/plans/<date>-<task-slug>-plan.md:
ordered, bite-sized tasks; per task: exact file paths, the actual code or
```

...so the template instructs the planner to emit **both** the plan and one task
file per task. The full replacement for that template block is:

```text
Read the approved design at <spec-path> and explore the codebase as needed.
Write an implementation plan to docs/plans/<date>-<task-slug>-plan.md:
ordered, bite-sized tasks; per task: exact file paths, the actual code or
edit, verification commands with expected output, and the interfaces later
tasks rely on. Mark any tasks that are safe to run in parallel (disjoint
files). Tag every task with `tier: cheap | mid | strong` — cheap when the
task is pure transcription of code this plan already contains, mid when it
needs local judgment within a clear spec, strong when it makes design
decisions. Assume each task's executor has ZERO context beyond that one task.
Size every task to fit comfortably in a single fresh session's context
window: if completing it would require reading more than roughly 100k tokens
of code, docs, and test output, split it further — a task that blows up its
executor's context costs a handoff mid-implementation.

Then emit one self-contained task file per task at
docs/plans/<date>-<task-slug>-tasks/task-NN-<name>.md. Each task file must
stand alone for a child that reads nothing else:
- the relevant design-doc extracts EMBEDDED verbatim, never linked;
- acceptance criteria;
- exact file paths and the actual code or edit;
- verification commands with expected output;
- an `## Interfaces` block with `consumes:` and `produces:` — the exact
  names, signatures, and paths this task relies on and hands over, so a
  child that sees only its own file knows its neighbours' names;
- a trailing `## Record (append-only)` section, left empty, for the
  implementer to append its commits, files touched, and concerns.

No placeholders (no TBD / "add error handling" / "similar to task N").
Commit the plan and the task files to the current branch. Do NOT implement
anything.
```

**1b.** In `### Reviewing the plan`, the paragraph beginning "The plan's task
list now replaces your own decomposition" currently reads:

```text
The plan's task list now replaces your own decomposition: subtasks = plan
tasks (see `references/single-issue-split.md`), each implementer receives its
plan task verbatim as its spec, and each reviewer receives that same plan
task as the spec to check compliance against.
```

Replace with:

```text
The plan's task list now replaces your own decomposition: subtasks = plan
tasks (see `references/single-issue-split.md`), each implementer is pointed
at its own task file as its spec, and each reviewer is pointed at that same
task file as the spec to check compliance against.

**Implementers read only their own task file.** Do not hand a child the
design doc or the full plan alongside it — a child that never reads the full
design cannot drift from it, and the task file was written to be sufficient.
The `## Record (append-only)` section at the end of each task file is the
child's audit trail: it appends its commits, the files it touched, and any
concern it hit. That record costs you no context — you read it only when a
task goes needs-attention.
```

### Change 2 — reviewer children run the shared layers

**2a.** In `### 2. Fresh review`, replace the whole reviewer prompt template
(the fenced block that currently starts `You are a code reviewer with fresh
eyes.` and ends with the `VERDICT: findings blockers=<n> should-fix=<n>
nits=<n>` line) with:

```text
You are a code reviewer with fresh eyes. You are READ-ONLY: edit nothing,
commit nothing, run only read-only commands plus the test suite. You may be
sharing this worktree with a live implementer session, so never run a command
that rewrites the working tree: no `git stash`, `git checkout`, `git restore`,
`git reset`, `git clean`, no branch switching. A tree that looks dirty or
wrong is a finding to report, never a thing for you to tidy up.

The task this branch is supposed to implement is in this file — read it and
nothing else for the spec: <task-file-path>

Review the full branch diff: git diff $(git merge-base <base-branch> HEAD)...HEAD

Execute the review layers per <agent-deck-repo>/skills/review/references/ —
run `adversarial.md`, `edge-cases.md` and `verification-gap.md`, plus
`deletion-check.md` if the diff removes meaningful code — then merge, dedup,
grade severity and triage exactly as <agent-deck-repo>/skills/review/SKILL.md
describes. Add spec compliance against the task file above as an explicit
concern threaded into every layer: anything missing, extra, or misunderstood
is a finding. Also run the test suite and judge whether the tests actually
cover the change.

Known pre-existing test failures (the implementer's recorded baseline):
<baseline list, or "none">. These are NOT findings — only failures new
against this baseline are.

Write your full output to <verdict-file-path>, and print only the findings
list, the "Checked:" lines and the verdict line as your response.
End with exactly one line, using real counts:
VERDICT: clean
VERDICT: fix-needed patch=<n> decision-needed=<n> defer=<n>
```

**2b.** In `### 3. Fix loop`, the first bullet currently reads:

```text
- A findings verdict with `blockers=0 should-fix=0` (nits only) counts as
  clean; list the nits in the final report.
```

Replace with:

```text
- The verdict is machine-readable and you branch on it directly. `VERDICT:
  clean` → proceed. `VERDICT: fix-needed` → look at the buckets, not the raw
  count:
  - `patch` items go back to the implementer as a fix round.
  - `decision-needed` items are **not** the implementer's to resolve —
    escalate them to the user exactly like a waiting child's question (see
    "Answering waiting children"), and hold that task while you wait.
  - `defer` items append to `$RUN_DIR/deferred-work.md` and **never extend
    the loop**; they are listed in the final report and go no further.
  A verdict whose only findings are `defer` items is emitted as
  `VERDICT: clean` by construction, so nothing extra is needed for that case.
```

**2c.** In `### 3. Fix loop`, the severity paragraph currently begins "**The
reviewer proposes a severity; you decide it.**" and uses the old scale. Update
only the scale words in that paragraph: `nit` → `minor`, and the closing
sentence "Regrade upward and send it back" keeps its meaning. The rule to
preserve verbatim in meaning is: **a finding whose blast radius is existing
data, introduced by this branch, is never a `minor`.** Keep the worked example
about the stored workload of 0 seeded as 100.

**2d.** In `### 3. Fix loop`, the incremental reviewer prompt template ends
with the same old verdict lines. **Apply 2a first** — the four-line block below
appears twice in the untouched file (once in each reviewer template), and 2a
removes the first occurrence, leaving this one unambiguous:

```text
Report findings as a numbered list: file:line — severity (blocker |
should-fix | nit) — one line each. Then 2-3 "Checked:" evidence lines.
End with exactly one line, using real counts:
VERDICT: clean
VERDICT: findings blockers=<n> should-fix=<n> nits=<n>
```

Replace with:

```text
Report findings in the merged format from
<agent-deck-repo>/skills/review/SKILL.md: file:line — severity (critical |
major | minor) — [patch | decision-needed | defer] — provenance — one line
each. Then 2-3 "Checked:" evidence lines.
End with exactly one line, using real counts:
VERDICT: clean
VERDICT: fix-needed patch=<n> decision-needed=<n> defer=<n>
```

**2e.** In `### 3. Fix loop`, the "Full-branch end gate" bullet says "Gate
clean or nits-only → proceed to the PR." Change `nits-only` to `defer-only`.
In `### 5. CI babysit`, the sentence "A task counts as **done** only when the
review is clean (or nits-only)" becomes "(or defer-only)". In the
`## Final report` template, `open items: <list>` stays as is.

**2f.** In `### 3. Fix loop`, the caps bullet says "Budget exhausted with
blockers remaining → the task is **needs-attention**, no PR; only
should-fixes/nits remaining → proceed to the PR". Rewrite the scale words:
"Budget exhausted with `patch` items remaining → the task is
**needs-attention**, no PR; only `defer` items remaining → proceed to the PR
and list them in the final report."

### Change 3 — discipline preambles shrink

**3a.** In `### 1. Implement`, the implementer prompt template currently has
step 3:

```text
3. Implement the task test-first where practical.
```

Replace with:

```text
3. Implement the task test-first (`tdd`), debug failures at the root (`debug`),
   and gate every completion claim on fresh evidence (`verify`).
```

**3b.** In `### 1. Implement`, the implementer prompt template's opening lines
currently read:

```text
Task: <title>

<task spec: issue body or freeform description>
```

Replace with:

```text
Task: <title>

Your spec is this file — read it, and read nothing else for the spec:
<task-file-path>

(For a task with no task file — a freeform or single-small-task run — paste
the spec here instead: <task spec: issue body or freeform description>)
```

**3c.** In `## Child prompt preamble (every child, every role)`, append this
paragraph immediately **after** the existing fenced preamble block and the
planner-exception paragraph, leaving both unchanged (the anti-superpowers
defenses stay — upstream users may still run superpowers):

```text
Keep the preamble to that block. The rest of the executor discipline —
"use tdd", "verify before reporting done", "do not spawn your own review
loop" — is injected automatically by the agent-deck plugin's SessionStart
hook for any session that has a parent, so it does not belong in your prompt
text. One line pointing at the leaf skills (`tdd`, `debug`, `verify`) is
enough; the hook carries the rest. The anti-brainstorm block above stays
regardless: a user's own globally-installed process skills are outside the
hook's reach.
```

### Verification

```bash
cd /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-workflow-skill-suite

# The three changes landed.
grep -c 'task-NN-<name>.md' skills/orchestrate/SKILL.md          # >= 1
grep -c 'Record (append-only)' skills/orchestrate/SKILL.md       # >= 2
grep -c 'skills/review/references' skills/orchestrate/SKILL.md   # >= 2
grep -c 'VERDICT: fix-needed patch=' skills/orchestrate/SKILL.md # == 2
grep -n '`tdd`' skills/orchestrate/SKILL.md
grep -n 'SessionStart' skills/orchestrate/SKILL.md

# The OLD scale is fully gone. Before your edits this prints 15 matching
# lines (454, 460, 465, 467, 470, 472, 475, 520, 523, 532, 539, 540, 599 and
# two more inside the same paragraphs); after them it must print 0.
grep -cE 'blockers?=|should-fix|nits|blocker \||\bnit\b' skills/orchestrate/SKILL.md   # expect: 0

# The anti-superpowers defense is still there (must NOT be removed).
grep -c 'superpowers' skills/orchestrate/SKILL.md                # expect: 4 (unchanged)
grep -c 'This is EXECUTION of already-approved work' skills/orchestrate/SKILL.md  # expect: 1

# Referenced layer files actually exist.
ls skills/review/SKILL.md skills/review/references/*.md

# Not a rewrite: the diff should be surgical.
git diff --stat skills/orchestrate/SKILL.md
```

Expected: the first six greps all print; the old-scale grep prints `0`;
`superpowers` still appears 4 times and the preamble block exactly once; the
`ls` lists six files; `git diff --stat` shows roughly 90–150 changed lines on a
917-line file — if it shows several hundred, you rewrote it, so
`git checkout skills/orchestrate/SKILL.md` and redo the targeted edits.

**Every one of the 15 old-scale lines is covered by an edit above** — 454/460
by 2a, 465 by 2b, 467–475 by 2c, 520/523 by 2d, 532/599 by 2e, 539/540 by 2f.
If the final grep is non-zero, find the line and map it back to whichever of
those changes should have caught it rather than patching it in isolation.

Also re-read the file end to end once to confirm no section was orphaned by an
edit (a template block that lost its introduction, a bullet that now
contradicts the one above it).

### Commit

```bash
cd /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-workflow-skill-suite
git branch --show-current   # must print: feature/workflow-skill-suite
git add skills/orchestrate/SKILL.md
git commit -m "docs(skills): orchestrate story-file tasks, shared review layers, lean preambles"
```

---

## T14 — CHANGELOG entry and final suite check

**tier: cheap** · wave 3 (last)

### File

- Modify `CHANGELOG.md`

### Edit

Under `## [Unreleased]` → `### Added`, insert these two bullets **at the top of
the existing `### Added` list** (keep the existing bullets below, untouched):

```markdown
- **Workflow discipline skills shipped with the plugin.** Five new skills —
  `design` (collaborative brainstorming behind a hard approval gate, writing a
  committed design doc), `review` (an adversarial pass, a mechanical edge-case
  path trace and a verification-gap check, merged into one deduplicated,
  severity-graded, triaged findings list with a machine-readable verdict),
  `tdd`, `debug` and `verify` — plus a shared review methodology under
  `skills/review/references/` that both the interactive `review` skill and
  `orchestrate`'s fresh-reviewer children execute from the same files. The
  `orchestrate` skill gains self-contained per-task story files (each with
  embedded design extracts and a consumes/produces interfaces block, so an
  implementer reads only its own task), reviewer children that run the shared
  layers, and leaner child preambles.
- **Child sessions announce themselves in their tmux environment.** A session
  launched with a parent now carries `AGENTDECK_ROLE=child` and
  `AGENTDECK_PARENT_ID=<parent id>` in its tmux session environment, alongside
  the existing `AGENTDECK_INSTANCE_ID` and `AGENTDECK_PROFILE`. An unparented
  session carries neither, and the markers are cleared if a session loses its
  parent. This lets in-session hooks distinguish a dispatched executor from an
  interactive session without a database lookup — the plugin's new SessionStart
  hook uses it to inject an executor preamble into children and a lean pipeline
  nudge into interactive sessions, degrading silently to the interactive
  preamble outside tmux and outside agent-deck.
```

### Verification

```bash
cd /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-workflow-skill-suite

grep -n 'Workflow discipline skills' CHANGELOG.md
grep -n 'AGENTDECK_ROLE=child' CHANGELOG.md

# Whole-repo gate: build, vet, and the Go suite.
go build ./... && echo BUILD_OK
go vet ./... && echo VET_OK
go test -race -count=1 ./internal/session/ ./internal/tmux/ ./cmd/agent-deck/ 2>&1 | tail -30

# Every deliverable is present and committed.
git status --short          # expect: clean after the commit below
ls skills/design/SKILL.md skills/review/SKILL.md skills/tdd/SKILL.md \
   skills/debug/SKILL.md skills/verify/SKILL.md \
   skills/review/references/adversarial.md \
   skills/review/references/edge-cases.md \
   skills/review/references/verification-gap.md \
   skills/review/references/deletion-check.md \
   skills/review/references/principles.md \
   hooks/hooks.json hooks/session-start \
   hooks/preamble-child.md hooks/preamble-interactive.md

# Upstream-genericity sweep across everything this branch added.
grep -riE 'doozyx|/Users/|docs/superpowers' skills/ hooks/ .claude-plugin/ | wc -l   # expect: 0

# Nothing shipped is gitignored (the trap the design names).
for f in skills/design/SKILL.md skills/review/SKILL.md hooks/session-start hooks/hooks.json; do
  git check-ignore -v "$f" && echo "IGNORED: $f" || echo "tracked ok: $f"
done
```

Expected: both CHANGELOG greps print; `BUILD_OK` and `VET_OK`; the Go suite tail
shows only failures that also reproduce on a clean checkout (verify with
`git stash` / `git stash pop` and **report the baseline explicitly**); the `ls`
lists all 14 files; the genericity grep prints `0`; four `tracked ok:` lines and
no `IGNORED:` line.

### Commit

```bash
cd /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-workflow-skill-suite
git branch --show-current   # must print: feature/workflow-skill-suite
git add CHANGELOG.md
git commit -m "docs(changelog): workflow skill suite and child role env markers"
```

---

## Post-implementation, for the conductor (not a task)

The design's §7 testing and migration steps are **manual and interactive** —
they need a human at a keyboard and a live agent-deck, so they are deliberately
not tasks in this plan:

- **Hook end-to-end:** launch one parented child and one plain session; confirm
  each received the correct preamble.
- **Discipline pressure-test:** hand a subagent a tempting shortcut ("just fix
  it quickly, skip the test") and confirm it complies with `tdd` anyway.
- **Review golden test:** run `review` on a historical diff from this repo with
  known documented bugs; the layers must find them without hallucinating
  severity.
- **Migration:** reinstall the agent-deck plugin from this repo (directory
  marketplace), do one session of side-by-side sanity (`design` on something
  small, `review` on a real diff), then uninstall superpowers at user scope.

## Out of scope for this plan (from the design's own out-of-scope list)

- Porting superpowers' `writing-plans` / `executing-plans` /
  `subagent-driven-development` / `finishing-a-development-branch`.
- BMAD-style personas, PRD stacks, sprint/retro rituals, TEA test-scoring.
- An external conductor state-machine file for orchestrate.
- Any change to the `fleet`, `session-share`, or `agent-deck` skills.
