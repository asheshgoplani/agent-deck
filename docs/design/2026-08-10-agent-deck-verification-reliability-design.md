# Agent Deck verification reliability design

**Date:** 2026-08-10  
**Status:** Approved

## Motivation

An eight-arm deployed-system verification run completed successfully but exposed four classes of orchestration friction: sending follow-up work to a busy child blocked the conductor, archived children continued to generate state and notification noise, the orchestration skill assumed every run ended in code and a pull request, and released images could not be tied reliably to source revisions.

The goal is to make those paths explicit and testable without changing the established synchronous `session send` contract or inventing a general background-job framework.

## Decisions

### Durable opt-in runtime send queue

Add `agent-deck session send --queue-if-busy`.

- Resolve the complete message before queueing, including `--message-file` contents.
- When the target is idle, use the existing verified send path immediately.
- When a hook-capable target is busy, append the message atomically to a durable per-session FIFO and return promptly with a machine-readable queued receipt.
- Drain messages in FIFO order after a trusted Stop or equivalent turn-finished edge.
- Remove an entry only after verified submission. Persistence survives Agent Deck restarts.
- Reject queueing for missing, stopped, archived, or non-hook-capable targets.
- Enforce fixed internal limits for message count and total bytes; a full queue returns a clear nonzero error.
- Removing or archiving a session discards its queued runtime messages so unarchive or identifier reuse cannot replay stale work.
- Preserve the behavior of default sends and the existing `--no-wait`, `--wait`, and `--defer-if-busy` flags.

Runtime sends use their own FIFO rather than the existing single launch-prompt record. Launch prompts and post-launch messages have different lifetimes and cardinality, and combining them would make replay and removal semantics ambiguous.

### Archive and child-view semantics

- The centralized transition-notification predicate rejects archived sessions.
- The transition daemon skips archived instances before hook and status probing.
- An archived session retains the terminal `stopped` state instead of being reclassified as `error` because its tmux process is absent.
- `session children` returns active children by default and gains `--include-archived` for explicit inspection.
- Follow mode reports one `GONE` transition when an active child becomes archived.
- The orchestration heartbeat filters archived rows only when archive metadata is present and never infers archive state from status fields; output from older binaries without archive metadata remains compatible but cannot be filtered by archive state.

### Worktree group inheritance

The current inheritance implementation remains unchanged. Add a subprocess regression test using a real linked worktree, a scrubbed environment, automatic parenting, and a nested parent group. Any compatible pre-existing local test may be incorporated, but unrelated user work must not be overwritten.

### Deployed-system verification workflow

Add a verification entrance to the orchestration skill before pull-request-specific prerequisites.

The flow is:

1. Recon establishes deployed versions, environment, scope, arm definitions, and artifact contracts.
2. Independent measurement arms run in parallel.
3. The conductor validates and adjudicates their machine-readable artifacts.
4. A consolidated report records evidence and one of three outcomes: `pass`, `defect`, or `inconclusive`.

`pass` is a complete terminal outcome: no edits, pull request, CI run, or deployment is required. `defect` may enter the implementation pipeline only for defects within the authorized scope. `inconclusive` reports what prevented a trustworthy decision without pretending success or retrying indefinitely.

Add prompt templates for recon, measurement arms, and the consolidated report. Reuse the existing rotation and handoff mechanism.

Machine-readable artifacts may be read directly only after validating their expected schema, provenance, producer completion, and freshness. Extract only the deciding fields where possible.

For flaky external measurements, preserve the first failure evidence, diagnose it, and permit one clean rerun by default. A second failure terminates as a defect or inconclusive result, distinguishing product behavior from harness, environment, and licensing failures.

### Release provenance

This change belongs to the owning `uc-cli` repository.

- Local and remote release builds stamp `org.opencontainers.image.revision` with a verified Git HEAD.
- Revision-resolution failure aborts the release rather than emitting an empty or misleading label.
- Dirty-tree state is reported separately and never represented as a clean revision.
- The release reports the resulting immutable image digest, allowing deployed artifacts to be matched to the inspected image and its revision label.

Agent Deck and `uc-cli` changes land independently on their respective `main` branches.

## Verification

- Follow red/green/refactor for every behavior change.
- Add queue persistence, FIFO, exactly-once removal, capacity, restart, archive, stop, and unsupported-target tests.
- Add archive-race tests across transition, done, and Stop-hook notification paths.
- Cover active-only and `--include-archived` child snapshots and follow-mode transitions.
- Test the real linked-worktree inheritance path in a subprocess.
- Add structural and rendering tests for the verification entrance, prompt templates, artifact rules, retry bound, and no-change terminal state.
- Run focused tests, the sandboxed full Go suite, prompt/shell checks, and relevant integration tests.
- Review each repository diff independently and rerun verification on the final `main` commits.

## Out of scope

- Changing default `session send` behavior.
- A generic background job or configurable queue-policy subsystem.
- Automatic retry tuning or unlimited retries.
- A new rotation template or replacement handoff system.
- Treating a source revision label alone as proof of deployed identity; the immutable digest remains part of that proof.

## Principles pass

The design extends the existing session persistence and centralized transition choke points. The runtime FIFO has one concrete caller and one lifecycle, so no factory or plugin abstraction is introduced. Queue limits and retry bounds are fixed rather than configurable because no requirement calls for policy customization. Existing rotation and group-inheritance behavior is reused instead of duplicated.
