# Agent-boundary output cap results

## Pre-registered endpoint

Primary metric: median `cache_read_tokens` per conductor turn, measured on a
strictly paired task set. Baseline is compared with the treatment using the
per-task median and a Wilcoxon signed-rank test. Target: at most 80,000 tokens
(at least 30% below the researched 115,000-token reference baseline).

Guardrail: re-fetch rate, defined as the share of child sessions for which
`session output` is called at least twice in the evaluation window. It must not
increase by more than 10% relative. Malformed tool calls are counted from the
same paired transcripts; time-to-detection is unchanged because this change
does not alter status or waiting behavior.

## Baseline (recorded before implementation)

At 2026-08-22 UTC, the current `internal/costs` SQLite spool for the preceding
14 days contained 3,641 turns (available data covered 2026-08-20 through
2026-08-22):

- median `cache_read_tokens` per recorded turn: **270,997**
- total cache-read tokens: **1,355,547,269**
- total output tokens: **3,399,819**
- cache-read share of all recorded token classes: **99.15%**

The researched pipeline baseline supplied in `SPEC.md` is 115,000 median
cache-read tokens per conductor turn, with 6.40M prompt cache-read tokens versus
5.5k output tokens and a 98.5% cache-read share. The local spool does not retain
a conductor flag on cost events, so its 270,997 figure is recorded as the
current all-turn baseline rather than mislabeled as conductor-only.

`agent-deck session context --json` is not present on current `main`. The
existing context-inspector build (commit `16b1c02`) was therefore run against
the current session before implementation. It reported a projected Codex
snapshot with no resolved rollout: fixed tokens 0 (incomplete), attributed
tokens 0, unknown context window, and reconciliation status `no-anchor`. This
is an explicit unavailable baseline, not a measured zero.

## Change

Only spec item #1 was implemented. `session output` and `session output --pane`
now:

- default to a positive `--max-tokens 25000` budget (four UTF-8 bytes per
  approximate token), including the truncation footer;
- strip ANSI before content crosses the CLI/agent boundary;
- preserve both the head and tail when truncating;
- retain the exact full text in the agent-deck data directory and print its
  path in the footer;
- append one read event to `logs/session-output-reads.jsonl` with session,
  source, budget, and truncation state.

The durable event log makes the guardrail directly measurable: group events by
`session_id` in each paired evaluation arm and count sessions with two or more
events. No polling, heartbeat, list serialization, prompt template, context
summarization, or indexing behavior changed.

## Red-path tests

`TestPrepareAgentBoundaryOutputCapsStripsANSIAndKeepsHeadTail` fails without the
new behavior by requiring the byte ceiling, ANSI removal, both retained ends,
and recovery footer. Additional tests pin UTF-8-safe cuts, ANSI removal below
the truncation threshold, exact full-output persistence, and parseable durable
guardrail events.

## Expected effect

The immediate deterministic effect is that a single session-output result can
no longer inject unbounded transcript or terminal chrome into the next turn.
The paired evaluation should move the primary metric toward the <=80,000 target.
The treatment is considered unsuccessful if re-fetch rate rises by more than
10%, even if token usage falls.
