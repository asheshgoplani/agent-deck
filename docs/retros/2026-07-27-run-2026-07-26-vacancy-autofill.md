# Retro — `run-2026-07-26-vacancy-autofill`

**Run:** GitLab issue #146, vacancy auto-fill from a pasted job description (BaBa monorepo).
**Shape:** spec-fed → planner → plan review → 5 sequential implementation batches, each reviewed
→ integration check → 2 full-branch gate reviews → 3 fix rounds → merged to `dev` as `85cf8cc`.
**Outcome:** shipped. 34 commits, ~40 files, +6.7k/−287, no Prisma migration.
Final gate on the combined result (feature + advanced `dev`): frontend 821, backend 1283 passed /
12 skipped, ai-service 28, i18n 1897 keys / 242 files, exit 0 on both `pnpm gate` and the
ai-service suite.

## agent-deck defects (reproducible)

1. **`group update --max-concurrent 12` (space form) prints usage AND silently writes 11.**
   Looks like an arg-parse failure, yet it still mutated state — and to a *different* value than
   asked (it appears to take the `1` of `12`). `--max-concurrent=12` works. A command that prints
   usage must not have mutated anything. Prior retros note "needs `=`"; this adds the
   silent-wrong-write half.

2. **A launch queued at group cap loses its `--message-file` prompt.** `launch` reports
   `✓ Queued session`; a later `session start <id>` boots the session to an **empty composer, 0
   tokens**. The prompt is never delivered, and `session children` reports the session as
   `waiting` — which reads as "asking a question", not "never got its work". Recovery is a manual
   `session send <id> --message-file <same file>`. Either `session start` should replay the queued
   launch message, or `launch` should refuse to queue when a message is attached.

3. **`done_status` is never populated (v1.10.10+gb35ac688).** For every child in this run —
   including ones whose pane clearly showed `===AGENTDECK_DONE=== status=ok summary=…` —
   `session children --json` returned `done_status: null`, `done_at: null`, `done_stale: null`.
   Consequences, all of which bit during the run:
   - the documented until-loop `jq 'all(.children[]; .done_status != null)'` **never terminates**;
   - `--follow --until-done` cannot fire;
   - the Claude turn-start fleet snapshot reports "0 done" while children are finished;
   - `status` additionally lags ~10 minutes and then settles on `idle`, not a terminal state.

   Working detection for the whole run was **grepping the child's tmux pane for the sentinel**.
   This is the single highest-impact bug here: it silently converts every supervision primitive
   the skills document into a no-op, and it made the conductor report two finished sessions as
   still running until the user corrected it.

4. **`session children --follow --heartbeat 300`** — `--heartbeat` is documented in the fleet
   skill but rejected by the binary; it printed usage and **exited 0**. Exit 0 on an arg error is
   a trap for any background-task harness, which reads it as a successful wait.

5. **`parent_id` disagreement between views.** Right after launch, `session children` listed the
   child while `ls --json` showed `parent_id: null`; after `session set-parent` succeeded
   ("✓ Linked … as sub-session of …"), a later `ls --json` still showed `parent_id: null`. One of
   the two views is wrong.

## Skill friction

1. **`orchestrate` stages 4–5 are GitHub-only** (`gh pr create`, `gh pr checks`). This repo is
   GitLab and its `CLAUDE.md` prescribes a *merge into `dev`* endgame, not a PR at all. The skill
   has no branch for "the project's own workflow says otherwise", so the conductor improvises the
   entire endgame. Suggest: *if the repo prescribes its own finish workflow, that wins — confirm
   it with the user once, then follow it.*

2. **"The conductor never edits code" vs. a merge-to-`dev` endgame.** The hard rule permits merges
   only per `references/single-issue-split.md`. Finishing a ticket by merging the feature branch
   into `dev` from the main checkout sits outside that carve-out and needs an explicit one.

3. **Read-only reviewer flags don't cover `git`.** `--disallowedTools Edit,Write,NotebookEdit`
   blocks the editing tools but leaves Bash, so a reviewer ran **`git stash`** and swept a sibling
   implementer's in-flight work in the shared worktree. Both sessions recovered it, but the fix is
   a prompt rule that must now be pasted into every child: *never run `git stash`, `git checkout`,
   `git restore`, `git reset`, `git clean`*. Worth promoting into the skill's reviewer template —
   or into the reviewer launch flags.

4. **Parallel worktrees are the wrong default for a plan with 17 small tasks.** The first topology
   (one worktree per task) spent its first hour on `pnpm install` + env copying + merge planning
   and produced three commits, all documents — the user's "i don't see it working" was correct.
   Rewriting it as a **sequential relay batched into 5 sessions in one worktree** started landing
   code immediately. Rule of thumb worth adding: parallel worktrees pay off only when tasks are
   both long and genuinely disjoint; a fine-grained plan wants a relay.

## What the review loop actually caught

Six defects, none of which any implementer noticed, and four of which were silent-when-wrong:

- an AI-matched `customerId` accepted without checking the picker could display it (the picker
  loads `pageSize=100`; the backend matches all clients);
- the D6 regression test asserted 9 of 16 fields, so dropping `location: job.workPlace ?? ''`
  would have wiped workplace with the suite green;
- blank optional fields omitted from the PATCH, so clearing a field on edit silently did nothing;
- a stored workload of **0** seeding the form as 100, so an untouched Save destroyed it —
  reachable from the feature's own happy path ("bis 50%" extracts as min 0 / max 50);
- nothing asserted `@ThrottleByUser()` / `ZodBodyPipe` were *on* the route (the spec constructs
  the controller directly, bypassing decorators) — deleting either left 1266 tests green while
  reopening two shipped bug classes;
- a partial workload extraction producing an inverted range, with the *wrong* value being the one
  without an AI marker, so the human review step structurally could not catch it.

The last fix round existed because the conductor **overruled the reviewer's severity**: gate
review 2 called the edit-seed issue a nit; it was in fact a regression on existing rows caused by
our own new `.refine`, turning previously-savable vacancies unsavable. Worth encoding: a finding
whose blast radius is *existing data*, introduced by *this branch*, is never a nit.

## Tiering

Planner, integration check and both full-branch gates on opus; implementers and batch reviewers on
the plan's own `tier:` tags. No reviewer oscillation, so no escalation fired. The planner did hit
297k context and was rotated by agent-deck's own budget handler into a `(cont.)` session with an
**empty** handoff dir — harmless here only because the plan was already committed.
