# `/orchestrate` Skill Design

**Date:** 2026-07-15
**Status:** Approved design, pre-implementation

## Goal

A new skill, `agent-deck:orchestrate`, that turns a list of tasks/issues into
merged-ready pull requests with zero touch-ups needed from the user. For each
task it: creates a dedicated worktree, implements with tests, verifies
end-to-end (visually for UI changes, with screenshots), runs an independent
code-review loop until clean, creates a PR, and babysits CI to green. The user
receives one final report — the only place screenshots are ever referenced.

## Relationship to `/fleet`

Fleet is the transport layer; orchestrate is a workflow built on it.

- Orchestrate's SKILL.md **does not restate** launch/supervision mechanics.
  Its first instruction is: read `skills/fleet/SKILL.md` for fan-out mechanics
  (launch flags, `--parent` vs `-p` pitfall, group inheritance, deps-install-
  first for worktrees, `session children` polling, `session send`/`approve`,
  done sentinel).
- **One edit to fleet:** add a disambiguation line to its "When to use"
  section — if the user wants full implement→verify→review→PR pipelines per
  task (not just fan-out and supervise), use the `orchestrate` skill.
- No other fleet refactoring: extracting shared mechanics into a common
  reference was considered and rejected — orchestrate needs essentially all of
  fleet's mechanics, so a wholesale read is simpler than an extraction layer.

## Skill structure (progressive disclosure)

```
skills/orchestrate/
  SKILL.md                         # shared core: input parsing, mode choice,
                                   # per-task pipeline, review loop,
                                   # screenshot policy, PR + CI babysitting
  references/single-issue-split.md # loaded only in split mode: decomposition,
                                   # integration branch vs sequential relay,
                                   # merge sessions, final integration check
```

One skill, not two: the two modes share the per-task pipeline (~70% of the
substance), and in split mode each subtask runs that same pipeline. Two skills
would duplicate it and drift. The split-mode text is kept out of multi-task
runs' context via the reference file: SKILL.md says "single big task you decide
to split → read `references/single-issue-split.md` before proceeding."

## Invocation & roles

Invoked from inside an agent-deck session, which becomes the **conductor**.
Hard rules for the conductor:

- Never edits code itself; all code work happens in child sessions.
- Never works in the main checkout — every task gets a dedicated worktree
  (`launch -w <branch>`), including single-task sequential-relay mode.
- Supervises fleet-style: non-blocking `session children` heartbeat, unblocks
  `waiting` children via `session send` / `session approve`.

## Input parsing & mode selection

- Arguments that look like issue refs (`#123`, issue URLs, "issue 123") are
  fetched with `gh issue view` for title/body; everything else is a freeform
  task description.
- **Multiple tasks** → independent parallel pipelines, one PR each
  (`Fixes #N` when sourced from an issue). Concurrency capped at ~3 pipelines
  at once; remaining tasks are staggered as slots free up.
- **Single task** → the conductor judges whether to decompose into subtasks
  (for context hygiene). Split or not, the outcome is one branch, one PR.

## Per-task pipeline

1. **Implement.** Conductor launches an implementer child:
   `agent-deck launch <repo> -w <branch> -c claude -m <task prompt>`.
   The prompt requires, in order: install deps from the frozen lockfile;
   implement with tests; run the full test suite; verify the change end-to-end
   by actually driving the app; if the change touches UI, capture screenshots
   (see screenshot policy).
2. **Review.** When the implementer asserts done, the conductor launches a
   **fresh reviewer** session in the same worktree: read-only, reviews
   `git diff <base>...HEAD`, returns a findings list, edits nothing.
3. **Fix loop.** Findings are sent back to the **implementer** (it holds the
   context) via `session send`. After fixes, a *new* fresh reviewer re-reviews.
   Loop until the reviewer returns clean, **max 3 rounds**; anything still
   open after round 3 is flagged in the final report rather than looping.
4. **PR.** Implementer pushes the branch (for agent-deck: SSH remote to the
   DoozyX fork); conductor runs `gh pr create` targeting upstream. The PR body
   describes the change and links the issue. It must contain **no screenshot
   paths and no orchestrate run details**.
5. **CI babysit.** Conductor polls `gh pr checks` on its normal heartbeat.
   Failures are routed back to the still-alive implementer to fix and push.
   A task is *done* only when checks are green.

## Single-issue split mode (`references/single-issue-split.md`)

Conductor decomposes the issue into subtasks, then picks a topology:

- **Clearly disjoint areas** (e.g. backend vs web UI vs docs) → parallel
  subtask worktrees branched off an **integration branch**; the conductor
  merges each back as it completes, spawning a session to resolve conflicts
  when they arise.
- **Overlapping or unsure** → **sequential relay**: one shared worktree (never
  the main checkout), subtask sessions run one after another, each building on
  the previous session's commits. Default to this when in doubt.

Each subtask gets its own review loop. After integration, one final session
runs build + full test suite on the merged branch. Then a single PR.

## Screenshot & visual-verification policy

- Screenshots are saved to `~/.agent-deck/orchestrate/<run-id>/<task-slug>/`
  — structurally outside every repo, so they cannot leak into commits or PRs.
- Private by default, published by human choice: for each UI task the final
  report links absolute paths and **nominates the best before/after pair** as
  worth attaching to the PR manually. The skill never uploads screenshots
  anywhere.
- Rationale: verification captures can leak local context (session titles,
  paths) and are rarely presentation-quality; publishing is an irreversible
  outward-facing action that gets a human eye first.

## Failure handling & cleanup

- A task that cannot pass tests, exhausts its review rounds, or cannot reach
  green CI is reported as **failed / needs-attention**, with its session and
  worktree left intact for inspection. Nothing is force-pushed or deleted.
- On success, child sessions are stopped; worktrees remain until their PRs
  merge. The final report ends with copy-paste cleanup commands.

## Final report (user-facing only)

Per task: PR link + CI status, review-rounds summary, screenshot directory +
nominated before/after pair, and any flagged leftovers. This report is the
only artifact that references screenshots.

## Out of scope

- No Go/CLI changes: this is a pure skill (markdown) addition plus the
  one-line fleet cross-pointer.
- No automatic screenshot upload to PRs.
- No post-merge automation (branch deletion, release notes).
