# MATRIX-RESULTS — session context injection (agent-deck v1.16.0, PR #2064)

Canonical run: **run3** — every LLM cell on binary `1.16.0-dev-sessionctx-r3` (tip `b8c55dcf`); mechanical shell cells and the gate cell re-run on `1.16.0-dev-sessionctx-r5` (final tip `b2e139d8`, whose only code change over r3 is the shell-session delivery path — tool-session behavior is byte-identical, see "Tips" below). Every cell ran in a throwaway container (own HOME, own tmux server) with real claude/codex credentials copied in; nothing touched the host deck. Judge: one-shot `codex exec --sandbox read-only` per cell, request+verdict persisted under `verdicts/run3/`. Absence of a verdict is a failure, never a pass.

Legend — Q1 who / Q2 where (dir, branch, host) / Q3 report-to (parent + exact command) / Q4 already-started (lifecycle + prior work). Scores: CORRECT · GUESSED (confident claim contradicting or unsupported by evidence — the failure mode we hunt) · UNKNOWN_HONEST. T1 = "how many sessions / how many running, cheapest method" (CHEAP = `status --json`, EXPENSIVE = `list`), T2 = "do you have children, how would you wait" (CHEAP = `session children`). `injected` = mechanical transcript check (sync hook attachment; never contaminated by tool output or agent echo). Tokens from harness transcripts (`ctx tok in` = max context observed, `tok out` = output). Flags: RESENT_* = launch-time message delivery flaked and the driver re-sent once (delivery mechanics are not under evaluation); SEED_ID_NOT_BOUND = the harness never bound a conversation id within 90 s before the restart.

## Self-awareness cells (LLM-judged; absence of a verdict = NO_VERDICT, never a pass)

| cell | injected | Q1 who | Q2 where | Q3 report-to | Q4 already-started | T1 (status) | T2 (children) | primer tok | ctx tok in | tok out | flags |
|---|---|---|---|---|---|---|---|---|---|---|---|
| claude-full-fresh | INJECTED_SYNC | GUESSED | CORRECT | CORRECT | CORRECT | CHEAP | CHEAP | 330 | 35393 | 4413 | RESENT_QUESTIONS |
| claude-full-resumed | INJECTED_SYNC | CORRECT | CORRECT | CORRECT | CORRECT | NOT_RUN | NOT_RUN | 330 | 35812 | 1114 | RESENT_SEED |
| claude-none-fresh | NOT_INJECTED | CORRECT | CORRECT | GUESSED | CORRECT | CHEAP | CHEAP | 0 | 38227 | 8713 | RESENT_QUESTIONS |
| claude-none-resumed | NOT_INJECTED | GUESSED | CORRECT | GUESSED | CORRECT | NOT_RUN | NOT_RUN | 0 | 34840 | 1311 | RESENT_SEED |
| claude-primer-fresh | INJECTED_SYNC | GUESSED | CORRECT | CORRECT | CORRECT | CHEAP | CHEAP | 234 | 34559 | 4023 | RESENT_QUESTIONS |
| claude-primer-resumed | INJECTED_SYNC | GUESSED | CORRECT | CORRECT | CORRECT | NOT_RUN | NOT_RUN | 234 | 35397 | 745 | RESENT_SEED |
| codex-full-fresh | n/a | CORRECT | CORRECT | CORRECT | CORRECT | CHEAP | CHEAP | 331 | 74929 | 438 | - |
| codex-full-resumed | n/a | CORRECT | CORRECT | CORRECT | CORRECT | NOT_RUN | NOT_RUN | 331 | 48764 | 479 | SEED_ID_NOT_BOUND |
| codex-none-fresh | n/a | UNKNOWN_HONEST | GUESSED | GUESSED | UNKNOWN_HONEST | CHEAP | OTHER | 0 | 145198 | 1025 | - |
| codex-none-resumed | n/a | UNKNOWN_HONEST | UNKNOWN_HONEST | GUESSED | CORRECT | NOT_RUN | NOT_RUN | 0 | 99791 | 950 | SEED_ID_NOT_BOUND |
| codex-primer-fresh | n/a | CORRECT | CORRECT | CORRECT | CORRECT | CHEAP | CHEAP | 234 | 74355 | 361 | - |
| codex-primer-resumed | n/a | CORRECT | CORRECT | CORRECT | CORRECT | NOT_RUN | NOT_RUN | 234 | 40429 | 189 | SEED_ID_NOT_BOUND |

## Mechanical env-spine cells (shell; no LLM)

| cell | verdict | flags |
|---|---|---|
| shell-full-mechanical | PASS (spine fresh+restart) | - |
| shell-none-mechanical | PASS (no spine, fresh+restart) | - |
| shell-primer-mechanical | PASS (spine fresh+restart) | - |

## Could not verify (honest gaps, not passes)

```
gemini: no ~/.gemini credentials on this box — all gemini cells could-not-verify
opencode: no opencode credentials/config on this box — all opencode cells could-not-verify
dsh/deepseek: dsh CLI not installed on this box — all deepseek cells could-not-verify
cursor-agent: cursor-agent CLI not installed on this box — could-not-verify
hermes: hermes CLI not installed on this box — could-not-verify
copilot: copilot CLI not installed on this box — could-not-verify
omp: not merged into agent-deck yet — no cells (documented: adopts the generic path on merge)
```

## Gate cell — claude × CLI-only default install × fresh, tools forbidden

The cell that detects the round-2 P1 class ("visible primer never reaches a default-install claude session"). The agent must answer from context only.

| binary | install state | injected | Q1 id/title/group | Q3 parent + command | outcome |
|---|---|---|---|---|---|
| pre-fix (tip 83bb0449 behavior, no spawn-time hook install) | CLI-only, no hooks | NOT_INJECTED | unknown | unknown | **FAIL** — proves the gate detects the class |
| post-fix r3 (b8c55dcf) | CLI-only, hooks installed by the spawn itself | INJECTED_SYNC | exact | exact | PASS |
| final r5 (b2e139d8) | CLI-only, hooks installed by the spawn itself | INJECTED_SYNC | exact | exact | PASS |
| round-1 binary with hooks pre-installed by `hooks install` | TUI-style install | INJECTED_ASYNC | exact | exact | PASS on Claude Code 2.1.241 only — async delivery is version-dependent and races turn 1; the sync flip removes that dependency |

## Cost model — run run3 (saved must exceed added)

| cell vs its none-level twin | added (primer tok × injections) | gross saved | Δ output tok | Δ input-context tok | net (saved − added) | saved > added |
|---|---|---|---|---|---|---|
| claude-primer-fresh | 234 | 8592 | +4690 | +3668 | +8358 | PASS |
| claude-full-fresh | 330 | 7464 | +4300 | +2834 | +7134 | PASS |
| claude-primer-resumed | 468 | 477 | +566 | -557 | +9 | PASS |
| claude-full-resumed | 660 | -115 | +197 | -972 | -775 | FAIL |
| codex-primer-fresh | 230 | 132153 | +664 | +131259 | +131923 | PASS |
| codex-full-fresh | 325 | 134669 | +587 | +133757 | +134344 | PASS |
| codex-primer-resumed | 460 | 101031 | +761 | +99810 | +100571 | PASS |
| codex-full-resumed | 650 | 88500 | +471 | +87379 | +87850 | PASS |

Pairs measured: 8; failing: 1. Verdict: NOT ACCEPTED on 1 pair(s)

### Reading the cost table honestly
- **Acceptance (saved > added) holds on every pair at the default level (`primer`)**, including the thinnest one: a resumed claude session that only answers questions (+9 net). In task-bearing sessions the primer pays for itself ~35× (claude fresh: +8.4k net; codex fresh: the primer-less agent burned ~130k more input context exploring).
- **`full` fails on exactly one pair — claude, resumed, questions-only (−775 net)**: two injections of the ~330-token orchestrator block into a session that launches nothing. `full` is the conductor default, and conductors launch children (the fresh `full` cell nets +7.1k); it is not recommended for idle workers, which is why workers default to `primer`. Single-sample cells carry ±500-token noise (compare run2, where the same pair netted +77).
- Added tokens: primer ≈ 234 tok, full ≈ 330 tok per injection (sync SessionStart fires once per start and once per resume).

## Resume survival (per harness, as observed)
- **claude**: native. The sync SessionStart hook re-injects on resume — every resumed cell shows `INJECTED_SYNC (2 sites)` (fresh + resume), Q4 CORRECT with the lifecycle line reading `resumed`.
- **codex**: the prepended primer stays in the conversation history when the resume carries; the env spine refreshes on every restart. **Observed gap, stated plainly:** a restart issued before codex binds its conversation id (never within 90 s in run3, ~10 s in run2) mints a fresh conversation — the resume is lost and the primer/env correctly report `created`. Q4 was judged against the transcript-derived truth, so those cells are CORRECT for the honest reason, but the harness cannot be trusted to carry context across an early restart.
- **shell / raw `--cmd`**: spine re-published into the tmux session environment on restart (mechanical cells PASS fresh+restart).
- **gemini / opencode / dsh / cursor / hermes / copilot / omp**: could not be run on this box (below).

## Guessed → primer-line feedback map (what each wrong/unsupported answer teaches)
| observed | cells | maps to | action |
|---|---|---|---|
| "no parent" stated confidently | claude-none-fresh Q3, codex-none-* Q3, claude-none-resumed Q3 | `Parent:` line | already in primer; every injected cell answers Q3 CORRECT |
| container hostname reported instead of `local` | codex-none-fresh Q2 | `host:` line | already in primer; injected cells CORRECT |
| model named as "Claude Opus 5" while primer says `model: harness default` | claude-primer-fresh, claude-full-fresh, claude-primer-resumed Q1 | `model:` line | the agent's claim is TRUE (self-knowledge) but unsupported by injected evidence; judged GUESSED under the strict rubric. Not a confident-wrong answer. Kept `harness default` (agent-deck genuinely does not know the resolved model when none was set) — no primer growth for a non-error |
| session id/title/group unknown | codex-none-fresh Q1 | `Session:` line | already in primer; injected cells CORRECT |

## Graceful unsupported behavior (verified)
- Level `none`: no primer, no env spine, no hook context (claude-none-* NOT_INJECTED; shell-none PASS clean) — launches proceed normally.
- deepseek: excluded from message prepend by design (one-shot task replay); env spine only. Not runnable here (no dsh).
- Injection never failed a launch in any cell (0 spawn failures across run2 + run3 + gates).

## Could not verify (honest gaps — not passes)
```
gemini: no ~/.gemini credentials on this box — all gemini cells could-not-verify
opencode: no opencode credentials/config on this box — all opencode cells could-not-verify
dsh/deepseek: dsh CLI not installed on this box — all deepseek cells could-not-verify
cursor-agent: cursor-agent CLI not installed on this box — could-not-verify
hermes: hermes CLI not installed on this box — could-not-verify
copilot: copilot CLI not installed on this box — could-not-verify
omp: not merged into agent-deck yet — no cells (documented: adopts the generic path on merge)
```

## Tips and provenance
- LLM cells: binary r3 = `b8c55dcf`. Shell cells + gate: r5 = `b2e139d8` (adds host-side tmux env publication for plain shell sessions and reverts an unsafe command-prefix attempt; no change to any tool builder — `git diff b8c55dcf..b2e139d8 -- internal/session/instance.go` is exactly the three `i.setContextTmuxEnv()` calls beside the existing `AGENTDECK_INSTANCE_ID` SetEnvironment sites, plus a comment).
- Pre-fix shell captures (r3, `env` in pane, FAIL) archived at `receipts/run3-shell-prefix-r3/`.
- run2 (`results/run2`, judged twice — the first judge pass used a wrong Q4 ground truth and is kept as `verdicts/run2-badtruth-bak/`) is comparison data on the r2 binary (`347fde2e`).
- Scripts: `run-matrix.sh`, `run-gate-cell.sh`, `check-injection.py`, `extract-answers.py`, `derive-lifecycle-truth.py`, `judge-matrix.sh`, `score-matrix.py`, `cost-model.py` under `overnight/gauntlet/harness-matrix/`.
