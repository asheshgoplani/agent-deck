# Worktree cross-group launch guard

Status: approved 2026-08-10
Scope: `agent-deck launch`, agent-deck skill guidance

## Motivation

A child launched at
`/Users/doozyx/DoozyX/Uniqcast/tvmid-core/.worktrees/bugfix-7.17.3`
was placed in `doozyx/uniqcast` instead of `uniqcast`. The spawning agent
explicitly passed `--group doozyx/uniqcast`, so the existing linked-worktree
inheritance correctly deferred to the explicit override. That makes a mistaken
agent-synthesized group indistinguishable from deliberate cross-group placement.

## Decisions

### Reject conflicting explicit groups for parented linked worktrees

During `agent-deck launch`, after the parent is resolved and the target path is
known to be a linked worktree, compare an explicit group with the parent's group.
If they differ, fail before creating or starting a session. The error reports both
groups and tells the operator to omit `--group` to inherit the parent group.

Matching explicit groups remain valid. Non-worktree children, unparented
worktrees, `--no-parent`, and launches without an explicit group retain their
current behavior.

### Deliberate escape hatch

Add `--allow-cross-group` to `launch`. It permits a conflicting explicit group
for the uncommon case where a parented linked-worktree child intentionally belongs
elsewhere. The flag does not otherwise affect group selection.

### Agent guidance and verification

Update the agent-deck skill to state that agents must not synthesize group paths
from filesystem components. A linked-worktree child should be launched with an
explicit parent and no group override. The guidance includes a post-launch JSON
check of both the persisted group and `parent_session_id`.

## Verification

- A table-driven unit test covers conflict, match, override, non-worktree, and
  unparented cases.
- A CLI regression test reproduces `uniqcast` versus `doozyx/uniqcast` and proves
  the launch is rejected without creating a session.
- The same regression proves `--allow-cross-group` permits deliberate placement.
- Focused tests and the full sandboxed Go suite must pass.

## Out of scope

- No migration or automatic cleanup of existing groups or sessions.
- No change to group-path inference or explicit-group resolution.
- No generalized group-policy framework.
