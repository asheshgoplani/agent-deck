# Performance regression: mandatory test coverage

> Originally drafted by @JMBattista in PR #790. The prose below lived in
> `CLAUDE.md` in JM's branch; after #1002 untracked `CLAUDE.md` as a
> per-developer file, this content was relocated here so it stays
> version-controlled and shared across contributors.

Agent-deck has a recurring complaint that lifecycle operations (cold start, group create/delete) drift slower release-over-release. **As of v1.7.x, hot-path walltime is permanently test-gated.**

## Required tests

Any PR modifying performance-sensitive lifecycle paths MUST run:

```bash
GOTOOLCHAIN=go1.25.12 PERF_BUDGET_MULTIPLIER=2.0 \
  go test -run '^TestPerf_' -race -count=1 -timeout 120s \
  ./...
```

Scope is `./...` (not just `cmd/agent-deck/...`) so a `TestPerf_*` added in any
package is exercised — the mandate covers the whole module (`**/*_perf_test.go`)
and the durable-outbox drain gate lives in `internal/session`. `make test-perf`
and `.github/workflows/perf-smoke.yml` both use this scope.

CI runs this as `.github/workflows/perf-smoke.yml`. Only the `perf-tests` job is a hard gate; `perf-bench-trend` is advisory (`continue-on-error: true`) and may show red without blocking merge.

## Paths under the mandate

- `cmd/agent-deck/main.go`
- `internal/testutil/perfbudget.go`
- `**/*_perf_test.go`
- `.github/workflows/perf-smoke.yml`

## Cold vs warm classification (REQUIRED for new TestPerf_* tests)

Every `TestPerf_*` test classifies its work as COLD or WARM based on whether it crosses a process or syscall boundary. The classification picks both the budget formula and the measurement helper.

| Class | When to use | Budget formula | Helper |
|------|------------|----------------|--------|
| **COLD** | Cold-start exec, real-disk fsync, child-process spawn, network | `max(base × 5, 1ms) × multiplier` | `testutil.ColdBudget(t, base)` + `testutil.TrimmedMean(fn)` |
| **WARM** | Pure in-process Go work measurable under controlled GC | `max(base × 3, 1ms) × multiplier` | `testutil.WarmBudget(t, base)` + `testutil.TrimmedMeanWarm(fn)` |

`base` MUST cite the last observed local median (under `-race`, multiplier=1.0) in a comment next to the constant. CI sets `PERF_BUDGET_MULTIPLIER=2.0`; effective CI gate is therefore 10× local for COLD, 6× local for WARM.

The 1 ms floor (`testutil.PerfBudgetFloor`) caps minimum budgets — anything faster is either "just fast" (sub-ms timing dominated by clock resolution / scheduler jitter) or signals the unit under test is too small to be a meaningful regression target. Move such tests to `Benchmark*` (Track A) instead.

## Measurement: n=11 trimmed mean

`TrimmedMean` runs 11 timed iterations (plus 1 warm-up), drops the top 2 + bottom 2, averages the middle 7. Picked because:
- Odd n: median is well-defined as a fallback diagnostic.
- ~110 ms per test (11 × ~10 ms typical) — well under the 120 s suite timeout.
- Drop 2/2 absorbs one GC pause + one scheduler hiccup.
- Middle 7 → variance scales as 1/√7 ≈ 0.38 (~2× noise reduction vs single sample).

Larger n (21, 51) was rejected: ~30% marginal variance reduction at 2–5× test cost.

## Tier 1 vs Tier 2 (for future I/O-touching tests)

Disk-touching tests fall into one of two tiers. **This PR adds neither tier; the convention is documented for the next contributor.**

- **Tier 1 — tmpfs walltime** for code-side regressions in TUI-touching or persistence-touching paths. Run the test against a tmpfs-backed scratch dir (`/dev/shm` on Linux; `t.TempDir()` is already tmpfs on most CI Linux distros). fsync is effectively free, so the measured cost is CPU + Go-runtime cost only. Catches "we added 200 ms of CPU work in the save path" but NOT "we now do 5 fsyncs instead of 1". Classify as WARM.

- **Tier 2 — real-disk count assertions** for I/O-pattern regressions. Disk walltime varies 100× across SSDs / HDDs / cloud volumes — un-normalizable. Instead instrument the storage layer (`*sql.DB` and `*os.File` wrappers exposing fsync count, transaction count, rows written) and assert on counts: "create one group = exactly 1 transaction + 1 fsync". No walltime budget. Classify as COLD because fsync count is on the syscall side.

When adding a TUI-flow or persistence-touching test, write the Tier 1 walltime gate AND, if the test exercises a save path, the Tier 2 count gate. Both prevent different bug classes.

## Budget changes require an RFC

- Loosening any `TestPerf_*` budget (i.e. raising the `base`) by more than 25% requires an RFC at `docs/rfc/PERF_BUDGETS.md` documenting the cause and the upper bound.
- Removing a `TestPerf_*` test is forbidden without an RFC.
- Adding a budget MUST cite the local median and use the matching ColdBudget/WarmBudget helper.

## Track A vs Track B

`Benchmark*` functions are advisory (no `-race`, run via `make bench`). `TestPerf_*` are hard-gated walltime regressions and must remain tmux-free in Track B — including suppressing the macOS `tmux -V` warning probe via `AGENTDECK_SUPPRESS_TMUX_WARNING=1` in any TestPerf_* that exercises `cmd/agent-deck/main.go`. Real-tmux benches live in Track A only.
