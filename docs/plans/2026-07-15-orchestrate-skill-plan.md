# Orchestrate Skill Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the `agent-deck:orchestrate` skill — a conductor-driven pipeline that turns tasks/issues into merge-ready PRs (worktree → implement → verify → review loop → PR → CI green) — plus a one-line disambiguation edit to the fleet skill.

**Architecture:** Pure markdown skill addition following the existing `skills/fleet/` pattern: `skills/orchestrate/SKILL.md` holds the shared core (input parsing, per-task pipeline, review loop, screenshots, PR + CI); `skills/orchestrate/references/single-issue-split.md` is loaded only when the conductor decides to split one big issue. Fleet stays the transport-mechanics manual that orchestrate reads first and never restates.

**Tech Stack:** Markdown skills (agent-deck plugin `skills/` directory), agent-deck CLI (`launch`, `session children/send/output`), `gh` CLI, git worktrees.

**Spec:** `docs/plans/2026-07-15-orchestrate-skill-design.md` (same branch).

## Global Constraints

- All work happens in the worktree `.worktrees/orchestrate-skill` on branch `feat/orchestrate-skill`. Run `git branch --show-current` before every commit and stop if it is not `feat/orchestrate-skill` (other agents may switch branches).
- Frontmatter must match the fleet skill's shape exactly: `name`, `description`, `metadata.compatibility: "claude, opencode"`.
- The skill must never instruct anyone to put screenshot paths, run directories, or orchestrate/session details into a PR, commit, or push.
- Screenshot root is exactly `~/.agent-deck/orchestrate/<run-id>/<task-slug>/`.
- Review loop cap is exactly 3 rounds; pipeline concurrency cap is 3.
- The conductor never edits code and never works in the main checkout — these two rules must appear verbatim in SKILL.md.
- File content in this plan is final copy — transcribe it exactly; do not paraphrase or "improve" it.

---

### Task 1: `skills/orchestrate/SKILL.md`

**Files:**
- Create: `skills/orchestrate/SKILL.md`

**Interfaces:**
- Consumes: `skills/fleet/SKILL.md` (must exist; referenced by relative path).
- Produces: the reference pointer `references/single-issue-split.md` (Task 2 must create exactly that path); session-title conventions `impl-<task-slug>`, `review-<task-slug>-r<N>` (Task 2 reuses them); the "per-task pipeline" stage numbering 1–5 (Task 2 refers to "stages 1–3" and "stages 4–5").

- [ ] **Step 1: Create the file with this exact content**

````markdown
---
name: orchestrate
description: End-to-end delivery pipeline for tasks/issues. Per task - dedicated worktree child implements + tests + verifies e2e (screenshots for UI), a fresh-reviewer fix loop runs until clean, then a PR is created and CI babysat to green, ending in one private report. Use when the user wants tasks or issues "orchestrated", taken "all the way to PRs", "implemented, reviewed and PR'd", or wants one big issue split across sessions into a single branch and PR. For plain fan-out-and-supervise without the delivery pipeline, use the fleet skill instead.
metadata:
  compatibility: "claude, opencode"
---

# Orchestrate

Turn a batch of tasks/issues into merge-ready pull requests that need zero
touch-ups: per task — dedicated worktree, implement with tests, verify
end-to-end (visually with screenshots for UI), independent review loop until
clean, PR, CI babysat to green. The user gets one final report, and that
report is the only place screenshots are ever referenced.

**Requires:** everything `fleet` requires, plus an authenticated `gh` for the
target repo.

**Read `skills/fleet/SKILL.md` first.** This skill builds on fleet and does
not restate its mechanics: launch flags, the `--parent`-not-`-p` pitfall,
group inheritance, deps-install-first for worktree children, `session
children` polling, `session send` / `session approve`, the done sentinel, and
long-prompts-via-file all come from there.

## When to use

The user wants tasks/issues taken end-to-end to green PRs. If they only want
to fan out children and supervise them, use `fleet`. If they want one child
for one job, use the sub-agent pattern in the `agent-deck` skill.

## Conductor rules

You (the session running this skill) are the **conductor**. Hard rules:

- **You never edit code yourself.** All code work happens in child sessions.
  Running read-only git/gh commands, `mkdir`, and merges per
  `references/single-issue-split.md` is fine; changing source files is not.
- **You never work in the main checkout.** Every task gets a dedicated
  worktree (`launch -w <branch>`), including single-task relay mode.
- **You never block.** Supervise via the `session children --json` heartbeat;
  answer `waiting` children; poll `gh pr checks` on the same heartbeat.

## Run setup

Pick a run id (`run-<date>-<short-slug>`) and create the private screenshot
root — outside every repo, so it structurally cannot leak into a commit:

```bash
RUN_DIR="$HOME/.agent-deck/orchestrate/<run-id>"
mkdir -p "$RUN_DIR"
```

Everything any child captures goes under `$RUN_DIR/<task-slug>/`. Nothing
under `$RUN_DIR` is ever committed, pushed, uploaded, or mentioned in a PR.

## Input parsing & mode

- An argument that looks like an issue ref (`#123`, an issue URL, "issue
  123") → fetch the spec: `gh issue view <n> --json title,body,url`. Its PR
  body must include `Fixes #<n>`.
- Anything else → treat as a freeform task description.
- **Two or more tasks** → run the per-task pipeline below for each,
  in parallel, capped at **3 concurrent pipelines**; start the rest as slots
  free up.
- **One task** → judge whether to split it into subtasks for context hygiene
  (would one session have to hold too much? does it decompose into clearly
  separable pieces?). If you split, **read
  `references/single-issue-split.md` now** and follow it. If not, run the
  pipeline below once. Either way the outcome is one branch, one PR.

## Per-task pipeline

### 1. Implement

Derive a short `<task-slug>` and branch name. Write the implementer prompt to
a file (never inline a long `-m`), then launch:

```bash
agent-deck launch <repo-root> -w <branch> -c claude -t "impl-<task-slug>" -m "$(cat /tmp/impl-<task-slug>.md)"
```

Implementer prompt template — fill every `<...>`:

```text
Task: <title>

<task spec: issue body or freeform description>

Work strictly in this worktree on the current branch. Do, in order:
1. Install dependencies from the frozen lockfile (never regenerate it).
2. Implement the task test-first where practical.
3. Run the FULL test suite; everything must pass.
4. Verify the change end-to-end by actually driving the app — not only tests.
5. Only if the change affects UI: capture before/after screenshots into
   <run-dir>/<task-slug>/ using descriptive names (before-<what>.png,
   after-<what>.png). Never commit them, never mention them or that
   directory in any commit message, and take a screenshot of the final
   working state.
6. Commit your work in clear logical commits. Do NOT push yet.
```

### 2. Fresh review

When `session children` shows the implementer done (`done_status=ok`), launch
a **fresh** reviewer in the **same worktree path** (plain path, no `-w`):

```bash
agent-deck launch <worktree-path> -c claude -t "review-<task-slug>-r1" -m "$(cat /tmp/review-<task-slug>.md)"
```

Reviewer prompt template:

```text
You are a code reviewer with fresh eyes. You are READ-ONLY: edit nothing,
commit nothing, run only read-only commands plus the test suite.

Review the full branch diff: git diff $(git merge-base <base-branch> HEAD)...HEAD
Also run the test suite and judge whether the tests actually cover the change.

Report findings as a numbered list: file:line — severity (blocker |
should-fix | nit) — one line on what is wrong and why it matters.
End with exactly one line: VERDICT: clean  or  VERDICT: findings
```

### 3. Fix loop

- `VERDICT: findings` → `session send` the full findings list to
  `impl-<task-slug>` with: fix every blocker and should-fix (use judgment on
  nits), rerun the full suite and the e2e check, update screenshots if the UI
  changed again, and commit.
- When the implementer is done, launch a **new** fresh reviewer
  (`review-<task-slug>-r2`, then `-r3`).
- **Maximum 3 review rounds.** After round 3: if blockers remain, the task is
  **needs-attention** — no PR; if only should-fixes/nits remain, proceed to
  the PR and list them in the final report.

### 4. PR

Tell the implementer to push its branch (it knows the remotes; on forks that
means the fork remote). Then create the PR from the worktree:

```bash
cd <worktree-path> && gh pr create --title "<title>" --body "<body>"
```

The body covers what changed, why, and how it was verified, plus
`Fixes #<n>` for issue-sourced tasks. It must contain **no screenshot paths,
no run directory, no orchestrate/session details**.

### 5. CI babysit

On every heartbeat, for each open PR: `gh pr checks <pr-url>`. On a failure,
pull the failing details (`gh pr checks`, `gh run view <run-id> --log-failed`)
and `session send` them to the still-alive implementer to fix and push.
A task counts as **done** only when the review is clean (or nits-only), the
PR exists, and all checks are green.

## Failure handling

A task that cannot pass its tests, exhausts its 3 review rounds with blockers
remaining, or cannot reach green CI after a few fix attempts is reported as
**needs-attention**: leave its session and worktree fully intact for
inspection, and never force-push, reset, or delete anything.

## Cleanup (successful tasks only)

Stop finished children (`agent-deck session stop <id>`), but keep worktrees
until their PRs merge. The final report ends with copy-paste cleanup
commands:

```bash
agent-deck session remove <id>
git -C <repo-root> worktree remove .worktrees/<branch>
```

## Final report

Deliver to the user only — this is the single place screenshots are ever
referenced. Per task:

```text
## <task title>
- PR: <url> — checks: green | failing | none
- Review: <N> round(s) — clean | open items: <list>
- Screenshots: <run-dir>/<task-slug>/ (UI tasks only)
  Nominated pair worth attaching to the PR manually, if you like:
  before-<what>.png + after-<what>.png
- Needs attention: <anything left, or omit>
```

Close with the cleanup command block for every successful task.
````

- [ ] **Step 2: Verify frontmatter shape and fleet reference**

Run:
```bash
cd /Users/doozyx/DoozyX/agent-deck/.worktrees/orchestrate-skill && head -7 skills/orchestrate/SKILL.md && grep -c 'skills/fleet/SKILL.md' skills/orchestrate/SKILL.md && grep -c 'references/single-issue-split.md' skills/orchestrate/SKILL.md
```
Expected: frontmatter block showing `name: orchestrate`, `description:` and `compatibility: "claude, opencode"`; then `1` (the fleet reference); then `2` (the reference file is mentioned in the conductor rules and the mode section).

- [ ] **Step 3: Commit**

```bash
cd /Users/doozyx/DoozyX/agent-deck/.worktrees/orchestrate-skill && git branch --show-current && git add skills/orchestrate/SKILL.md && git commit -m "feat(skills): add orchestrate skill core playbook"
```
Expected: branch prints `feat/orchestrate-skill`; commit succeeds with 1 file changed.

---

### Task 2: `skills/orchestrate/references/single-issue-split.md`

**Files:**
- Create: `skills/orchestrate/references/single-issue-split.md`

**Interfaces:**
- Consumes: from Task 1 — the pipeline stage numbering (stages 1–5), session-title conventions `impl-<task-slug>` / `review-<task-slug>-r<N>`, `$RUN_DIR` screenshot policy, needs-attention semantics.
- Produces: nothing consumed later; must live at exactly the path Task 1's SKILL.md points to.

- [ ] **Step 1: Create the file with this exact content**

````markdown
# Single-issue split mode

Read this only when orchestrating **one** big task you have decided to split.
Everything here reuses the per-task pipeline from `SKILL.md`; "stages 1–3"
means implement → fresh review → fix loop, "stages 4–5" means PR → CI babysit.
The end state is always **one branch, one PR**.

## Decompose

Split the issue into 2–5 subtasks, each independently implementable and
testable, ordered by dependency. For each subtask write a mini-spec: goal,
likely files/areas, done criteria. The reason to split is **context
hygiene** — each session holds one small coherent job — not raw speed.

## Choose the topology

- Subtasks touch **clearly disjoint areas** (different dirs/layers, no shared
  files — e.g. backend vs web UI vs docs) → **parallel worktrees +
  integration branch**.
- Subtasks overlap, build on each other, or you are unsure → **sequential
  relay**. **Default to sequential when in doubt.**

## Sequential relay (default)

1. Launch subtask 1's implementer with a fresh worktree for the whole issue:
   `agent-deck launch <repo-root> -w <issue-branch> -c claude -t "impl-<issue-slug>-1" -m ...`
   using the stage-1 prompt template with the subtask's mini-spec.
2. Run stages 1–3 (implement, fresh review, fix loop) for subtask 1 in that
   worktree.
3. When clean, launch subtask 2's implementer in the **same worktree path**
   (plain path, no `-w`). Its prompt starts with: "You continue work on an
   existing branch. Read `git log --oneline -20` and the diff so far before
   starting." Then the normal stage-1 template with subtask 2's mini-spec.
4. Repeat for each remaining subtask: stages 1–3, one session at a time,
   each building on the previous commits.

## Parallel worktrees + integration branch

1. Create the integration branch and its worktree yourself (git plumbing is
   conductor work, not code editing):
   `git -C <repo-root> worktree add .worktrees/<issue-slug> -b <issue-branch>`
2. For each subtask, create its worktree branched **off the integration
   branch**:
   `git -C <repo-root> worktree add .worktrees/<issue-slug>-<n> -b <issue-branch>-<n> <issue-branch>`
   then launch its implementer at that worktree path (plain path — the
   worktree already exists) and run stages 1–3. Cap: 3 concurrent subtasks.
3. As each subtask's review comes back clean, merge it:
   `git -C .worktrees/<issue-slug> merge <issue-branch>-<n>`
4. On merge conflict, do not resolve it yourself — launch a session in the
   integration worktree: "Resolve the in-progress merge conflicts preserving
   the intent of both sides, run the full test suite, and commit the merge."
5. Continue until every subtask is merged.

## Final integration check

Both topologies: launch one last session in the issue worktree to run the
build and the FULL test suite on the combined result, do a quick e2e sanity
pass of the issue's overall behavior, fix only trivial integration breakage,
and commit. If it finds non-trivial breakage, treat it as findings: route to
a fix session and re-check (this counts toward a shared 3-round cap).

## PR

Run stages 4–5 once, from the issue worktree, on `<issue-branch>`: one PR for
the whole issue, CI babysat to green. Screenshot policy is unchanged — all
subtask screenshots live under `$RUN_DIR/<issue-slug>/` and appear only in
the final report.
````

- [ ] **Step 2: Verify the path matches SKILL.md's pointer**

Run:
```bash
cd /Users/doozyx/DoozyX/agent-deck/.worktrees/orchestrate-skill && ls skills/orchestrate/references/single-issue-split.md && grep -n 'references/single-issue-split.md' skills/orchestrate/SKILL.md
```
Expected: `ls` prints the path (file exists); grep shows at least the "read `references/single-issue-split.md` now" line.

- [ ] **Step 3: Commit**

```bash
cd /Users/doozyx/DoozyX/agent-deck/.worktrees/orchestrate-skill && git branch --show-current && git add skills/orchestrate/references/single-issue-split.md && git commit -m "feat(skills): add orchestrate single-issue split reference"
```
Expected: branch prints `feat/orchestrate-skill`; commit succeeds with 1 file changed.

---

### Task 3: Fleet disambiguation line

**Files:**
- Modify: `skills/fleet/SKILL.md:17-25` (the "## When to use" section)

**Interfaces:**
- Consumes: skill name `orchestrate` from Task 1.
- Produces: nothing.

- [ ] **Step 1: Add the disambiguation paragraph**

In `skills/fleet/SKILL.md`, the "## When to use" section currently ends with
this paragraph (around lines 23–25):

```markdown
This differs from the single sub-agent pattern in the `agent-deck` skill (one
child + fire-&-forget / on-demand / blocking retrieval). Fleet is **many
children + a non-blocking peek** across all of them.
```

Immediately **after** that paragraph (and before the "**Run from inside an
agent-deck session.**" paragraph), insert a blank line and this new paragraph:

```markdown
Want each task taken all the way to a merge-ready PR — implement → verify →
review loop → PR → CI green — rather than just fan-out and supervision? Use
the `orchestrate` skill instead; it builds on this one.
```

- [ ] **Step 2: Verify the insertion**

Run:
```bash
cd /Users/doozyx/DoozyX/agent-deck/.worktrees/orchestrate-skill && grep -n 'orchestrate' skills/fleet/SKILL.md
```
Expected: exactly one match, inside the "When to use" section (line ~27), reading `` Use the `orchestrate` skill instead ``.

- [ ] **Step 3: Commit**

```bash
cd /Users/doozyx/DoozyX/agent-deck/.worktrees/orchestrate-skill && git branch --show-current && git add skills/fleet/SKILL.md && git commit -m "docs(skills): point fleet users to orchestrate for full pipelines"
```
Expected: branch prints `feat/orchestrate-skill`; commit succeeds with 1 file changed.

---

### Task 4: Cross-file consistency check

**Files:**
- Verify only (fix in place if a check fails): `skills/orchestrate/SKILL.md`, `skills/orchestrate/references/single-issue-split.md`, `skills/fleet/SKILL.md`

**Interfaces:**
- Consumes: all three files from Tasks 1–3.
- Produces: a clean, self-consistent skill set on `feat/orchestrate-skill`.

- [ ] **Step 1: Run the consistency checks**

```bash
cd /Users/doozyx/DoozyX/agent-deck/.worktrees/orchestrate-skill
# 1. Every cross-referenced path exists:
ls skills/fleet/SKILL.md skills/orchestrate/SKILL.md skills/orchestrate/references/single-issue-split.md
# 2. No leftover screenshot/PR leak instructions (should print nothing):
grep -rn 'screenshot' skills/orchestrate/ | grep -iv 'never\|only\|not\|report\|nominat\|policy\|capture\|update\|unchanged\|before-\|after-\|pair\|final working'
# 3. Session-title conventions are consistent across both orchestrate files:
grep -rn 'impl-<' skills/orchestrate/ | wc -l && grep -rn 'review-<task-slug>-r' skills/orchestrate/SKILL.md | wc -l
# 4. Frontmatter compatibility matches fleet:
grep -h 'compatibility' skills/fleet/SKILL.md skills/orchestrate/SKILL.md
```
Expected: (1) all three paths print; (2) no output; (3) both counts ≥ 1; (4) two identical `compatibility: "claude, opencode"` lines.

- [ ] **Step 2: Fix anything that failed, amend or commit the fix**

If any check fails, correct the offending file and commit:
```bash
cd /Users/doozyx/DoozyX/agent-deck/.worktrees/orchestrate-skill && git branch --show-current && git add -u && git commit -m "fix(skills): orchestrate consistency fixes"
```
If everything passed, there is nothing to commit — the branch is ready for PR (fork remote `git@github.com:DoozyX/agent-deck-upstream.git`, PR to upstream per project workflow).
