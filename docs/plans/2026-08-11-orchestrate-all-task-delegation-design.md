# Orchestrate: Delegate All Task Execution

## Motivation

The orchestrate conductor currently delegates implementation and most inspection,
but still performs some task work itself, including cleanup. This blurs the
conductor/worker boundary and consumes the only context that persists for the
whole run.

## Decision

The conductor delegates all task execution to child sessions. It only:

- decomposes and sequences work;
- launches and supervises children;
- routes decisions and results;
- maintains orchestration state; and
- reports outcomes.

Task execution includes audits, research, planning, cleanup, implementation,
testing, verification, review, merges, release work, and CI investigation.
Unavoidable control-plane actions such as rendering prompts, operating
agent-deck, updating the run manifest, and managing the heartbeat remain with
the conductor.

## Cleanup workflow

Cleanup is serialized because worktrees and branches share repository-wide Git
metadata. A dedicated cleanup child executes an exact approved candidate list.
A fresh read-only child then verifies the resulting state independently.

## Out of scope

- Changing agent-deck's session-parenting behavior.
- Delegating child launches, supervision, or user decisions.
- Parallelizing destructive cleanup operations.
