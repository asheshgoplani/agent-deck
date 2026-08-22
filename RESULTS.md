# Skeleton-first worker briefs — results

## Pre-registered endpoint

The primary endpoint is the median total tokens per completed worker task on a frozen, strictly paired task set: the same 10 tasks and seeds run with the baseline and treatment prompts. The treatment succeeds at a reduction of at least 25%. Per-task medians will be compared with a Wilcoxon signed-rank test; aggregate totals are not the decision metric.

Reliability is the non-inferiority guardrail. Each frozen golden task is run three times and success is reported as pass^3 (all three deterministic verifiers pass), not pass@1. The treatment must not reduce golden-set pass^3. Transcript telemetry will also count malformed tool calls and repeated reads of the same file or symbol after the skeleton stage; these are early warning counters and will be reported per completed task.

This endpoint was recorded before changing the worker prompt. No paired treatment runs were spent before pre-registration.

## Baseline captured 2026-08-22 UTC

Source: the production SQLite spool used by `internal/costs/`, `/home/ashesh/.local/share/agent-deck/profiles/default/state.db`, over the available 14-day window. Although the requested window was 14 days, the spool contained events only from 2026-08-20T12:37:36Z through 2026-08-22T21:55:50Z.

- All sessions: 3,641 events and 1,367,125,212 total tokens.
- Parent-linked worker proxy: 1 session, 57 events, 5,587,875 total tokens.
- Worker components: 114 uncached input, 60,851 output, 5,348,276 cache-read, and 178,634 cache-write tokens.
- Median total tokens per parent-linked worker: 5,587,875. With `n=1`, this is recorded only as a pipeline sanity check and is not a valid paired-task baseline.

The installed `agent-deck` binary was also queried exactly as required with `agent-deck session context --json`. It returned `Error: unknown session command: context` and the session-command usage text, so no context-inspector number is available from this baseline environment. This missing instrument must be repaired or supplied before the paired experiment; it is not replaced with an estimate.

## Change

The goal worker prompt now requires a three-stage reading funnel whenever a worker enters an unfamiliar repository area:

1. map the repository tree with `rg --files` without dumping contents;
2. inspect declaration skeletons with language-appropriate `rg -n` searches while omitting implementation bodies;
3. read full code only for the smallest set of files and symbols selected by the first two stages.

The prompt explicitly forbids full-source reads before the first two stages and tells workers to repeat the funnel rather than broaden full-file reads when evidence changes direction. No serialization, indexing, output capping, polling, heartbeat, or summarization behavior changed.

## Red-path tests

`skills/agent-deck/scripts/goal/tests/test_worker_prompt.py` now checks that:

- all three stages exist in the required order;
- the prompt explicitly gates full-file reads on tree and skeleton inspection;
- concrete grep-first `rg --files` and `rg -n` commands are present.

On the unchanged prompt, all three new tests failed. After the prompt change, the targeted test suite passes.

## Expected effect and measurement

Expected effect: at least 25% lower median total tokens per completed worker task, primarily from fewer broad source reads. The effect is not claimed from the one-worker spool proxy; it must be established with the pre-registered paired ladder: transcript replay, 10 tasks at k=1, the same tasks at k=3, then the full suite twice.

For every run, retain the prompt version, task and seed, total token fields from `internal/costs/`, deterministic verifier result, malformed tool-call count, and repeated-read count. Confirm from the transcript that the treatment actually performed tree → skeleton → narrowed-body reading. Report per-task medians, the Wilcoxon result, pass^3, malformed calls per task, and repeated reads per task. A token win is rejected if pass^3 falls, regardless of the primary endpoint.
