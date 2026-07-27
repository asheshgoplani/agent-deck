# Conductor context budget — design

**Date:** 2026-07-27
**Status:** skill layer implemented; agent-deck tooling track specified, not built.

## Problem

An orchestrate conductor's context grows without bound. A child's context is
bounded by its task — it finishes, it is archived, its cost is capped. The
conductor outlives every child and keeps supervising, so its context is a
function of wall-clock time rather than of work done.

No run has failed from this yet. The observed symptom is cost and latency:
every heartbeat is charged against a context that only ever gets bigger, so
the longer a run goes the more each beat costs, for information that is mostly
identical to the last one.

Where a long run's conductor tokens actually go:

| Driver | Growth |
| --- | --- |
| `session children --json` heartbeat polls | **O(time × children)** — unbounded, and ~95% unchanged rows |
| Turn-start fleet snapshot (injected every turn) | O(turns × children) |
| Reviewer verdicts read, then re-pasted into fix prompts | O(rounds), each crossing context twice |
| `session output` reads of waiting children | O(questions) |
| `gh pr checks` plus failing CI logs | O(CI failures) |

Only the first is unbounded in the strict sense, and it dominates: five hours
of polling three children costs more than the entire review traffic of the
run.

## Design

The invariant: **the conductor's context grows with decisions taken, never
with time elapsed.** The manifest is the run's state; context is a cache of
it. Four idle hours should cost almost nothing.

Three mechanisms, in priority order.

### 1. Delta-only observation (primary)

`references/poll.sh`, copied into `$RUN_DIR` at run setup, replaces the raw
`session children --json` dump as the heartbeat. It projects each child to the
five fields that drive decisions, diffs against the previous call, and prints
only what moved. A quiet beat is one line (~12 tokens) instead of a full JSON
dump (~1.5k for three children).

Two details are load-bearing:

- **`context_tokens` churns on every poll** for any live child. Diffed raw, no
  beat is ever "unchanged" and the mechanism is worthless. It is bucketed to
  `ok`/`soft`/`HARD` and reported outside the diff key.
- **`done_at` / `last_sent_at` churn identically** and are dropped in favour of
  the `done_stale` boolean the supervision rules already depend on.

Growth stops being a function of wall-clock and becomes a function of real
state transitions, which is bounded by the run's actual work.

Verified against canned fixtures covering: first poll, unchanged-with-churn,
status transitions, a soft-threshold crossing, an archived child, a new child,
and a repeated identical poll.

### 2. Findings yes, transcripts never

Large payloads land in `$RUN_DIR` files by shell redirection; the conductor
reads only the decision-carrying line. Verdicts, CI logs, and waiting-child
questions all get cut this way, and fix-round prompts are assembled by shell
so findings never cross context a second time.

The deliberate exception: the conductor still reads findings lists in full.
They are short, and judging severity is the conductor's job — the
vacancy-autofill run's last fix round existed only because the conductor
overruled a reviewer's "nit" that was in fact a regression on existing data.

### 3. Subagents for the rare big read

A subagent burns its own context and returns a summary. Right tool for a
5k-line CI log or an unexplained stall; wrong tool for the heartbeat, where a
launch per beat is slow and heavy for a three-line answer.

### 4. Rotation as a backstop

Conductor thresholds sit below the child ones — soft ~120k (flush to manifest,
`/compact` at an inter-task boundary), hard ~200k (write a handoff, launch a
fresh conductor on the manifest, re-parent live children, archive self). Lower
because a child that compacts loses one task, whereas the conductor loses
supervision state for every task at once with no reviewer downstream to catch
it.

## agent-deck tooling track (not built)

Each item makes the skill-layer discipline above cheaper or unnecessary.
Roughly in value order:

1. **Delta-only turn-start fleet snapshot.** The snapshot is injected into the
   parent on *every* turn regardless of whether anything changed — a per-turn
   tax on exactly the sessions that supervise the most. Making it report only
   changes (or adding a suppress flag for sessions that poll explicitly) is the
   largest single win, and it is the one thing the skill layer cannot fix from
   outside.
2. **`session children --delta`.** Server-side diff against the caller's
   previous call, printing a one-line roll-up when nothing moved. Makes
   `poll.sh` unnecessary and removes the jq dependency.
3. **Bucketed / flagged `context_tokens`.** Report a threshold flag alongside
   the raw number so any consumer's diff is stable. This is the subtle one —
   without it, every delta mechanism at any layer is defeated by the same
   churn.
4. **`session children --fields id,status,done_status`.** Projection, so
   callers stop paying for fields they discard.
5. **`session output --extract <pattern>`.** Server-side extraction (`^VERDICT:`
   being the obvious case), so large outputs never cross into a supervisor's
   context at all.
6. **Fix the empty-handoff-dir bug in the built-in budget rotation.** The
   vacancy-autofill run's planner hit 297k and was auto-rotated into a
   `(cont.)` session whose handoff dir was empty — harmless only because its
   plan was already committed. A conductor rotated the same way would lose the
   run.

## Rejected

- **Subagent-mediated heartbeats.** Genuinely bounded, but a subagent launch
  per beat trades a token problem for a latency problem.
- **Aggressive scheduled rotation.** Bounds growth by accepting it, at the cost
  of a re-orientation per rotation and the risk of dropping run state — and
  item 6 above shows that risk is real today.
