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
