---
name: orchestrate
description: End-to-end delivery pipeline for tasks/issues. Per task - dedicated worktree child implements + tests + verifies e2e (screenshots for UI), a fresh-reviewer fix loop runs until clean, then a PR is created and CI babysat to green, ending in one private report. Use when the user wants tasks or issues "orchestrated", taken "all the way to PRs", "implemented, reviewed and PR'd", wants one big issue split across sessions into a single branch and PR, or has an approved design/spec document to be planned and executed in dedicated child sessions. For plain fan-out-and-supervise without the delivery pipeline, use the fleet skill instead.
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
- **You never pass `-g` to a child launch.** Worktree children auto-inherit your
  group; an explicit `-g` overrides that inheritance, and a group name guessed
  from the repo folder (`-g baba` when the group is really `doozyx/baba`) strands
  the child away from its siblings. Omit it — see the group trap in `fleet`.
- **Children auto-parent to you — verify it, don't hand-wire it.** A `launch`
  issued from this session attaches the child to you automatically: agent-deck
  reads your instance id from the tmux session environment, so it resolves even
  from a shell that lost `$AGENTDECK_INSTANCE_ID` (a subagent shell, a scrubbed
  env). Pass no parent flag in the normal case. **Still confirm** each child
  parented and landed in your group (the `fleet` "verify the group" check) —
  a `launch` from *outside* your tmux session, or against an agent-deck older
  than the tmux-env auto-parent fix, can still orphan the child, and a worktree
  child then strays into its branch-leaf group. Repair a stray with
  `agent-deck session set-parent <id> "$AGENTDECK_INSTANCE_ID"` and
  `agent-deck group move <id> "$AGENTDECK_RESOLVED_GROUP"`. Use `--parent <id>`
  only to deliberately re-home a child to a *different* conductor (long form —
  never `-p`, which the global `--profile` extractor eats).

## Run setup

Pick a run id (`run-<date>-<short-slug>`) and create the private screenshot
root — outside every repo, so it structurally cannot leak into a commit:

```bash
RUN_DIR="$HOME/.agent-deck/orchestrate/<run-id>"
mkdir -p "$RUN_DIR"
```

Everything any child captures goes under `$RUN_DIR/<task-slug>/`, and the
prompt files you write for children live there too (`impl-prompt.md`,
`review-r1-prompt.md`, …) — not `/tmp`, where they collide across runs,
vanish on reboot, and break resume. Nothing under `$RUN_DIR` is ever
committed, pushed, uploaded, or mentioned in a PR.

Maintain a run manifest at `$RUN_DIR/manifest.md` and update it after every
stage transition — per task: slug, branch, worktree path, session ids,
current stage, review round, the HEAD sha each review round saw, PR url. If
the conductor session dies, a fresh session can resume the run from the
manifest plus `session children <old-conductor-id>` — but the surviving
children are still parented to the dead session, so first re-parent them
(`agent-deck session set-parent <child> <new-conductor-id>`) so waiting/done
notifications and the turn-start snapshot route to the new conductor.

## Input parsing & mode

- An argument that looks like an issue ref (`#123`, an issue URL, "issue
  123") → fetch the spec: `gh issue view <n> --json title,body,url`. Its PR
  body must include `Fixes #<n>`.
- An argument that is a path to a **design/spec document** (e.g.
  `docs/plans/<date>-<topic>-design.md`) → a spec-fed task: run the
  **planning stage** below before any implementation; the resulting plan
  drives decomposition.
- Anything else → treat as a freeform task description.
There is **one flow with four entrances** — planning and splitting are
stages some entrances pass through, never a prerequisite. Pick by what you
were given:

```text
list of tasks/issues (2+) ─→ parallel per-task pipelines, one PR each
                             (capped at 3 concurrent; rest queue up;
                              a big item in the list may still get its
                              own planner, per-task judgment)
single small task ─────────→ one pipeline, one PR
single big task, no spec ──→ split it: obvious decomposition → decompose
                             yourself (references/single-issue-split.md);
                             approach unclear → planner child first,
                             then plan-driven split. One branch, one PR.
design/spec document ──────→ planning stage → plan-driven split.
                             One branch, one PR.
```

Splitting a single task is judged by **context hygiene**: would one session
have to hold too much? Does it decompose into clearly separable pieces? If
you split, **read `references/single-issue-split.md` now** and follow it.
Brainstorming/design with the user is upstream of this skill entirely — it
happens only when the user chooses it, and its output arrives here as just
another input (the spec document).

## Planning stage (spec-fed tasks, or any task you judge big)

Design and plan are separate artifacts produced by separate roles: the
**design/spec** (what and why) is user-approved and arrives as input — if it
doesn't exist yet, brainstorm it with the user *before* orchestrating; that
part is interactive and never delegated. The **plan** (how, task by task) is
written by a dedicated **planner child** in the task's worktree — it needs
deep codebase reading, which is neither your job (supervision only) nor the
user's session's:

```bash
agent-deck launch <repo-root> -w <branch> -c claude -t "plan-<task-slug>" --message-file "$RUN_DIR/<task-slug>/plan-prompt.md"
```

Planner prompt template:

```text
Read the approved design at <spec-path> and explore the codebase as needed.
Write an implementation plan to docs/plans/<date>-<task-slug>-plan.md:
ordered, bite-sized tasks; per task: exact file paths, the actual code or
edit, verification commands with expected output, and the interfaces later
tasks rely on. Mark any tasks that are safe to run in parallel (disjoint
files). Assume each task's executor has ZERO context beyond that one task.
No placeholders (no TBD / "add error handling" / "similar to task N").
Commit the plan to the current branch. Do NOT implement anything.
```

Then treat the plan like code: launch a fresh read-only reviewer in the same
worktree (same `--disallowedTools` flags as stage 2 — findings go back to
the planner to apply, the reviewer edits nothing) to check the plan
**against the spec** — coverage (every spec
requirement maps to a task), placeholders, contradictions, task ordering —
using the same findings format and verdict line as a code review. Findings →
back to the planner; clean or nits-only → proceed, then **archive the planner
and plan-reviewer sessions** (see "Archiving finished sessions").

The plan's task list now replaces your own decomposition: subtasks = plan
tasks (see `references/single-issue-split.md`), each implementer receives its
plan task verbatim as its spec, and each reviewer receives that same plan
task as the spec to check compliance against.

Skip this stage for small tasks — a single focused change with an obvious
approach (most issues) goes straight into the per-task pipeline.

## Model tiering

You (the conductor) run on the strong model; children don't have to. Pass a
model per child with `--extra-arg --model --extra-arg <model>` (requires
`-c claude` — other connectors have no per-child model flag, so skip tiering
there and use the default); omit it to use the user's default. **Decide per
session, by how much judgment the job leaves open:**

- **Planner, merge-conflict, and integration-check sessions:** strong model
  (e.g. opus) — they make design decisions.
- **Implementers:** scale to spec explicitness. Executing a reviewed plan
  task (complete code, exact paths — pure transcription + verification) →
  cheap model (e.g. haiku). A clear spec but no plan → mid tier (e.g.
  sonnet). Freeform/issue work that must design its own approach → strong.
- **Reviewers:** mid tier (e.g. sonnet) by default, regardless of the
  implementer's tier — review is verification work (diff vs. spec, run the
  suite), and the Checked/VERDICT format keeps it honest. **Escalate the
  reviewer to the strong model** when (a) the task was freeform or
  design-heavy — spec compliance is then a judgment call, not a checklist —
  or (b) rounds oscillate: a round reports new findings in code an earlier
  round already passed, meaning the reviewer is missing things. Once
  escalated, stay strong for the rest of that task's rounds.
- **Escalate on failure:** if round 2 still reports blockers from a
  downgraded implementer, don't send it a third round — launch the fix as a
  NEW strong-model session in the same worktree (tell it to read
  `git log` and the diff first). Caps the worst case at roughly
  strong-model cost.

## Per-task pipeline

### 1. Implement

Derive a short `<task-slug>` and branch name. Write the implementer prompt to
`$RUN_DIR/<task-slug>/impl-prompt.md` and pass it with `--message-file` —
never inline via `-m "$(cat ...)"`: the shell mangles backticks and `$`, and
issue bodies are full of both. Then launch:

```bash
agent-deck launch <repo-root> -w <branch> -c claude -t "impl-<task-slug>" --message-file "$RUN_DIR/<task-slug>/impl-prompt.md"
```

Implementer prompt template — fill every `<...>`:

```text
Task: <title>

<task spec: issue body or freeform description>

Work strictly in this worktree on the current branch. Do, in order:
1. Install dependencies from the frozen lockfile (never regenerate it).
2. Run the FULL test suite once BEFORE changing anything and record the
   baseline. If something already fails, note it and leave it alone — you
   are accountable only for introducing no NEW failures. List the baseline
   failures in your final summary ("baseline: none" if all green) — the
   reviewer will be given that list. If the repo has no test suite, say so
   and lean on the lint/build checks plus the e2e verification instead.
3. Implement the task test-first where practical.
4. Run the FULL test suite; no new failures versus the baseline. Also run
   the repo's lint/format/build checks — whatever CI runs — and fix what
   they flag on your changes.
5. Verify the change end-to-end by actually driving the app — not only tests.
   For browser work use an isolated browser instance (Playwright-style), not
   a shared Chrome — other tasks may be driving browsers in parallel.
6. Only if the change affects UI: capture before/after screenshots into
   <run-dir>/<task-slug>/ using descriptive names (before-<what>.png,
   after-<what>.png). Never commit them, never mention them or that
   directory in any commit message, and take a screenshot of the final
   working state.
7. Commit your work in clear logical commits. Do NOT push yet.
```

### 2. Fresh review

When `session children` shows the implementer done (`done_status=ok`), launch
a **fresh** reviewer in the **same worktree path** (plain path, no `-w`).
Back the prompt's read-only rule with tool flags — they block the editing
tools (Bash stays available for running tests, so the prompt rule still
carries the rest):

```bash
agent-deck launch <worktree-path> -c claude -t "review-<task-slug>-r1" \
  --extra-arg --disallowedTools --extra-arg "Edit,Write,NotebookEdit" \
  --message-file "$RUN_DIR/<task-slug>/review-r1-prompt.md"
```

Record the worktree's current HEAD sha in the manifest when you launch each
reviewer — incremental rounds and the full-branch gate need it.

Reviewer prompt template:

```text
You are a code reviewer with fresh eyes. You are READ-ONLY: edit nothing,
commit nothing, run only read-only commands plus the test suite.

The task this branch is supposed to implement:
<task spec: the same spec the implementer received>

Review the full branch diff: git diff $(git merge-base <base-branch> HEAD)...HEAD
Check BOTH: (a) spec compliance — does the diff actually implement the task
above? Anything missing, extra, or misunderstood is a finding; and (b) code
quality. Also run the test suite and judge whether the tests actually cover
the change.

Known pre-existing test failures (the implementer's recorded baseline):
<baseline list, or "none">. These are NOT findings — only failures new
against this baseline are.

Report findings as a numbered list: file:line — severity (blocker |
should-fix | nit) — one line on what is wrong and why it matters.
Then print 2-3 lines starting with "Checked:" summarizing what you actually
verified (which spec points, test suite result, coverage judgment) — a
verdict with no evidence is not acceptable.
End with exactly one line, using real counts:
VERDICT: clean
VERDICT: findings blockers=<n> should-fix=<n> nits=<n>
```

### 3. Fix loop

- A findings verdict with `blockers=0 should-fix=0` (nits only) counts as
  clean; list the nits in the final report.
- On findings → `session send` the fix-round prompt to `impl-<task-slug>`
  (write it to `$RUN_DIR/<task-slug>/fix-r<n>.md` and pass
  `--message-file` — findings lists are full of backticks too):

```text
Review round <n> found issues on your branch — fix them:

<findings list, verbatim>

Fix every blocker and should-fix (use judgment on nits). Rerun the full
test suite (no new failures vs your baseline), the lint/format checks, and
the e2e check; update screenshots if the UI changed again; commit.
Do NOT push.
```

- When the implementer is done, launch the next fresh reviewer
  (`review-<task-slug>-r2`, then `-r3`) with the same `--disallowedTools`
  flags. **Rounds 2+ are incremental** — the round-1 full review already
  happened, so re-reviewing the whole branch each round is wasted cost.
  Incremental reviewer prompt template:

```text
You are a code reviewer with fresh eyes. You are READ-ONLY: edit nothing,
commit nothing, run only read-only commands plus the test suite.

The task this branch is supposed to implement:
<task spec: the same spec the implementer received>

A previous review at commit <reviewed-sha> reported:
<previous round's findings, verbatim>

Do, in order:
1. Verify each finding above is actually fixed — an unfixed or half-fixed
   finding is a new finding.
2. Closely review the commits made since then: git diff <reviewed-sha>...HEAD
3. Quick-scan the rest of the branch diff for anything the fixes broke.
4. Run the test suite. Known pre-existing failures (baseline): <list, or
   "none"> — only NEW failures are findings.

Report findings as a numbered list: file:line — severity (blocker |
should-fix | nit) — one line each. Then 2-3 "Checked:" evidence lines.
End with exactly one line, using real counts:
VERDICT: clean
VERDICT: findings blockers=<n> should-fix=<n> nits=<n>
```

  Once you have read the previous round's findings, **archive the
  superseded reviewer** (see "Archiving finished sessions").
- **Full-branch end gate: the loop only ends on a clean full-branch
  verdict.** A round-1 clean qualifies directly. A clean from an
  *incremental* round does not — launch one more fresh reviewer with the
  full-branch (round-1) prompt to confirm the branch as a whole. Gate
  clean or nits-only → proceed to the PR. Gate findings → that is
  oscillation by definition: escalate the reviewer tier (see "Model
  tiering"), send the findings through the fix-round prompt, and continue
  the loop.
- **Caps: maximum 3 fix rounds** (rounds whose findings go back to the
  implementer — a gate-findings round consumes one like any other) **and 2
  full-branch gate reviews.** Budget exhausted with
  blockers remaining → the task is **needs-attention**, no PR; only
  should-fixes/nits remaining → proceed to the PR and list them in the
  final report.

### 4. PR

The branch was cut when the task started and the base may have moved since.
`session send` the implementer a pre-PR sync step: fetch and merge the
current <base-branch>, resolve any conflicts preserving both sides' intent,
then rerun the full test suite **and the build/vet checks** — auto-merges
can compile-and-be-wrong (duplicated route handlers, duplicate test
function names) — commit the merge, and push the branch (it knows the
remotes; on forks that means the fork remote). Then create the PR from the
worktree:

```bash
cd <worktree-path> && gh pr create --base <base-branch> --title "<title>" --body "<body>"
```

On a fork setup, be fully explicit or `gh` stops to ask questions
interactively — which hangs a non-interactive conductor: add
`--repo <upstream-owner>/<repo>` and `--head <fork-owner>:<branch>`.

The body covers what changed, why, and how it was verified, plus
`Fixes #<n>` for issue-sourced tasks. It must contain **no screenshot paths,
no run directory, no orchestrate/session details**.

### 5. CI babysit

On every heartbeat, for each open PR: `gh pr checks <pr-url>`. On a failure,
pull the failing details (`gh pr checks`, `gh run view <run-id> --log-failed`)
and `session send` them to the still-alive implementer to fix and push.
A mechanical fix (lint, format, flaky rerun) pushes directly; a fix that
touches logic gets one incremental review round on the new commits
(`<reviewed-sha>` = the sha the last clean review saw) before the task can
count as done. A task counts as **done** only when the review is clean (or
nits-only), the PR exists, and all checks are green.

## Answering waiting children

A child in `waiting` has stopped to ask something — its question is pushed to
you, and unanswered it stalls that task forever. On every heartbeat, for each
`waiting` child: read the question (`session output`), then decide **who**
should answer:

- **You answer** when the answer is derivable from what you already hold:
  the task spec / plan task, this skill's rules (screenshot policy, no push
  before review, frozen lockfile, branch naming), or an earlier decision in
  this run. Answer decisively via `session send` — a vague answer buys a
  second question. Tool-permission menus are per-connector: **Codex** →
  `session approve <id> once` (never `session send "1"` — Codex takes the
  digit as the decision and the trailing Enter lands in the resumed turn);
  **Claude** → `session send <id> "1"` (the digit picks the option and the
  trailing Enter is harmless — the TUI's quick-approve sends exactly this;
  `session approve` only detects Codex menus and fails closed on Claude's).
- **The user answers** when the question is genuinely theirs: scope changes
  ("should I also refactor X?" — default no, stay on spec), destructive or
  irreversible actions (dropping data, force-push, deleting files beyond the
  task), secrets/credentials, or product decisions the spec doesn't settle.
  Relay the question to the user, leave that child waiting, and keep every
  other pipeline running — one blocked task never blocks the run. Note the
  pending question in the manifest and in the final report if it is still
  open at the end.

## Supervision notes

- `agent-deck session children --json` returns an object
  `{"children": [...], "parent": "..."}` — iterate `.children[]`, not the
  root array.
- `done_status` / `done_summary` **persist from the previous round**, so a
  round-1 `ok` is still on the row when round 2 starts. It no longer passes for
  a new completion: once you send the child fix-round work, the old entry is
  flagged `done_stale: true` (alongside `last_sent_at`), is excluded from
  `done_ok`/`done_fail`, emits no `done` event, and does **not** count as
  terminal for `--until-done`. Wait for `done_stale` to clear — equivalently,
  for `done_at` to move past `last_sent_at`.
- Reading a child's result right after nudging it has the same trap: use
  `agent-deck session output <id> --json --require-fresh`, which exits 3 while
  the newest response still predates your message, instead of handing you the
  previous turn's answer.
- **Stall rule:** a child with no status change for ~20 minutes is stuck.
  Read its `session output`, nudge it once with `session send`; if it is
  still stuck on the next check, mark the task **needs-attention** instead of
  polling forever.

## Failure handling

A task that cannot pass its tests, exhausts its 3 review rounds with blockers
remaining, or cannot reach green CI after a few fix attempts is reported as
**needs-attention**: leave its session and worktree fully intact for
inspection, and never force-push, reset, or delete anything.

## Archiving finished sessions

The moment a child is no longer needed, **archive** it — don't leave it
cluttering the active list, and don't hard-delete it either:

```bash
agent-deck session archive <id>
```

`session archive` stops the session *and* hides it from active lists while
retaining it in storage, so its history stays inspectable later. It replaces
the old `stop && remove` pair — no separate stop is needed. Archive:

- a **reviewer** once you've read its verdict and are moving on (launching the
  next round, or proceeding to the PR);
- a **planner** (and its plan-reviewer) once the plan review comes back clean;
- the **implementer** at task-done cleanup (below).

**Never archive a needs-attention task's sessions** — those stay visible and
fully intact for inspection (see "Failure handling").

## Cleanup (successful tasks only)

When a task reaches **done** (review clean, PR created, checks green), clean
up yourself — the pushed remote branch backs the PR, so nothing local is
still needed:

```bash
agent-deck session archive <id>
git -C <repo-root> worktree remove <worktree-path>
git -C <repo-root> branch -d <branch>
```

Take `<worktree-path>` and the exact `<branch>` name from
`git -C <repo-root> worktree list` — agent-deck may prefix the branch you
passed to `-w` (e.g. `add-x` becomes `feature/add-x`), so never guess the
path from the name you typed.

If review feedback arrives on the PR later, recreate a worktree from the
remote branch. **Needs-attention tasks are the exception**: leave their
session, worktree, and branch fully intact for inspection.

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

Close by listing what was cleaned up (archived sessions, removed worktrees
and branches of successful tasks) and what was deliberately left in place for
needs-attention tasks.
