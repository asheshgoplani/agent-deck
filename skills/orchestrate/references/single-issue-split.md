# Single-issue split mode

Read this only when orchestrating **one** big task you have decided to split.
Everything here reuses the per-task pipeline from `SKILL.md`; "stages 1–3"
means implement → fresh review → fix loop, "stages 4–5" means PR → CI babysit.
The end state is always **one branch, one PR**.

## Decompose

**If a plan exists** — the planning stage ran (spec-fed task), or you were
handed one (plan-fed task) — decomposition is already done: subtasks = the
plan's tasks, in plan order, with the plan's parallel-safe markings deciding
the topology below. Each implementer is pointed at its own task file as its
spec and reads nothing else for it; do not re-decompose or reorder. A
plan-fed task's plan may lack `tier:` tags; tier those tasks yourself from
the tier table in `SKILL.md`.

**Otherwise** split the issue yourself into 2–5 subtasks, each independently
implementable and testable, ordered by dependency. For each subtask write a
mini-spec: goal, likely files/areas, done criteria. The reason to split is
**context hygiene** — each session holds one small coherent job — not raw
speed.

Your own split is subject to the same gate as a planner's plan: it feeds 2+
implementer sessions, so it gets **one** plan review before any
implementation — see "Reviewing the plan" in `SKILL.md`. Write the mini-specs
to `$RUN_DIR/<issue-slug>/subtasks.md` and point the reviewer at that plus the
issue/spec; it is checking coverage, cross-subtask interface mismatch and
ordering, not the code you propose. Being the author of the split is not a
reason to skip it — every downstream reviewer will be handed one mini-spec as
ground truth and can't see the ones around it.

## Choose the topology

- Subtasks touch **clearly disjoint areas** (different dirs/layers, no shared
  files — e.g. backend vs web UI vs docs) → **parallel worktrees +
  integration branch**.
- Subtasks overlap, build on each other, or you are unsure → **sequential
  relay**. **Default to sequential when in doubt.**

## Sequential relay (default)

1. Launch subtask 1's implementer with a fresh worktree for the whole issue:
   `agent-deck launch <repo-root> -w <issue-branch> -c claude -t "impl-<issue-slug>-1" --message-file ...`
   using the stage-1 prompt template with the subtask's mini-spec.
2. Run stages 1–3 (implement, fresh review, fix loop) for subtask 1 in that
   worktree.
3. When clean, record the worktree's HEAD sha in the manifest as subtask 2's
   **start sha**, then launch subtask 2's implementer in the **same worktree
   path** (plain path, no `-w`). Its prompt starts with: "You continue work
   on an existing branch. Read `git log --oneline -20` and the diff so far
   before starting." Then the normal stage-1 template with subtask 2's
   mini-spec.
4. Repeat for each remaining subtask: stages 1–3, one session at a time,
   each building on the previous commits (recording each subtask's start sha
   first).

**Review scope in relay mode.** From subtask 2 on, the branch already carries
earlier subtasks' reviewed work — a reviewer told to judge the full branch
diff against one mini-spec would flag that work as "extra" or "missing".
Scope every review round for subtask N — including its stage-3 end gate — to
`git diff <subtask-start-sha>...HEAD`, and add to the reviewer prompt:
"Commits before <subtask-start-sha> are earlier, already-reviewed subtasks
of the same issue — context, not review scope." The one true full-branch
review runs once, after the final integration check (below).

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
5. As soon as a subtask is merged, clean up its now-redundant worktree and
   branch (the integration branch holds the commits):
   `git -C <repo-root> worktree remove .worktrees/<issue-slug>-<n> && git -C <repo-root> branch -d <issue-branch>-<n>`
6. Continue until every subtask is merged and only the integration worktree
   remains.

## Final integration check

Both topologies: launch one last session in the issue worktree to run the
build and the FULL test suite on the combined result, do a quick e2e sanity
pass of the issue's overall behavior, fix only trivial integration breakage,
and commit. If it finds non-trivial breakage, treat it as findings: route to
a fix session and re-check (this counts toward the shared 3-fix-round cap).

Then, in relay mode, run the deferred full-branch gate: one fresh reviewer
with the stage-2 round-1 prompt, the **whole issue** (all mini-specs / plan
tasks) as its spec, and the full branch diff. `VERDICT: clean` → PR; any
`patch` or `decision-needed` finding → the normal fix loop, still under the
shared caps. A verdict carrying only `defer` findings is `clean` by
construction — that is the reviewer's call to make, not yours to infer from
severities. (Parallel mode already reviewed each subtask branch in full
against its own spec, so skip this extra gate unless the merges were
conflict-heavy.)

## PR

Run stages 4–5 once, from the issue worktree, on `<issue-branch>`: one PR for
the whole issue, CI babysat to green. Screenshot policy is unchanged — all
subtask screenshots live under `$RUN_DIR/<issue-slug>/` and appear only in
the final report. Once the PR is green, the issue worktree and local branch
are cleaned up per the SKILL.md cleanup rules (subtask worktrees are already
gone by then).
