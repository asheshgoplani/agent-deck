# G3 LIVE MATRIX — context-inspector — **FAIL** (1 wrong cell of 15)

> The fixture matrix (`../G3-matrix`) declared 18 invented worlds and passed all 18. This run pointed the same
> journey at the **real deck**: 69 live sessions, 552 invocations, every tool and state that actually exists on
> this machine. It found a cell the fixtures could not model — a report that states two things about a live
> session that are **false**.

**Entry point:** `agent-deck -p personal session context <session>` and its `--tab / --item / --all / --json /
--capabilities / --strict / --timeout` flags. `--verify` was never used (it types into live sessions).
**Read-only:** run against a `cp -a` copy of the live profile in a sandbox `HOME`; the real `state.db` was never
opened. Real transcripts were exposed read-only by symlink so the run measured real data, not empty fixtures.

## The table

| # | session kind (live) | n | outcome | what the user sees | honest? |
|---|---|---|---|---|---|
| 1 | claude, healthy, cold anchor | 41 | ok | `[##...] 61.9k / 1.0M (6.2%) fixed startup overhead` + ranked breakdown | ✅ yes |
| 2 | **claude, `<synthetic>` zero-usage first turn** | **4** | **WRONG** | *"no model identifier was recorded for this session"* and *"this session has not completed a model turn yet"* | ❌ **NO — both false** |
| 3 | claude, model known but window not in table | 44 | ok | `window: unknown … no context-window size is known for model "claude-fable-5"; set AGENTDECK_CONTEXT_WINDOW` | ✅ yes, and actionable |
| 4 | claude, reconciliation incomplete | 5 | ok | `≥19.2k` lower-bound marker; residual `null` with a written reason | ✅ yes |
| 5 | claude, resumed past a compaction boundary | 1 | ok | anchor withheld: *"a compaction boundary precedes the first turn"* | ✅ yes |
| 6 | claude, compacted 36× but genuine cold start | 1 | ok | cold anchor kept, `anchor-warm-cache` info caveat | ✅ yes |
| 7 | claude, largest transcript (181 MB) | 1 | ok | full report in **43 ms** | ✅ yes |
| 8 | claude, smallest transcript (47 KB) | 1 | ok | full report, nothing degraded | ✅ yes |
| 9 | claude, no transcript (stopped) | 1 | ok | `basis: projected`, `no-transcript` caveat, no invented numbers | ✅ yes |
| 10 | codex, rollout observed | 15 | ok | **best output of the run**: `26.8k / 353.4k (7.6%)`, window harness-reported, recon OK | ✅ yes |
| 11 | codex, no rollout on disk | 1 | ok | `basis: projected`, `no-rollout` caveat, every figure `—` | ✅ yes |
| 12 | `pi` — tool with no adapter | 1 | ok | *"token accounting unsupported for pi … agent-deck will not guess one"*; inventory only | ✅ exemplary |
| 13 | cold first run (empty HOME, no config) | — | ok | `Error: session '<id>' not found`, exit 2 — no crash, no half-built config | ⚠️ honest but unhelpful |
| 14 | bad args (`--tab bogus`, bad `--item`, `--timeout 1ns`) | — | ok | distinct exit codes 1 / 2 / 1, each message actionable | ✅ yes |
| 15 | `--item memory:0` — the example in `--help` | — | **WRONG** | `Error: no context item with id "memory:0"` — matches **0 of 7,693** real ids | ❌ doc is wrong |

**Performance:** 552 invocations, median **45 ms**, p95 65 ms, max **545 ms** (a codex rollout). **Zero** runs
exceeded 1 s. Nothing was slow; a 181 MB transcript costs 43 ms because the head-read is genuinely bounded.

**Robustness:** 0 crashes, 0 panics, 0 bytes on stderr across all 552 runs. Every error path exits with its own
documented code.

## The finding that fails the gate

Four live sessions — `wt-billing-arch`, `wt-appstore-audit`, `wt-safety-toby`, `wt-dfy-stages` — print:

> `no model identifier was recorded for this session`
> `this session has not completed a model turn yet, so there is no provider-measured total to reconcile against`

Both statements are false. Measured against their own transcripts:

| session | claims | actual model turns | model in transcript |
|---|---|---|---|
| `wt-billing-arch` | "no model turn yet" | **187** | `claude-fable-5` ×187 |
| `wt-appstore-audit` | "no model turn yet" | **124** | recorded |
| `wt-safety-toby` | "no model turn yet" | **114** | recorded |
| `wt-dfy-stages` | "no model turn yet" | **86** | recorded |

**Root cause** — `internal/ctxinspect/claude/transcript.go:519` `readFirstTurn`:

```go
if h.firstTurnRead && !final { return }
h.firstTurnRead = true          // ← set BEFORE the turn is known to be usable
...
if turn.PromptTokens() <= 0 {
    h.Warnings = append(...)     // warns
    return                       // ← FirstTurn stays nil, but firstTurnRead is already true
}
```

These transcripts open with two `<synthetic>` assistant records carrying `usage.input_tokens: 0`. The first one
sets `firstTurnRead = true`, then bails on the zero-prompt check without populating `FirstTurn`. Every later
assistant record — including the 187 real `claude-fable-5` turns with real usage — is skipped by the guard on
line 1. `FirstTurn` stays `nil` forever, so `modelOf` (`adapter.go:219`) falls through to the empty `req.Model`,
and `ResolveWindow` takes its `id == ""` branch (`window.go:101`) → *"no model identifier was recorded"*.

**Why it matters.** This is precisely the failure the six-gate framework exists to catch: the output *looks*
honest — an unknown, labelled, with a reason — but the reason is **fabricated**. A user reading it concludes
their session never ran; it ran 187 turns. It also costs them the actionable remedy: had the model been read,
row 3's message would have told them to set `AGENTDECK_CONTEXT_WINDOW`. Instead they get a dead end.

The design that refuses to invent a denominator (`window.go:17-45`) is *correct and should not change*. The bug
is upstream of it: the model is thrown away, so the good code is asked the wrong question.

## Second finding (documentation)

`session_context_cmd.go:116` advertises `--item memory:0`. Real memory ids are full paths
(`memory:/Users/ashesh/.agent-deck/CLAUDE.md`). Across 69 live sessions and 7,693 items, ids matching
`memory:<digit>` = **0**. The advertised example always exits 2.

## Honest gaps in this run

- `opencode` / `shell` / `--cmd` sessions: **none exist** in the live profile. Untested here, not "passed".
- Rows 1–12 counts are sessions, not independent worlds; 51 of 69 sessions show no percentage at all,
  because only 18 resolve a window. That is honest per-cell, but the headline number the feature exists to
  show is unavailable on **74%** of this real deck.
- `--verify` was deliberately not exercised (it mutates live sessions).

## Reproduce

`run_matrix.sh` (in this directory) — sandbox HOME, copied profile, symlinked read-only transcript roots.
Per-session facts and per-flag exit codes and timings: `rows.json`.
