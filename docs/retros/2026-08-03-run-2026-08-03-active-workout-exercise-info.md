# Retro: run-2026-08-03-active-workout-exercise-info (2026-08-03)

## agent-deck issues

- `agent-deck launch <repo> -w issue-24-active-workout-exercise-info ...` created
  the worktree from `origin/main` instead of the current local `main`, so the
  freshly committed design was absent. The conductor had to fast-forward the
  worktree to local `main` before the planner could proceed.
- Archived sessions sometimes remained visible to `session children` with an
  outcome of `error` or `ok`, adding noise to the delta heartbeat even though
  their done sentinel was valid.

## Skill friction

- Two reviewers delegated their layers internally and entered `waiting` without
  a done sentinel. Conductor nudges were needed to force verdict-file completion.
- One clean T02 gate response omitted its required external verdict file and had
  to be relaunched, even though the visible response contained a complete clean
  verdict.
- `xcodebuild test` could not attach to iOS Simulator while macOS Developer Mode
  was disabled. The non-privileged `build-for-testing` fallback worked, but no UI
  screenshots could be captured without a system-wide sudo change.

## Tiering summary

Quantitative duration and cost data were not captured for this run, so these
results record model selection, review rounds, and escalation outcomes only.

- Planner: Claude/opus; rotated once at the 250k hard context threshold after
  watch-simulator baseline hangs.
- T01: Claude/haiku implementer; one fix round; Claude/sonnet reviews; clean full
  gate.
- T02: Claude/haiku implementer; one fix round; Claude/sonnet reviews; clean full
  gate after a verdict-file retry.
- T03: Claude/sonnet implementer; escalated to Claude/opus after the second fix
  round. It exhausted 3 fix rounds and 2 full gates with 2 patch items remaining,
  so the run ended needs-attention.
- T04: Claude/sonnet implementer and reviewer; clean in one review round.

## Suggested changes

- Add `--base-ref <ref>` to `agent-deck launch -w`. When supplied, resolve the
  local ref and create the child worktree from that commit instead of implicitly
  using `origin/main`; fail with a clear error if the ref cannot be resolved.
- Exclude archived sessions from `session children` delta-heartbeat output by
  default, with an explicit flag for callers that need archived results.
- In the reviewer harness, create the expected verdict-file destination before
  review, validate the verdict file after review, and reject a done sentinel when
  the file is absent or invalid.
- A review-layer agent may finish after internal delegation only when every
  expected layer-result file exists locally and passes validation; it must not
  remain in `waiting` solely because delegated child sessions are still visible.
- Add a preflight check for macOS Developer Mode before simulator-dependent
  verification. Report screenshot capture as unavailable before launching the
  test when Developer Mode is disabled, then use `build-for-testing` where valid.
