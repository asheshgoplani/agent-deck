# Low-Friction Workflow Design

**Date:** 2026-08-04
**Status:** Approved

## Motivation

The interactive design workflow currently requires approval after every
section and again after writing the committed document. This turns a small,
well-understood change into six approval turns. The orchestration workflow
also checks only that a design is committed before it launches a worktree;
the worktree can still be based on an older ref and omit the approved design.

## Decisions

- A complete design is presented once and receives one explicit approval.
- That approval covers the committed design document after non-material
  self-review edits. A new approval is required only when self-review changes
  scope, user-visible behavior, interfaces, or explicitly excluded work.
- The design path is checked for ignore rules before the document is written.
  An ignored convention is skipped in favor of the next tracked design
  directory.
- A focused, low-risk change may use the direct `tdd` and `verify` path even
  when it spans a few closely related files. Orchestration is for work that
  needs a dedicated PR pipeline, independent execution, or decomposition.
- For spec- and plan-fed orchestration, the child worktree must be created
  from the commit containing the input file, then checked for both commit
  ancestry and file presence before a child session is launched.

## Architecture

```text
interactive design
  -> present complete design
  -> one user approval
  -> self-review and commit
  -> direct execution or orchestration

spec-fed orchestration
  -> resolve input-file commit
  -> create worktree at that commit
  -> verify ancestry and file presence
  -> launch child
```

The behavior lives entirely in `skills/brainstorming/SKILL.md` and
`skills/orchestrate/SKILL.md`. No agent-deck CLI behavior or persistent
configuration is added.

## Interfaces

The approval prompt is exactly one request for the complete design. A second
approval prompt states what materially changed and why it needs confirmation.

For a spec-fed task, the conductor records the resolved input commit in the
run manifest. Before launch it verifies:

```bash
git -C <worktree-path> merge-base --is-ancestor <input-commit> HEAD
test -f <worktree-path>/<input-file-path>
```

Either failure stops the launch. The worktree is recreated from
`<input-commit>`; the executor is never asked to proceed from a pasted or
missing spec.

## Out of Scope

- Changing the agent-deck CLI worktree implementation
- Removing the design approval gate
- Relaxing deployment, review, test, or CI verification
- Adding approval settings or per-repository workflow configuration

## Verification

- Read both skills to confirm each approval path and worktree gate is
  unambiguous.
- Check that the design document is tracked and committed.
- Inspect the final diff for only the two skill files and this design.
