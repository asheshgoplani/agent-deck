# G4 — the strong oracle, finally collected: Claude Code's own `/context`, live session — 2026-07-29

The oracle this gate had been priced against but never held: on 2026-07-29 at 19:06:29 UTC
Ashesh typed `/context` inside the live `agent-buddi-rn` session (deck title
`stream-testflight-87`, profile `personal`, instance `b81d755d-1785346375`, running on
the buddii account). The panel he saw is preserved twice: his paste
(`/tmp/claudemd-work/ORACLE-2026-07-29-buddii.md`) and — stronger — the structured export
Claude Code itself wrote into the session transcript at that moment
(`c3dad234-4fb5-4d2c-a5c5-5236fc11d0fa.jsonl`, record 788, a markdown table with every
figure). The two agree with each other on every number; the comparison below grades
against the transcript-embedded copy, since nobody re-typed it.

**Strength: `independent-authorship`.** Claude Code produced these numbers for its own
purposes, on its own tokenizer, with no interest in agreeing with agent-deck. This is the
oracle the parity artifact says was "not collected"; for this one live session, it now is.

**Ours:** a fresh binary built from a clean clone (`git clone /tmp/ctx-build /tmp/ctx-grade`,
branch `feat/context-inspector` @ `430bbced`, `go build`/`go vet`/`gofmt` clean), run
READ-ONLY against the same session:
`agent-deck -p personal session context b81d755d [--json | --tab verify | --tab breakdown -all]`.
`--verify` was never used (it types into the live session).

## Tolerance, stated before the table

| figure kind | tolerance | why |
|---|---|---|
| provider-measured (anchor, cache-read split) | **exact, 0 tokens** | a measured figure that needs a tolerance against its own source is not measured |
| identity (model id, file paths, item counts, zero-vs-zero) | **exact match** | an identity that is "close" is wrong |
| estimated (`~est`) figures | **the row's own printed error band (±5% listings, ±8% memory markdown) must overlap the oracle's printed rounding interval** (e.g. "18.4k" = [18,350..18,449], "~280" = [275..284], "393" = exact); central delta reported alongside | the band is the estimator's published honesty bound; the oracle's own rounding is not our error |
| files that changed on disk AFTER the capture (mtime > 19:06:29Z) | graded against the on-screen contract ("RECON is as of now on disk; a running session keeps its boot-time copy until you restart it"), with the drift evidence stated | the oracle describes capture-time content; a RECON row describes now; both are dated claims |

Anything outside its band is a **finding**, listed at the bottom — not argued into tolerance.

## Per-figure comparison

| figure | oracle | ours | delta | within tolerance? | provenance we claim |
|---|---|---|---|---|---|
| **model id** | `claude-fable-5` | `claude-opus-5` | mismatch | ❌ **FINDING 1** | "observed from this session's own startup records" |
| window | 1m | 1.0M | 0 | ✅ | model-default from model prefix — the right number, reached via the stale model id (fable-5 also resolves to 1.0M) |
| memory: `~/.agent-deck/worker-scratch/b81d755d-1785346375/CLAUDE.md` | 3.4k | 3,113 [2,864..3,362] | −287 (−8.4%) | ✅ band overlap [3,350..3,362], marginal; file also changed post-capture (mtime 20:14Z) | RECON/~est ±8%, as-of-now; **one row** — the alias chain → `~/.claude-buddii/CLAUDE.md` → `~/.claude/CLAUDE.md` is collapsed and named on `--item` |
| memory: `~/others/ryan/CLAUDE.md` | 18.4k | 18,173 [16,719..19,627] | −227 (−1.2%) | ✅ | RECON/~est ±8%; file byte-identical to capture time (43,909 B, mtime Jul 10) → pure estimator test, and it holds |
| memory: `~/others/ryan/agent-buddi-rn/CLAUDE.md` | 4.6k | 5,009 [4,608..5,410] | +409 (+8.9%) | ✅ band overlap [4,608..4,649], at the edge | RECON/~est ±8%; file byte-identical to capture time (11,919 B, mtime Jul 4) → pure estimator test |
| memory: `.../memory/MEMORY.md` | 393 | 597 [549..645] | +204 (+51.9%) | ⚠️ outside band vs capture-time value; **explained and labelled**: auto-memory grew after the capture (file mtime 19:50Z > capture 19:06Z; the session appends to it), and the screen's own legend states RECON is as-of-now | RECON/~est, as-of-now; chars include the "Contents of …" wrapper Claude Code actually injects |
| **memory total (4 files)** | 26.8k | 26,892 | +92 (+0.3%) | ✅ | sum of the four rows; count 4 = 4 |
| **skills total** | 5.6k (52 skills) | 5,658 across 52 priced catalogue lines | +58 (+1.0%) | ✅ | ABSENT/~est ±5% per listing; catalogue captured verbatim from the startup burst |
| skills count | 52 | 52 listed (+51 on-disk, unlisted, **unpriced** rows shown for completeness) | 0 | ✅ | the 51 extras carry no cost and say why |
| skill `mac-control` | ~280 | 278 [264..292] | −2 (−0.7%) | ✅ | ~est, 827 chars / 2.98 |
| skill `agent-deck:agent-deck` | ~160 | 162 [154..170] | +2 (+1.3%) | ✅ | ~est, 482 chars / 2.98 |
| skill `dataviz` | ~380 | 385 [366..404] | +5 (+1.3%) | ✅ | ~est, 1,146 chars / 2.98 |
| **MCP tools, charged** | **0** (34 tools, "loaded on-demand"; the export prices their would-be cost at 9.1k, excluded from the 189.3k in-use total) | **0** (no MCP instruction blocks recorded; deferred schemas never guessed at) | 0 | ✅ exact | ABSENT/— with the note saying why; POTENTIAL-column model matches the oracle's deferred semantics |
| system tools | 17.9k (schemas, charged) | 95 (names only) + schemas inside the residual | model difference, reconciled below | ✅ by design, **verified not assumed** | CAPT/~est for the names; ABSENT/residual for the schemas |
| **anchor (measured)** | 62,243 — my own arithmetic on transcript record 20: `input_tokens 2 + cache_creation 41,200 + cache_read 21,041` | **62,243, stated to the digit** on the verify frame, with the provider field path and all three components | 0 | ✅ exact | provider-measured, `message.usage.iterations[0].*`, recorded-at timestamp shown |
| cache-read split | 21,041 | 21,041 (in the measured-figure block and the anchor-warm-cache caveat) | 0 | ✅ exact | provider-measured |
| residual arithmetic | — | `62,243 measured − 33,416 attributed = 28,827 unattributed remainder`, spelled out on the frame | arithmetic checks: 62,243 − 33,416 = 28,827 ✓ | ✅ | residual by subtraction, never clamped |
| agents | (no such row in `/context` — Claude Code does not price agents separately) | 771 across 6 catalogue lines | no oracle | ✅ labelled ~est | CAPT/~est |

### The system-tools reconciliation, done rather than assumed

The oracle charges 17.9k for system tools (schemas) and 4.1k for the system prompt — 22.0k
that our model deliberately does not attribute (we price only the 95 tokens of injected
names). The check the gate demands: does our residual **visibly** absorb it?

- residual on screen: **28,827** = 62,243 − 33,416, arithmetic stated in full and correct;
- it must contain: oracle's system tools 17.9k + system prompt 4.1k = **22.0k**;
- the verify frame itself declares the rest of its contents (caveat
  `residual-includes-turn-inputs`): this session's first typed message (4,399 chars) and
  one session-start hook injection (3,276 chars) ≈ 2.6–3.2k tokens at the calibrated divisors;
- remaining ≈ 3.6–4.2k is absorbed estimator error on the attributed 33.4k (whose printed
  band is ±2.5k) plus whatever Claude Code priced into "System prompt" that its /context
  panel and the provider's first-request count divide differently.

28,827 ≥ 22.0k with the named extras accounting for most of the rest: the residual absorbs
the difference, the arithmetic reconciles, and both facts are printed on a reachable frame
rather than inferred here. Also cross-checked in the oracle's own terms: its five charged
categories (4.1 + 17.9 + 26.8 + 5.6 + 134.9) sum to exactly its 189.3k header, and its two
deferred rows (MCP 9.1k, system-tools-deferred 14.6k) are excluded from it — the same
zero-until-loaded model our POTENTIAL column implements.

## Carryover fixes, verified independently on the live fleet

- **2A (synthetic boundary / recovered model):** `wt-billing-arch`, `wt-appstore-audit`,
  `wt-safety-toby`, `wt-dfy-stages` — the four sessions that printed *"no model identifier
  was recorded"* over transcripts holding 86–187 real turns — now all report
  `model: claude-fable-5`, a resolved 1.0M window, and `basis: observed`. Fleet-wide sweep
  (all 80 personal-profile sessions, read-only): **0** occurrences of either false sentence.
- **2B (no bare zero for the unobserved):** same 80-session sweep: **0** `skills (0 items)`
  rows. Two sessions with an unread skill catalogue print `—` with the
  `skill-catalogue-unobserved` caveat; the only zero-token category row on the deck is an
  **established** absence (`ABSENT/measured`, an unattached MCP catalogue), which is the
  contract working, not a violation.
- **2C (measured figure checkable to the digit):** the buddii verify frame states
  `62,243 tokens`, the provider field path with components `(input 2 + cache creation
  41200 + cache read 21041)`, the recorded-at timestamp, and the full subtraction — each
  verified above against my own read of transcript record 20. On the fixture side the
  re-pointed G4 probes now find `27,008` and `3,000` on the recorded `11-verify-tab` frames
  of both drivers, exact at tolerance 0.

## Findings (outside tolerance — not rounding notes)

1. **Model id: ours `claude-opus-5`, oracle `claude-fable-5`.** Root cause, from the
   transcript: the session booted on `claude-opus-5` (first assistant turn 17:33:49Z,
   records 20–70) and switched to `claude-fable-5` at 17:35:39Z (record 71) — two minutes
   into a session that then ran fable-5 for hours, including at the oracle capture. The
   inspector reads the model where it reads the anchor: the first assistant turn. For the
   anchor itself that is correct (the 62,243-token request WAS served by opus-5, and the
   window size happens to be 1.0M either way), but the header prints `model:
   claude-opus-5` as an unqualified present-tense fact and nothing on any frame discloses
   that the session has since run a different model. A reader asking "what model is this
   session on" gets a stale answer with no hint. **Fix direction (not applied here — this
   is a grading pass): keep the anchor's model for anchor provenance, but report the
   latest model turn in the header, or at minimum caveat the switch (`model changed after
   startup: claude-opus-5 → claude-fable-5 at 17:35:39Z`).**
2. **`MEMORY.md` +51.9% vs the capture-time value** — fully explained by post-capture
   auto-memory growth (mtime evidence above) and covered by the screen's as-of-now
   contract, so it is an honestly-labelled disagreement rather than a defect; recorded
   here so nobody mistakes the 26.9k total's accuracy for per-file accuracy on a file the
   session rewrites.
3. **New G3 blank finding (regression, caught this run):** at 80x24 the overview's legend
   heading `reading the columns:` lands exactly on the viewport fold with its body below
   it, on 3 frames of `claude-unknown-version-narrow` (empty-label, Blank Detector). The
   19:41 run was clean here; pass 2A's longer — and more honest — no-model sentence wraps
   one line taller at 80 columns and pushed the fold onto the heading. The screen is
   honest but the last visible line is a label announcing content the reader cannot see.
   Left unfixed by this grading pass; the verdict stays red on G3 until the pager keeps a
   heading glued to its first body line (or equivalent) and the row re-records clean.

## Verdict of this comparison

Sixteen of seventeen graded figures are within tolerance or honestly labelled, including
every figure the estimator recalibration was asked to fix (memory chain +0.3%, skills
+1.0%, worst calibratable file +8.9% inside its band, sampled skills within ±1.3%) and
both carryover contracts (no bare zeros, anchor checkable to the digit). The gate still
does not pass this round: the **model id disagrees with the oracle and the disagreement
is not disclosed on screen** (finding 1), and the G3 matrix regressed at 80x24 (finding
3). Nothing here was tuned to pass; the numbers that disagree are reported disagreeing.
