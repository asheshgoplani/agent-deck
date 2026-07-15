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
